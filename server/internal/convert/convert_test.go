package convert

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAIChatToAnthropic基础对话(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "req_basic_openai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req OpenAIChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	out, dropped, err := OpenAIChatToAnthropic(&req)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 0 {
		t.Fatalf("不应有丢弃字段: %v", dropped)
	}
	if out.Model != "gpt-4o" || out.MaxTokens != 1024 {
		t.Fatalf("模型或 max_tokens 错误: %+v", out)
	}
	if out.System != "你是一个翻译助手" {
		t.Fatalf("system 错误: %q", out.System)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Fatalf("消息数/角色错误: %d %+v", len(out.Messages), out.Messages)
	}
}

func TestOpenAIChatToAnthropic多轮工具(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "req_tools_openai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req OpenAIChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	out, _, err := OpenAIChatToAnthropic(&req)
	if err != nil {
		t.Fatal(err)
	}
	// 期望序列：user / assistant(tool_use) / user(tool_result+文本) —— tool_result 与文本合并进同一条 user
	if len(out.Messages) != 3 {
		t.Fatalf("消息数错误: %d（连续 user/tool 应合并）", len(out.Messages))
	}
	blocks := anthropicBlocks(out.Messages[1].Content)
	if len(blocks) != 1 || blocks[0].Type != "tool_use" {
		t.Fatalf("assistant 块错误: %+v", blocks)
	}
	if blocks[0].ID != "call_1" || blocks[0].Name != "get_weather" {
		t.Fatalf("tool_use 字段错误: %+v", blocks[0])
	}
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Fatalf("input 非法 JSON: %v", err)
	}
	if input["city"] != "北京" {
		t.Fatalf("input 内容错误: %v", input)
	}
	resultBlocks := anthropicBlocks(out.Messages[2].Content)
	if len(resultBlocks) != 2 || resultBlocks[0].Type != "tool_result" || resultBlocks[1].Type != "text" {
		t.Fatalf("tool_result 合并错误: %+v", resultBlocks)
	}
	if resultBlocks[0].ToolUseID != "call_1" {
		t.Fatalf("tool_use_id 错误: %+v", resultBlocks[0])
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "get_weather" {
		t.Fatalf("tools 映射错误: %+v", out.Tools)
	}
	if out.ToolChoice == nil || out.ToolChoice.Type != "auto" {
		t.Fatalf("tool_choice 映射错误: %+v", out.ToolChoice)
	}
}

func TestOpenAIChatToAnthropic图片与丢弃字段(t *testing.T) {
	req := &OpenAIChatRequest{
		Model:           "gpt-4o",
		N:               1,
		Stream:          true,
		StreamOptions:   &StreamOptions{IncludeUsage: true},
		ResponseFormat:  json.RawMessage(`{"type":"json_object"}`),
		ReasoningEffort: "high",
		Messages: []OpenAIChatMessage{
			{Role: "user", Content: []OpenAIContentPart{
				{Type: "text", Text: "这是什么"},
				{Type: "image_url", ImageURL: &OpenAIImageURL{URL: "https://example.com/a.png"}},
				{Type: "image_url", ImageURL: &OpenAIImageURL{URL: "data:image/jpeg;base64,QUJD"}},
			}},
		},
	}
	out, dropped, err := OpenAIChatToAnthropic(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"response_format", "reasoning_effort", "stream_options"}
	if !reflect.DeepEqual(dropped, want) {
		t.Fatalf("丢弃字段错误: %v（期望 %v）", dropped, want)
	}
	if out.MaxTokens != DefaultMaxTokens {
		t.Fatalf("max_tokens 缺省错误: %d", out.MaxTokens)
	}
	blocks := anthropicBlocks(out.Messages[0].Content)
	if len(blocks) != 3 {
		t.Fatalf("块数错误: %+v", blocks)
	}
	if blocks[1].Source.Type != "url" || blocks[1].Source.URL != "https://example.com/a.png" {
		t.Fatalf("url source 错误: %+v", blocks[1].Source)
	}
	if blocks[2].Source.Type != "base64" || blocks[2].Source.MediaType != "image/jpeg" || blocks[2].Source.Data != "QUJD" {
		t.Fatalf("base64 source 错误: %+v", blocks[2].Source)
	}
}

func TestOpenAIChatToAnthropic拒绝N大于一(t *testing.T) {
	_, _, err := OpenAIChatToAnthropic(&OpenAIChatRequest{N: 2})
	if err != ErrNNotSupported {
		t.Fatalf("应返回 ErrNNotSupported: %v", err)
	}
}

func TestAnthropicToOpenAIChat剥离与映射(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "req_tools_anthropic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req AnthropicMessagesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	out, dropped := AnthropicToOpenAIChat(&req)
	if len(dropped) != 2 || dropped[0] != "thinking" || dropped[1] != "cache_control" {
		t.Fatalf("丢弃字段错误: %v", dropped)
	}
	if out.MaxTokens != 512 || out.User != "user-1" {
		t.Fatalf("max_tokens/user 错误: %+v", out)
	}
	if out.Stop == nil || !reflect.DeepEqual(out.Stop, []string{"END"}) {
		t.Fatalf("stop_sequences 映射错误: %v", out.Stop)
	}
	// 序列：system / user / assistant(tool_calls) / tool / user
	if len(out.Messages) != 5 {
		t.Fatalf("消息数错误: %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "系统提示" {
		t.Fatalf("system 错误: %+v", out.Messages[0])
	}
	assistant := out.Messages[2]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant 错误: %+v", assistant)
	}
	if assistant.ToolCalls[0].Function.Arguments != `{"city":"北京"}` {
		t.Fatalf("arguments 错误: %s", assistant.ToolCalls[0].Function.Arguments)
	}
	toolMsg := out.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "晴，26 度" {
		t.Fatalf("tool 消息错误: %+v", toolMsg)
	}
	if out.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools 映射错误: %+v", out.Tools)
	}
}

func Test请求双向RoundTrip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "req_tools_openai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var orig OpenAIChatRequest
	if err := json.Unmarshal(raw, &orig); err != nil {
		t.Fatal(err)
	}
	an, dropped, err := OpenAIChatToAnthropic(&orig)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 0 {
		t.Fatalf("源请求无丢弃字段: %v", dropped)
	}
	back, _ := AnthropicToOpenAIChat(an)
	// 语义等价：消息角色序列与文本
	var roles1, roles2 []string
	var texts1, texts2 []string
	for _, m := range orig.Messages {
		roles1 = append(roles1, m.Role)
		texts1 = append(texts1, strings.Join(openAITexts(m.Content), ""))
	}
	for _, m := range back.Messages {
		roles2 = append(roles2, m.Role)
		texts2 = append(texts2, strings.Join(openAITexts(m.Content), ""))
	}
	// tool 消息在 anthropic 侧并入 user：比较时把 tool 视作 user
	norm := func(rs []string) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			if r == "tool" {
				r = "user"
			}
			out = append(out, r)
		}
		return out
	}
	if !reflect.DeepEqual(norm(roles1), norm(roles2)) {
		t.Fatalf("角色序列不等价: %v vs %v", norm(roles1), norm(roles2))
	}
	if !reflect.DeepEqual(texts1, texts2) {
		t.Fatalf("文本不等价: %v vs %v", texts1, texts2)
	}
	if !reflect.DeepEqual(orig.Stop, back.Stop) {
		t.Fatalf("stop 不等价")
	}
}

func Test响应转换双向(t *testing.T) {
	anRaw, err := os.ReadFile(filepath.Join("testdata", "resp_anthropic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var anResp AnthropicMessagesResponse
	if err := json.Unmarshal(anRaw, &anResp); err != nil {
		t.Fatal(err)
	}
	oiResp := AnthropicToOpenAIResponse(&anResp)
	choice := oiResp.Choices[0]
	if choice.Message.Content != "北京今天晴" {
		t.Fatalf("content 错误: %q", choice.Message.Content)
	}
	if choice.Message.ReasoningContent != "先查天气" {
		t.Fatalf("reasoning 错误: %q", choice.Message.ReasoningContent)
	}
	if len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls 错误: %+v", choice.Message.ToolCalls)
	}
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason 错误: %q", choice.FinishReason)
	}
	if oiResp.Usage.PromptTokens != 100+50+20 {
		t.Fatalf("usage 归一错误: %+v", oiResp.Usage)
	}
	if oiResp.Usage.PromptTokensDetails == nil || oiResp.Usage.PromptTokensDetails.CachedTokens != 50 {
		t.Fatalf("缓存明细缺失: %+v", oiResp.Usage)
	}

	// 反向：prompt=170（含缓存读 50）→ input=120, cache_read=50（cache_write 有损并入 input）
	back := OpenAIToAnthropicResponse(oiResp)
	if back.StopReason != "tool_use" {
		t.Fatalf("stop_reason 反向错误: %q", back.StopReason)
	}
	if back.Usage.InputTokens != 120 || back.Usage.CacheReadInputTokens != 50 {
		t.Fatalf("usage 反向错误: %+v", back.Usage)
	}
	var blockTypes []string
	for _, b := range back.Content {
		blockTypes = append(blockTypes, b.Type)
	}
	if !reflect.DeepEqual(blockTypes, []string{"thinking", "text", "tool_use"}) {
		t.Fatalf("块序列错误: %v", blockTypes)
	}
}

func Test错误转换(t *testing.T) {
	oi := OpenAIErrorToAnthropic([]byte(`{"error":{"message":"额度不足","type":"insufficient_quota","code":429}}`))
	var body AnthropicErrorBody
	if err := json.Unmarshal(oi, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Message != "额度不足" || body.Error.Type != "insufficient_quota" {
		t.Fatalf("错误转换错误: %s", oi)
	}
	an := AnthropicErrorToOpenAI(oi)
	var ob OpenAIErrorBody
	if err := json.Unmarshal(an, &ob); err != nil {
		t.Fatal(err)
	}
	if ob.Error.Message != "额度不足" || ob.Error.Type != "insufficient_quota" {
		t.Fatalf("错误反向转换错误: %s", an)
	}
}

func TestNormalizeUsage(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want Usage
	}{
		{"openai含缓存明细", &OpenAIUsage{
			PromptTokens: 1000, CompletionTokens: 200,
			PromptTokensDetails: &PromptTokensDetails{CachedTokens: 700},
		}, Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 700}},
		{"openai DeepSeek旧字段", &OpenAIUsage{
			PromptTokens: 1000, CompletionTokens: 200, PromptCacheHitTokens: 800,
		}, Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 800}},
		{"anthropic三段互斥", &AnthropicUsage{
			InputTokens: 500, CacheCreationInputTokens: 100, CacheReadInputTokens: 700, OutputTokens: 90,
		}, Usage{PromptTokens: 1300, CompletionTokens: 90, CachedTokens: 700, CacheWriteTokens: 100}},
		{"空值", nil, Usage{}},
	}
	for _, c := range cases {
		var got Usage
		switch v := c.in.(type) {
		case *OpenAIUsage:
			got = NormalizeOpenAIUsage(v)
		case *AnthropicUsage:
			got = NormalizeAnthropicUsage(v)
		}
		if got != c.want {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func Test流式AN转OI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream_anthropic.txt"))
	if err != nil {
		t.Fatal(err)
	}
	conv := NewAnthropicToOpenAIStream()
	var out bytes.Buffer
	done := false
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		outb, d, err := conv.Feed([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(outb)
		if d {
			done = true
		}
	}
	if !done {
		t.Fatal("流未终结")
	}
	s := out.String()
	if !strings.Contains(s, `"role":"assistant"`) {
		t.Fatalf("缺少角色首块: %s", s)
	}
	if !strings.Contains(s, `"content":"你好"`) {
		t.Fatalf("缺少文本增量: %s", s)
	}
	if !strings.Contains(s, `"reasoning_content":"思考"`) {
		t.Fatalf("缺少思考增量: %s", s)
	}
	if !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("缺少工具首块: %s", s)
	}
	if !strings.Contains(s, `"arguments":"{\"city\":"`) {
		t.Fatalf("缺少工具参数增量: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"tool_calls"`) {
		t.Fatalf("缺少 finish_reason: %s", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("缺少 [DONE]: %s", s)
	}
	u := conv.Usage()
	if u.PromptTokens != 100+50+20 || u.CompletionTokens != 42 || u.CachedTokens != 50 {
		t.Fatalf("流式 usage 错误: %+v", u)
	}
}

func Test流式OI转AN(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream_openai.txt"))
	if err != nil {
		t.Fatal(err)
	}
	conv := NewOpenAIToAnthropicStream()
	var out bytes.Buffer
	done := false
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		outb, d, err := conv.Feed([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(outb)
		if d {
			done = true
		}
	}
	if !done {
		t.Fatal("流未终结")
	}
	s := out.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"type":"thinking"`,
		`"thinking_delta"`,
		`"text_delta"`,
		`"type":"tool_use"`,
		`"input_json_delta"`,
		"event: content_block_stop",
		"event: message_delta",
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺少 %s\n输出:\n%s", want, s)
		}
	}
	// 每个打开的块必须恰好关闭一次
	opens := strings.Count(s, "event: content_block_start")
	closes := strings.Count(s, "event: content_block_stop")
	if opens != closes {
		t.Fatalf("块开合不配对: %d 开 %d 合\n%s", opens, closes, s)
	}
	u := conv.Usage()
	if u.PromptTokens != 120 || u.CompletionTokens != 42 || u.CachedTokens != 60 {
		t.Fatalf("流式 usage 错误: %+v", u)
	}
}

func Test流式截断Finish补全(t *testing.T) {
	conv := NewAnthropicToOpenAIStream()
	_, _, _ = conv.Feed([]byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3","content":[],"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`))
	_, _, _ = conv.Feed([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	_, _, _ = conv.Feed([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"部分"}}`))
	out, err := conv.Finish()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("Finish 未补 [DONE]: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"stop"`) {
		t.Fatalf("Finish 未补终结 chunk: %s", s)
	}

	conv2 := NewOpenAIToAnthropicStream()
	_, _, _ = conv2.Feed([]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	out2, err := conv2.Finish()
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(out2)
	if !strings.Contains(s2, "event: message_delta") || !strings.Contains(s2, "event: message_stop") {
		t.Fatalf("Finish 未补终结事件: %s", s2)
	}
}

func TestEstimateTokens(t *testing.T) {
	req := &AnthropicMessagesRequest{
		System: "系统提示词",
		Messages: []AnthropicInMessage{
			{Role: "user", Content: "帮我查一下北京今天的天气，最好给出出行建议"},
		},
		Tools: []AnthropicTool{{Name: "get_weather", Description: "查询城市天气", InputSchema: map[string]any{"type": "object"}}},
	}
	n := EstimateTokens(req)
	if n < 10 || n > 200 {
		t.Fatalf("估算不合理: %d", n)
	}
}
