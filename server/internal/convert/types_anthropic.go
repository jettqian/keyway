package convert

import "encoding/json"

// ---------- Anthropic 侧类型 ----------

// AnthropicMessagesRequest 入站 Anthropic 格式请求
type AnthropicMessagesRequest struct {
	Model         string               `json:"model"`
	Messages      []AnthropicInMessage `json:"messages"`
	System        any                  `json:"system,omitempty"` // string 或 []AnthropicSystemBlock
	MaxTokens     int                  `json:"max_tokens"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	Tools         []AnthropicTool      `json:"tools,omitempty"`
	ToolChoice    *AnthropicToolChoice `json:"tool_choice,omitempty"`
	Metadata      *AnthropicMetadata   `json:"metadata,omitempty"`
}

type AnthropicSystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type CacheControl struct {
	Type string `json:"type"`
}

type AnthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// AnthropicInMessage Content 为 string 或 []AnthropicContentBlock
type AnthropicInMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// AnthropicContentBlock 统一内容块（text/image/tool_use/tool_result/thinking）
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	Source *AnthropicImageSource `json:"source,omitempty"` // image

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use

	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result
	Content   any    `json:"content,omitempty"`     // tool_result：string 或块数组

	Thinking  string `json:"thinking,omitempty"`  // thinking
	Signature string `json:"signature,omitempty"` // thinking

	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type AnthropicImageSource struct {
	Type      string `json:"type"` // url | base64
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type AnthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type AnthropicToolChoice struct {
	Type string `json:"type"` // auto | any | none | tool
	Name string `json:"name,omitempty"`
}

// AnthropicUsage usage 结构（三段互斥：input / cache_read / cache_creation）
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// AnthropicMessagesResponse 非流式响应
type AnthropicMessagesResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Model        string                  `json:"model"`
	Content      []AnthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicStreamEvent SSE 事件统一结构
type AnthropicStreamEvent struct {
	Type         string                     `json:"type"`
	Message      *AnthropicMessagesResponse `json:"message,omitempty"` // message_start
	Index        int                        `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock     `json:"content_block,omitempty"` // content_block_start
	Delta        *AnthropicStreamDelta      `json:"delta,omitempty"`         // content_block_delta / message_delta
	Usage        *AnthropicUsage            `json:"usage,omitempty"`         // message_delta
}

type AnthropicStreamDelta struct {
	Type        string `json:"type,omitempty"` // text_delta | input_json_delta | thinking_delta | signature_delta
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// AnthropicErrorBody Anthropic 错误结构
type AnthropicErrorBody struct {
	Type  string               `json:"type"`
	Error AnthropicErrorDetail `json:"error"`
}

type AnthropicErrorDetail struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
}
