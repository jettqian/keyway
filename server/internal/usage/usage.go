package usage

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"keyway/internal/convert"
	"keyway/internal/store"
)

// Writer 异步日志批写（满 200 条或 1s 刷盘；队列满则丢弃保转发）
type Writer struct {
	db   *gorm.DB
	ch   chan *store.Log
	drop int64
	mu   sync.Mutex
}

func NewWriter(db *gorm.DB) *Writer {
	return &Writer{db: db, ch: make(chan *store.Log, 4096)}
}

func (w *Writer) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	buf := make([]*store.Log, 0, 200)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := w.db.Create(&buf).Error; err != nil {
			// 批量失败逐条降级，尽量保住日志
			for _, l := range buf {
				w.db.Create(l)
			}
		}
		buf = buf[:0]
	}
	for {
		select {
		case l := <-w.ch:
			buf = append(buf, l)
			if len(buf) >= 200 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-stop:
			for {
				select {
				case l := <-w.ch:
					buf = append(buf, l)
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

func (w *Writer) Submit(l *store.Log) {
	select {
	case w.ch <- l:
	default:
		w.mu.Lock()
		w.drop++
		w.mu.Unlock()
	}
}

func (w *Writer) Dropped() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.drop
}

// ---------- 费用快照（DESIGN §8.2）----------

// ComputeCost 按价目表快照计算费用；multiplier 为渠道价格倍率（默认 1）；
// 未定价返回 (nil, nil)
func ComputeCost(db *gorm.DB, model, upstreamModel string, multiplier float64, u convert.Usage) (inputCost, outputCost *float64) {
	if multiplier <= 0 {
		multiplier = 1
	}
	name := upstreamModel
	if name == "" {
		name = model
	}
	var p store.ModelPricing
	if err := db.Where("model = ?", name).First(&p).Error; err != nil {
		// 回退入站模型名
		if err := db.Where("model = ?", model).First(&p).Error; err != nil {
			return nil, nil
		}
	}
	cachedInput := p.InputPerM
	if p.CachedInputPerM != nil {
		cachedInput = *p.CachedInputPerM
	}
	cacheWrite := p.InputPerM
	if p.CacheWritePerM != nil {
		cacheWrite = *p.CacheWritePerM
	}
	plain := float64(u.PromptTokens-u.CachedTokens-u.CacheWriteTokens) / 1e6
	if plain < 0 {
		plain = 0
	}
	ic := (plain*p.InputPerM +
		float64(u.CachedTokens)/1e6*cachedInput +
		float64(u.CacheWriteTokens)/1e6*cacheWrite) * multiplier
	oc := float64(u.CompletionTokens) / 1e6 * p.OutputPerM * multiplier
	return &ic, &oc
}

// ---------- 查询 ----------

// LogQuery 日志过滤条件
type LogQuery struct {
	Page       int
	PageSize   int
	Start      int64
	End        int64
	ChannelID  *int64
	Model      string
	StatusCode *int
}

// QueryLogs 查询日志（userID 为 nil 时管理员查全部）
func QueryLogs(db *gorm.DB, userID *int64, q LogQuery) ([]store.Log, int64, error) {
	tx := db.Model(&store.Log{})
	if userID != nil {
		tx = tx.Where("user_id = ?", *userID)
	}
	if q.Start > 0 {
		tx = tx.Where("created_at >= ?", q.Start)
	}
	if q.End > 0 {
		tx = tx.Where("created_at < ?", q.End)
	}
	if q.ChannelID != nil {
		tx = tx.Where("channel_id = ?", *q.ChannelID)
	}
	if q.Model != "" {
		tx = tx.Where("model LIKE ?", "%"+q.Model+"%")
	}
	if q.StatusCode != nil {
		tx = tx.Where("status_code = ?", *q.StatusCode)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 100 {
		q.PageSize = 20
	}
	var logs []store.Log
	if err := tx.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// StatsGroup 分组统计行
type StatsGroup struct {
	Dim              string  `json:"dim"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	Cost             float64 `json:"cost"`
	Errors           int64   `json:"errors"`
}

// StatsSummary 汇总
type StatsSummary struct {
	Requests         int64   `json:"requests"`
	ErrorRate        float64 `json:"errorRate"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	Cost             float64 `json:"cost"`
	Unpriced         bool    `json:"unpriced"`
}

// Stats 完整统计响应
type Stats struct {
	Summary   StatsSummary `json:"summary"`
	ByChannel []StatsGroup `json:"byChannel"`
	ByModel   []StatsGroup `json:"byModel"`
	ByKey     []StatsGroup `json:"byKey"`
}

// QueryStats 用量/花费统计（userID 为 nil 时全员）
func QueryStats(db *gorm.DB, userID *int64, days int) (*Stats, error) {
	if days < 1 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	base := func() *gorm.DB {
		tx := db.Model(&store.Log{}).Where("created_at >= ?", since)
		if userID != nil {
			tx = tx.Where("user_id = ?", *userID)
		}
		return tx
	}

	var sum struct {
		Requests     int64
		Errors       int64
		Prompt       int64
		Completion   int64
		Cost         float64
		UnpricedRows int64
	}
	if err := base().Select(`
		COUNT(*) AS requests,
		SUM(CASE WHEN status_code >= 400 OR status_code IS NULL THEN 1 ELSE 0 END) AS errors,
		COALESCE(SUM(prompt_tokens),0) AS prompt,
		COALESCE(SUM(completion_tokens),0) AS completion,
		COALESCE(SUM(input_cost),0)+COALESCE(SUM(output_cost),0) AS cost,
		SUM(CASE WHEN input_cost IS NULL AND status_code < 400 THEN 1 ELSE 0 END) AS unpriced_rows
	`).Scan(&sum).Error; err != nil {
		return nil, err
	}

	st := &Stats{
		Summary: StatsSummary{
			Requests:         sum.Requests,
			PromptTokens:     sum.Prompt,
			CompletionTokens: sum.Completion,
			Cost:             sum.Cost,
			Unpriced:         sum.UnpricedRows > 0,
		},
	}
	if sum.Requests > 0 {
		st.Summary.ErrorRate = float64(sum.Errors) * 100 / float64(sum.Requests)
	}

	group := func(col string) []StatsGroup {
		var rows []StatsGroup
		db.Model(&store.Log{}).
			Where("created_at >= ?", since).
			Scopes(whereUser(userID)).
			Select(fmt.Sprintf(`
				IFNULL(CAST(%s AS TEXT),'-') AS dim,
				COUNT(*) AS requests,
				COALESCE(SUM(prompt_tokens),0) AS prompt_tokens,
				COALESCE(SUM(completion_tokens),0) AS completion_tokens,
				COALESCE(SUM(input_cost),0)+COALESCE(SUM(output_cost),0) AS cost,
				SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) AS errors
			`, col)).
			Group(col).
			Order("requests DESC").
			Limit(50).
			Scan(&rows)
		return rows
	}
	st.ByChannel = group("channel_id")
	st.ByModel = group("model")
	st.ByKey = group("key_id")
	return st, nil
}

func whereUser(userID *int64) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if userID != nil {
			return db.Where("user_id = ?", *userID)
		}
		return db
	}
}

// StartRetention 日志保留期清理循环（每 6 小时执行一次）
func (w *Writer) StartRetention(stop <-chan struct{}, retentionDays int) {
	if retentionDays < 1 {
		retentionDays = 30
	}
	run := func() {
		if n, err := DeleteOldLogs(w.db, retentionDays); err == nil && n > 0 {
			// 清理量写入 stderr 日志便于观察
			fmt.Printf("[keyway] 清理过期日志 %d 条\n", n)
		}
	}
	run()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			run()
		}
	}
}

// DeleteOldLogs 保留期清理
func DeleteOldLogs(db *gorm.DB, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		retentionDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	res := db.Where("created_at < ?", cutoff).Delete(&store.Log{})
	return res.RowsAffected, res.Error
}
