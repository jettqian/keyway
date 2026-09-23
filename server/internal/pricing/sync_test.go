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

// models.dev 解析：仅保留输入输出模态均含 text 且输入输出价均大于 0 的条目；
// 价目为 USD/百万 token 直取；缓存价 0/缺失归 NULL
func TestParseModelsDev(t *testing.T) {
	body := []byte(`{
		"anthropic": {"models": {
			"claude-sonnet-4-5": {"modalities": {"input": ["text", "image"], "output": ["text"]}, "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75}},
			"claude-audio": {"modalities": {"input": ["text", "audio"], "output": ["audio"]}, "cost": {"input": 3, "output": 15}},
			"image-only": {"modalities": {"input": ["image"], "output": ["text"]}, "cost": {"input": 1, "output": 2}},
			"free-model": {"modalities": {"input": ["text"], "output": ["text"]}, "cost": {"input": 0, "output": 0}},
			"no-cost": {"modalities": {"input": ["text"], "output": ["text"]}}
		}},
		"zhipuai": {"models": {
			"glm-5.3": {"modalities": {"input": ["text"], "output": ["text"]}, "cost": {"input": 1.11, "output": 3.89, "cache_read": 0.2, "cache_write": 0}}
		}}
	}`)
	out, err := parseModelsDev(body)
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
	an := byName["anthropic/claude-sonnet-4-5"]
	if an.InputPerM != 3 || an.OutputPerM != 15 {
		t.Errorf("输入/输出取值错误：%v/%v", an.InputPerM, an.OutputPerM)
	}
	if an.CachedInputPerM == nil || *an.CachedInputPerM != 0.3 {
		t.Errorf("缓存读错误：%v", an.CachedInputPerM)
	}
	if an.CacheWritePerM == nil || *an.CacheWritePerM != 3.75 {
		t.Errorf("缓存写错误：%v", an.CacheWritePerM)
	}
	glm := byName["zhipuai/glm-5.3"]
	if glm.InputPerM != 1.11 || glm.OutputPerM != 3.89 {
		t.Errorf("glm 输入/输出取值错误：%v/%v", glm.InputPerM, glm.OutputPerM)
	}
	if glm.CachedInputPerM == nil || *glm.CachedInputPerM != 0.2 {
		t.Errorf("glm 缓存读错误：%v", glm.CachedInputPerM)
	}
	if glm.CacheWritePerM != nil {
		t.Errorf("缓存写 0 应归 NULL：%v", glm.CacheWritePerM)
	}
}

// 非法 JSON 报错而非 panic
func TestParseInvalid(t *testing.T) {
	if _, err := parseLiteLLM([]byte("not json")); err == nil {
		t.Error("LiteLLM 非法 JSON 应报错")
	}
	if _, err := parseModelsDev([]byte("not json")); err == nil {
		t.Error("models.dev 非法 JSON 应报错")
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
	if len(srcs) != 2 || srcs[0].Name != "LiteLLM" || srcs[1].Name != "models.dev" {
		t.Errorf("同步源信息不符：%+v", srcs)
	}
	for _, s := range srcs {
		if !strings.HasPrefix(s.URL, "https://") {
			t.Errorf("同步源链接非法：%s", s.URL)
		}
	}
}

// 同步落库语义（v1.5.65）：只刷新目录关联的价目条目；不补缺、不新增任何模型，
// 未关联条目不动；关联名不在价目表仅计数提示
func TestSyncRefreshLinked(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 目录关联 prov/m（在表）；prov/gone（不在表，验证不补插）
	if err := db.Create(&store.CatalogModel{Name: "cm", PricingModel: "prov/m", Enabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.CatalogModel{Name: "cm2", PricingModel: "prov/gone", Enabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "prov/m", InputPerM: 1, OutputPerM: 1, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "plain", InputPerM: 2, OutputPerM: 2, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}

	cr := 0.1
	refreshed, missing := refreshLinked(db, []ModelPrice{
		{Model: "prov/m", InputPerM: 3, OutputPerM: 7, CachedInputPerM: &cr},
		{Model: "prov/gone", InputPerM: 9, OutputPerM: 9},
		{Model: "plain", InputPerM: 4, OutputPerM: 4},
	})
	if refreshed != 1 || missing != 1 {
		t.Fatalf("期望 刷新1/缺失1，实际 %d/%d", refreshed, missing)
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
	var n int64
	db.Model(&store.ModelPricing{}).Count(&n)
	if n != 2 {
		t.Fatalf("同步不得新增条目（含关联缺失名），实际 %d 条", n)
	}
}
