package relay

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"keyway/internal/convert"
)

// ---------- usage 兜底估算（DESIGN §7.3：注入 include_usage 之外的本地兜底）----------
//
// 估算口径与 convert.EstimateTokens 一致：Σ ceil(runes/3.6)。
// 仅在上游未回传 usage（对应字段为 0）时用于补齐统计，绝不覆盖真实值。

// estimateRequestTokens 入站请求的 prompt 侧本地估算（openai chat / anthropic messages；
// 其余入站协议无稳定结构，返回 0）
func estimateRequestTokens(inbound string, rawBody []byte) int {
	switch inbound {
	case "anthropic":
		var req convert.AnthropicMessagesRequest
		if json.Unmarshal(rawBody, &req) != nil {
			return 0
		}
		return convert.EstimateTokens(&req)
	case "openai":
		var req convert.OpenAIChatRequest
		if json.Unmarshal(rawBody, &req) != nil {
			return 0
		}
		return convert.EstimateOpenAITokens(&req)
	}
	return 0
}

// streamTally 流式输出侧文本累计器：喂入透传的原始上游 SSE 行，
// 流结束时若上游未回传 usage，用累计文本估算 completion tokens。
// 覆盖三种增量形态：OpenAI chunk 的 choices[].delta、Anthropic 的
// content_block_delta.delta、Responses 的 response.output_text.delta（delta 为字符串）。
type streamTally struct{ runes int }

func (t *streamTally) feed(line []byte) {
	payload := ssePayload(line)
	if payload == nil || string(payload) == "[DONE]" {
		return
	}
	if !bytes.Contains(payload, []byte(`"delta"`)) {
		return
	}
	var probe struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Delta json.RawMessage `json:"delta"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return
	}
	var n int
	for _, ch := range probe.Choices { // OpenAI chat chunk
		n += len([]rune(ch.Delta.Content)) + len([]rune(ch.Delta.ReasoningContent))
		for _, tc := range ch.Delta.ToolCalls {
			n += len([]rune(tc.Function.Arguments))
		}
	}
	if len(probe.Choices) == 0 && len(probe.Delta) > 0 {
		if s := deltaString(probe.Delta); s != "" { // Responses output_text.delta
			n += len([]rune(s))
		} else { // Anthropic content_block_delta
			var d struct {
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			}
			if json.Unmarshal(probe.Delta, &d) == nil {
				n += len([]rune(d.Text)) + len([]rune(d.Thinking)) + len([]rune(d.PartialJSON))
			}
		}
	}
	t.runes += n
}

func (t *streamTally) tokens() int {
	if t.runes == 0 {
		return 0
	}
	return int(math.Ceil(float64(t.runes) / 3.6))
}

// ssePayload 剥离 SSE 行的 data: 前缀；非数据行返回 nil
func ssePayload(line []byte) []byte {
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "data:") {
		return nil
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(s, "data:")))
}

// deltaString Responses API 的 delta 为纯字符串形态时提取文本，否则返回空
func deltaString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// estimateResponseTokens 非流式响应体的 completion 侧本地估算：
// 兼容 OpenAI choices[].message.content（string 或多模态段）、Anthropic content 块、
// Responses output[].content[].text 三种结构
func estimateResponseTokens(body []byte) int {
	if !bytes.Contains(body, []byte(`"content"`)) {
		return 0
	}
	var probe struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Content json.RawMessage `json:"content"` // anthropic 顶层 content 块数组
		Output  []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"` // responses
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return 0
	}
	var n int
	for _, ch := range probe.Choices {
		n += len([]rune(contentText(ch.Message.Content))) + len([]rune(ch.Message.ReasoningContent))
	}
	n += len([]rune(contentText(probe.Content)))
	for _, o := range probe.Output {
		for _, c := range o.Content {
			n += len([]rune(c.Text))
		}
	}
	return int(float64(n) / 3.6)
}

// contentText 提取 content 字段文本：string 或 [{type,text,thinking}] 块数组
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
			if b.Thinking != "" {
				texts = append(texts, b.Thinking)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}
