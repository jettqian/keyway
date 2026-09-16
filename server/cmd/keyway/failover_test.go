package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// failoverUpstream 可编程上游：按序返回预设状态码，并统计命中次数
type failoverUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int // 每次请求返回的状态码
	hits   int
	body   string
}

func newFailoverUpstream(status int) *failoverUpstream {
	u := &failoverUpstream{status: status}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		st := u.status
		u.hits++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"%s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, u.body)
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
}
