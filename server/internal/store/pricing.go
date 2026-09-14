package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 内置价目：公开定价的近似值（每百万 token），管理员可在价目管理页修改；
// 种子按 model 主键冲突即跳过，不会覆盖已有改动
var seedPricing = []ModelPricing{
	// OpenAI（USD）
	{Model: "gpt-4o", InputPerM: 2.5, CachedInputPerM: f64Ptr(1.25), OutputPerM: 10, Currency: "USD"},
	{Model: "gpt-4o-mini", InputPerM: 0.15, CachedInputPerM: f64Ptr(0.075), OutputPerM: 0.6, Currency: "USD"},
	{Model: "gpt-4.1", InputPerM: 2, CachedInputPerM: f64Ptr(0.5), OutputPerM: 8, Currency: "USD"},
	{Model: "gpt-4.1-mini", InputPerM: 0.4, CachedInputPerM: f64Ptr(0.1), OutputPerM: 1.6, Currency: "USD"},
	// Anthropic（USD；缓存读 0.1×、缓存写 1.25× 输入价）
	{Model: "claude-sonnet-4-20250514", InputPerM: 3, CachedInputPerM: f64Ptr(0.3), CacheWritePerM: f64Ptr(3.75), OutputPerM: 15, Currency: "USD"},
	{Model: "claude-opus-4-20250514", InputPerM: 15, CachedInputPerM: f64Ptr(1.5), CacheWritePerM: f64Ptr(18.75), OutputPerM: 75, Currency: "USD"},
	{Model: "claude-3-5-haiku-20241022", InputPerM: 0.8, CachedInputPerM: f64Ptr(0.08), CacheWritePerM: f64Ptr(1), OutputPerM: 4, Currency: "USD"},
	// DeepSeek（USD）
	{Model: "deepseek-chat", InputPerM: 0.27, CachedInputPerM: f64Ptr(0.07), OutputPerM: 1.1, Currency: "USD"},
	{Model: "deepseek-reasoner", InputPerM: 0.55, CachedInputPerM: f64Ptr(0.14), OutputPerM: 2.19, Currency: "USD"},
	// 智谱（CNY）
	{Model: "glm-4.5", InputPerM: 0.8, CachedInputPerM: f64Ptr(0.16), OutputPerM: 2, Currency: "CNY"},
	{Model: "glm-4.5-air", InputPerM: 0.2, CachedInputPerM: f64Ptr(0.04), OutputPerM: 0.6, Currency: "CNY"},
	// Moonshot（CNY）
	{Model: "kimi-k2-0711-preview", InputPerM: 4, CachedInputPerM: f64Ptr(1), OutputPerM: 16, Currency: "CNY"},
	{Model: "moonshot-v1-128k", InputPerM: 60, OutputPerM: 60, Currency: "CNY"},
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
