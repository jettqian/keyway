package convert

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ---------- OpenAI chunks → Anthropic SSE ----------

// OpenAIToAnthropicStream 消费 OpenAI chunk 流，合成 Anthropic wire 格式事件序列
type OpenAIToAnthropicStream struct {
	id        string
	model     string
	openIdx   int         // 当前打开的块序号，-1 表示无
	openType  string      // text | thinking | tool_use
	toolBlock map[int]int // openai tool_calls index → anthropic 块序号
	usage     Usage
	hasUsage  bool
	finish    string
	started   bool
	done      bool
}

func NewOpenAIToAnthropicStream() *OpenAIToAnthropicStream {
	return &OpenAIToAnthropicStream{openIdx: -1, toolBlock: map[int]int{}}
}

func (c *OpenAIToAnthropicStream) Feed(line []byte) ([]byte, bool, error) {
	if c.done {
		return nil, true, nil
	}
	payload := parseSSEData(line)
	if payload == nil {
		return nil, false, nil
	}
	if string(payload) == "[DONE]" {
		out, err := c.closeOut()
		return out, true, err
	}
	var chunk OpenAIChatChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil, false, err
	}
	var buf bytes.Buffer

	if !c.started {
		c.started = true
		c.id = "msg_" + strings.TrimPrefix(chunk.ID, "chatcmpl-")
		c.model = chunk.Model
		buf.Write(c.emitMessageStart())
	}
	if chunk.Usage != nil {
		c.usage = NormalizeOpenAIUsage(chunk.Usage)
		c.hasUsage = true
	}
	if len(chunk.Choices) == 0 {
		if buf.Len() > 0 {
			return buf.Bytes(), false, nil
		}
		return nil, false, nil
	}
	delta := chunk.Choices[0].Delta

	if delta.ReasoningContent != "" {
		idx, prelude, isNew := c.ensureBlock("thinking")
		buf.Write(prelude)
		if isNew {
			buf.Write(c.emitBlockStart(idx, &AnthropicContentBlock{Type: "thinking"}))
		}
		buf.Write(c.emitBlockDelta(idx, &AnthropicStreamDelta{Type: "thinking_delta", Thinking: delta.ReasoningContent}))
	}
	if delta.Content != "" {
		idx, prelude, isNew := c.ensureBlock("text")
		buf.Write(prelude)
		if isNew {
			buf.Write(c.emitBlockStart(idx, &AnthropicContentBlock{Type: "text", Text: ""}))
		}
		buf.Write(c.emitBlockDelta(idx, &AnthropicStreamDelta{Type: "text_delta", Text: delta.Content}))
	}
	for _, tc := range delta.ToolCalls {
		if tc.ID != "" || (tc.Function != nil && tc.Function.Name != "") {
			idx, prelude, isNew := c.ensureToolBlock(tc.Index)
			buf.Write(prelude)
			if isNew {
				block := &AnthropicContentBlock{Type: "tool_use", ID: tc.ID, Input: json.RawMessage("{}")}
				if tc.Function != nil {
					block.Name = tc.Function.Name
				}
				buf.Write(c.emitBlockStart(idx, block))
			}
		}
		if tc.Function != nil && tc.Function.Arguments != "" {
			if idx, ok := c.toolBlock[tc.Index]; ok {
				buf.Write(c.emitBlockDelta(idx, &AnthropicStreamDelta{
					Type:        "input_json_delta",
					PartialJSON: tc.Function.Arguments,
				}))
			}
		}
	}
	if fr := chunk.Choices[0].FinishReason; fr != "" {
		c.finish = fr
	}
	if buf.Len() > 0 {
		return buf.Bytes(), false, nil
	}
	return nil, false, nil
}

// Finish 流截断时补全：关闭打开块并发出终结事件
func (c *OpenAIToAnthropicStream) Finish() ([]byte, error) {
	if c.done {
		return nil, nil
	}
	return c.closeOut()
}

func (c *OpenAIToAnthropicStream) Usage() Usage { return c.usage }

// closeOut 关闭打开的块 → message_delta → message_stop，标记终结
func (c *OpenAIToAnthropicStream) closeOut() ([]byte, error) {
	c.done = true
	var buf bytes.Buffer
	if !c.started {
		c.started = true
		c.id = "msg_stream"
		buf.Write(c.emitMessageStart())
	}
	if c.openIdx >= 0 {
		buf.Write(c.emitBlockStop(c.openIdx))
		c.openIdx = -1
		c.openType = ""
	}
	buf.Write(c.emitMessageDelta())
	buf.Write(c.emitMessageStop())
	return buf.Bytes(), nil
}

// ensureBlock 打开/复用指定类型的块；返回 (块序号, 需先写出的关闭事件, 是否新开)
func (c *OpenAIToAnthropicStream) ensureBlock(typ string) (int, []byte, bool) {
	if c.openIdx >= 0 && c.openType == typ {
		return c.openIdx, nil, false
	}
	var prelude []byte
	if c.openIdx >= 0 {
		prelude = c.emitBlockStop(c.openIdx)
	}
	c.openIdx++
	c.openType = typ
	return c.openIdx, prelude, true
}

// ensureToolBlock 为 openai tool index 打开专属 tool_use 块（先关闭当前块）
func (c *OpenAIToAnthropicStream) ensureToolBlock(toolIdx int) (int, []byte, bool) {
	if idx, ok := c.toolBlock[toolIdx]; ok && c.openIdx == idx {
		return idx, nil, false
	}
	var prelude []byte
	if c.openIdx >= 0 {
		prelude = c.emitBlockStop(c.openIdx)
	}
	c.openIdx++
	c.openType = "tool_use"
	c.toolBlock[toolIdx] = c.openIdx
	return c.openIdx, prelude, true
}

func (c *OpenAIToAnthropicStream) emitMessageStart() []byte {
	ev := AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicMessagesResponse{
			ID: c.id, Type: "message", Role: "assistant", Model: c.model,
			Content: []AnthropicContentBlock{},
		},
	}
	return anthropicEvent("message_start", ev)
}

func (c *OpenAIToAnthropicStream) emitBlockStart(idx int, block *AnthropicContentBlock) []byte {
	ev := AnthropicStreamEvent{Type: "content_block_start", Index: idx, ContentBlock: block}
	return anthropicEvent("content_block_start", ev)
}

func (c *OpenAIToAnthropicStream) emitBlockDelta(idx int, delta *AnthropicStreamDelta) []byte {
	ev := AnthropicStreamEvent{Type: "content_block_delta", Index: idx, Delta: delta}
	return anthropicEvent("content_block_delta", ev)
}

func (c *OpenAIToAnthropicStream) emitBlockStop(idx int) []byte {
	ev := AnthropicStreamEvent{Type: "content_block_stop", Index: idx}
	return anthropicEvent("content_block_stop", ev)
}

func (c *OpenAIToAnthropicStream) emitMessageDelta() []byte {
	u := c.anthropicUsage()
	ev := AnthropicStreamEvent{
		Type:  "message_delta",
		Delta: &AnthropicStreamDelta{StopReason: openAIStopToAnthropic(firstNonEmpty(c.finish, "stop"))},
		Usage: &u,
	}
	return anthropicEvent("message_delta", ev)
}

func (c *OpenAIToAnthropicStream) emitMessageStop() []byte {
	ev := AnthropicStreamEvent{Type: "message_stop"}
	return anthropicEvent("message_stop", ev)
}

func (c *OpenAIToAnthropicStream) anthropicUsage() AnthropicUsage {
	return AnthropicUsage{
		InputTokens:          c.usage.PromptTokens - c.usage.CachedTokens,
		CacheReadInputTokens: c.usage.CachedTokens,
		OutputTokens:         c.usage.CompletionTokens,
	}
}

// anthropicEvent 产出 anthropic wire 格式事件（event: 行 + data: 行）
func anthropicEvent(name string, ev any) []byte {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(name)
	buf.WriteString("\ndata: ")
	buf.Write(marshalCompact(ev))
	buf.WriteString("\n\n")
	return buf.Bytes()
}
