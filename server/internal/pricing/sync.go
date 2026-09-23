// Package pricing 官方价目远程同步：从 models.dev 与 LiteLLM 拉取模型单价（USD/百万 token）。
// 同步语义（v1.5.70 修订，替代 v1.5.65 的「只刷新不新增」）：**全量 upsert**——
// 远程源收录的条目缺失即新增（记命中源）、已存在的远程来源条目与目录关联条目
// 刷新四档单价；manual/import 且未被目录关联的条目不覆盖、不删除（人工维护优先）。
// 官方白名单（v1.5.75）：仅收录模型厂商第一方价目（聚合商/转售商/云托管/订阅计划
// 均排除，见 officialModelsDevProviders / officialLiteLLMProviders）；来源为远程但
// 不在收录集（白名单外/远程下架）且未被目录关联的条目随同步清理，价目表保持
// 「官方集 + 人工条目」
// 双源合并（v1.5.69）：models.dev 先到先得（官方文档口径优先），LiteLLM 补缺名/裸名。
package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"keyway/internal/store"
	"keyway/internal/usage"
)

const (
	litellmURL   = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	modelsDevURL = "https://models.dev/api.json"

	// KeySyncedAt settings 表键：最近一次同步完成时间（RFC3339；双源全失败不记录）
	KeySyncedAt = "pricing_synced_at"

	// 价目来源标识（model_pricing.source）：双远程源 + 本地录入
	SrcLiteLLM   = "LiteLLM"
	SrcModelsDev = "models.dev"
	SrcManual    = "manual"
	SrcImport    = "import"
)

// officialModelsDevProviders 官方厂商白名单（models.dev provider id）：
// 仅保留模型厂商第一方价目，排除聚合商/转售商（openrouter、nano-gpt、kilo、zenmux…）
// 与云托管（azure、amazon-bedrock、google-vertex…）及订阅计划（*-coding-plan）
var officialModelsDevProviders = map[string]bool{
	"openai": true, "anthropic": true, "google": true, "xai": true, "mistral": true,
	"cohere": true, "ai21": true, "perplexity": true, "perplexity-agent": true,
	"deepseek": true, "zhipuai": true, "zai": true, "moonshotai": true, "moonshotai-cn": true,
	"alibaba": true, "alibaba-cn": true, "minimax": true, "minimax-cn": true,
	"stepfun": true, "stepfun-ai": true, "sensenova": true, "volcengine": true,
	"longcat": true, "xiaomi": true, "nvidia": true, "meta": true, "upstage": true, "sarvam": true,
}

// officialLiteLLMProviders 官方厂商白名单（litellm_provider 标识），口径同上
var officialLiteLLMProviders = map[string]bool{
	"openai": true, "text-completion-openai": true, "anthropic": true, "gemini": true,
	"mistral": true, "xai": true, "perplexity": true, "deepseek": true, "zhipu": true,
	"moonshot": true, "dashscope": true, "qwencloud": true, "qwen_ai_platform": true,
	"volcengine": true, "minimax": true, "ai21": true, "cohere": true,
	"meta_llama": true, "nvidia_nim": true,
}

// Source 官方价目同步源（名称 + 链接，供管理界面展示）
type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Sources 返回全部同步源信息
func Sources() []Source {
	return []Source{
		{Name: SrcLiteLLM, URL: litellmURL},
		{Name: SrcModelsDev, URL: modelsDevURL},
	}
}

// ModelPrice 归一化后的单模型价目（USD/百万 token）
type ModelPrice struct {
	Model           string
	InputPerM       float64
	OutputPerM      float64
	CachedInputPerM *float64
	CacheWritePerM  *float64
	Source          string
}

// mergeSources 双源合并：按传入顺序先到先得（dst 为已合并结果，src 不覆盖既有名）
func mergeSources(dst map[string]ModelPrice, src []ModelPrice) map[string]ModelPrice {
	for _, p := range src {
		if _, ok := dst[p.Model]; !ok {
			dst[p.Model] = p
		}
	}
	return dst
}

// Result 同步结果
type Result struct {
	Added     int      `json:"added"`     // 远程源收录、价目表缺失而新增的条目数
	Refreshed int      `json:"refreshed"` // 已存在且被刷新的条目数（远程来源/目录关联）
	Pruned    int      `json:"pruned"`    // 清理的条目数（非官方白名单/远程已下架的远程来源条目）
	Warnings  []string `json:"warnings,omitempty"`
}

var syncMu sync.Mutex

// SyncRemote 拉取两个源并刷新目录关联条目；单源失败降级为警告，全部失败返回错误
func SyncRemote(db *gorm.DB, timeout time.Duration) (*Result, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	client := &http.Client{Timeout: timeout}
	res := &Result{}

	// 双源合并（v1.5.69：models.dev 先到先得——官方文档口径优先，LiteLLM 补缺名/裸名）
	merged := map[string]ModelPrice{}
	modelsDev, err := fetchParse(client, modelsDevURL, parseModelsDev)
	if err != nil {
		res.Warnings = append(res.Warnings, "models.dev 拉取失败："+err.Error())
	} else {
		merged = mergeSources(merged, modelsDev)
	}
	litellm, err := fetchParse(client, litellmURL, parseLiteLLM)
	if err != nil {
		res.Warnings = append(res.Warnings, "LiteLLM 拉取失败："+err.Error())
	} else {
		merged = mergeSources(merged, litellm)
	}
	if len(res.Warnings) == 2 {
		return res, fmt.Errorf("两个价目源均拉取失败")
	}

	prices := make([]ModelPrice, 0, len(merged))
	for _, p := range merged {
		prices = append(prices, p)
	}
	res.Added, res.Refreshed, res.Pruned = upsertRemote(db, prices)

	// 记录最近一次同步完成时间（至少单源成功才记录）
	if err := markSynced(db); err != nil {
		res.Warnings = append(res.Warnings, "记录同步时间失败："+err.Error())
	}
	usage.InvalidatePricingCache(db)
	return res, nil
}

// SyncedAt 读取最近一次同步完成时间；未同步过返回空串
func SyncedAt(db *gorm.DB) string {
	var row store.Setting
	if err := db.Where("key = ?", KeySyncedAt).First(&row).Error; err != nil {
		return ""
	}
	return row.Value
}

func markSynced(db *gorm.DB) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&store.Setting{Key: KeySyncedAt, Value: time.Now().Format(time.RFC3339)}).Error
}

// StartSyncLoop 启动时执行一次，此后按周期执行；供 main 后台协程调用
func StartSyncLoop(db *gorm.DB, hours int, stop <-chan struct{}) {
	run := func() {
		res, err := SyncRemote(db, 90*time.Second)
		if err != nil {
			fmt.Printf("[keyway] 价目同步失败: %v\n", err)
			return
		}
		fmt.Printf("[keyway] 价目同步完成：新增 %d、刷新 %d、清理 %d\n", res.Added, res.Refreshed, res.Pruned)
	}
	run()
	ticker := time.NewTicker(time.Duration(hours) * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			run()
		case <-stop:
			return
		}
	}
}

// upsertRemote 全量 upsert（v1.5.70）+ 官方白名单清理（v1.5.75）：对远程源收录的每个条目——
//   - 价目表缺失 → Create（source 记命中源）
//   - 已存在且来源为远程（models.dev/LiteLLM）或被目录关联 → 刷新四档单价与 source
//   - 已存在且为 manual/import 且未被目录关联 → 跳过（人工维护优先，不覆盖）
//
// 清理：来源为远程但不在本次远程收录集（白名单过滤/远程下架）且未被目录关联的条目
// 一并删除（保持价目表 = 官方集 + 人工条目）；manual/import 条目永不删除。
// 返回 (新增数, 刷新数, 清理数)
func upsertRemote(db *gorm.DB, prices []ModelPrice) (added, refreshed, pruned int) {
	if len(prices) == 0 {
		return 0, 0, 0
	}
	var rows []store.ModelPricing
	db.Find(&rows)
	existing := make(map[string]store.ModelPricing, len(rows))
	for i := range rows {
		existing[rows[i].Model] = rows[i]
	}
	linked := linkedPricingModels(db)
	remoteSet := make(map[string]bool, len(prices))
	for _, p := range prices {
		remoteSet[p.Model] = true
	}
	now := time.Now().Unix()
	db.Transaction(func(tx *gorm.DB) error {
		for _, p := range prices {
			row, ok := existing[p.Model]
			if !ok {
				if err := tx.Create(&store.ModelPricing{
					Model: p.Model, InputPerM: p.InputPerM, OutputPerM: p.OutputPerM,
					CachedInputPerM: p.CachedInputPerM, CacheWritePerM: p.CacheWritePerM,
					Currency: "USD", Source: p.Source, UpdatedAt: now,
				}).Error; err != nil {
					continue // 单条失败跳过
				}
				existing[p.Model] = store.ModelPricing{Model: p.Model, Source: p.Source}
				added++
				continue
			}
			remoteSource := row.Source == SrcLiteLLM || row.Source == SrcModelsDev
			if !remoteSource && !linked[p.Model] {
				continue // 人工条目且未关联：不覆盖
			}
			if err := tx.Model(&store.ModelPricing{}).Where("model = ?", p.Model).Updates(map[string]any{
				"input_per_m": p.InputPerM, "output_per_m": p.OutputPerM,
				"cached_input_per_m": p.CachedInputPerM, "cache_write_per_m": p.CacheWritePerM,
				"source": p.Source, "updated_at": now,
			}).Error; err != nil {
				continue // 单条失败跳过
			}
			refreshed++
		}
		// 清理（v1.5.75）：远程来源但不在本次收录集（白名单外/已下架）且未被目录关联
		for name, row := range existing {
			if remoteSet[name] || (row.Source != SrcLiteLLM && row.Source != SrcModelsDev) || linked[name] {
				continue // 在收录集 / 人工条目 / 目录关联：保留
			}
			if err := tx.Where("model = ?", name).Delete(&store.ModelPricing{}).Error; err != nil {
				continue // 单条失败跳过
			}
			pruned++
		}
		return nil
	})
	return added, refreshed, pruned
}

// linkedPricingModels 被模型目录 pricing_model 引用的价目名集合（这些条目同步时刷新）
func linkedPricingModels(db *gorm.DB) map[string]bool {
	var names []string
	db.Model(&store.CatalogModel{}).Where("pricing_model != ''").Distinct("pricing_model").Pluck("pricing_model", &names)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func fetchParse(client *http.Client, url string, parse func([]byte) ([]ModelPrice, error)) ([]ModelPrice, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	return parse(body)
}

// ---------- LiteLLM ----------

// litellmEntry model_prices_and_context_window.json 的单条结构（仅取需要的字段）
type litellmEntry struct {
	Mode                    string  `json:"mode"`
	InputCostPerToken       float64 `json:"input_cost_per_token"`
	OutputCostPerToken      float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost float64 `json:"cache_read_input_token_cost"`
	CacheCreationTokenCost  float64 `json:"cache_creation_input_token_cost"`
	LitellmProvider         string  `json:"litellm_provider"`
}

func parseLiteLLM(body []byte) ([]ModelPrice, error) {
	var raw map[string]litellmEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("非法 JSON: %w", err)
	}
	out := make([]ModelPrice, 0, len(raw))
	for name, e := range raw {
		if name == "" || e.Mode != "chat" {
			continue
		}
		if !officialLiteLLMProviders[e.LitellmProvider] {
			continue // 仅官方厂商条目（v1.5.75）
		}
		if e.InputCostPerToken <= 0 || e.OutputCostPerToken <= 0 {
			continue // 免费或未定价条目
		}
		out = append(out, ModelPrice{
			Model:           name,
			InputPerM:       e.InputCostPerToken * 1e6,
			OutputPerM:      e.OutputCostPerToken * 1e6,
			CachedInputPerM: positivePtr(e.CacheReadInputTokenCost * 1e6),
			CacheWritePerM:  positivePtr(e.CacheCreationTokenCost * 1e6),
			Source:          SrcLiteLLM,
		})
	}
	return out, nil
}

// ---------- models.dev ----------

// modelsDevModalities models.dev 单模型的模态标注
type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// modelsDevCost 单模型价目（USD/百万 token，直取无需换算）
type modelsDevCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type modelsDevModel struct {
	Modalities *modelsDevModalities `json:"modalities"`
	Cost       *modelsDevCost       `json:"cost"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

// modelsDevResp api.json 顶层：provider_id → { models: { model_id → 单模型 } }
type modelsDevResp map[string]modelsDevProvider

// parseModelsDev 解析 models.dev api.json：条目名取 provider/model（如 anthropic/claude-sonnet-4-5），
// 仅保留输入输出模态均含 text 且输入输出价均大于 0 的条目；缓存价 0/缺失归 NULL
func parseModelsDev(body []byte) ([]ModelPrice, error) {
	var raw modelsDevResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("非法 JSON: %w", err)
	}
	out := make([]ModelPrice, 0)
	for pid, p := range raw {
		if pid == "" {
			continue
		}
		if !officialModelsDevProviders[pid] {
			continue // 仅官方厂商条目（v1.5.75）
		}
		for mid, m := range p.Models {
			if mid == "" || m.Modalities == nil || m.Cost == nil {
				continue
			}
			if !containsStr(m.Modalities.Input, "text") || !containsStr(m.Modalities.Output, "text") {
				continue // 仅文本对话模型
			}
			if m.Cost.Input <= 0 || m.Cost.Output <= 0 {
				continue // 免费或未定价条目
			}
			out = append(out, ModelPrice{
				Model:           pid + "/" + mid,
				InputPerM:       m.Cost.Input,
				OutputPerM:      m.Cost.Output,
				CachedInputPerM: positivePtr(m.Cost.CacheRead),
				CacheWritePerM:  positivePtr(m.Cost.CacheWrite),
				Source:          SrcModelsDev,
			})
		}
	}
	return out, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func positivePtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}
