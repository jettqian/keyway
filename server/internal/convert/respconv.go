package convert

import (
	"encoding/json"
	"strings"
)

// ---------- 非流式响应转换 ----------

// AnthropicToOpenAIResponse Anthropic 响应 → OpenAI 响应（DESIGN §7.3）
func AnthropicToOpenAIResponse(r *AnthropicMessagesResponse) *OpenAIChatResponse {
	var text strings.Builder
	var reasoning strings.Builder
	var toolCalls []OpenAIToolCall
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			reasoning.WriteString(b.Thinking)
		case "tool_use":
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:       b.ID,
				Type:     "function",
				Function: OpenAIFunctionCall{Name: b.Name, Arguments: string(b.Input)},
			})
		}
	}
	msg := OpenAIChatMessage{Role: "assistant", Content: text.String()}
	if reasoning.Len() > 0 {
		msg.ReasoningContent = reasoning.String()
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	usage := &OpenAIUsage{
		PromptTokens:     r.Usage.InputTokens + r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens,
		CompletionTokens: r.Usage.OutputTokens,
	}
	if r.Usage.CacheReadInputTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: r.Usage.CacheReadInputTokens}
	}
	return &OpenAIChatResponse{
		ID:     "chatcmpl-" + strings.TrimPrefix(r.ID, "msg_"),
		Object: "chat.completion",
		Model:  r.Model,
		Choices: []OpenAIChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: anthropicStopToOpenAI(r.StopReason),
		}},
		Usage: usage,
	}
}

// anthropicStopToOpenAI stop_reason 映射
func anthropicStopToOpenAI(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "stop_sequence", "end_turn", "":
		return "stop"
	}
	return "stop"
}

// OpenAIToAnthropicResponse OpenAI 响应 → Anthropic 响应；
// reasoning_content → thinking 块
func OpenAIToAnthropicResponse(r *OpenAIChatResponse) *AnthropicMessagesResponse {
	if len(r.Choices) == 0 {
		return &AnthropicMessagesResponse{
			ID: "msg_", Type: "message", Role: "assistant", Model: r.Model,
			Content: []AnthropicContentBlock{{Type: "text", Text: ""}},
		}
	}
	choice := r.Choices[0]
	var blocks []AnthropicContentBlock
	if choice.Message.ReasoningContent != "" {
		blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: choice.Message.ReasoningContent})
	}
	if s, ok := choice.Message.Content.(string); ok && s != "" {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: s})
	}
	for _, tc := range choice.Message.ToolCalls {
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: normalizeJSONObject([]byte(tc.Function.Arguments)),
		})
	}
	if len(blocks) == 0 {
		blocks = []AnthropicContentBlock{{Type: "text", Text: ""}}
	}
	resp := &AnthropicMessagesResponse{
		ID:           "msg_" + strings.TrimPrefix(r.ID, "chatcmpl-"),
		Type:         "message",
		Role:         "assistant",
		Model:        r.Model,
		Content:      blocks,
		StopReason:   openAIStopToAnthropic(choice.FinishReason),
		StopSequence: nil,
	}
	if r.Usage != nil {
		// openai prompt_tokens 含缓存；有明细则拆出 cache_read
		prompt := r.Usage.PromptTokens
		cached := 0
		if r.Usage.PromptTokensDetails != nil {
			cached = r.Usage.PromptTokensDetails.CachedTokens
		}
		if cached == 0 {
			cached = r.Usage.PromptCacheHitTokens
		}
		resp.Usage = AnthropicUsage{
			InputTokens:          prompt - cached,
			CacheReadInputTokens: cached,
			OutputTokens:         r.Usage.CompletionTokens,
		}
	}
	return resp
}

// openAIStopToAnthropic finish_reason 反向映射
func openAIStopToAnthropic(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	case "stop", "":
		return "end_turn"
	}
	return "end_turn"
}

// ---------- 错误转换（DESIGN §7.4）----------

// OpenAIErrorToAnthropic 保留 message 的跨协议错误体转换
func OpenAIErrorToAnthropic(body []byte) []byte {
	var parsed OpenAIErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return defaultAnthropicError(body)
	}
	msg := parsed.Error.Message
	if msg == "" {
		msg = string(body)
	}
	typ := parsed.Error.Type
	if typ == "" {
		typ = "api_error"
	}
	out, _ := json.Marshal(AnthropicErrorBody{
		Type:  "error",
		Error: AnthropicErrorDetail{Type: typ, Message: msg},
	})
	return out
}

// AnthropicErrorToOpenAI 反向
func AnthropicErrorToOpenAI(body []byte) []byte {
	var parsed AnthropicErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return defaultOpenAIError(body)
	}
	msg := parsed.Error.Message
	if msg == "" {
		msg = string(body)
	}
	typ := parsed.Error.Type
	if typ == "" {
		typ = "api_error"
	}
	out, _ := json.Marshal(OpenAIErrorBody{
		Error: OpenAIErrorDetail{Message: msg, Type: typ},
	})
	return out
}

func defaultAnthropicError(raw []byte) []byte {
	out, _ := json.Marshal(AnthropicErrorBody{
		Type:  "error",
		Error: AnthropicErrorDetail{Type: "api_error", Message: string(raw)},
	})
	return out
}

func defaultOpenAIError(raw []byte) []byte {
	out, _ := json.Marshal(OpenAIErrorBody{
		Error: OpenAIErrorDetail{Message: string(raw), Type: "api_error"},
	})
	return out
}
