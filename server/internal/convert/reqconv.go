package convert

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNNotSupported OpenAI n>1 多候选不支持跨协议转发
var ErrNNotSupported = errors.New("不支持 n>1 的多候选生成，请使用同协议渠道")

// OpenAIChatToAnthropic OpenAI Chat 请求 → Anthropic Messages 请求（DESIGN §7.1）
// 返回转换结果与被丢弃字段列表（relay 层据此设置 X-Keyway-Dropped）。
func OpenAIChatToAnthropic(req *OpenAIChatRequest) (*AnthropicMessagesRequest, []string, error) {
	if req.N > 1 {
		return nil, nil, ErrNNotSupported
	}

	var dropped []string
	if len(req.ResponseFormat) > 0 {
		dropped = append(dropped, "response_format")
	}
	if req.ReasoningEffort != "" {
		dropped = append(dropped, "reasoning_effort")
	}
	if req.TopK > 0 {
		dropped = append(dropped, "top_k")
	}
	if len(req.LogitBias) > 0 {
		dropped = append(dropped, "logit_bias")
	}
	if req.PresencePenalty != nil {
		dropped = append(dropped, "presence_penalty")
	}
	if req.FrequencyPenalty != nil {
		dropped = append(dropped, "frequency_penalty")
	}
	if req.StreamOptions != nil {
		dropped = append(dropped, "stream_options")
	}

	out := &AnthropicMessagesRequest{
		Model:         req.Model,
		Stream:        req.Stream,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
	}
	if req.User != "" {
		out.Metadata = &AnthropicMetadata{UserID: req.User}
	}

	// max_tokens：max_tokens 优先，其次 max_completion_tokens，缺省 DefaultMaxTokens
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = req.MaxCompletionTokens
	}
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	out.MaxTokens = maxTokens

	// tools
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, AnthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	// tool_choice：string | 对象
	out.ToolChoice = openAIToolChoiceToAnthropic(req.ToolChoice)

	// system 收集 + 消息合并（anthropic 要求 user/assistant 交替）
	var sysTexts []string
	var blocks []AnthropicContentBlock
	curRole := ""
	flush := func() {
		if curRole != "" && len(blocks) > 0 {
			out.Messages = append(out.Messages, AnthropicInMessage{Role: curRole, Content: blocks})
		}
		blocks = nil
		curRole = ""
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			sysTexts = append(sysTexts, openAITexts(m.Content)...)
		case "user":
			if curRole != "user" {
				flush()
				curRole = "user"
			}
			blocks = append(blocks, openAIBlocks(m.Content)...)
		case "assistant":
			if curRole != "assistant" {
				flush()
				curRole = "assistant"
			}
			if texts := openAITexts(m.Content); len(texts) > 0 {
				blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: strings.Join(texts, "")})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: normalizeJSONObject([]byte(tc.Function.Arguments)),
				})
			}
		case "tool":
			if curRole != "user" {
				flush()
				curRole = "user"
			}
			content := strings.Join(openAITexts(m.Content), "\n")
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   content,
			})
		}
	}
	flush()

	if len(sysTexts) > 0 {
		out.System = strings.Join(sysTexts, "\n")
	}
	return out, dropped, nil
}

// systemHasCacheControl system 块是否带 cache_control
func systemHasCacheControl(sys any) bool {
	switch v := sys.(type) {
	case []AnthropicSystemBlock:
		for _, b := range v {
			if b.CacheControl != nil {
				return true
			}
		}
	case []any:
		for _, item := range v {
			raw, _ := json.Marshal(item)
			var b AnthropicSystemBlock
			if json.Unmarshal(raw, &b) == nil && b.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

// openAIToolChoiceToAnthropic auto/none/required/{function} → auto/none/any/tool
func openAIToolChoiceToAnthropic(tc any) *AnthropicToolChoice {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return &AnthropicToolChoice{Type: "auto"}
		case "none":
			return &AnthropicToolChoice{Type: "none"}
		case "required":
			return &AnthropicToolChoice{Type: "any"}
		}
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return &AnthropicToolChoice{Type: "tool", Name: name}
			}
		}
	}
	return nil
}

// normalizeJSONObject 校验并归一化 JSON 对象（失败返回 {}）
func normalizeJSONObject(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return json.RawMessage("{}")
	}
	return marshalCompact(obj)
}

// AnthropicToOpenAIChat Anthropic Messages 请求 → OpenAI Chat 请求（DESIGN §7.2）
// thinking 块与 cache_control 剥离并计入丢弃列表。
func AnthropicToOpenAIChat(req *AnthropicMessagesRequest) (*OpenAIChatRequest, []string) {
	dropped := []string{}
	hasThinking := false
	hasCacheControl := systemHasCacheControl(req.System)

	out := &OpenAIChatRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.StopSequences,
	}
	if req.Metadata != nil {
		out.User = req.Metadata.UserID
	}

	// tools
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	// tool_choice 反向
	if tc := req.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto":
			out.ToolChoice = "auto"
		case "none":
			out.ToolChoice = "none"
		case "any":
			out.ToolChoice = "required"
		case "tool":
			out.ToolChoice = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}
		}
	}

	// system
	if sys := anthropicSystemText(req.System); sys != "" {
		out.Messages = append(out.Messages, OpenAIChatMessage{Role: "system", Content: sys})
	}

	for _, m := range req.Messages {
		blocks := anthropicBlocks(m.Content)
		switch m.Role {
		case "user":
			// tool_result → role=tool 消息；text/image → user 消息
			var parts []OpenAIContentPart
			for _, b := range blocks {
				if b.CacheControl != nil {
					hasCacheControl = true
				}
				switch b.Type {
				case "tool_result":
					out.Messages = append(out.Messages, OpenAIChatMessage{
						Role:       "tool",
						ToolCallID: b.ToolUseID,
						Content:    toolResultText(b.Content),
					})
				case "text":
					if b.Text != "" {
						parts = append(parts, OpenAIContentPart{Type: "text", Text: b.Text})
					}
				case "image":
					if u := imageURLFromSource(b.Source); u != "" {
						parts = append(parts, OpenAIContentPart{Type: "image_url", ImageURL: &OpenAIImageURL{URL: u}})
					}
				case "thinking":
					hasThinking = true
				}
			}
			if len(parts) == 1 && parts[0].Type == "text" {
				out.Messages = append(out.Messages, OpenAIChatMessage{Role: "user", Content: parts[0].Text})
			} else if len(parts) > 0 {
				out.Messages = append(out.Messages, OpenAIChatMessage{Role: "user", Content: parts})
			}
		case "assistant":
			var text strings.Builder
			var toolCalls []OpenAIToolCall
			for _, b := range blocks {
				if b.CacheControl != nil {
					hasCacheControl = true
				}
				switch b.Type {
				case "text":
					text.WriteString(b.Text)
				case "tool_use":
					toolCalls = append(toolCalls, OpenAIToolCall{
						ID:   b.ID,
						Type: "function",
						Function: OpenAIFunctionCall{
							Name:      b.Name,
							Arguments: string(b.Input),
						},
					})
				case "thinking":
					hasThinking = true
				}
			}
			msg := OpenAIChatMessage{Role: "assistant", Content: text.String()}
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
			}
			out.Messages = append(out.Messages, msg)
		}
	}

	if hasThinking {
		dropped = append(dropped, "thinking")
	}
	if hasCacheControl {
		dropped = append(dropped, "cache_control")
	}
	return out, dropped
}
