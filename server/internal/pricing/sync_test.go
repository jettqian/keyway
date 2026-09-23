package pricing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"keyway/internal/store"
)

// LiteLLM 解析：仅保留 chat 模式且输入输出价均大于 0 的条目；缓存价 0 归 NULL
func TestParseLiteLLM(t *testing.T) {
	body := []byte(`{
		"claude-sonnet-4-6": {"mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "cache_read_input_token_cost": 0.0000003, "cache_creation_input_token_cost": 0.00000375, "litellm_provider": "anthropic"},
		"text-embedding-3-small": {"mode": "embedding", "input_cost_per_token": 0.00000002, "output_cost_per_token": 0},
		"some-free-model": {"mode": "chat", "input_cost_per_token": 0, "output_cost_per_token": 0},
		"glm-5.3": {"mode": "chat", "input_cost_per_token": 0.00000111, "output_cost_per_token": 0.00000389, "litellm_provider": "zhipu"}
	}`)
	out, err := parseLiteLLM(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(out))
	}
	byName := map[string]ModelPrice{}
	for _, p := range out {
		byName[p.Model] = p
	}
	an := byName["claude-sonnet-4-6"]
	if an.InputPerM != 3 || an.OutputPerM != 15 {
		t.Errorf("输入/输出换算错误：%v/%v", an.InputPerM, an.OutputPerM)
	}
	if an.CachedInputPerM == nil || *an.CachedInputPerM != 0.3 {
		t.Errorf("缓存读错误：%v", an.CachedInputPerM)
	}
	if an.CacheWritePerM == nil || *an.CacheWritePerM != 3.75 {
		t.Errorf("缓存写错误：%v", an.CacheWritePerM)
	}
	glm := byName["glm-5.3"]
	if glm.CachedInputPerM != nil || glm.CacheWritePerM != nil {
		t.Errorf("无缓存价时应为 NULL：%v %v", glm.CachedInputPerM, glm.CacheWritePerM)
	}
}

// OpenRouter 解析：仅保留 text->text 且输入输出价可解析且大于 0 的条目
func TestParseOpenRouter(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openai/gpt-4o","architecture":{"modality":"text->text"},"pricing":{"prompt":"0.0000025","completion":"0.00001","input_cache_read":"0.00000125","input_cache_write":"0.000001875"}},
		{"id":"openai/gpt-4o-audio","architecture":{"modality":"text+audio->text"},"pricing":{"prompt":"0.0000025","completion":"0.0001"}},
		{"id":"meta-llama/llama-3.1-8b-instruct:free","architecture":{"modality":"text->text"},"pricing":{"prompt":"0","completion":"0"}},
		{"id":"no-pricing","architecture":{"modality":"text->text"}}
	]}`)
	out, err := parseOpenRouter(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(out))
	}
	p := out[0]
	if p.Model != "openai/gpt-4o" || p.InputPerM != 2.5 || p.OutputPerM != 10 {
		t.Errorf("映射错误：%+v", p)
	}
	if p.CachedInputPerM == nil || *p.CachedInputPerM != 1.25 {
		t.Errorf("缓存读错误：%v", p.CachedInputPerM)
	}
	if p.CacheWritePerM == nil || *p.CacheWritePerM != 1.875 {
		t.Errorf("缓存写错误：%v", p.CacheWritePerM)
	}
}

// 非法 JSON 报错而非 panic
func TestParseInvalid(t *testing.T) {
	if _, err := parseLiteLLM([]byte("not json")); err == nil {
		t.Error("LiteLLM 非法 JSON 应报错")
	}
	if _, err := parseOpenRouter([]byte("not json")); err == nil {
		t.Error("OpenRouter 非法 JSON 应报错")
	}
	var _ = json.Marshal // 保留 import
}

// 同步时间记录：markSynced upsert 写入 settings，SyncedAt 读取；Sources 暴露全部源链接
func TestSyncedAtRoundTrip(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	if got := SyncedAt(db); got != "" {
		t.Errorf("未同步时应返回空串，实际 %q", got)
	}
	if err := markSynced(db); err != nil {
		t.Fatal(err)
	}
	first := SyncedAt(db)
	if first == "" {
		t.Fatal("同步后应能读取到时间")
	}
	if _, err := time.Parse(time.RFC3339, first); err != nil {
		t.Errorf("时间应为 RFC3339 格式：%q", first)
	}
	if err := markSynced(db); err != nil {
		t.Fatal(err)
	}
	if SyncedAt(db) == "" {
		t.Error("重复写入不应清空")
	}

	srcs := Sources()
	if len(srcs) != 2 || srcs[0].Name != "LiteLLM" || srcs[1].Name != "OpenRouter" {
		t.Errorf("同步源信息不符：%+v", srcs)
	}
	for _, s := range srcs {
		if !strings.HasPrefix(s.URL, "https://") {
			t.Errorf("同步源链接非法：%s", s.URL)
		}
	}
}

// 同步落库语义（v1.5.64）：目录关联条目刷新为网络最新价；其余已存在条目跳过；
// 缺失条目补插
func TestApplyMissingRefresh(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 目录关联 prov/m；plain 存在但无关联
	if err := db.Create(&store.CatalogModel{Name: "cm", PricingModel: "prov/m", Enabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "prov/m", InputPerM: 1, OutputPerM: 1, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "plain", InputPerM: 2, OutputPerM: 2, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}

	cr := 0.1
	added, refreshed, skipped := applyMissing(db, []ModelPrice{
		{Model: "prov/m", InputPerM: 3, OutputPerM: 7, CachedInputPerM: &cr},
		{Model: "plain", InputPerM: 4, OutputPerM: 4},
		{Model: "brandnew", InputPerM: 5, OutputPerM: 5},
	})
	if added != 1 || refreshed != 1 || skipped != 1 {
		t.Fatalf("期望 新增1/刷新1/跳过1，实际 %d/%d/%d", added, refreshed, skipped)
	}

	var pm store.ModelPricing
	if err := db.First(&pm, "model = ?", "prov/m").Error; err != nil {
		t.Fatal(err)
	}
	if pm.InputPerM != 3 || pm.OutputPerM != 7 || pm.CachedInputPerM == nil || *pm.CachedInputPerM != 0.1 {
		t.Fatalf("关联条目应刷新为网络最新价: %+v", pm)
	}
	var plain store.ModelPricing
	if err := db.First(&plain, "model = ?", "plain").Error; err != nil {
		t.Fatal(err)
	}
	if plain.InputPerM != 2 || plain.OutputPerM != 2 {
		t.Fatalf("未关联条目不应被覆盖: %+v", plain)
	}
	var bn store.ModelPricing
	if err := db.First(&bn, "model = ?", "brandnew").Error; err != nil {
		t.Fatal(err)
	}
	if bn.InputPerM != 5 || bn.Currency != "USD" {
		t.Fatalf("缺失条目应补插: %+v", bn)
	}
}
