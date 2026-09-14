package main

import (
	"testing"
	"time"

	"keyway/internal/convert"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// Test渠道价格倍率 倍率影响费用快照（渠道优惠场景，FR v0.8）
func Test渠道价格倍率(t *testing.T) {
	c, _ := setupApp(t)
	c.bootstrap(t, "http://upstream.invalid")

	// 设价目：mult-model 输入 $1/M、输出 $2/M
	if err := c.store.DB().Save(&store.ModelPricing{
		Model: "mult-model", InputPerM: 1, OutputPerM: 2, Currency: "USD", UpdatedAt: time.Now().Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	u := convert.Usage{PromptTokens: 10000, CompletionTokens: 1000}

	// 倍率 0.5（渠道五折）：10k 输入 → $0.005；1k 输出 → $0.001
	ic, oc := usage.ComputeCost(c.store.DB(), "mult-model", "mult-model", 0.5, u)
	if ic == nil || oc == nil {
		t.Fatal("应命中价目")
	}
	if *ic < 0.0049 || *ic > 0.0051 {
		t.Fatalf("输入费用倍率错误: %v（期望 0.005）", *ic)
	}
	if *oc < 0.0009 || *oc > 0.0011 {
		t.Fatalf("输出费用倍率错误: %v（期望 0.001）", *oc)
	}

	// 默认倍率 1：10k 输入 → $0.01
	ic2, _ := usage.ComputeCost(c.store.DB(), "mult-model", "mult-model", 1, u)
	if *ic2 < 0.0099 || *ic2 > 0.0101 {
		t.Fatalf("默认倍率输入费用错误: %v（期望 0.01）", *ic2)
	}

	// 倍率含缓存档：5k 输入 + 5k 缓存读 ×0.5 倍率
	c.store.DB().Model(&store.ModelPricing{}).Where("model = ?", "mult-model").
		Update("cached_input_per_m", 0.1)
	u2 := convert.Usage{PromptTokens: 10000, CachedTokens: 5000, CompletionTokens: 0}
	ic3, _ := usage.ComputeCost(c.store.DB(), "mult-model", "mult-model", 0.5, u2)
	// 5k×1 + 5k×0.1 = 0.0055 × 0.5 = 0.00275
	if *ic3 < 0.00274 || *ic3 > 0.00276 {
		t.Fatalf("缓存+倍率费用错误: %v（期望 0.00275）", *ic3)
	}
}
