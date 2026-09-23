package relay

import (
	"testing"

	"keyway/internal/convert"
)

// TestSniffUsageAnthropic anthropic 透传 usage 嗅探（v1.5.66）：非流式顶层 usage、
// 流式 message_start（input+缓存）与 message_delta（output）分片合并；
// openai / responses 形状不受影响
func TestSniffUsageAnthropic(t *testing.T) {
	// 非流式：input 18 + cache_read 1536 → 总输入 1554，缓存读 1536
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","usage":{"input_tokens":18,"output_tokens":16,"cache_read_input_tokens":1536}}`)
	u := sniffUsage(convert.Usage{}, body)
	if u.PromptTokens != 1554 || u.CachedTokens != 1536 || u.CompletionTokens != 16 {
		t.Fatalf("非流式 anthropic usage 解析错误: %+v", u)
	}

	// 流式：message_start（input+缓存）→ message_delta（output）逐行合并
	u = sniffUsage(convert.Usage{}, []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":18,"cache_read_input_tokens":1536,"output_tokens":1}}}`))
	if u.PromptTokens != 1554 || u.CachedTokens != 1536 {
		t.Fatalf("message_start 解析错误: %+v", u)
	}
	u = sniffUsage(u, []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":16}}`))
	if u.PromptTokens != 1554 || u.CachedTokens != 1536 || u.CompletionTokens != 16 {
		t.Fatalf("message_delta 合并错误: %+v", u)
	}

	// openai chat 形状不受影响
	u = sniffUsage(convert.Usage{}, []byte(`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":42,"prompt_tokens_details":{"cached_tokens":60}}}`))
	if u.PromptTokens != 120 || u.CompletionTokens != 42 || u.CachedTokens != 60 {
		t.Fatalf("openai usage 解析错误: %+v", u)
	}

	// responses 形状不受影响
	u = sniffUsage(convert.Usage{}, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":5,"input_tokens_details":{"cached_tokens":10}}}}`))
	if u.PromptTokens != 30 || u.CompletionTokens != 5 || u.CachedTokens != 10 {
		t.Fatalf("responses usage 解析错误: %+v", u)
	}

	// 无 usage 字段：原样返回
	u = sniffUsage(convert.Usage{PromptTokens: 7}, []byte(`{"type":"content_block_delta"}`))
	if u.PromptTokens != 7 {
		t.Fatalf("无 usage 应原样返回: %+v", u)
	}
}
