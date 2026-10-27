package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"keyway/internal/chatgpt"
	"keyway/internal/store"
)

const testChatGPTSecret = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 与 setupApp 的 cfg.Secret 一致

// codexUpstream 模拟 ChatGPT 后端：只在 /backend-api/codex/responses 提供服务，
// 记录收到的请求供断言；respond 为空时回一个最小的成功流
type codexUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
	respond func(w http.ResponseWriter)
}

func newCodexUpstream(t *testing.T) *codexUpstream {
	t.Helper()
	u := &codexUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		u.headers = append(u.headers, r.Header.Clone())
		u.bodies = append(u.bodies, body)
		respond := u.respond
		u.mu.Unlock()
		if r.URL.Path != "/backend-api/codex/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if respond != nil {
			respond(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *codexUpstream) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

// allowLocalBackend 测试期间把本地 httptest 主机（127.0.0.1）视为 ChatGPT 后端
func allowLocalBackend(t *testing.T) {
	old := chatgpt.BackendHosts
	chatgpt.BackendHosts = []string{"127.0.0.1"}
	t.Cleanup(func() { chatgpt.BackendHosts = old })
}

// seedChatGPTKey 直接落一把 chatgpt 密钥（绕过 OAuth 登录）
func seedChatGPTKey(t *testing.T, c *ctx, name string, expiresAt int64) int64 {
	t.Helper()
	var u store.User
	if err := c.store.DB().Where("username = ?", "gpt-user").First(&u).Error; err != nil {
		t.Fatal(err)
	}
	enc, err := chatgpt.EncryptBundle(testChatGPTSecret, &chatgpt.Bundle{
		AccessToken: "gpt-access", RefreshToken: "gpt-refresh",
		AccountID: "acct-42", Plan: "plus", ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	k := store.Key{UserID: u.ID, Name: name, ValueEnc: enc, Kind: store.KeyKindChatGPT, Status: 1, CreatedAt: time.Now().Unix()}
	if err := c.store.DB().Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	return k.ID
}

func createChannel(c *ctx, name string, baseURLs []string, keyIDs []int64) *httptest.ResponseRecorder {
	return c.do("POST", "/api/channels", map[string]any{
		"name": name, "type": "openai", "baseUrls": baseURLs, "keyIds": keyIDs,
		"models": []string{"gpt-sub"}, "priority": 10, "enabled": true,
	}, true)
}

// setupChatGPT 注册用户、落 chatgpt 密钥、建指向模拟后端的渠道、签令牌
func setupChatGPT(t *testing.T) (*ctx, *codexUpstream, int64, int64) {
	t.Helper()
	allowLocalBackend(t)
	c, upstream := setupApp(t)
	t.Cleanup(upstream.Close)
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "gpt-user", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	backend := newCodexUpstream(t)
	keyID := seedChatGPTKey(t, c, "gpt-acct", time.Now().Add(24*time.Hour).Unix())
	w := createChannel(c, "gpt", []string{backend.URL + "/backend-api/codex"}, []int64{keyID})
	if w.Code != 200 {
		t.Fatalf("建渠道失败: %d %s", w.Code, w.Body.String())
	}
	var chResp struct {
		Channel struct {
			ID int64 `json:"id"`
		} `json:"channel"`
	}
	json.Unmarshal(w.Body.Bytes(), &chResp)
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	tw := c.do("POST", "/api/tokens", map[string]any{"name": "t"}, true)
	json.Unmarshal(tw.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext
	return c, backend, keyID, chResp.Channel.ID
}

func loadKey(t *testing.T, c *ctx, id int64) store.Key {
	t.Helper()
	var k store.Key
	if err := c.store.DB().First(&k, id).Error; err != nil {
		t.Fatal(err)
	}
	return k
}

func loadChannel(t *testing.T, c *ctx, id int64) store.Channel {
	t.Helper()
	var ch store.Channel
	if err := c.store.DB().First(&ch, id).Error; err != nil {
		t.Fatal(err)
	}
	return ch
}

// 转发形态：/codex/responses 纯追加端点、Bearer access_token + account 头 + Codex
// 协议头、请求体规范化（store:false、字符串 input 展开）
func TestE2EChatGPT转发形态(t *testing.T) {
	c, backend, _, _ := setupChatGPT(t)
	w := c.do("POST", "/v1/responses", map[string]any{"model": "gpt-sub", "input": "你好", "stream": true}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("转发失败: %d %s", w.Code, w.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.paths) != 1 || backend.paths[0] != "/backend-api/codex/responses" {
		t.Fatalf("上游路径错误（不应插入 /v1）: %v", backend.paths)
	}
	h := backend.headers[0]
	if h.Get("Authorization") != "Bearer gpt-access" || h.Get("chatgpt-account-id") != "acct-42" ||
		h.Get("OpenAI-Beta") != "responses=experimental" || h.Get("originator") != "codex_cli_rs" {
		t.Fatalf("出站头错误: %v", h)
	}
	body := backend.bodies[0]
	if body["store"] != false {
		t.Fatalf("应补 store:false: %v", body["store"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("字符串 input 应展开为 message 数组: %v", body["input"])
	}
}

// chat/completions 等 responses 以外的端点：纯 chatgpt 渠道直接未命中（404），不打上游
func TestE2EChatGPT渠道不服务Chat端点(t *testing.T) {
	c, backend, _, _ := setupChatGPT(t)
	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "gpt-sub", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("应 404 未命中，实际 %d %s", w.Code, w.Body.String())
	}
	if backend.hits() != 0 {
		t.Fatalf("不应请求 ChatGPT 后端，实际 %d 次", backend.hits())
	}
}

// 渠道保存校验：chatgpt 密钥只能绑 ChatGPT 后端线路，且不得与常规密钥混绑
func TestE2EChatGPT渠道绑定校验(t *testing.T) {
	c, backend, keyID, _ := setupChatGPT(t)
	var apiKey struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	kw := c.do("POST", "/api/keys", map[string]any{"name": "api", "value": "sk-x"}, true)
	json.Unmarshal(kw.Body.Bytes(), &apiKey)

	cases := []struct {
		name    string
		urls    []string
		keys    []int64
		wantErr string
	}{
		{"第三方线路", []string{"https://relay.example.com/v1"}, []int64{keyID}, "线路只能是 ChatGPT 后端"},
		{"后端线路混入第三方线路", []string{backend.URL + "/backend-api/codex", "https://relay.example.com"}, []int64{keyID}, "线路只能是 ChatGPT 后端"},
		{"与常规密钥混绑", []string{backend.URL + "/backend-api/codex"}, []int64{keyID, apiKey.Key.ID}, "不能与常规 API 密钥绑定"},
		{"常规密钥不受限", []string{"https://relay.example.com/v1"}, []int64{apiKey.Key.ID}, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := createChannel(c, fmt.Sprintf("bind-%d", i), tc.urls, tc.keys)
			if tc.wantErr == "" {
				if w.Code != 200 {
					t.Fatalf("应创建成功: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.wantErr) {
				t.Fatalf("应被拒绝（%s）: %d %s", tc.wantErr, w.Code, w.Body.String())
			}
		})
	}
}

// 出站兜底：存量数据里 chatgpt 密钥绑在第三方线路上（绕过保存校验），令牌也不得发出
func TestE2EChatGPT令牌不外发第三方(t *testing.T) {
	c, _, _, chID := setupChatGPT(t)
	var leaked []string
	var mu sync.Mutex
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	// localhost 不在测试的 BackendHosts 内，视为第三方
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	urls, _ := json.Marshal([]string{evilURL})
	c.store.DB().Model(&store.Channel{}).Where("id = ?", chID).Update("base_urls_json", string(urls))

	w := c.do("POST", "/v1/responses", map[string]any{"model": "gpt-sub", "input": "hi"}, false)
	if w.Code == 200 {
		t.Fatalf("不应成功: %s", w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Fatalf("令牌被发往第三方上游: %v", leaked)
	}
	if ch := loadChannel(t, c, chID); ch.LastError != nil {
		t.Fatalf("凭据拒发不应记为渠道错误: %s", *ch.LastError)
	}
}

// 边缘拦截页（403 + HTML）按线路问题处理：不记密钥错误，渠道记 HTML 摘要
func TestE2EChatGPT边缘拦截不记密钥错误(t *testing.T) {
	c, backend, keyID, chID := setupChatGPT(t)
	backend.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<html>_cf_chl_opt</html>"))
	}
	w := c.do("POST", "/v1/responses", map[string]any{"model": "gpt-sub", "input": "hi"}, false)
	if w.Code == 200 {
		t.Fatalf("不应成功: %s", w.Body.String())
	}
	if k := loadKey(t, c, keyID); k.LastError != nil {
		t.Fatalf("拦截页不应记为密钥错误: %s", *k.LastError)
	}
	if ch := loadChannel(t, c, chID); ch.LastError == nil || !strings.Contains(*ch.LastError, "HTML") {
		t.Fatalf("渠道应记 HTML 拦截摘要: %v", ch.LastError)
	}
}

// 订阅额度耗尽（429 usage_limit_reached，无 Retry-After）按错误体重置时间冷却
func TestE2EChatGPT额度耗尽按重置时间冷却(t *testing.T) {
	c, backend, keyID, _ := setupChatGPT(t)
	backend.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":7200}}`))
	}
	if w := c.do("POST", "/v1/responses", map[string]any{"model": "gpt-sub", "input": "hi"}, false); w.Code != http.StatusTooManyRequests {
		t.Fatalf("应透传 429: %d %s", w.Code, w.Body.String())
	}
	want := time.Now().Unix() + 7200
	if k := loadKey(t, c, keyID); k.CooldownUntil < want-10 || k.CooldownUntil > want+10 {
		t.Fatalf("冷却应约 7200s，实际 cooldown_until=%d（期望≈%d）", k.CooldownUntil, want)
	}
}

// 令牌刷新失败：错误记在密钥上（不污染渠道），退避期内不再打令牌端点
func TestE2EChatGPT刷新失败记在密钥(t *testing.T) {
	c, backend, keyID, chID := setupChatGPT(t)
	var tokenCalls int
	var mu sync.Mutex
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer tokenSrv.Close()
	oldTokenURL := chatgpt.TokenURL
	chatgpt.TokenURL = tokenSrv.URL
	t.Cleanup(func() { chatgpt.TokenURL = oldTokenURL })
	// 令牌已过期，转发前必须刷新
	enc, _ := chatgpt.EncryptBundle(testChatGPTSecret, &chatgpt.Bundle{
		AccessToken: "gpt-access", RefreshToken: "gpt-refresh", AccountID: "acct-42", ExpiresAt: 1,
	})
	c.store.DB().Model(&store.Key{}).Where("id = ?", keyID).Update("value_enc", enc)

	for i := 0; i < 3; i++ {
		if w := c.do("POST", "/v1/responses", map[string]any{"model": "gpt-sub", "input": "hi"}, false); w.Code == 200 {
			t.Fatalf("刷新失败时不应成功: %s", w.Body.String())
		}
	}
	mu.Lock()
	n := tokenCalls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("退避期内应只请求令牌端点 1 次，实际 %d 次", n)
	}
	if backend.hits() != 0 {
		t.Fatalf("无有效令牌时不应请求后端，实际 %d 次", backend.hits())
	}
	k := loadKey(t, c, keyID)
	if k.LastError == nil || !strings.Contains(*k.LastError, "令牌刷新失败") || k.CooldownUntil <= time.Now().Unix() {
		t.Fatalf("密钥应记刷新错误并冷却: err=%v cooldown=%d", k.LastError, k.CooldownUntil)
	}
	if ch := loadChannel(t, c, chID); ch.LastError != nil {
		t.Fatalf("刷新失败不应记为渠道错误: %s", *ch.LastError)
	}
}

// 完整导出不带 ChatGPT 令牌；导入时 chatgpt 条目跳过并提示重新登录（含旧版带令牌文件）
func TestE2EChatGPT导出导入不迁移令牌(t *testing.T) {
	c, _, _, _ := setupChatGPT(t)
	w := c.do("GET", "/api/config/export?secrets=1", nil, true)
	if w.Code != 200 {
		t.Fatalf("导出失败: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "gpt-refresh") || strings.Contains(w.Body.String(), "gpt-access") {
		t.Fatalf("完整导出不应包含 ChatGPT 令牌: %s", w.Body.String())
	}
	// 旧版（v1.5.81）导出文件：chatgpt 条目带令牌 JSON
	file := map[string]any{
		"app": "keyway", "format": "user-config", "version": 1, "mode": "full",
		"keys": []map[string]any{{"name": "legacy-gpt", "kind": "chatgpt", "status": 1,
			"value": `{"access_token":"x","refresh_token":"y","account_id":"z","expires_at":1}`}},
		"channels": []any{}, "tokens": []any{},
	}
	iw := c.do("POST", "/api/config/import", file, true)
	if iw.Code != 200 || !strings.Contains(iw.Body.String(), "重新登录") {
		t.Fatalf("导入应成功并提示重新登录: %d %s", iw.Code, iw.Body.String())
	}
	var n int64
	c.store.DB().Model(&store.Key{}).Where("name = ?", "legacy-gpt").Count(&n)
	if n != 0 {
		t.Fatal("chatgpt 条目不应按令牌导入")
	}
}

// 导入的渠道复用本地同名 chatgpt 密钥时同样受绑定校验约束（防经导入绕过）
func TestE2EChatGPT导入渠道绑定校验(t *testing.T) {
	c, _, _, _ := setupChatGPT(t)
	file := map[string]any{
		"app": "keyway", "format": "user-config", "version": 1, "mode": "structure",
		"keys": []any{},
		"channels": []map[string]any{{
			"id": 1, "name": "smuggle", "type": "openai",
			"baseUrls": []string{"https://relay.example.com/v1"}, "keyNames": []string{"gpt-acct"},
			"models": []string{"gpt-sub"}, "enabled": true,
		}},
		"tokens": []any{},
	}
	iw := c.do("POST", "/api/config/import", file, true)
	if iw.Code != 200 || !strings.Contains(iw.Body.String(), "线路只能是 ChatGPT 后端") {
		t.Fatalf("应跳过违规渠道并提示: %d %s", iw.Code, iw.Body.String())
	}
	var n int64
	c.store.DB().Model(&store.Channel{}).Where("name = ?", "smuggle").Count(&n)
	if n != 0 {
		t.Fatal("违规渠道不应被导入")
	}
}

// 「逐密钥测试」：chatgpt 密钥只试 responses 一种形态，请求体与真实转发同一规范化
func TestE2EChatGPT逐密钥测试(t *testing.T) {
	c, backend, _, chID := setupChatGPT(t)
	w := c.do("POST", fmt.Sprintf("/api/channels/%d/test_keys", chID), nil, true)
	if w.Code != 200 {
		t.Fatalf("逐密钥测试失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("chatgpt 密钥应测试通过: %s", w.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.paths) != 1 || backend.paths[0] != "/backend-api/codex/responses" {
		t.Fatalf("应只请求一次 /codex/responses: %v", backend.paths)
	}
	if _, ok := backend.bodies[0]["input"].([]any); !ok || backend.bodies[0]["store"] != false {
		t.Fatalf("探测请求体应规范化: %v", backend.bodies[0])
	}
}
