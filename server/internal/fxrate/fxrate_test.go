package fxrate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"keyway/internal/store"
)

// newTestEngine 用 httptest 源构建引擎（不打外网）
func newTestEngine(t *testing.T, handlers ...http.HandlerFunc) (*Engine, []*httptest.Server) {
	t.Helper()
	st, err := store.Open(store.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	srvs := make([]*httptest.Server, len(handlers))
	e := &Engine{store: st, client: &http.Client{Timeout: 5 * time.Second}}
	for i, h := range handlers {
		srv := httptest.NewServer(h)
		srvs[i] = srv
		e.sources = append(e.sources, source{
			name:  fmt.Sprintf("src%d", i),
			url:   srv.URL,
			parse: parseFrankfurter, // 各源统一按 frankfurter 响应格式 mock
		})
		t.Cleanup(srv.Close)
	}
	return e, srvs
}

func fxBody(rate float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"rates":{"CNY":%f}}`, rate)
	}
}

func TestSync按源顺序回退(t *testing.T) {
	// 主源 500、次源越界（视为异常继续回退）、第三源成功
	e, _ := newTestEngine(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		fxBody(0.01),
		fxBody(6.85),
	)
	rate, src, err := e.Sync()
	if err != nil || rate != 6.85 || src != "src2" {
		t.Fatalf("期望回退到第三源 6.85/src2，实际 %.4f/%s err=%v", rate, src, err)
	}
}

func TestSync主源成功(t *testing.T) {
	e, _ := newTestEngine(t, fxBody(7.01), fxBody(7.02))
	rate, src, err := e.Sync()
	if err != nil || rate != 7.01 || src != "src0" {
		t.Fatalf("期望主源 7.01/src0，实际 %.4f/%s err=%v", rate, src, err)
	}
}

func TestSync全部失败(t *testing.T) {
	e, _ := newTestEngine(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) },
		func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not-json")) },
	)
	if _, _, err := e.Sync(); err == nil {
		t.Fatal("全部源失败时应返回错误")
	}
}

func TestSyncNow写入settings(t *testing.T) {
	e, _ := newTestEngine(t, fxBody(6.99))
	rate, src, _, err := e.SyncNow()
	if err != nil || rate != 6.99 || src != "src0" {
		t.Fatalf("SyncNow 失败: %.4f/%s err=%v", rate, src, err)
	}
	if v, _ := e.store.GetSetting(KeyRate); v != "6.9900" {
		t.Fatalf("usd_cny_rate 应为 6.9900，实际 %q", v)
	}
	if v, _ := e.store.GetSetting(KeySource); v != "src0" {
		t.Fatalf("source 应为 src0，实际 %q", v)
	}
	if v, _ := e.store.GetSetting(KeyUpdatedAt); v == "" {
		t.Fatal("updated_at 应非空")
	}
}

func TestRunAuto仅auto模式写入(t *testing.T) {
	e, _ := newTestEngine(t, fxBody(6.5))

	// manual 模式（默认）：不写
	e.runAuto()
	if v, _ := e.store.GetSetting(KeyRate); v != "" {
		t.Fatalf("manual 模式不应写入，实际 %q", v)
	}

	// auto 模式：写
	e.store.SetSetting(KeyMode, ModeAuto)
	e.runAuto()
	if v, _ := e.store.GetSetting(KeyRate); v != "6.5000" {
		t.Fatalf("auto 模式应写入 6.5000，实际 %q", v)
	}
}

func TestSaveManualRate范围校验(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.SaveManualRate(0.3); err == nil {
		t.Fatal("0.3 应越界拒绝")
	}
	if err := e.SaveManualRate(25); err == nil {
		t.Fatal("25 应越界拒绝")
	}
	if err := e.SaveManualRate(7.25); err != nil {
		t.Fatalf("7.25 应接受: %v", err)
	}
	if v, _ := e.store.GetSetting(KeyRate); v != "7.2500" {
		t.Fatalf("应为 7.2500，实际 %q", v)
	}
	if v, _ := e.store.GetSetting(KeySource); v != "manual" {
		t.Fatalf("source 应为 manual，实际 %q", v)
	}
}

func TestMode缺省manual(t *testing.T) {
	e, _ := newTestEngine(t)
	if m := e.Mode(); m != ModeManual {
		t.Fatalf("缺省应为 manual，实际 %q", m)
	}
	e.store.SetSetting(KeyMode, ModeAuto)
	if m := e.Mode(); m != ModeAuto {
		t.Fatalf("应为 auto，实际 %q", m)
	}
	e.store.SetSetting(KeyMode, "garbage")
	if m := e.Mode(); m != ModeManual {
		t.Fatalf("非法值应回退 manual，实际 %q", m)
	}
}

func Test各源解析(t *testing.T) {
	cases := []struct {
		name  string
		parse func([]byte) (float64, error)
		body  string
		want  float64
		ok    bool
	}{
		{"frankfurter 正常", parseFrankfurter, `{"rates":{"CNY":6.7084}}`, 6.7084, true},
		{"frankfurter 缺CNY", parseFrankfurter, `{"rates":{"EUR":1}}`, 0, false},
		{"jsdelivr 正常", parseJsdelivr, `{"date":"2026-09-14","usd":{"cny":6.71}}`, 6.71, true},
		{"jsdelivr 缺cny", parseJsdelivr, `{"usd":{"jpy":150}}`, 0, false},
		{"erapi 正常", parseERAPI, `{"result":"success","rates":{"CNY":6.7}}`, 6.7, true},
		{"erapi 失败态", parseERAPI, `{"result":"error","rates":{}}`, 0, false},
		{"erapi 非法JSON", parseERAPI, `not-json`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.parse([]byte(c.body))
			if c.ok && (err != nil || got != c.want) {
				t.Fatalf("期望 %.4f 无错，实际 %.4f err=%v", c.want, got, err)
			}
			if !c.ok && err == nil {
				t.Fatalf("应返回错误，实际 %.4f", got)
			}
		})
	}
}
