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
		"glm-5.3": {"mode": "chat", "input_cost_per_token": 0.00000111, "output_cost_per_token": 0.00000389, "litellm_provider": "zhipu"},
		"openrouter/gpt-4o": {"mode": "chat", "input_cost_per_token": 0.0000025, "output_cost_per_token": 0.00001, "litellm_provider": "openrouter"},
		"bedrock/claude": {"mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "bedrock"}
	}`)
	out, err := parseLiteLLM(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("期望 2 条（非官方白名单 provider 应剔除），实际 %d", len(out))
	}
	byName := map[string]ModelPrice{}
	for _, p := range out {
		byName[p.Model] = p
	}
	an := byName["claude-sonnet-4-6"]
	if an.InputPerM != 3 || an.OutputPerM != 15 {
		t.Errorf("输入/输出换算错误：%v/%v", an.InputPerM, an.OutputPerM)
	}
	if an.Source != SrcLiteLLM {
		t.Errorf("来源标识错误：%q", an.Source)
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
		}},
		"kilo": {"models": {
			"z-ai/glm-5.3": {"modalities": {"input": ["text"], "output": ["text"]}, "cost": {"input": 1, "output": 2}}
		}}
	}`)
	out, err := parseModelsDev(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("期望 2 条（非官方白名单 provider 应剔除），实际 %d", len(out))
	}
	byName := map[string]ModelPrice{}
	for _, p := range out {
		byName[p.Model] = p
	}
	an := byName["anthropic/claude-sonnet-4-5"]
	if an.InputPerM != 3 || an.OutputPerM != 15 {
		t.Errorf("输入/输出取值错误：%v/%v", an.InputPerM, an.OutputPerM)
	}
	if an.Source != SrcModelsDev {
		t.Errorf("来源标识错误：%q", an.Source)
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

// 双源合并优先级（v1.5.69）：models.dev 先到先得，同名 LiteLLM 不抢占；LiteLLM 补缺名
func TestMergeSourcesPriority(t *testing.T) {
	modelsDev := []ModelPrice{
		{Model: "zai/glm-5.3-flash", InputPerM: 0.15, OutputPerM: 0.5, Source: SrcModelsDev},
	}
	litellm := []ModelPrice{
		{Model: "zai/glm-5.3-flash", InputPerM: 9, OutputPerM: 9, Source: SrcLiteLLM}, // 同名，不得覆盖
		{Model: "gpt-4o", InputPerM: 2.5, OutputPerM: 10, Source: SrcLiteLLM},         // 裸名补缺
	}
	merged := mergeSources(mergeSources(map[string]ModelPrice{}, modelsDev), litellm)
	if len(merged) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(merged))
	}
	flash := merged["zai/glm-5.3-flash"]
	if flash.Source != SrcModelsDev || flash.InputPerM != 0.15 {
		t.Errorf("同名应保留 models.dev 条目：%+v", flash)
	}
	gpt := merged["gpt-4o"]
	if gpt.Source != SrcLiteLLM {
		t.Errorf("缺名应由 LiteLLM 补齐：%+v", gpt)
	}
}

// 全量 upsert 落库语义（v1.5.70）：远程条目缺失即新增（记命中源）；已存在的远程来源
// 条目与目录关联条目刷新四档价；manual/import 且未关联的条目不覆盖、同步永不删除
func TestSyncUpsertRemote(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 目录关联 prov/linked-manual（人工条目但被关联 → 应刷新且来源转为远程）
	if err := db.Create(&store.CatalogModel{Name: "cm", PricingModel: "prov/linked-manual", Enabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "prov/linked-manual", InputPerM: 1, OutputPerM: 1, Currency: "USD", Source: "manual"}).Error; err != nil {
		t.Fatal(err)
	}
	// 远程来源条目 → 刷新；人工未关联条目 → 跳过；非官方远程条目（不在收录集）→ 清理
	if err := db.Create(&store.ModelPricing{Model: "prov/remote", InputPerM: 1, OutputPerM: 1, Currency: "USD", Source: SrcLiteLLM}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "my-manual", InputPerM: 8, OutputPerM: 28, Currency: "USD", Source: "manual"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "prov/gone-official", InputPerM: 1, OutputPerM: 1, Currency: "USD", Source: SrcModelsDev}).Error; err != nil {
		t.Fatal(err)
	}
	// 人工条目远程源没有同名 → 不得删除

	cr := 0.1
	added, refreshed, pruned := upsertRemote(db, []ModelPrice{
		{Model: "prov/linked-manual", InputPerM: 3, OutputPerM: 7, CachedInputPerM: &cr, Source: SrcModelsDev},
		{Model: "prov/remote", InputPerM: 2, OutputPerM: 5, Source: SrcModelsDev},
		{Model: "prov/new", InputPerM: 0.5, OutputPerM: 2, Source: SrcModelsDev}, // 缺失 → 新增
		{Model: "other/new", InputPerM: 1, OutputPerM: 1, Source: SrcLiteLLM},    // 缺失 → 新增
	})
	if added != 2 || refreshed != 2 || pruned != 1 {
		t.Fatalf("期望 新增2/刷新2/清理1，实际 %d/%d/%d", added, refreshed, pruned)
	}

	var linkedManual store.ModelPricing
	if err := db.First(&linkedManual, "model = ?", "prov/linked-manual").Error; err != nil {
		t.Fatal(err)
	}
	if linkedManual.InputPerM != 3 || linkedManual.OutputPerM != 7 || linkedManual.Source != SrcModelsDev {
		t.Fatalf("关联人工条目应刷新且来源转为远程: %+v", linkedManual)
	}
	var remoteRow store.ModelPricing
	if err := db.First(&remoteRow, "model = ?", "prov/remote").Error; err != nil {
		t.Fatal(err)
	}
	if remoteRow.InputPerM != 2 || remoteRow.OutputPerM != 5 {
		t.Fatalf("远程来源条目应刷新: %+v", remoteRow)
	}
	var addedRow store.ModelPricing
	if err := db.First(&addedRow, "model = ?", "prov/new").Error; err != nil {
		t.Fatal(err)
	}
	if addedRow.InputPerM != 0.5 || addedRow.Source != SrcModelsDev || addedRow.Currency != "USD" {
		t.Fatalf("缺失条目应新增并记命中源: %+v", addedRow)
	}
	var manual store.ModelPricing
	if err := db.First(&manual, "model = ?", "my-manual").Error; err != nil {
		t.Fatal(err)
	}
	if manual.InputPerM != 8 || manual.OutputPerM != 28 || manual.Source != "manual" {
		t.Fatalf("人工未关联条目不得被覆盖: %+v", manual)
	}
	var n int64
	db.Model(&store.ModelPricing{}).Count(&n)
	if n != 5 {
		t.Fatalf("清理后期望总数 5（人工条目保留、非官方远程条目清理），实际 %d 条", n)
	}
	if err := db.First(&store.ModelPricing{}, "model = ?", "prov/gone-official").Error; err == nil {
		t.Fatal("不在收录集的远程来源条目应被清理")
	}
}
