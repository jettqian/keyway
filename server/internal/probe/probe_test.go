package probe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"keyway/internal/config"
	"keyway/internal/crypto"
	"keyway/internal/proxyman"
	"keyway/internal/routing"
	"keyway/internal/store"
)

var testSecret = strings.Repeat("k", 32)

// anthropicOnly 模拟只支持 Anthropic 协议的上游：仅 /v1/messages + x-api-key；
// wantModel 非空时校验请求体模型名（验证模型映射生效）
func anthropicOnly(t *testing.T, wantModel string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if wantModel != "" && body["model"] != wantModel {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"model %v not found"}}`, body["model"])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant",` +
			`"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
}

// openAIOnly 模拟只支持 OpenAI 协议的上游：仅 /v1/chat/completions + Bearer
func openAIOnly(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cmpl_1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"pong"}}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
}

func newEngine(t *testing.T) (*store.Store, *Engine) {
	t.Helper()
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	r := routing.New(st, testSecret)
	pm := proxyman.New(st, testSecret)
	return st, New(st, testSecret, r, pm, config.Config{})
}

// seedChannel 落一把密钥并绑定到渠道（直连路径）
func seedChannel(t *testing.T, st *store.Store, ch *store.Channel) *store.Channel {
	t.Helper()
	enc, err := crypto.Encrypt(testSecret, crypto.PurposeKey, []byte("sk-test"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	k := &store.Key{UserID: 1, Name: "k1", ValueEnc: enc, Status: 1}
	if err := st.DB().Create(k).Error; err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	ch.UserID = 1
	ch.KeyIDsJSON = fmt.Sprintf(`[%d]`, k.ID)
	if err := st.DB().Create(ch).Error; err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	return ch
}

// 透明转发渠道 type 为空（前端 passthrough 模式不提交协议类型），但上游是
// Anthropic——真实转发可用，探测必须成功（旧实现按 openai 协议探测必失败）
func TestProbeChannel透明转发Anthropic上游(t *testing.T) {
	st, e := newEngine(t)
	srv := anthropicOnly(t, "")
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "an-relay", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["claude-3-5-sonnet"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("透明转发 anthropic 上游应探测成功，实际 %+v", results)
	}
}

// 探测请求须应用模型映射，发上游模型名（与真实转发一致）
func TestProbeChannel模型映射生效(t *testing.T) {
	st, e := newEngine(t)
	srv := anthropicOnly(t, "claude-3-5-sonnet-20241022")
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "mapped", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["claude-3-5-sonnet"]`,
		ModelMappingJSON: `{"claude-3-5-sonnet":"claude-3-5-sonnet-20241022"}`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("映射后上游模型名应探测成功，实际 %+v", results)
	}
}

// 透明转发 + claude 模型走 openai 兼容中转：首选协议失败后回退另一协议成功
func TestProbeChannel协议回退(t *testing.T) {
	st, e := newEngine(t)
	srv := openAIOnly(t)
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "oneapi", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["claude-3-5-sonnet"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("协议回退后应探测成功，实际 %+v", results)
	}
}

// convert 模式上游协议由渠道显式指定，不回退——配错就应报失败
func TestProbeChannel转换模式不回退(t *testing.T) {
	st, e := newEngine(t)
	srv := openAIOnly(t)
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "convert-an", Type: "anthropic", ForwardMode: "convert", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["claude-3-5-sonnet"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("convert 渠道探测 anthropic 失败时不应回退成功，实际 %+v", results)
	}
}

// 探测形态序列：responses/messages 优先、chat 靠后（对齐 agent 主力流量——
// Codex → /v1/responses、Claude Code → /v1/messages）；convert 在显式协议族内排序
func TestProbeShapes(t *testing.T) {
	cases := []struct {
		name  string
		ch    *store.Channel
		model string
		want  []string
	}{
		{"convert显式anthropic", &store.Channel{ForwardMode: "convert", Type: "anthropic"}, "gpt-4o", []string{"anthropic"}},
		{"convert显式openai", &store.Channel{ForwardMode: "convert", Type: "openai"}, "gpt-4o", []string{"openai-responses", "openai"}},
		{"passthrough推断anthropic", &store.Channel{ForwardMode: "passthrough"}, "claude-3-5-sonnet", []string{"anthropic", "openai-responses", "openai"}},
		{"passthrough推断openai", &store.Channel{ForwardMode: "passthrough"}, "gpt-4o", []string{"openai-responses", "openai", "anthropic"}},
		{"passthrough忽略stale类型", &store.Channel{ForwardMode: "passthrough", Type: "openai"}, "claude-sonnet-4", []string{"anthropic", "openai-responses", "openai"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := probeShapes(c.ch, c.model)
			if len(got) != len(c.want) {
				t.Fatalf("期望 %v，实际 %v", c.want, got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("期望 %v，实际 %v", c.want, got)
				}
			}
		})
	}
}

// responses-only 网关（oct-yescode 场景）：/chat/completions 与 /messages 均 403/503，
// 仅 /v1/responses 可用（Codex 客户端端点）——形态回退后组合应判健康
func TestProbeChannelResponsesOnly网关(t *testing.T) {
	st, e := newEngine(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/responses":
			w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"message":"This team does not have an enabled provider configured for this request."}}`))
		}
	}))
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "responses-only", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["gpt-6-astra"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("responses-only 网关应经 /v1/responses 回退探测成功，实际 %+v", results)
	}
}

// 模型回退：第一个模型上游 503（new_api "分组无渠道"场景），第二个模型可用 →
// 组合仍判健康；全失败场景（无可回退模型）汇报含第一个模型名的错误
func TestProbeChannel模型回退(t *testing.T) {
	st, e := newEngine(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "gpt-5.6-sol" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"cmpl_1","choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"code":"model_not_found","message":"No available channel for the current group"}}`))
	}))
	defer srv.Close()

	// 前两个模型不可用、第三个可用（超出 probeModelCap 的第 4 个不应被探测）
	ch := seedChannel(t, st, &store.Channel{
		Name: "fallback", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL),
		ModelsJSON:   `["gpt-5.6-luna","gpt-5.6-terra","gpt-5.6-sol","never-probed"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("第三个模型可用时组合应判健康，实际 %+v", results)
	}
}

// 探测全失败时错误信息须携带上游响应体 message 摘要（定位"为什么不过"）
func TestProbeChannel错误含上游摘要(t *testing.T) {
	st, e := newEngine(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"This team does not have an enabled provider configured for this request."}}`))
	}))
	defer srv.Close()
	ch := seedChannel(t, st, &store.Channel{
		Name: "team", ForwardMode: "passthrough", Enabled: 1,
		BaseURLsJSON: fmt.Sprintf(`["%s"]`, srv.URL), ModelsJSON: `["gpt-6-astra"]`,
	})
	results, err := e.ProbeChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("应探测失败，实际 %+v", results)
	}
	for _, kw := range []string{"403", "enabled provider"} {
		if !strings.Contains(results[0].Error, kw) {
			t.Errorf("错误摘要应含 %q，实际：%s", kw, results[0].Error)
		}
	}
	if !strings.Contains(results[0].Error, "gpt-6-astra") {
		t.Errorf("错误摘要应含模型名，实际：%s", results[0].Error)
	}
}

// 错误摘要解析：优先 error.message，截断超长文本，无法解析时回退原文
func TestUpstreamErrorSummary(t *testing.T) {
	if got := upstreamErrorSummary([]byte(`{"error":{"message":"分组无渠道"}}`)); got != "分组无渠道" {
		t.Errorf("嵌套 message 解析失败：%q", got)
	}
	if got := upstreamErrorSummary([]byte(`{"message":"顶层消息"}`)); got != "顶层消息" {
		t.Errorf("顶层 message 解析失败：%q", got)
	}
	if got := upstreamErrorSummary([]byte(`{}`)); got != "" {
		t.Errorf("空结构应返回空，实际 %q", got)
	}
	if got := upstreamErrorSummary([]byte("非 JSON 原文")); got != "非 JSON 原文" {
		t.Errorf("非 JSON 应回退原文，实际 %q", got)
	}
	long := strings.Repeat("长", 200)
	if got := upstreamErrorSummary([]byte(`{"error":{"message":"` + long + `"}}`)); len([]rune(got)) != 121 {
		t.Errorf("超长摘要应截断到 120 rune + 省略号，实际 %d rune", len([]rune(got)))
	}
}
