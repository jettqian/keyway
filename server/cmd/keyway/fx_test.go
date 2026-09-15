package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"keyway/internal/config"
)

// mockFxSource 模拟 frankfurter 格式汇率源
func mockFxSource(t *testing.T, rate float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"amount":1.0,"base":"USD","date":"2026-09-15","rates":{"CNY":%f}}`, rate)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupAdminApp 构建指向 mock 汇率源的应用并注册管理员会话（首个用户自动成为管理员）
func setupAdminApp(t *testing.T, fxURL string) *ctx {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir, err := os.MkdirTemp("", "keyway-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := config.Config{
		Secret:      "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		DataDir:     dir,
		BodyLimitMB: 50,
		FxSourceURL: fxURL,
	}
	a, err := buildApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stop)
	c := &ctx{e: a.engine, store: a.store, pm: a.pm, jar: newSimpleJar()}
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "admin", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	return c
}

func Test汇率手动同步与模式切换(t *testing.T) {
	fx := mockFxSource(t, 6.99)
	c := setupAdminApp(t, fx.URL)

	// 默认 manual：GET 回显模式与默认汇率
	var getResp struct {
		Settings struct {
			ExchangeRate     float64 `json:"exchangeRate"`
			ExchangeRateMode string  `json:"exchangeRateMode"`
		} `json:"settings"`
	}
	if w := c.do("GET", "/api/admin/settings", nil, true); w.Code != 200 {
		t.Fatalf("GET settings 失败: %d %s", w.Code, w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &getResp)
	}
	if getResp.Settings.ExchangeRateMode != "manual" || getResp.Settings.ExchangeRate != 7.2 {
		t.Fatalf("缺省应为 manual/7.2，实际 %s/%.4f",
			getResp.Settings.ExchangeRateMode, getResp.Settings.ExchangeRate)
	}

	// manual 固定值：PUT 写入
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "exchangeRateMode": "manual", "exchangeRate": 7.35,
	}, true); w.Code != 200 {
		t.Fatalf("PUT manual 固定值失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := c.store.GetSetting("usd_cny_rate"); v != "7.3500" {
		t.Fatalf("固定值应为 7.3500，实际 %q", v)
	}
	if v, _ := c.store.GetSetting("usd_cny_rate_source"); v != "manual" {
		t.Fatalf("source 应为 manual，实际 %q", v)
	}

	// 预览模式：apply=false 不落库
	if w := c.do("POST", "/api/admin/exchange-rate/sync", map[string]any{"apply": false}, true); w.Code != 200 {
		t.Fatalf("预览同步失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := c.store.GetSetting("usd_cny_rate"); v != "7.3500" {
		t.Fatalf("预览不应改库，实际 %q", v)
	}

	// 手动同步 apply=true：立即写入（含来源地址）
	if w := c.do("POST", "/api/admin/exchange-rate/sync", map[string]any{"apply": true}, true); w.Code != 200 {
		t.Fatalf("手动同步失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := c.store.GetSetting("usd_cny_rate"); v != "6.9900" {
		t.Fatalf("同步后应为 6.9900，实际 %q", v)
	}
	if v, _ := c.store.GetSetting("usd_cny_rate_source_url"); v != fx.URL {
		t.Fatalf("来源地址应为 %q，实际 %q", fx.URL, v)
	}

	// GET 回显来源地址
	var getSynced struct {
		Settings struct {
			ExchangeRateSourceURL string `json:"exchangeRateSourceUrl"`
		} `json:"settings"`
	}
	if w := c.do("GET", "/api/admin/settings", nil, true); w.Code != 200 {
		t.Fatalf("GET settings 失败: %d", w.Code)
	} else {
		json.Unmarshal(w.Body.Bytes(), &getSynced)
	}
	if getSynced.Settings.ExchangeRateSourceURL != fx.URL {
		t.Fatalf("回显来源地址应为 %q，实际 %q", fx.URL, getSynced.Settings.ExchangeRateSourceURL)
	}

	// 切 auto 模式：汇率值字段被忽略（由同步写入）
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "exchangeRateMode": "auto", "exchangeRate": 5.0,
	}, true); w.Code != 200 {
		t.Fatalf("切 auto 失败: %d %s", w.Code, w.Body.String())
	}
	if v, _ := c.store.GetSetting("usd_cny_rate_mode"); v != "auto" {
		t.Fatalf("mode 应为 auto，实际 %q", v)
	}
	if v, _ := c.store.GetSetting("usd_cny_rate"); v != "6.9900" {
		t.Fatalf("auto 模式提交的固定值应被忽略，实际 %q", v)
	}

	// 非法模式值拒绝
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "exchangeRateMode": "weekly",
	}, true); w.Code != 400 {
		t.Fatalf("非法模式应 400，实际 %d", w.Code)
	}
}

func Test汇率源全部失败时同步报错且不影响现有值(t *testing.T) {
	// mock 源返回 500：回退源均为外网地址，测试环境不可达，最终全部失败
	fx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(fx.Close)
	c := setupAdminApp(t, fx.URL)

	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "exchangeRateMode": "manual", "exchangeRate": 7.1,
	}, true); w.Code != 200 {
		t.Fatalf("设置固定值失败: %d", w.Code)
	}

	// 全部源失败（外网回退源在测试环境不可达或超时）→ 502，库内值不变
	w := c.do("POST", "/api/admin/exchange-rate/sync", map[string]any{"apply": true}, true)
	if w.Code == 200 {
		t.Fatalf("全部源失败应报错，实际 200: %s", w.Body.String())
	}
	if v, _ := c.store.GetSetting("usd_cny_rate"); v != "7.1000" {
		t.Fatalf("失败不应改库，实际 %q", v)
	}
}
