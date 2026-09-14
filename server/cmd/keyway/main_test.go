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

	"github.com/gin-gonic/gin"

	"keyway/internal/config"
	"keyway/internal/store"
)

// mockUpstream 模拟双协议上游
func mockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
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
	return &ctx{e: a.engine, store: a.store, jar: newSimpleJar()}, upstream
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
}

func TestE2EAnthropic入站转OpenAI(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

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
