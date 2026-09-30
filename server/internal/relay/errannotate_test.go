package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSrcTag(t *testing.T) {
	if got := srcTag("ch1·h1", false); got != "[keyway·ch1·h1] " {
		t.Errorf("srcTag 非熔断 = %q", got)
	}
	if got := srcTag("ch1·h1", true); got != "[keyway·ch1·h1·熔断中] " {
		t.Errorf("srcTag 熔断中 = %q", got)
	}
}

func TestLineHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://co-cdn.yes.vg/team", "co-cdn.yes.vg"},
		{"http://h:8080", "h:8080"},
		{"not-a-url", "not-a-url"},
	}
	for _, c := range cases {
		if got := lineHost(c.in); got != c.want {
			t.Errorf("lineHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAnnotateUpstreamError(t *testing.T) {
	// OpenAI 形状：error.message 加前缀，其余字段保留
	body := []byte(`{"error":{"message":"容量不足","type":"new_api_error","code":429}}`)
	out := annotateUpstreamError(body, "oct·h1", true)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	errObj := m["error"].(map[string]any)
	if got := errObj["message"].(string); got != "[keyway·oct·h1·熔断中] 容量不足" {
		t.Errorf("error.message = %q", got)
	}
	if errObj["type"] != "new_api_error" {
		t.Errorf("error.type 被改动: %v", errObj["type"])
	}

	// Anthropic 形状（error.message 同样命中）
	out = annotateUpstreamError([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`), "ch2·h2", false)
	if !bytes.Contains(out, []byte(`"message":"[keyway·ch2·h2] slow down"`)) {
		t.Errorf("anthropic 形状未标注: %s", out)
	}

	// 顶层 message 形状
	out = annotateUpstreamError([]byte(`{"message":"boom"}`), "ch3·h3", false)
	if !bytes.Contains(out, []byte(`"message":"[keyway·ch3·h3] boom"`)) {
		t.Errorf("顶层 message 未标注: %s", out)
	}

	// 数字精度保持（UseNumber）
	out = annotateUpstreamError([]byte(`{"error":{"message":"m","n":12345678901234567890}}`), "ch4·h4", false)
	if !bytes.Contains(out, []byte("12345678901234567890")) {
		t.Errorf("大整数精度丢失: %s", out)
	}

	// 保持原文的路径
	keep := []struct {
		name string
		body string
	}{
		{"非 JSON", `<html>bad gateway</html>`},
		{"JSON 数组", `[1,2,3]`},
		{"error 无 message", `{"error":{"code":"x"}}`},
		{"error 非对象", `{"error":"plain"}`},
		{"message 非字符串", `{"message":42}`},
		{"无 error/message", `{"foo":"bar"}`},
	}
	for _, c := range keep {
		if got := annotateUpstreamError([]byte(c.body), "ch5·h5", false); string(got) != c.body {
			t.Errorf("%s 被改动: %s", c.name, got)
		}
	}

	// src 为空 / body 为空：原样返回
	if got := annotateUpstreamError([]byte(`{"message":"m"}`), "", false); string(got) != `{"message":"m"}` {
		t.Errorf("src 为空仍改写: %s", got)
	}
	if got := annotateUpstreamError(nil, "ch·h", false); got != nil {
		t.Errorf("空 body 仍改写: %s", got)
	}
}

func TestRespondRawOrConvertedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 429 + Retry-After 透传 + JSON 标注
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := []byte(`{"error":{"message":"容量不足","type":"new_api_error"}}`)
	respondRawOrConverted(c, "anthropic", http.StatusTooManyRequests, body, false, "120", "oct-yescode·co-cdn.yes.vg", true)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "120" {
		t.Errorf("Retry-After = %q", ra)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("[keyway·oct-yescode·co-cdn.yes.vg·熔断中] 容量不足")) {
		t.Errorf("body 未标注来源: %s", w.Body.String())
	}

	// 无 Retry-After 时不写空头
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	respondRawOrConverted(c2, "anthropic", http.StatusBadGateway, body, false, "", "ch·h", false)
	if vals, ok := w2.Header()["Retry-After"]; ok {
		t.Errorf("不应写 Retry-After 头: %v", vals)
	}

	// 空 body 走协议错误
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	respondRawOrConverted(c3, "anthropic", http.StatusBadGateway, nil, false, "", "", false)
	if !strings.Contains(w3.Body.String(), "上游错误（无响应体）") {
		t.Errorf("空 body 未走协议错误: %s", w3.Body.String())
	}
}
