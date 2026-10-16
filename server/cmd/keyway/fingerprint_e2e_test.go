package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

// fingerprintUpstream 记录收到的指纹相关请求头，恒定返回成功补全
type fingerprintUpstream struct {
	srv *httptest.Server
	mu  sync.Mutex
	ua  string
	hdr http.Header
}

func newFingerprintUpstream() *fingerprintUpstream {
	u := &fingerprintUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.ua = r.Header.Get("User-Agent")
		u.hdr = r.Header.Clone()
		u.mu.Unlock()
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, req.Model)
	}))
	return u
}

func (u *fingerprintUpstream) seen() (string, http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ua, u.hdr
}

// setupFingerprint 注册用户并建开/关伪装两个渠道（同上游），签发令牌
func setupFingerprint(t *testing.T, c *ctx, upstreamURL string) {
	t.Helper()
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	w := c.do("POST", "/api/keys", map[string]any{"name": "k-fp", "value": "upstream-key"}, true)
	if w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &keyResp)
	mk := func(name string, mask bool, model string) {
		w := c.do("POST", "/api/channels", map[string]any{
			"name": name, "type": "openai",
			"baseUrls": []string{upstreamURL}, "keyIds": []int64{keyResp.Key.ID},
			"models": []string{model}, "priority": 10, "enabled": true,
			"maskFingerprint": mask,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道 %s 失败: %d %s", name, w.Code, w.Body.String())
		}
	}
	mk("fp-on", true, "gpt-on")
	mk("fp-off", false, "gpt-off")
	w = c.do("POST", "/api/tokens", map[string]any{"name": "t-fp"}, true)
	if w.Code != 200 {
		t.Fatalf("建令牌失败: %d %s", w.Code, w.Body.String())
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	c.token = tokResp.Plaintext
}

// chatWithHeaders 携带自定义入站请求头发起补全请求
func (c *ctx) chatWithHeaders(t *testing.T, model string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	return w
}

// TestE2E指纹伪装 渠道开启 maskFingerprint 后，上游收到固定桌面 UA 且
// SDK/浏览器 OS 指纹头被剥离；未开启渠道保持原样透传（FR-R11）
func TestE2E指纹伪装(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := setupApp(t)
	up := newFingerprintUpstream()
	defer up.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "fp1", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d", w.Code)
	}
	setupFingerprint(t, c, up.srv.URL)

	in := http.Header{}
	in.Set("User-Agent", "claude-cli/1.0.83 (external, cli; ubuntu 22.04; x64) linux/x64")
	in.Set("X-Stainless-Os", "Linux")
	in.Set("X-Stainless-Arch", "x64")
	in.Set("X-Stainless-Lang", "js")
	in.Set("Sec-Ch-Ua-Platform", `"Linux"`)

	// 渠道 1（开启伪装）：优先级相同按录入顺序，先命中 fp-on
	if w := c.chatWithHeaders(t, "gpt-on", in); w.Code != 200 {
		t.Fatalf("开启伪装的请求应成功: %d %s", w.Code, w.Body.String())
	}
	ua, hdr := up.seen()
	if ua != in.Get("User-Agent") {
		t.Fatalf("UA 应原样透传（拦截面在 os 头），得到 %q", ua)
	}
	if got := hdr.Get("X-Stainless-Os"); got != "Windows" {
		t.Fatalf("开启伪装时上游应收 x-stainless-os=Windows，得到 %q", got)
	}
	if got := hdr.Get("X-Stainless-Arch"); got != "x64" {
		t.Fatalf("开启伪装时上游应收 x-stainless-arch=x64，得到 %q", got)
	}
	if got := hdr.Get("Sec-Ch-Ua-Platform"); got != `"Windows"` {
		t.Fatalf("开启伪装时上游应收 sec-ch-ua-platform=Windows，得到 %q", got)
	}
	if got := hdr.Get("X-Stainless-Lang"); got != "js" {
		t.Fatalf("其余 x-stainless-* 应原样保留，得到 %q", got)
	}

	// 渠道 2（关闭伪装）：原样透传
	if w := c.chatWithHeaders(t, "gpt-off", in); w.Code != 200 {
		t.Fatalf("关闭伪装的请求应成功: %d %s", w.Code, w.Body.String())
	}
	ua, hdr = up.seen()
	if ua != in.Get("User-Agent") {
		t.Fatalf("未开启时上游应收到原 UA，得到 %q", ua)
	}
	if hdr.Get("X-Stainless-Os") != "Linux" || hdr.Get("Sec-Ch-Ua-Platform") != `"Linux"` {
		t.Fatalf("未开启时指纹头应原样透传: %v", hdr)
	}

	// API 往返：列表回显 maskFingerprint；编辑改写生效
	w := c.do("GET", "/api/channels", nil, true)
	if w.Code != 200 {
		t.Fatalf("渠道列表失败: %d", w.Code)
	}
	var listResp struct {
		Channels []struct {
			Name            string `json:"name"`
			MaskFingerprint bool   `json:"maskFingerprint"`
		} `json:"channels"`
	}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	seen := map[string]bool{}
	for _, ch := range listResp.Channels {
		seen[ch.Name] = ch.MaskFingerprint
	}
	if !seen["fp-on"] || seen["fp-off"] {
		t.Fatalf("列表回显不符: %v", seen)
	}
}
