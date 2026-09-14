package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 内置价目（每百万 token，统一 USD）：
// - OpenAI / Anthropic / DeepSeek：2026-09 按官方定价页实时抓取（DeepSeek 取峰值价）
// - Kimi：官方 CNY 定价按 1 USD ≈ 7.2 CNY 折算
// - GLM：官网 JS 渲染无法抓取，保留近似值，请管理员按 open.bigmodel.cn 核准
// 种子按 model 主键冲突即跳过，不会覆盖已有改动
var seedPricing = []ModelPricing{
	// OpenAI（2026-09 实时，Standard 短上下文）
	{Model: "gpt-6-astra", InputPerM: 10, CachedInputPerM: f64Ptr(1), CacheWritePerM: f64Ptr(12.5), OutputPerM: 50, Currency: "USD"},
	{Model: "gpt-5.6-sol", InputPerM: 4, CachedInputPerM: f64Ptr(0.4), CacheWritePerM: f64Ptr(5), OutputPerM: 20, Currency: "USD"},
	{Model: "gpt-5.6-terra", InputPerM: 2, CachedInputPerM: f64Ptr(0.2), CacheWritePerM: f64Ptr(2.5), OutputPerM: 12, Currency: "USD"},
	{Model: "gpt-5.6-luna", InputPerM: 0.2, CachedInputPerM: f64Ptr(0.02), CacheWritePerM: f64Ptr(0.25), OutputPerM: 1.2, Currency: "USD"},
	{Model: "gpt-5.5", InputPerM: 5, CachedInputPerM: f64Ptr(0.5), OutputPerM: 30, Currency: "USD"},
	{Model: "gpt-5.4", InputPerM: 2.5, CachedInputPerM: f64Ptr(0.25), OutputPerM: 15, Currency: "USD"},
	{Model: "gpt-5.4-mini", InputPerM: 0.75, CachedInputPerM: f64Ptr(0.075), OutputPerM: 4.5, Currency: "USD"},
	{Model: "gpt-5.4-nano", InputPerM: 0.2, CachedInputPerM: f64Ptr(0.02), OutputPerM: 1.25, Currency: "USD"},
	{Model: "gpt-5.2", InputPerM: 1.75, CachedInputPerM: f64Ptr(0.175), OutputPerM: 14, Currency: "USD"},
	{Model: "gpt-5.1", InputPerM: 1.25, CachedInputPerM: f64Ptr(0.125), OutputPerM: 10, Currency: "USD"},
	{Model: "gpt-5", InputPerM: 1.25, CachedInputPerM: f64Ptr(0.125), OutputPerM: 10, Currency: "USD"},
	{Model: "gpt-5-mini", InputPerM: 0.25, CachedInputPerM: f64Ptr(0.025), OutputPerM: 2, Currency: "USD"},
	{Model: "gpt-5-nano", InputPerM: 0.05, CachedInputPerM: f64Ptr(0.005), OutputPerM: 0.4, Currency: "USD"},
	{Model: "gpt-4.1", InputPerM: 2, CachedInputPerM: f64Ptr(0.5), OutputPerM: 8, Currency: "USD"},
	{Model: "gpt-4.1-mini", InputPerM: 0.4, CachedInputPerM: f64Ptr(0.1), OutputPerM: 1.6, Currency: "USD"},
	{Model: "gpt-4o", InputPerM: 2.5, CachedInputPerM: f64Ptr(1.25), OutputPerM: 10, Currency: "USD"},
	{Model: "gpt-4o-mini", InputPerM: 0.15, CachedInputPerM: f64Ptr(0.075), OutputPerM: 0.6, Currency: "USD"},

	// Anthropic（2026-09 实时；缓存读 0.1×、写 1.25× 输入价）
	{Model: "claude-opus-5", InputPerM: 5, CachedInputPerM: f64Ptr(0.5), CacheWritePerM: f64Ptr(6.25), OutputPerM: 25, Currency: "USD"},
	{Model: "claude-sonnet-5", InputPerM: 2, CachedInputPerM: f64Ptr(0.2), CacheWritePerM: f64Ptr(2.5), OutputPerM: 10, Currency: "USD"},
	{Model: "claude-haiku-4-5", InputPerM: 1, CachedInputPerM: f64Ptr(0.1), CacheWritePerM: f64Ptr(1.25), OutputPerM: 5, Currency: "USD"},
	{Model: "claude-sonnet-4-6", InputPerM: 3, CachedInputPerM: f64Ptr(0.3), CacheWritePerM: f64Ptr(3.75), OutputPerM: 15, Currency: "USD"},
	{Model: "claude-sonnet-4-20250514", InputPerM: 3, CachedInputPerM: f64Ptr(0.3), CacheWritePerM: f64Ptr(3.75), OutputPerM: 15, Currency: "USD"},
	{Model: "claude-opus-4-20250514", InputPerM: 15, CachedInputPerM: f64Ptr(1.5), CacheWritePerM: f64Ptr(18.75), OutputPerM: 75, Currency: "USD"},
	{Model: "claude-3-5-haiku-20241022", InputPerM: 0.8, CachedInputPerM: f64Ptr(0.08), CacheWritePerM: f64Ptr(1), OutputPerM: 4, Currency: "USD"},

	// DeepSeek（2026-09 实时，取峰值价；off-peak 减半）
	{Model: "deepseek-flash", InputPerM: 0.3, CachedInputPerM: f64Ptr(0.006), OutputPerM: 1.2, Currency: "USD"},
	{Model: "deepseek-v4-pro", InputPerM: 1.32, CachedInputPerM: f64Ptr(0.044), OutputPerM: 3.96, Currency: "USD"},
	// 旧模型名（保留以匹配历史日志）
	{Model: "deepseek-chat", InputPerM: 0.27, CachedInputPerM: f64Ptr(0.07), OutputPerM: 1.1, Currency: "USD"},
	{Model: "deepseek-reasoner", InputPerM: 0.55, CachedInputPerM: f64Ptr(0.14), OutputPerM: 2.19, Currency: "USD"},

	// Kimi（官方 CNY 按 7.2 折算 USD；2026-09 实时）
	{Model: "kimi-k3", InputPerM: 2.78, CachedInputPerM: f64Ptr(0.28), OutputPerM: 13.89, Currency: "USD"},
	{Model: "kimi-k2.7-code", InputPerM: 0.9, CachedInputPerM: f64Ptr(0.18), OutputPerM: 3.75, Currency: "USD"},
	{Model: "kimi-k2.6", InputPerM: 0.9, CachedInputPerM: f64Ptr(0.15), OutputPerM: 3.75, Currency: "USD"},
	{Model: "kimi-k2-0711-preview", InputPerM: 0.56, CachedInputPerM: f64Ptr(0.14), OutputPerM: 2.22, Currency: "USD"},

	// GLM（官网 JS 渲染无法抓取，近似值，请核准）
	{Model: "glm-4.5", InputPerM: 0.11, CachedInputPerM: f64Ptr(0.022), OutputPerM: 0.28, Currency: "USD"},
	{Model: "glm-4.5-air", InputPerM: 0.028, CachedInputPerM: f64Ptr(0.0056), OutputPerM: 0.083, Currency: "USD"},
}

// f64Ptr 浮点指针辅助
func f64Ptr(f float64) *float64 { return &f }

// seedModelPricing 首次插入内置价目，冲突即跳过
func seedModelPricing(db *gorm.DB) error {
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&seedPricing).Error; err != nil {
		return fmt.Errorf("写入价目种子失败: %w", err)
	}
	return nil
}
