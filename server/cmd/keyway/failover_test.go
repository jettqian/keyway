package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"keyway/internal/store"
)

// failoverUpstream 可编程上游：按序返回预设状态码，并统计命中次数
type failoverUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int // 每次请求返回的状态码
	hits   int
}

func newFailoverUpstream(status int) *failoverUpstream {
	u := &failoverUpstream{status: status}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		st := u.status
		u.hits++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if st >= 400 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"error":{"message":"mock %d"}}`, st)
			return
		}
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	return u
}

func (u *failoverUpstream) hitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func (u *failoverUpstream) setStatus(st int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = st
}

// waitLogs 轮询等待指定模型的日志行（异步批写，满 200 条或 1s 刷盘）
func (c *ctx) waitLogs(t *testing.T, model string, want int) []store.Log {
	t.Helper()
	var logs []store.Log
	for i := 0; i < 50; i++ {
		c.store.DB().Where("model = ?", model).Order("id").Find(&logs)
		if len(logs) >= want {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return logs
}

// assertAttemptLogs 断言逐次尝试落日志：失败行数与错误摘要、成功行数（FR-L1）
func assertAttemptLogs(t *testing.T, logs []store.Log, wantFail, wantOK int, failStatus int) {
	t.Helper()
	fail, ok := 0, 0
	for _, l := range logs {
		if l.StatusCode == nil {
			continue
		}
		if *l.StatusCode == failStatus {
			fail++
			if l.Error == nil || !strings.Contains(*l.Error, fmt.Sprintf("上游 %d", failStatus)) {
				t.Fatalf("失败日志应有状态码错误摘要，实际: %v", l.Error)
			}
			if l.ChannelID == nil {
				t.Fatal("失败日志应记录渠道")
			}
		} else if *l.StatusCode == 200 {
			ok++
		}
	}
	if fail != wantFail || ok != wantOK {
		t.Fatalf("失败/成功日志行数 = %d/%d，期望 %d/%d（总行 %d）", fail, ok, wantFail, wantOK, len(logs))
	}
}

// setupFailover 注册用户并按顺序建 3 个渠道（共用一把密钥），令牌限定 [c1, c2, c3]
func setupFailover(t *testing.T, c *ctx, urls []string, model string) {
	t.Helper()
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	w := c.do("POST", "/api/keys", map[string]any{"name": "k-failover", "value": "upstream-key"}, true)
	if w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &keyResp)

	var channelIDs []int64
	for i, u := range urls {
		w = c.do("POST", "/api/channels", map[string]any{
			"name": fmt.Sprintf("fo-%d", i), "type": "openai",
			"baseUrls": []string{u},
			"keyIds":   []int64{keyResp.Key.ID},
			"models":   []string{model},
			"priority": 100 - i, "enabled": true,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道 %d 失败: %d %s", i, w.Code, w.Body.String())
		}
		var chResp struct {
			Channel struct {
				ID int64 `json:"id"`
			} `json:"channel"`
		}
		json.Unmarshal(w.Body.Bytes(), &chResp)
		channelIDs = append(channelIDs, chResp.Channel.ID)
	}

	w = c.do("POST", "/api/tokens", map[string]any{
		"name": "fo-token", "channelIds": channelIDs,
	}, true)
	if w.Code != 200 {
		t.Fatalf("建令牌失败: %d %s", w.Code, w.Body.String())
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext
}

// TestE2E失败切换按顺序切渠道 渠道1 失败后应切到渠道2（不跳过）
func TestE2E失败切换按顺序切渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newFailoverUpstream(503) // 渠道1：一直 503
	up2 := newFailoverUpstream(200) // 渠道2：正常
	up3 := newFailoverUpstream(200) // 渠道3：正常
	defer up1.srv.Close()
	defer up2.srv.Close()
	defer up3.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fo1", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupFailover(t, c, []string{up1.srv.URL, up2.srv.URL, up3.srv.URL}, "gpt-fo")

	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "gpt-fo", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 {
		t.Fatalf("应切换到渠道2 成功，得到 %d %s", w.Code, w.Body.String())
	}
	if up2.hitCount() == 0 {
		t.Fatalf("渠道2 应被尝试（hits=%d）", up2.hitCount())
	}
	if up3.hitCount() != 0 {
		t.Fatalf("渠道2 成功后不应再访问渠道3（hits=%d）", up3.hitCount())
	}
	// 逐次尝试落日志：渠道1 失败一条 + 渠道2 成功一条（FR-L1）
	assertAttemptLogs(t, c.waitLogs(t, "gpt-fo", 2), 1, 1, 503)
}

// TestE2E失败切换Responses按顺序切渠道 /v1/responses 管线同场景
func TestE2E失败切换Responses按顺序切渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newFailoverUpstream(503)
	up2 := newFailoverUpstream(200)
	up3 := newFailoverUpstream(200)
	defer up1.srv.Close()
	defer up2.srv.Close()
	defer up3.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fo2", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupFailover(t, c, []string{up1.srv.URL, up2.srv.URL, up3.srv.URL}, "gpt-fo")

	w := c.do("POST", "/v1/responses", map[string]any{
		"model": "gpt-fo", "input": "hi",
	}, false)
	if w.Code != 200 {
		t.Fatalf("responses 应切换到渠道2 成功，得到 %d %s", w.Code, w.Body.String())
	}
	if up2.hitCount() == 0 {
		t.Fatalf("渠道2 应被尝试（hits=%d）", up2.hitCount())
	}
	if up3.hitCount() != 0 {
		t.Fatalf("渠道2 成功后不应再访问渠道3（hits=%d）", up3.hitCount())
	}
}

// TestE2E失败切换多线路耗尽预算切下一渠道 渠道1 多条线路全部失败时，
// 预算耗尽后仍应换下一候选渠道（DESIGN §5.2 第 5 条）
func TestE2E失败切换多线路耗尽预算切下一渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1a := newFailoverUpstream(503)
	up1b := newFailoverUpstream(503)
	up1c := newFailoverUpstream(503) // 渠道1 共 3 条线路
	up2 := newFailoverUpstream(200)  // 渠道2 正常
	defer up1a.srv.Close()
	defer up1b.srv.Close()
	defer up1c.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fo3", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	// 渠道1 三条线路都失败；渠道2 正常
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	w := c.do("POST", "/api/keys", map[string]any{"name": "k", "value": "upstream-key"}, true)
	if w.Code != 200 {
		t.Fatalf("建密钥失败: %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &keyResp)
	mk := func(name string, urls []string, priority int) {
		w := c.do("POST", "/api/channels", map[string]any{
			"name": name, "type": "openai",
			"baseUrls": urls, "keyIds": []int64{keyResp.Key.ID},
			"models": []string{"gpt-multi"}, "priority": priority, "enabled": true,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道 %s 失败: %d %s", name, w.Code, w.Body.String())
		}
	}
	mk("ch1", []string{up1a.srv.URL, up1b.srv.URL, up1c.srv.URL}, 100)
	mk("ch2", []string{up2.srv.URL}, 50)
	w = c.do("POST", "/api/tokens", map[string]any{"name": "t"}, true)
	if w.Code != 200 {
		t.Fatalf("建令牌失败: %d", w.Code)
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext

	w = c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "gpt-multi", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 {
		t.Fatalf("渠道1 线路耗尽后应换到渠道2，得到 %d %s", w.Code, w.Body.String())
	}
	if up2.hitCount() == 0 {
		t.Fatalf("渠道2 应被尝试（hits=%d）", up2.hitCount())
	}
	// 逐次尝试落日志：渠道1 三条线路各一条失败 + 渠道2 成功一条（FR-L1）
	assertAttemptLogs(t, c.waitLogs(t, "gpt-multi", 4), 3, 1, 503)
}

// TestE2E失败切换402计费限额换key 上游 402（计费限额耗尽，FR-K4）：key1 被 402
// 后进入长冷却并记录错误，同线路换 key2 完成，客户端无感
func TestE2E失败切换402计费限额换key(t *testing.T) {
	c, _ := setupApp(t)
	var aliveHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer sk-dead" {
			w.WriteHeader(402)
			fmt.Fprintf(w, `{"error":{"message":"Team weekly spending limit would be exceeded: $8000.04 + $0.03 / $8000.00"}}`)
			return
		}
		atomic.AddInt32(&aliveHits, 1)
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fo402a", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	mkKey := func(name, value string) int64 {
		w := c.do("POST", "/api/keys", map[string]any{"name": name, "value": value}, true)
		if w.Code != 200 {
			t.Fatalf("建密钥 %s 失败: %d %s", name, w.Code, w.Body.String())
		}
		var resp struct {
			Key struct {
				ID int64 `json:"id"`
			} `json:"key"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Key.ID
	}
	deadID, aliveID := mkKey("k-dead", "sk-dead"), mkKey("k-alive", "sk-alive")
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "ch-402", "type": "openai", "baseUrls": []string{up.URL},
		"keyIds": []int64{deadID, aliveID}, "models": []string{"gpt-402"},
		"priority": 100, "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建渠道失败: %d %s", w.Code, w.Body.String())
	}
	w := c.do("POST", "/api/tokens", map[string]any{"name": "t"}, true)
	if w.Code != 200 {
		t.Fatalf("建令牌失败: %d %s", w.Code, w.Body.String())
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext

	w = c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "gpt-402", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 {
		t.Fatalf("402 应换 key 后成功，得到 %d %s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&aliveHits) != 1 {
		t.Fatalf("key2 应恰好承接一次（hits=%d）", aliveHits)
	}
	var dead store.Key
	c.store.DB().First(&dead, deadID)
	if dead.CooldownUntil <= time.Now().Unix() {
		t.Fatalf("402 的密钥应进入长冷却，cooldown_until=%d", dead.CooldownUntil)
	}
	if dead.LastError == nil || !strings.Contains(*dead.LastError, "上游 402") {
		t.Fatalf("402 的密钥应记录最近错误，实际: %v", dead.LastError)
	}
	var alive store.Key
	c.store.DB().First(&alive, aliveID)
	if alive.CooldownUntil != 0 {
		t.Fatalf("成功密钥不应有冷却，cooldown_until=%d", alive.CooldownUntil)
	}
	// 逐次尝试落日志：key1 一条 402 失败 + key2 一条成功（FR-L1）
	assertAttemptLogs(t, c.waitLogs(t, "gpt-402", 2), 1, 1, 402)
}

// TestE2E失败切换Responses402计费限额切渠道 /v1/responses 上游 402（用户场景：
// Team weekly spending limit）：渠道1 密钥被 402 长冷却、组合耗尽后计熔断失败，
// 切渠道2 承接，客户端拿到 200 而非 402
func TestE2E失败切换Responses402计费限额切渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newFailoverUpstream(402)
	up2 := newFailoverUpstream(200)
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fo402b", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	setupFailover(t, c, []string{up1.srv.URL, up2.srv.URL}, "gpt-402r")

	w := c.do("POST", "/v1/responses", map[string]any{
		"model": "gpt-402r", "input": "hi",
	}, false)
	if w.Code != 200 {
		t.Fatalf("responses 渠道1 被 402 后应切渠道2 成功，得到 %d %s", w.Code, w.Body.String())
	}
	if up1.hitCount() != 1 || up2.hitCount() != 1 {
		t.Fatalf("两渠道各应被尝试一次（ch1=%d ch2=%d）", up1.hitCount(), up2.hitCount())
	}
	// 被 402 的密钥进入长冷却（密钥为两渠道共用，冷却不影响渠道2 已放行的组合）
	var key store.Key
	c.store.DB().Where("name = ?", "k-failover").First(&key)
	if key.CooldownUntil <= time.Now().Unix() {
		t.Fatalf("402 的密钥应进入长冷却，cooldown_until=%d", key.CooldownUntil)
	}
	// 逐次尝试落日志：渠道1 一条 402 失败 + 渠道2 一条成功（FR-L1）
	assertAttemptLogs(t, c.waitLogs(t, "gpt-402r", 2), 1, 1, 402)
}

// ---------- 流式失败检测与切换（v1.5.54，FR-SG） ----------

// sseDone 终止标记（拼接构造避免整串字面量）
var sseDone = "data: [" + "DONE]\n\n"

// newSSEUpstream 固定帧的 SSE 上游：200 + 一次性写完全部帧后结束
type newSSEUpstreamT struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   int
	frames string
}

func newSSEUpstream(frames string) *newSSEUpstreamT {
	u := &newSSEUpstreamT{frames: frames}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, frames)
	}))
	return u
}

func (u *newSSEUpstreamT) hitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// TestE2E流内错误事件切渠道（FR-SG2） chat 管线：上游 200 + 首帧 SSE 错误
// 事件（oct-yescode 场景，状态码层切换的盲区）→ 首帧窥探判定失败、未向
// 客户端写出任何字节 → 切渠道2 承接，客户端拿到正常流而非错误事件
func TestE2E流内错误事件切渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newSSEUpstream("data: {\"error\":{\"message\":\"Team spending limit exceeded\",\"type\":\"upstream_error\"}}\n\n")
	up2 := newSSEUpstream("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" + sseDone)
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fosg1", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	setupFailover(t, c, []string{up1.srv.URL, up2.srv.URL}, "gpt-sg")

	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "gpt-sg", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 {
		t.Fatalf("流内错误事件应切渠道2 成功，得到 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "\"content\":\"ok\"") {
		t.Fatalf("客户端应拿到渠道2 的正常流，实际: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "spending limit") {
		t.Fatalf("错误事件不应透传给客户端，实际: %s", w.Body.String())
	}
	if up1.hitCount() != 1 || up2.hitCount() != 1 {
		t.Fatalf("两渠道各应被尝试一次（ch1=%d ch2=%d）", up1.hitCount(), up2.hitCount())
	}
	// 逐次尝试落日志：渠道1 一条流内错误失败 + 渠道2 一条成功（FR-L1）
	logs := c.waitLogs(t, "gpt-sg", 2)
	fail, ok := 0, 0
	for _, l := range logs {
		if l.StatusCode == nil {
			continue
		}
		if *l.StatusCode == 502 {
			fail++
			if l.Error == nil || !strings.Contains(*l.Error, "流内错误事件") {
				t.Fatalf("失败日志应含流内错误摘要，实际: %v", l.Error)
			}
		} else if *l.StatusCode == 200 {
			ok++
		}
	}
	if fail != 1 || ok != 1 {
		t.Fatalf("失败/成功日志行数 = %d/%d，期望 1/1（总行 %d）", fail, ok, len(logs))
	}
}

// TestE2EResponses流内错误事件切渠道（FR-SG2） /v1/responses 管线同场景：
// 上游 200 + 首帧 type:"error" 事件（upstream_error stream_read_error，
// 用户实测的 oct-yescode 形态）→ 窥探判失败切渠道2，客户端拿到正常流
func TestE2EResponses流内错误事件切渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newSSEUpstream("data: {\"type\":\"error\",\"sequence_number\":0,\"error\":{\"type\":\"upstream_error\",\"code\":\"stream_read_error\",\"message\":\"stream_read_error\"}}\n\n")
	up2 := newSSEUpstream("data: {\"type\":\"response.created\",\"sequence_number\":0}\n\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fosg2", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	setupFailover(t, c, []string{up1.srv.URL, up2.srv.URL}, "gpt-sgr")

	w := c.do("POST", "/v1/responses", map[string]any{
		"model": "gpt-sgr", "input": "hi", "stream": true,
	}, false)
	if w.Code != 200 {
		t.Fatalf("responses 流内错误事件应切渠道2 成功，得到 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("客户端应拿到渠道2 的正常流，实际: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "stream_read_error") {
		t.Fatalf("错误事件不应透传给客户端，实际: %s", w.Body.String())
	}
	if up1.hitCount() != 1 || up2.hitCount() != 1 {
		t.Fatalf("两渠道各应被尝试一次（ch1=%d ch2=%d）", up1.hitCount(), up2.hitCount())
	}
}

// TestE2E流内错误事件中途计熔断（FR-SG1） 头已提交、内容已透传后出现
// 错误事件：无法换渠道重放，但必须计熔断失败（旧实现按 200 记成功、
// 清零熔断计数，限额渠道永不熔断）；3 次后渠道×模型熔断
func TestE2E流内错误事件中途计熔断(t *testing.T) {
	c, _ := setupApp(t)
	up := newSSEUpstream("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"error\":{\"message\":\"upstream died\",\"type\":\"upstream_error\"}}\n\n")
	defer up.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fosg3", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	setupFailover(t, c, []string{up.srv.URL}, "gpt-sgm")

	for i := 0; i < 3; i++ {
		w := c.do("POST", "/v1/chat/completions", map[string]any{
			"model": "gpt-sgm", "stream": true,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		}, false)
		if w.Code != 200 {
			t.Fatalf("中途错误已透传（头已提交不切换），得到 %d %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "upstream died") {
			t.Fatalf("错误事件应原样透传（无法重放），实际: %s", w.Body.String())
		}
	}
	// 3 次失败达到熔断阈值：breaker_states 应有该渠道×模型行（FR-SG1）
	var rows []store.BreakerState
	c.store.DB().Where("model = ?", "gpt-sgm").Find(&rows)
	if len(rows) == 0 {
		t.Fatal("流内错误连续 3 次应触发熔断（旧实现记成功永不熔断）")
	}
	if !strings.Contains(rows[0].LastError, "流内错误事件") {
		t.Fatalf("熔断行应记录流内错误摘要，实际: %q", rows[0].LastError)
	}
}
