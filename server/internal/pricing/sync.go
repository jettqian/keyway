// Package pricing 官方价目远程同步：从 LiteLLM 与 OpenRouter 拉取模型单价（USD/百万 token）。
// 同步语义（v1.5.65）：**只刷新目录关联的价目条目**——被模型目录 pricing_model 引用的
// 价目条目用网络最新价覆盖四档单价；不补缺、不新增任何模型（价目表增长完全由管理员
// 控制：手工录入 / JSON 导入；删除永久生效）
package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"keyway/internal/store"
	"keyway/internal/usage"
)

const (
	litellmURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	openrouterURL = "https://openrouter.ai/api/v1/models"

	// KeySyncedAt settings 表键：最近一次同步完成时间（RFC3339；双源全失败不记录）
	KeySyncedAt = "pricing_synced_at"
)

// Source 官方价目同步源（名称 + 链接，供管理界面展示）
type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Sources 返回全部同步源信息
func Sources() []Source {
	return []Source{
		{Name: "LiteLLM", URL: litellmURL},
		{Name: "OpenRouter", URL: openrouterURL},
	}
}

// ModelPrice 归一化后的单模型价目（USD/百万 token）
type ModelPrice struct {
	Model           string
	InputPerM       float64
	OutputPerM      float64
	CachedInputPerM *float64
	CacheWritePerM  *float64
}

// Result 同步结果
type Result struct {
	Refreshed int      `json:"refreshed"` // 目录关联条目刷新数
	Missing   int      `json:"missing"`   // 关联名不在价目表（同步不新增，仅提示；v1.5.65）
	Warnings  []string `json:"warnings,omitempty"`
}

var syncMu sync.Mutex

// SyncRemote 拉取两个源并刷新目录关联条目；单源失败降级为警告，全部失败返回错误
func SyncRemote(db *gorm.DB, timeout time.Duration) (*Result, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	client := &http.Client{Timeout: timeout}
	res := &Result{}

	// 双源合并（LiteLLM 先到先得，OpenRouter 补缺名）
	merged := map[string]ModelPrice{}
	litellm, err := fetchParse(client, litellmURL, parseLiteLLM)
	if err != nil {
		res.Warnings = append(res.Warnings, "LiteLLM 拉取失败："+err.Error())
	} else {
		for _, p := range litellm {
			if _, ok := merged[p.Model]; !ok {
				merged[p.Model] = p
			}
		}
	}
	openrouter, err := fetchParse(client, openrouterURL, parseOpenRouter)
	if err != nil {
		res.Warnings = append(res.Warnings, "OpenRouter 拉取失败："+err.Error())
	} else {
		for _, p := range openrouter {
			if _, ok := merged[p.Model]; !ok {
				merged[p.Model] = p
			}
		}
	}
	if len(res.Warnings) == 2 {
		return res, fmt.Errorf("两个价目源均拉取失败")
	}

	prices := make([]ModelPrice, 0, len(merged))
	for _, p := range merged {
		prices = append(prices, p)
	}
	res.Refreshed, res.Missing = refreshLinked(db, prices)

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
		fmt.Printf("[keyway] 价目同步完成：关联刷新 %d、关联缺失 %d\n", res.Refreshed, res.Missing)
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

// refreshLinked 仅刷新目录关联的价目条目（v1.5.65：同步不再补缺/新增模型）。
// 关联名不在价目表的不创建（missing 计数提示）；远端源没有的关联名保持现状。
// 返回 (刷新数, 关联名不在价目表的条数)
func refreshLinked(db *gorm.DB, prices []ModelPrice) (refreshed, missing int) {
	if len(prices) == 0 {
		return 0, 0
	}
	src := make(map[string]ModelPrice, len(prices))
	for _, p := range prices {
		src[p.Model] = p
	}
	var rows []store.ModelPricing
	db.Find(&rows)
	linked := linkedPricingModels(db)
	now := time.Now().Unix()
	inTable := make(map[string]bool, len(rows))
	db.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			name := rows[i].Model
			inTable[name] = true
			if !linked[name] {
				continue
			}
			p, ok := src[name]
			if !ok {
				continue // 远端源没有此关联名：人工维护优先，保持现状
			}
			// 关联条目：按网络最新价覆盖四档单价
			if err := tx.Model(&store.ModelPricing{}).Where("model = ?", name).Updates(map[string]any{
				"input_per_m": p.InputPerM, "output_per_m": p.OutputPerM,
				"cached_input_per_m": p.CachedInputPerM, "cache_write_per_m": p.CacheWritePerM,
				"updated_at": now,
			}).Error; err != nil {
				continue // 单条失败跳过
			}
			refreshed++
		}
		return nil
	})
	for name := range linked {
		if !inTable[name] {
			missing++
		}
	}
	return refreshed, missing
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
		if e.InputCostPerToken <= 0 || e.OutputCostPerToken <= 0 {
			continue // 免费或未定价条目
		}
		out = append(out, ModelPrice{
			Model:           name,
			InputPerM:       e.InputCostPerToken * 1e6,
			OutputPerM:      e.OutputCostPerToken * 1e6,
			CachedInputPerM: positivePtr(e.CacheReadInputTokenCost * 1e6),
			CacheWritePerM:  positivePtr(e.CacheCreationTokenCost * 1e6),
		})
	}
	return out, nil
}

// ---------- OpenRouter ----------

type openrouterResp struct {
	Data []struct {
		ID           string `json:"id"`
		Architecture *struct {
			Modality string `json:"modality"`
		} `json:"architecture"`
		Pricing *struct {
			Prompt          string `json:"prompt"`
			Completion      string `json:"completion"`
			InputCacheRead  string `json:"input_cache_read"`
			InputCacheWrite string `json:"input_cache_write"`
		} `json:"pricing"`
	} `json:"data"`
}

func parseOpenRouter(body []byte) ([]ModelPrice, error) {
	var raw openrouterResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("非法 JSON: %w", err)
	}
	out := make([]ModelPrice, 0, len(raw.Data))
	for _, m := range raw.Data {
		if m.ID == "" || m.Pricing == nil {
			continue
		}
		if m.Architecture != nil && m.Architecture.Modality != "text->text" {
			continue // 仅文本对话模型
		}
		in, err := strconv.ParseFloat(m.Pricing.Prompt, 64)
		if err != nil || in <= 0 {
			continue
		}
		outv, err := strconv.ParseFloat(m.Pricing.Completion, 64)
		if err != nil || outv <= 0 {
			continue
		}
		cr, e1 := strconv.ParseFloat(m.Pricing.InputCacheRead, 64)
		cw, e2 := strconv.ParseFloat(m.Pricing.InputCacheWrite, 64)
		var cached, cachew *float64
		if e1 == nil && cr > 0 {
			v := cr * 1e6
			cached = &v
		}
		if e2 == nil && cw > 0 {
			v := cw * 1e6
			cachew = &v
		}
		out = append(out, ModelPrice{
			Model:           m.ID,
			InputPerM:       in * 1e6,
			OutputPerM:      outv * 1e6,
			CachedInputPerM: cached,
			CacheWritePerM:  cachew,
		})
	}
	return out, nil
}

func positivePtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}
