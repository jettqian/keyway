package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"keyway/internal/store"
)

// breakerUpstream 可编程上游：按请求体 model 返回预设状态码（未命中返回 200），
// 并统计命中次数
type breakerUpstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits int
	fail map[string]int // model → 状态码
}

func newBreakerUpstream(fail map[string]int) *breakerUpstream {
	u := &breakerUpstream{fail: fail}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		u.hits++
		st := 200
		if code, ok := u.fail[req.Model]; ok {
			st = code
		}
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if st >= 400 {
			w.WriteHeader(st)
			fmt.Fprintf(w, `{"error":{"message":"mock %d"}}`, st)
			return
		}
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, req.Model)
	}))
	return u
}

func (u *breakerUpstream) hitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// setHealthy 清除失败配置（上游恢复）
func (u *breakerUpstream) setHealthy() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fail = nil
}

// setupBreaker 注册用户并建渠道（各绑一把密钥、共用同一令牌）：
// urls1 → 渠道 1（priority 100），urls2 → 渠道 2（priority 50，nil = 不建）
func setupBreaker(t *testing.T, c *ctx, urls1, urls2 []string, models []string) {
	t.Helper()
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	w := c.do("POST", "/api/keys", map[string]any{"name": "k-brk", "value": "upstream-key"}, true)
	if w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &keyResp)
	mk := func(name string, urls []string, priority int) {
		if len(urls) == 0 {
			return
		}
		w := c.do("POST", "/api/channels", map[string]any{
			"name": name, "type": "openai",
			"baseUrls": urls, "keyIds": []int64{keyResp.Key.ID},
			"models": models, "priority": priority, "enabled": true,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道 %s 失败: %d %s", name, w.Code, w.Body.String())
		}
	}
	mk("brk-1", urls1, 100)
	mk("brk-2", urls2, 50)
	w = c.do("POST", "/api/tokens", map[string]any{"name": "t-brk"}, true)
	if w.Code != 200 {
		t.Fatalf("建令牌失败: %d %s", w.Code, w.Body.String())
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext
}

func (c *ctx) chat(t *testing.T, model string) *httptest.ResponseRecorder {
	t.Helper()
	return c.do("POST", "/v1/chat/completions", map[string]any{
		"model": model, "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
}

// listBreakersViaAPI GET /api/breakers（会话）
func (c *ctx) listBreakersViaAPI(t *testing.T) []map[string]any {
	t.Helper()
	w := c.do("GET", "/api/breakers", nil, true)
	if w.Code != 200 {
		t.Fatalf("查询熔断列表失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Breakers []map[string]any `json:"breakers"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	return resp.Breakers
}

// expireCooldown 把指定渠道全部熔断行的冷却置为已过期（模拟时间推进，测试半开）
func (c *ctx) expireCooldown(t *testing.T, channelID int64) {
	t.Helper()
	now := time.Now().Unix()
	if err := c.store.DB().Model(&store.BreakerState{}).
		Where("channel_id = ?", channelID).
		Update("cooldown_until", now-1).Error; err != nil {
		t.Fatalf("推进冷却失败: %v", err)
	}
}

// firstChannelID 按创建顺序取第一个渠道 ID（测试内每 app 只有本用户建渠道）
func (c *ctx) firstChannelID(t *testing.T) int64 {
	t.Helper()
	var ch store.Channel
	if err := c.store.DB().Order("id").First(&ch).Error; err != nil {
		t.Fatalf("查询渠道失败: %v", err)
	}
	return ch.ID
}

// TestE2E熔断跳过渠道 冷却期内不再浪费尝试，手动恢复后重新参与路由（FR-B1/B6）
func TestE2E熔断跳过渠道(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newBreakerUpstream(map[string]int{"gpt-brk": 503}) // 渠道1：恒 503
	up2 := newBreakerUpstream(nil)                            // 渠道2：正常
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk1", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL}, []string{up2.srv.URL}, []string{"gpt-brk"})

	// 连续 3 个请求把渠道 1 的组合耗尽 → 熔断（每请求 1 次上游尝试）
	for i := 0; i < 3; i++ {
		if w := c.chat(t, "gpt-brk"); w.Code != 200 {
			t.Fatalf("请求 %d 应由渠道 2 成功，得到 %d %s", i+1, w.Code, w.Body.String())
		}
	}
	if n := up1.hitCount(); n != 3 {
		t.Fatalf("渠道 1 应被尝试 3 次，实际 %d", n)
	}
	// 第 4 个请求：熔断冷却期内直接跳过渠道 1
	if w := c.chat(t, "gpt-brk"); w.Code != 200 {
		t.Fatalf("第 4 个请求应成功，得到 %d", w.Code)
	}
	if n := up1.hitCount(); n != 3 {
		t.Fatalf("熔断后不应再尝试渠道 1，实际 %d 次", n)
	}
	// 前端可见：列表 1 条，含模型与失败次数
	list := c.listBreakersViaAPI(t)
	if len(list) != 1 {
		t.Fatalf("熔断列表应有 1 条，实际 %d", len(list))
	}
	if list[0]["model"] != "gpt-brk" || list[0]["failCount"].(float64) != 3 {
		t.Fatalf("熔断条目不符: %v", list[0])
	}
	// 手动恢复：下一个请求重新尝试渠道 1（仍 503，再切渠道 2）
	if w := c.do("POST", "/api/breakers/reset", map[string]any{"channelId": list[0]["channelId"]}, true); w.Code != 200 {
		t.Fatalf("手动恢复失败: %d %s", w.Code, w.Body.String())
	}
	if got := c.listBreakersViaAPI(t); len(got) != 0 {
		t.Fatalf("恢复后熔断列表应为空，实际 %d", len(got))
	}
	if w := c.chat(t, "gpt-brk"); w.Code != 200 {
		t.Fatalf("恢复后请求应成功，得到 %d", w.Code)
	}
	if n := up1.hitCount(); n != 4 {
		t.Fatalf("恢复后渠道 1 应被重新尝试 1 次，实际 %d", n)
	}
}

// TestE2E熔断模型维度 熔断按渠道×模型隔离：gpt-x 熔断不影响同渠道的 gpt-y（FR-B1）
func TestE2E熔断模型维度(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newBreakerUpstream(map[string]int{"gpt-x": 503}) // 渠道 1：仅 gpt-x 失败
	up2 := newBreakerUpstream(nil)
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk2", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL}, []string{up2.srv.URL}, []string{"gpt-x", "gpt-y"})

	for i := 0; i < 3; i++ {
		if w := c.chat(t, "gpt-x"); w.Code != 200 {
			t.Fatalf("gpt-x 请求 %d 应由渠道 2 成功，得到 %d", i, w.Code)
		}
	}
	// 渠道 1 的 gpt-x 已熔断
	list := c.listBreakersViaAPI(t)
	if len(list) != 1 || list[0]["model"] != "gpt-x" {
		t.Fatalf("应只有 gpt-x 熔断: %v", list)
	}
	// gpt-y 仍正常路由到渠道 1（不受 gpt-x 熔断影响）
	if w := c.chat(t, "gpt-y"); w.Code != 200 {
		t.Fatalf("gpt-y 应由渠道 1 成功，得到 %d %s", w.Code, w.Body.String())
	}
	// gpt-x 继续走渠道 2（渠道 1 被跳过，不新增尝试）
	hitsBefore := up1.hitCount()
	if w := c.chat(t, "gpt-x"); w.Code != 200 {
		t.Fatalf("gpt-x 应成功，得到 %d", w.Code)
	}
	if up1.hitCount() != hitsBefore {
		t.Fatalf("gpt-x 熔断期间渠道 1 不应被尝试: %d → %d", hitsBefore, up1.hitCount())
	}
}

// TestE2E全部候选熔断旁路 唯一渠道熔断后仍照常尝试（可用性优先，FR-B3）
func TestE2E全部候选熔断旁路(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newBreakerUpstream(map[string]int{"gpt-solo": 503})
	defer up1.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk3", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL}, nil, []string{"gpt-solo"})

	for i := 0; i < 3; i++ {
		if w := c.chat(t, "gpt-solo"); w.Code != 503 {
			t.Fatalf("请求 %d 应透传 503，得到 %d", i, w.Code)
		}
	}
	if n := up1.hitCount(); n != 3 {
		t.Fatalf("渠道 1 应被尝试 3 次，实际 %d", n)
	}
	if list := c.listBreakersViaAPI(t); len(list) != 1 {
		t.Fatalf("应已熔断: %v", list)
	}
	// 第 4 个请求：无备用渠道可切 → 旁路熔断照常尝试（与无熔断行为一致）
	if w := c.chat(t, "gpt-solo"); w.Code != 503 {
		t.Fatalf("旁路请求应透传 503，得到 %d", w.Code)
	}
	if n := up1.hitCount(); n != 4 {
		t.Fatalf("旁路应照常尝试渠道 1，实际 %d 次", n)
	}
}

// TestE2E半开试探恢复 冷却到期后仅放行单个组合试探：成功即关闭、流量切回（FR-B2）
func TestE2E半开试探恢复(t *testing.T) {
	c, _ := setupApp(t)
	// 渠道 1 三条线路全部 503（正常状态下每请求最多耗 3 次预算）
	up1 := newBreakerUpstream(map[string]int{"gpt-ho": 503})
	up2 := newBreakerUpstream(nil)
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk4", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL, up1.srv.URL, up1.srv.URL}, []string{up2.srv.URL}, []string{"gpt-ho"})
	ch1 := c.firstChannelID(t)

	for i := 0; i < 3; i++ {
		if w := c.chat(t, "gpt-ho"); w.Code != 200 {
			t.Fatalf("请求 %d 应由渠道 2 成功，得到 %d", i, w.Code)
		}
	}
	if n := up1.hitCount(); n != 9 {
		t.Fatalf("渠道 1 三线路 × 3 请求应命中 9 次，实际 %d", n)
	}
	// 熔断后再请求：整渠道跳过
	c.chat(t, "gpt-ho")
	if n := up1.hitCount(); n != 9 {
		t.Fatalf("熔断期不应尝试渠道 1，实际 %d", n)
	}

	// 冷却到期（测试中直接推进冷却时间），渠道 1 仍 503：
	// 半开仅放行 1 个组合（而非 3 个预算），失败后继续走渠道 2
	c.expireCooldown(t, ch1)
	if w := c.chat(t, "gpt-ho"); w.Code != 200 {
		t.Fatalf("试探失败后应由渠道 2 成功，得到 %d", w.Code)
	}
	if n := up1.hitCount(); n != 10 {
		t.Fatalf("半开试探应只放行 1 个组合，实际新增 %d 次", n-9)
	}
	// 再次到期且渠道 1 已恢复：试探成功 → 关闭熔断、流量切回
	up1.setHealthy()
	c.expireCooldown(t, ch1)
	if w := c.chat(t, "gpt-ho"); w.Code != 200 {
		t.Fatalf("试探成功应切回渠道 1，得到 %d %s", w.Code, w.Body.String())
	}
	if n := up1.hitCount(); n != 11 {
		t.Fatalf("恢复后渠道 1 应承接流量，实际 %d 次", n)
	}
	if list := c.listBreakersViaAPI(t); len(list) != 0 {
		t.Fatalf("试探成功后熔断应关闭: %v", list)
	}
	// 关闭后渠道 1 恢复正常尝试：首个组合即成功
	c.chat(t, "gpt-ho")
	if n := up1.hitCount(); n != 12 {
		t.Fatalf("关闭后渠道 1 应正常承接（首个组合成功），实际 %d 次", n)
	}
}

// TestE2E熔断恢复越权 越权恢复他人渠道熔断应 404，列表互不可见（FR-B6/A5）
func TestE2E熔断恢复越权(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newBreakerUpstream(map[string]int{"gpt-own": 503})
	defer up1.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk5a", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL}, nil, []string{"gpt-own"})
	for i := 0; i < 3; i++ {
		c.chat(t, "gpt-own")
	}
	otherID := c.firstChannelID(t)

	// 用户 B：注册后尝试恢复用户 A 的渠道熔断 → 404，列表也不可见
	c.jar = newSimpleJar()
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk5b", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册 B 失败: %d", w.Code)
	}
	if w := c.do("POST", "/api/breakers/reset", map[string]any{"channelId": otherID}, true); w.Code != 404 {
		t.Fatalf("越权恢复应 404，得到 %d", w.Code)
	}
	if list := c.listBreakersViaAPI(t); len(list) != 0 {
		t.Fatalf("用户 B 不应看到用户 A 的熔断: %v", list)
	}
}

// TestE2E熔断Responses管线 /v1/responses 管线同场景：熔断后跳过、试探恢复
func TestE2E熔断Responses管线(t *testing.T) {
	c, _ := setupApp(t)
	up1 := newBreakerUpstream(map[string]int{"gpt-res": 503})
	up2 := newBreakerUpstream(nil)
	defer up1.srv.Close()
	defer up2.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "brk6", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupBreaker(t, c, []string{up1.srv.URL}, []string{up2.srv.URL}, []string{"gpt-res"})

	call := func() *httptest.ResponseRecorder {
		return c.do("POST", "/v1/responses", map[string]any{"model": "gpt-res", "input": "hi"}, false)
	}
	for i := 0; i < 3; i++ {
		if w := call(); w.Code != 200 {
			t.Fatalf("responses 请求 %d 应由渠道 2 成功，得到 %d", i, w.Code)
		}
	}
	if n := up1.hitCount(); n != 3 {
		t.Fatalf("渠道 1 应被尝试 3 次，实际 %d", n)
	}
	if w := call(); w.Code != 200 {
		t.Fatalf("熔断后 responses 请求应成功，得到 %d", w.Code)
	}
	if n := up1.hitCount(); n != 3 {
		t.Fatalf("熔断后不应再尝试渠道 1，实际 %d 次", n)
	}
	// 半开试探：到期后单次试探成功 → 切回
	up1.setHealthy()
	c.expireCooldown(t, c.firstChannelID(t))
	if w := call(); w.Code != 200 {
		t.Fatalf("试探成功应由渠道 1 承接，得到 %d %s", w.Code, w.Body.String())
	}
	if n := up1.hitCount(); n != 4 {
		t.Fatalf("试探应只放行 1 次且成功切回，实际 %d 次", n)
	}
}
