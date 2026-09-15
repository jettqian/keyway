package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/config"
	"keyway/internal/proxyman"
	"keyway/internal/store"
)

// mockUpstream 模拟双协议上游
func mockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_request_error"}}`))
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好，世界\"}}]}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\""+model+"\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20}}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"upstream-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, model)
	})

	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_request_error"}}`))
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"你好，世界\"}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":%q,\"usage\":{\"input_tokens\":15,\"output_tokens\":9,\"total_tokens\":24}}}\n\n", model)
			flusher.Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"resp_1","object":"response","model":%q,"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"responses-ok"}]}],"usage":{"input_tokens":11,"output_tokens":6,"total_tokens":17}}`, model)
	})

	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`))
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"anthropic-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}`, model)
	})

	return httptest.NewServer(mux)
}

type ctx struct {
	e         *gin.Engine
	store     *store.Store
	pm        *proxyman.Manager
	jar       *simpleJar
	token     string
	channelID int64
}

type simpleJar struct{ cookies map[string]*http.Cookie }

func newSimpleJar() *simpleJar { return &simpleJar{cookies: map[string]*http.Cookie{}} }

func (j *simpleJar) set(c *http.Cookie) { j.cookies[c.Name] = c }

func (j *simpleJar) list() []*http.Cookie {
	var out []*http.Cookie
	for _, c := range j.cookies {
		out = append(out, c)
	}
	return out
}

func setupApp(t *testing.T) (*ctx, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir, err := os.MkdirTemp("", "keyway-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := config.Config{
		Secret:      "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", // base64(32B)
		DataDir:     dir,
		BodyLimitMB: 50,
	}
	a, err := buildApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stop)
	upstream := mockUpstream(t)
	return &ctx{e: a.engine, store: a.store, pm: a.pm, jar: newSimpleJar()}, upstream
}

func (c *ctx) do(method, path string, body any, useSession bool) *httptest.ResponseRecorder {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if useSession {
		req.Header.Set("X-Keyway-CSRF", "1")
		for _, ck := range c.jar.list() {
			req.AddCookie(ck)
		}
	} else if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		c.jar.set(ck)
	}
	return w
}

// bootstrap 注册→建密钥→建渠道→签令牌
func (c *ctx) bootstrap(t *testing.T, upstreamURL string) string {
	t.Helper()
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "alice", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	if w := c.do("POST", "/api/keys", map[string]any{"name": "k1", "value": "upstream-key"}, true); w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &keyResp)
	}
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "c1", "type": "openai",
		"baseUrls":     []string{upstreamURL},
		"keyIds":       []int64{keyResp.Key.ID},
		"models":       []string{"test-model", "test-stream"},
		"modelMapping": map[string]string{"test-model": "real-model"},
		"priority":     10, "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建渠道失败: %d %s", w.Code, w.Body.String())
	} else {
		var chResp struct {
			Channel struct {
				ID int64 `json:"id"`
			} `json:"channel"`
		}
		json.Unmarshal(w.Body.Bytes(), &chResp)
		c.channelID = chResp.Channel.ID
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	if w := c.do("POST", "/api/tokens", map[string]any{"name": "t1"}, true); w.Code != 200 {
		t.Fatalf("签令牌失败: %d %s", w.Code, w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &tokResp)
	}
	if !strings.HasPrefix(tokResp.Plaintext, "sk-keyway-") {
		t.Fatalf("令牌前缀异常: %s", tokResp.Plaintext)
	}
	c.token = tokResp.Plaintext
	return c.token
}

func TestE2E上游HTML回退换线路(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	// SPA 型上游：任何路径都返回 200 + text/html（网关型站点对未知端点的回退行为）
	htmlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html>spa-fallback</html>"))
	}))
	defer htmlSrv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "erin", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	if w := c.do("POST", "/api/keys", map[string]any{"name": "k1", "value": "upstream-key"}, true); w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &keyResp)
	}
	// 线路 1 = HTML 回退站（录入在前），线路 2 = 正常上游
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "html-first", "type": "openai",
		"baseUrls": []string{htmlSrv.URL, upstream.URL},
		"keyIds":   []int64{keyResp.Key.ID},
		"models":   []string{"fallback-model"}, "priority": 5, "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建渠道失败: %s", w.Body.String())
	}
	// base_url 以 /v1 结尾：拼接不应产生 /v1/v1
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "v1-suffixed", "type": "openai",
		"baseUrls": []string{upstream.URL + "/v1"},
		"keyIds":   []int64{keyResp.Key.ID},
		"models":   []string{"v1-suffix-model"}, "priority": 4, "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建渠道 v1-suffixed 失败: %s", w.Body.String())
	}
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	if w := c.do("POST", "/api/tokens", map[string]any{"name": "t1"}, true); w.Code != 200 {
		t.Fatalf("签令牌失败: %d %s", w.Code, w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &tokResp)
	}
	c.token = tokResp.Plaintext

	// chat：应跳过 HTML 线路、走正常上游
	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "fallback-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "upstream-ok") {
		t.Fatalf("HTML 回退换线路失败: %d %s", w.Code, w.Body.String())
	}

	// responses：同样换线路
	w = c.do("POST", "/v1/responses", map[string]any{"model": "fallback-model", "input": "hi"}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "responses-ok") {
		t.Fatalf("responses HTML 回退换线路失败: %d %s", w.Code, w.Body.String())
	}

	// base_url 以 /v1 结尾：端点直接拼接（无 /v1/v1）
	w = c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "v1-suffix-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "upstream-ok") {
		t.Fatalf("/v1 后缀拼接失败: %d %s", w.Code, w.Body.String())
	}
}

func TestE2EOpenAI透传与模型映射(t *testing.T) {
	c, upstream := setupApp(t)
	token := c.bootstrap(t, upstream.URL)

	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "test-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 {
		t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	choices := resp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "upstream-ok" {
		t.Fatalf("内容异常: %s", w.Body.String())
	}
	if resp["model"] != "real-model" {
		t.Fatalf("模型映射未生效: %v", resp["model"])
	}
	_ = token
	// 耗时指标落库（FR-L1：首字节/总时长；异步批写，轮询等待）
	var last store.Log
	for i := 0; i < 30; i++ {
		c.store.DB().Order("id DESC").First(&last)
		if last.TtftMs != nil && last.TotalMs != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last.TtftMs == nil || *last.TtftMs < 0 || last.TotalMs == nil || *last.TotalMs <= 0 {
		t.Fatalf("耗时指标缺失: ttft=%v total=%v", last.TtftMs, last.TotalMs)
	}
}

func TestE2EAnthropic入站转OpenAI(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)
	// 跨协议转换必须显式开启高级模式。
	c.store.DB().Model(&store.Channel{}).Where("id = ?", c.channelID).Update("forward_mode", "convert")

	req := httptest.NewRequest("POST", "/v1/messages",
		bytes.NewBufferString(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["type"] != "message" || resp["role"] != "assistant" {
		t.Fatalf("anthropic 响应结构异常: %s", w.Body.String())
	}
	block := resp["content"].([]any)[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "upstream-ok" {
		t.Fatalf("内容块异常: %s", w.Body.String())
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 10 || usage["output_tokens"].(float64) != 5 {
		t.Fatalf("usage 异常: %v", usage)
	}
}

func TestE2E流式透传(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"test-stream","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+c.token)
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "你好，世界") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("流式内容异常:\n%s", body)
	}
}

func TestModelBindingsAndTokenReveal(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)

	if w := c.do("PUT", "/api/models/bindings", map[string]any{
		"name": "shared-model", "channelIds": []int64{c.channelID},
	}, true); w.Code != 200 {
		t.Fatalf("模型绑定失败: %d %s", w.Code, w.Body.String())
	}
	if w := c.do("GET", "/api/channels", nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), "shared-model") {
		t.Fatalf("渠道未保存模型绑定: %d %s", w.Code, w.Body.String())
	}
	var tokens struct {
		Tokens []struct {
			ID int64 `json:"id"`
		} `json:"tokens"`
	}
	if w := c.do("GET", "/api/tokens", nil, true); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil || len(tokens.Tokens) != 1 {
		t.Fatalf("读取令牌失败: %d %s", w.Code, w.Body.String())
	}
	if w := c.do("POST", fmt.Sprintf("/api/tokens/%d/reveal", tokens.Tokens[0].ID), nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), "sk-keyway-") {
		t.Fatalf("回看令牌失败: %d %s", w.Code, w.Body.String())
	}
}

func TestE2EResponses透传(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	// 非流式：模型映射 + 透传
	w := c.do("POST", "/v1/responses", map[string]any{"model": "test-model", "input": "hi"}, false)
	if w.Code != 200 {
		t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["object"] != "response" {
		t.Fatalf("Responses 结构异常: %s", w.Body.String())
	}
	if resp["model"] != "real-model" {
		t.Fatalf("模型映射未生效: %v", resp["model"])
	}

	// 流式：根路径前缀（不带 /v1）也可直连
	req := httptest.NewRequest("POST", "/responses",
		bytes.NewBufferString(`{"model":"test-stream","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	sw := httptest.NewRecorder()
	c.e.ServeHTTP(sw, req)
	if sw.Code != 200 {
		t.Fatalf("流式状态码 %d: %s", sw.Code, sw.Body.String())
	}
	body := sw.Body.String()
	if !strings.Contains(body, "你好，世界") || !strings.Contains(body, "response.completed") {
		t.Fatalf("流式内容异常:\n%s", body)
	}

	// 错误令牌 401
	c.token = "sk-keyway-wrong"
	w = c.do("POST", "/v1/responses", map[string]any{"model": "test-model"}, false)
	if w.Code != 401 {
		t.Fatalf("错误令牌应 401，实际 %d", w.Code)
	}

	// 异步日志：usage 嗅探 Responses 嵌套结构（15 输入 / 9 输出）
	var last store.Log
	for i := 0; i < 30; i++ {
		c.store.DB().Order("id DESC").First(&last)
		if last.PromptTokens != nil && *last.PromptTokens > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last.PromptTokens == nil || *last.PromptTokens != 15 || last.CompletionTokens == nil || *last.CompletionTokens != 9 {
		t.Fatalf("Responses usage 嗅探异常: prompt=%v completion=%v", last.PromptTokens, last.CompletionTokens)
	}
}

func TestE2E鉴权与未命中(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	// 错误令牌
	good := c.token
	c.token = "sk-keyway-wrong"
	w := c.do("POST", "/v1/chat/completions", map[string]any{"model": "test-model"}, false)
	if w.Code != 401 {
		t.Fatalf("错误令牌应 401，实际 %d", w.Code)
	}

	// 正确令牌但模型未命中
	c.token = good
	w = c.do("POST", "/v1/chat/completions", map[string]any{"model": "no-such-model"}, false)
	if w.Code != 404 {
		t.Fatalf("未命中模型应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

func TestE2E模型列表(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	w := c.do("GET", "/v1/models", nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "test-model") {
		t.Fatalf("模型列表异常: %d %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("anthropic-version", "2023-06-01")
	w = httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "display_name") {
		t.Fatalf("anthropic 模型列表异常: %s", w.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	c, _ := setupApp(t)
	w := c.do("GET", "/healthz", nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("healthz 异常: %d %s", w.Code, w.Body.String())
	}
}

var _ = url.Parse

func TestE2E价目表CRUD(t *testing.T) {
	c, _ := setupApp(t)
	c.bootstrap(t, "http://upstream.invalid")

	// 新增（body 不带 model，URL 为权威）
	w := c.do("PUT", "/api/admin/pricing/test-price-model", map[string]any{
		"inputPerM": 0.5, "outputPerM": 2.0, "cachedInputPerM": 0.1,
	}, true)
	if w.Code != 200 {
		t.Fatalf("新增价目失败: %s", w.Body.String())
	}
	// 读取并校验 camelCase 字段
	w = c.do("GET", "/api/admin/pricing", nil, true)
	var resp struct {
		Pricing []struct {
			Model           string   `json:"model"`
			InputPerM       float64  `json:"inputPerM"`
			CachedInputPerM *float64 `json:"cachedInputPerM"`
			OutputPerM      float64  `json:"outputPerM"`
		} `json:"pricing"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	var found bool
	for _, p := range resp.Pricing {
		if p.Model == "test-price-model" {
			found = true
			if p.InputPerM != 0.5 || p.OutputPerM != 2 || p.CachedInputPerM == nil || *p.CachedInputPerM != 0.1 {
				t.Fatalf("价目字段错误: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("未找到新增价目")
	}
	// 删除
	w = c.do("DELETE", "/api/admin/pricing/test-price-model", nil, true)
	if w.Code != 200 {
		t.Fatalf("删除价目失败: %s", w.Body.String())
	}
}
