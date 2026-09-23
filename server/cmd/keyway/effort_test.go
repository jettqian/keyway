package main

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"keyway/internal/store"
)

// waitLastLog 轮询等待异步日志落库（默认 1s 批写），返回满足条件的最新一条
func waitLastLog(t *testing.T, c *ctx, match func(l *store.Log) bool) store.Log {
	t.Helper()
	var last store.Log
	for i := 0; i < 40; i++ {
		var probe store.Log
		c.store.DB().Order("id DESC").First(&probe)
		if probe.ID != 0 && match(&probe) {
			last = probe
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last.ID == 0 {
		t.Fatal("等待日志落库超时")
	}
	return last
}

// TestE2E推理强度落库：三协议入站的推理强度抓取（chat 顶层
// reasoning_effort / responses reasoning.effort / anthropic thinking 预算归一
// thinking:N），未开启为 NULL
func TestE2E推理强度落库(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)

	// chat：顶层 reasoning_effort
	c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "test-model", "reasoning_effort": "high",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if l := waitLastLog(t, c, func(l *store.Log) bool { return l.ReasoningEffort != nil }); l.ReasoningEffort == nil || *l.ReasoningEffort != "high" {
		t.Fatalf("chat 推理强度应落库 high，实际 %v", l.ReasoningEffort)
	}

	// responses：reasoning.effort
	c.do("POST", "/v1/responses", map[string]any{
		"model": "test-model", "input": "hi", "reasoning": map[string]any{"effort": "low"},
	}, false)
	if l := waitLastLog(t, c, func(l *store.Log) bool { return l.ReasoningEffort != nil && *l.ReasoningEffort == "low" }); l.ID == 0 {
		t.Fatal("responses 推理强度未落库 low")
	}

	// anthropic 入站（透传到 mock /v1/messages）：thinking 预算归一 thinking:N
	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"test-model","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("anthropic 请求失败: %d %s", w.Code, w.Body.String())
	}
	if l := waitLastLog(t, c, func(l *store.Log) bool { return l.ReasoningEffort != nil && *l.ReasoningEffort == "thinking:2048" }); l.ID == 0 {
		t.Fatal("anthropic thinking 预算未归一落库 thinking:2048")
	}

	// 未开启推理：NULL
	c.do("POST", "/v1/chat/completions", map[string]any{
		"model":    "test-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if l := waitLastLog(t, c, func(l *store.Log) bool { return l.ReasoningEffort == nil }); l.ReasoningEffort != nil {
		t.Fatalf("未开启推理应为 NULL，实际 %v", *l.ReasoningEffort)
	}
}
