package relay

import (
	"testing"

	"keyway/internal/convert"
)

// TestReasoningEffortOf 三协议入站的推理强度解析口径
func TestReasoningEffortOf(t *testing.T) {
	cases := []struct {
		name    string
		inbound string
		body    string
		want    string
	}{
		{name: "chat顶层effort", inbound: "openai", body: `{"model":"gpt-5","reasoning_effort":"high","messages":[]}`, want: "high"},
		{name: "responses嵌套effort", inbound: "openai", body: `{"model":"gpt-5","reasoning":{"effort":"low"}}`, want: "low"},
		{name: "chat未开启", inbound: "openai", body: `{"model":"gpt-4o","messages":[]}`, want: ""},
		{name: "顶层优先于嵌套", inbound: "openai", body: `{"reasoning_effort":"medium","reasoning":{"effort":"low"}}`, want: "medium"},
		{name: "anthropic预算", inbound: "anthropic", body: `{"model":"claude-sonnet-4","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":1024}}`, want: "thinking:1024"},
		{name: "anthropic预算为零", inbound: "anthropic", body: `{"thinking":{"budget_tokens":0}}`, want: ""},
		{name: "anthropic未开启", inbound: "anthropic", body: `{"model":"claude-3-5-haiku","max_tokens":100}`, want: ""},
		{name: "非法JSON", inbound: "openai", body: `{`, want: ""},
	}
	for _, tc := range cases {
		if got := reasoningEffortOf(tc.inbound, []byte(tc.body)); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestSniffUsage 回归：透传流式行含 "data:" 前缀时也要能解析 usage（线上 e2e 发现的 bug）
func TestSniffUsage(t *testing.T) {
	cases := []struct {
		name string
		line string
		want convert.Usage
	}{
		{
			name: "SSE行带前缀",
			line: `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`,
			want: convert.Usage{PromptTokens: 12, CompletionTokens: 8},
		},
		{
			name: "完整响应体",
			line: `{"usage":{"prompt_tokens":5,"completion_tokens":7}}`,
			want: convert.Usage{PromptTokens: 5, CompletionTokens: 7},
		},
		{
			name: "无usage行",
			line: `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			want: convert.Usage{},
		},
	}
	for _, c := range cases {
		got := sniffUsage(convert.Usage{}, []byte(c.line))
		if got != c.want {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
