package convert

import (
	"bytes"
	"encoding/json"
	"strings"
)

// StreamConverter 流式转换器接口：Feed 逐行喂入 SSE 行（含 "data:" 前缀），
// 返回待写出的完整 SSE 片段（含结尾空行）；done 表示流终结（[DONE] 已产出）。
type StreamConverter interface {
	Feed(line []byte) (out []byte, done bool, err error)
	Finish() (out []byte, err error)
	Usage() Usage
}

// sseDataLine 组装一行 SSE data
func sseDataLine(payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("data: ")
	buf.Write(payload)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

// parseSSEData 解析单行，返回 data 负载；非 data 行返回 nil
func parseSSEData(line []byte) []byte {
	s := strings.TrimSpace(string(line))
	if s == "" || strings.HasPrefix(s, ":") || strings.HasPrefix(s, "event:") {
		return nil
	}
	if strings.HasPrefix(s, "data:") {
		return bytes.TrimSpace([]byte(strings.TrimPrefix(s, "data:")))
	}
	return nil
}

// ---------- Anthropic SSE → OpenAI chunks ----------

// AnthropicToOpenAIStream 消费 Anthropic SSE，产出 OpenAI chunk 流
type AnthropicToOpenAIStream struct {
	id               string
	model            string
	blocks           map[int]string // 块序号 → 类型
	usage            Usage
	finish           string
	started          bool
	done             bool
	messageDeltaSent bool
}

func NewAnthropicToOpenAIStream() *AnthropicToOpenAIStream {
	return &AnthropicToOpenAIStream{blocks: map[int]string{}}
}

func (c *AnthropicToOpenAIStream) Feed(line []byte) ([]byte, bool, error) {
	if c.done {
		return nil, true, nil
	}
	payload := parseSSEData(line)
	if payload == nil {
		return nil, false, nil
	}
	if string(payload) == "[DONE]" {
		return nil, false, nil
	}
	var ev AnthropicStreamEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, false, err
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			c.id = "chatcmpl-" + strings.TrimPrefix(ev.Message.ID, "msg_")
			c.model = ev.Message.Model
			u := ev.Message.Usage
			c.usage.PromptTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			c.usage.CachedTokens = u.CacheReadInputTokens
			c.usage.CacheWriteTokens = u.CacheCreationInputTokens
		}
		if !c.started {
			c.started = true
			chunk := c.chunk(OpenAIMessageDelta{Role: "assistant"}, "")
			chunk.Usage = c.openAIUsage()
			return sseDataLine(marshalCompact(chunk)), false, nil
		}
	case "content_block_start":
		if ev.ContentBlock != nil {
			c.blocks[ev.Index] = ev.ContentBlock.Type
			if ev.ContentBlock.Type == "tool_use" {
				delta := OpenAIMessageDelta{ToolCalls: []OpenAIToolCallDelta{{
					Index:    ev.Index,
					ID:       ev.ContentBlock.ID,
					Type:     "function",
					Function: &OpenAIFuncDelta{Name: ev.ContentBlock.Name},
				}}}
				return sseDataLine(marshalCompact(c.chunk(delta, ""))), false, nil
			}
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return nil, false, nil
		}
		c.usage.CompletionTokens += deltaTokenCount(ev.Delta)
		switch ev.Delta.Type {
		case "text_delta":
			return sseDataLine(marshalCompact(c.chunk(OpenAIMessageDelta{Content: ev.Delta.Text}, ""))), false, nil
		case "thinking_delta":
			return sseDataLine(marshalCompact(c.chunk(OpenAIMessageDelta{ReasoningContent: ev.Delta.Thinking}, ""))), false, nil
		case "input_json_delta":
			delta := OpenAIMessageDelta{ToolCalls: []OpenAIToolCallDelta{{
				Index:    ev.Index,
				Function: &OpenAIFuncDelta{Arguments: ev.Delta.PartialJSON},
			}}}
			return sseDataLine(marshalCompact(c.chunk(delta, ""))), false, nil
		case "signature_delta":
			return nil, false, nil
		}
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			c.finish = anthropicStopToOpenAI(ev.Delta.StopReason)
		}
		if ev.Usage != nil {
			c.usage.CompletionTokens = ev.Usage.OutputTokens
		}
		chunk := c.chunk(OpenAIMessageDelta{}, firstNonEmpty(c.finish, "stop"))
		chunk.Usage = c.openAIUsage()
		c.messageDeltaSent = true
		return sseDataLine(marshalCompact(chunk)), false, nil
	case "message_stop":
		c.done = true
		return sseDataLine([]byte("[DONE]")), true, nil
	case "ping":
		return nil, false, nil
	}
	return nil, false, nil
}

// Finish 流意外截断时补全：终末 chunk + [DONE]
func (c *AnthropicToOpenAIStream) Finish() ([]byte, error) {
	if c.done {
		return nil, nil
	}
	c.done = true
	var out bytes.Buffer
	if !c.messageDeltaSent {
		chunk := c.chunk(OpenAIMessageDelta{}, firstNonEmpty(c.finish, "stop"))
		chunk.Usage = c.openAIUsage()
		out.Write(sseDataLine(marshalCompact(chunk)))
		c.messageDeltaSent = true
	}
	out.Write(sseDataLine([]byte("[DONE]")))
	return out.Bytes(), nil
}

func (c *AnthropicToOpenAIStream) Usage() Usage { return c.usage }

func (c *AnthropicToOpenAIStream) chunk(delta OpenAIMessageDelta, finish string) *OpenAIChatChunk {
	return &OpenAIChatChunk{
		ID:     c.id,
		Object: "chat.completion.chunk",
		Model:  c.model,
		Choices: []OpenAIChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finish,
		}},
	}
}

func (c *AnthropicToOpenAIStream) openAIUsage() *OpenAIUsage {
	u := &OpenAIUsage{
		PromptTokens:     c.usage.PromptTokens,
		CompletionTokens: c.usage.CompletionTokens,
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	if c.usage.CachedTokens > 0 {
		u.PromptTokensDetails = &PromptTokensDetails{CachedTokens: c.usage.CachedTokens}
	}
	return u
}

// deltaTokenCount 流式过程中的粗略输出计数（message_delta 会覆盖为精确值）
func deltaTokenCount(d *AnthropicStreamDelta) int {
	n := len(d.Text) + len(d.Thinking) + len(d.PartialJSON)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
