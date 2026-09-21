package relay

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestClassifyDataLine 各方言下错误帧/终止标记/普通帧的判定
func TestClassifyDataLine(t *testing.T) {
	cases := []struct {
		name    string
		dialect streamDialect
		line    string
		want    sseFrameKind
	}{
		// chat
		{"chat DONE", dialectChat, "data: [DONE]", frameTerminal},
		{"chat DONE 无空格", dialectChat, "data:[DONE]", frameTerminal},
		{"chat 普通增量", dialectChat, `data: {"id":"x","choices":[{"delta":{"content":"hi"}}]}`, frameData},
		{"chat 顶层 error 对象", dialectChat, `data: {"error":{"message":"insufficient credits","type":"upstream_error"}}`, frameError},
		{"chat error 空对象不算", dialectChat, `data: {"error":{}}`, frameData}, // len<=2 的 {}
		{"chat 非法 JSON", dialectChat, "data: not-json", frameData},
		{"chat 注释行", dialectChat, ": ping", frameData},
		{"chat event 行", dialectChat, "event: message", frameData},
		{"chat 空 data 行", dialectChat, "data:", frameData},
		// responses
		{"responses created", dialectResponses, `data: {"type":"response.created","sequence_number":0}`, frameData},
		{"responses 错误事件", dialectResponses, `data: {"type":"error","sequence_number":0,"error":{"type":"upstream_error","code":"stream_read_error","message":"stream_read_error"}}`, frameError},
		{"responses completed", dialectResponses, `data: {"type":"response.completed","sequence_number":5}`, frameTerminal},
		{"responses failed", dialectResponses, `data: {"type":"response.failed","sequence_number":2}`, frameTerminal},
		{"responses incomplete", dialectResponses, `data: {"type":"response.incomplete","sequence_number":3}`, frameTerminal},
		{"responses cancelled", dialectResponses, `data: {"type":"response.cancelled","sequence_number":3}`, frameTerminal},
		// anthropic
		{"anthropic message_start", dialectAnthropic, `data: {"type":"message_start","message":{}}`, frameData},
		{"anthropic message_stop", dialectAnthropic, `data: {"type":"message_stop"}`, frameTerminal},
		{"anthropic error 事件", dialectAnthropic, `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, frameError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDataLine(tc.dialect, []byte(tc.line)); got != tc.want {
				t.Fatalf("classifyDataLine(%s) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}
}

// TestStreamTracker结局判定 透传喂行后的结局分类
func TestStreamTracker结局判定(t *testing.T) {
	// 正常：增量 + 终止
	tr := newStreamTracker(dialectChat)
	tr.feedLine([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}"))
	tr.feedLine([]byte("data: [DONE]"))
	if fate, _ := tr.ended(); fate != fateOK {
		t.Fatalf("应判定正常，got %d", fate)
	}
	// 错误帧
	tr = newStreamTracker(dialectResponses)
	tr.feedLine([]byte(`data: {"type":"error","sequence_number":0,"error":{"type":"upstream_error","code":"stream_read_error","message":"stream_read_error"}}`))
	fate, summary := tr.ended()
	if fate != fateErrFrame || summary == "" {
		t.Fatalf("应判定错误帧，got %d %q", fate, summary)
	}
	// 异常终止：有数据但未见终止
	tr = newStreamTracker(dialectAnthropic)
	tr.feedLine([]byte(`data: {"type":"message_start","message":{}}`))
	if fate, _ := tr.ended(); fate != fateAborted {
		t.Fatalf("应判定异常终止，got %d", fate)
	}
	// 客户端断开优先级最高
	tr.markClientGone()
	if fate, _ := tr.ended(); fate != fateClientGone {
		t.Fatalf("应判定客户端断开，got %d", fate)
	}
	// 错误帧晚于终止标记仍计失败
	tr = newStreamTracker(dialectChat)
	tr.feedLine([]byte("data: [DONE]"))
	tr.feedLine([]byte(`data: {"error":{"message":"late"}}`))
	if fate, _ := tr.ended(); fate != fateErrFrame {
		t.Fatalf("错误后置仍应计失败，got %d", fate)
	}
}

// TestPeekFirstData错误帧不提交 oct-yescode 场景：上游 200 + 首帧 SSE 错误事件
func TestPeekFirstData错误帧不提交(t *testing.T) {
	body := "data: {\"type\":\"error\",\"sequence_number\":0,\"error\":{\"type\":\"upstream_error\",\"code\":\"stream_read_error\",\"message\":\"stream_read_error\"}}\n\n"
	resp := peekTestResponse(t, body)
	pk := peekFirstData(resp, dialectResponses, time.Second, context.Background())
	if pk.committed {
		t.Fatal("首帧为错误事件时不应提交")
	}
	if pk.fate != fateErrFrame {
		t.Fatalf("应判定错误帧，got %d", fate0(pk.fate))
	}
	if pk.summary == "" {
		t.Fatal("应有错误摘要")
	}
}

// TestPeekFirstData正常流提交 首个数据帧为正常内容 → 提交并带回缓冲
func TestPeekFirstData正常流提交(t *testing.T) {
	body := ": ping\n\nevent: message\n\ndata: {\"type\":\"message_start\",\"message\":{}}\n\ndata: {\"type\":\"content_block_delta\"}\n\n"
	resp := peekTestResponse(t, body)
	pk := peekFirstData(resp, dialectAnthropic, time.Second, context.Background())
	if !pk.committed {
		t.Fatalf("正常流应提交，summary=%s", pk.summary)
	}
	if !bytes.Contains(pk.buf, []byte("message_start")) {
		t.Fatal("提交时应带回窥探缓冲字节")
	}
}

// TestPeekFirstData零数据终止不提交 上游 200 但流立即 EOF
func TestPeekFirstData零数据终止不提交(t *testing.T) {
	resp := peekTestResponse(t, "")
	pk := peekFirstData(resp, dialectChat, time.Second, context.Background())
	if pk.committed {
		t.Fatal("零数据终止不应提交")
	}
	if pk.fate != fateAborted {
		t.Fatalf("应判定异常终止，got %d", fate0(pk.fate))
	}
}

// TestPeekFirstData超时不提交 上游挂起不发首帧 → 超时关闭并判失败
func TestPeekFirstData超时不提交(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release // 挂起：不发任何数据
	}))
	defer srv.Close()
	defer close(release)

	resp, err := http.Post(srv.URL, "POST", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	pk := peekFirstData(resp, dialectChat, 300*time.Millisecond, context.Background())
	if pk.committed {
		t.Fatal("首帧超时不应提交")
	}
	if pk.fate != fateAborted {
		t.Fatalf("超时应判异常终止，got %d", fate0(pk.fate))
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("应在超时时长后返回")
	}
}

// TestPeekFirstData终止标记为首帧也提交 空响应（首帧即 [DONE]）是合法流
func TestPeekFirstData终止标记为首帧也提交(t *testing.T) {
	resp := peekTestResponse(t, "data: [DONE]\n\n")
	pk := peekFirstData(resp, dialectChat, time.Second, context.Background())
	if !pk.committed {
		t.Fatalf("首帧为终止标记的空流应提交，summary=%s", pk.summary)
	}
}

// peekTestResponse 构造流式上游响应（读完全部 body 后关闭，测试结束清理）
func peekTestResponse(t *testing.T, body string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "POST", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func fate0(f streamFate) int { return int(f) }
