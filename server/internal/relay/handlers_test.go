package relay

import (
	"testing"

	"keyway/internal/convert"
)

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
