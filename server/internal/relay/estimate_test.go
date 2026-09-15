package relay

import (
	"testing"
)

// TestEstimateRequestTokens 入站请求 prompt 估算（openai / anthropic 双协议）
func TestEstimateRequestTokens(t *testing.T) {
	cases := []struct {
		name    string
		inbound string
		body    string
		min     int
	}{
		{
			name:    "openai 消息与工具",
			inbound: "openai",
			body:    `{"model":"m","messages":[{"role":"user","content":"你好，这是一段用于估算的文本内容"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"查询天气"}}]}`,
			min:     10,
		},
		{
			name:    "anthropic 消息",
			inbound: "anthropic",
			body:    `{"model":"m","max_tokens":10,"system":"你是助手","messages":[{"role":"user","content":"估算测试文本"}]}`,
			min:     3,
		},
		{
			name:    "非法 JSON 返回 0",
			inbound: "openai",
			body:    `{`,
			min:     0,
		},
	}
	for _, tc := range cases {
		if got := estimateRequestTokens(tc.inbound, []byte(tc.body)); got < tc.min {
			t.Errorf("%s: 期望 ≥%d，实际 %d", tc.name, tc.min, got)
		}
	}
}

// TestStreamTally 流式增量文本累计（OpenAI chunk / Anthropic 事件 / Responses delta）
func TestStreamTally(t *testing.T) {
	var tally streamTally
	lines := []string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"这是回答的第一段内容"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"思考过程文本"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"{\"city\":"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Anthropic 增量文本"}}`,
		`data: {"type":"response.output_text.delta","delta":"Responses 增量文本"}`,
		`data: [DONE]`,
		`: 注释行应被忽略`,
		``,
	}
	for _, l := range lines {
		tally.feed([]byte(l + "\n"))
	}
	// 累计 52 runes：ceil(52/3.6) = 15
	if got := tally.tokens(); got != 15 {
		t.Fatalf("期望 15 tokens（52 runes ÷ 3.6 向上取整），实际 %d", got)
	}
}

// TestEstimateResponseTokens 非流式响应文本估算（三种协议结构）
func TestEstimateResponseTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int // -1 表示只断言 > 0
	}{
		{
			name: "openai string content",
			body: `{"choices":[{"message":{"role":"assistant","content":"回答内容若干文字"}}]}`,
			want: -1,
		},
		{
			name: "openai 多模态段",
			body: `{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"多模态回答文本"}]}}]}`,
			want: -1,
		},
		{
			name: "anthropic content 块",
			body: `{"type":"message","role":"assistant","content":[{"type":"text","text":"Anthropic 回答内容"}]}`,
			want: -1,
		},
		{
			name: "responses output",
			body: `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"Responses 回答内容"}]}]}`,
			want: -1,
		},
		{
			name: "无 content 字段",
			body: `{"object":"list","data":[]}`,
			want: 0,
		},
	}
	for _, tc := range cases {
		got := estimateResponseTokens([]byte(tc.body))
		if tc.want == 0 && got != 0 {
			t.Errorf("%s: 期望 0，实际 %d", tc.name, got)
		}
		if tc.want == -1 && got <= 0 {
			t.Errorf("%s: 期望 >0，实际 %d", tc.name, got)
		}
	}
}

// TestEnsureIncludeUsage 流式注入 include_usage；非流式与显式 false 不注入
func TestEnsureIncludeUsage(t *testing.T) {
	m := map[string]any{"stream": true}
	ensureIncludeUsage(m)
	so, ok := m["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("流式请求应注入 include_usage: %+v", m)
	}
	// 已有 stream_options：补齐 include_usage
	m2 := map[string]any{"stream": true, "stream_options": map[string]any{}}
	ensureIncludeUsage(m2)
	if so2, _ := m2["stream_options"].(map[string]any); so2["include_usage"] != true {
		t.Fatalf("已有 stream_options 应补齐 include_usage: %+v", m2)
	}
	// 显式 false：尊重原值
	m3 := map[string]any{"stream": true, "stream_options": map[string]any{"include_usage": false}}
	ensureIncludeUsage(m3)
	if so3, _ := m3["stream_options"].(map[string]any); so3["include_usage"] != false {
		t.Fatalf("显式 false 不应被覆盖: %+v", m3)
	}
	// 非流式：不动
	m4 := map[string]any{"stream": false}
	ensureIncludeUsage(m4)
	if _, ok := m4["stream_options"]; ok {
		t.Fatalf("非流式不应注入 stream_options: %+v", m4)
	}
}
