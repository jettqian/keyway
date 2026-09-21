package usage

import (
	"database/sql"
	"fmt"
	"strconv"
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

// ComputeCost 按价目表快照计算费用；渠道计价模式：
//   - "usd"（默认）：费用 = 官方 USD 价 × multiplier（美元渠道折扣）
//   - "cny_ratio"：渠道按人民币计价（$1 官方用量实收 cnyRatio 元），如 0.5 表示 $1 → ¥0.5；
//     统计 USD 费用 = 官方价 × cnyRatio ÷ 汇率（settings.usd_cny_rate，默认 7.2）
//
// 未定价返回 (nil, nil)
func ComputeCost(db *gorm.DB, model, upstreamModel, pricingMode string, multiplier, cnyRatio float64, u convert.Usage) (inputCost, outputCost *float64) {
	if pricingMode != "cny_ratio" {
		pricingMode = "usd"
	}
	snap := pricingSnapshot(db)
	p, ok := lookupPricing(snap.table, model, upstreamModel)
	if !ok {
		return nil, nil
	}
	fx := snap.fx
	ic, oc := applyPricing(&p, pricingMode, multiplier, cnyRatio, fx, u)
	return &ic, &oc
}

// applyPricing 按价目与渠道计价模式计算费用（缓存档回退见 DESIGN §8.2）
func applyPricing(p *store.ModelPricing, pricingMode string, multiplier, cnyRatio, fxRate float64, u convert.Usage) (inputCost, outputCost float64) {
	if pricingMode != "cny_ratio" {
		pricingMode = "usd"
	}
	if multiplier <= 0 {
		multiplier = 1
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
	ic := plain*p.InputPerM +
		float64(u.CachedTokens)/1e6*cachedInput +
		float64(u.CacheWriteTokens)/1e6*cacheWrite
	oc := float64(u.CompletionTokens) / 1e6 * p.OutputPerM
	if pricingMode == "cny_ratio" {
		if cnyRatio <= 0 {
			cnyRatio = 7.2 // 兜底：按官方等价
		}
		ic = ic * cnyRatio / fxRate
		oc = oc * cnyRatio / fxRate
	} else {
		ic *= multiplier
		oc *= multiplier
	}
	return ic, oc
}

// ---------- 价目/汇率内存缓存（DESIGN §8.2：写日志时不再逐请求查库）----------

// priceCaches 按底层数据库实例缓存的价目+汇率快照。
// 生产为单实例；测试的内存库各自独立、互不污染。
// 快照不可变（读侧拿到引用后仅遍历），TTL 兜底 + 写路径显式失效双保险。
var priceCaches sync.Map // *sql.DB → *priceSnapshot

type priceSnapshot struct {
	table map[string]store.ModelPricing
	fx    float64
	at    time.Time
}

const priceCacheTTL = time.Minute

// pricingSnapshot 取价目与汇率快照：命中且未过期直接用，否则全量重建
func pricingSnapshot(db *gorm.DB) *priceSnapshot {
	key := sqlDBOf(db)
	if key != nil {
		if v, ok := priceCaches.Load(key); ok {
			if s := v.(*priceSnapshot); time.Since(s.at) < priceCacheTTL {
				return s
			}
		}
	}
	var list []store.ModelPricing
	db.Find(&list)
	table := make(map[string]store.ModelPricing, len(list))
	for i := range list {
		table[list[i].Model] = list[i]
	}
	s := &priceSnapshot{table: table, fx: usdCNYRate(db), at: time.Now()}
	if key != nil {
		priceCaches.Store(key, s)
	}
	return s
}

// InvalidatePricingCache 价目或汇率写入后按库失效（管理端 CRUD / 远程同步 / 汇率更新）
func InvalidatePricingCache(db *gorm.DB) {
	if key := sqlDBOf(db); key != nil {
		priceCaches.Delete(key)
	}
}

func sqlDBOf(db *gorm.DB) *sql.DB {
	if raw, err := db.DB(); err == nil && raw != nil {
		return raw
	}
	return nil
}

// lookupPricing 与 ComputeCost 同口径取价：upstream 优先、回退入站名
func lookupPricing(m map[string]store.ModelPricing, model, upstreamModel string) (store.ModelPricing, bool) {
	name := upstreamModel
	if name == "" {
		name = model
	}
	if p, ok := m[name]; ok {
		return p, true
	}
	if upstreamModel != "" {
		if p, ok := m[model]; ok {
			return p, true
		}
	}
	return store.ModelPricing{}, false
}

// usdCNYRate 全局美元兑人民币汇率（settings.usd_cny_rate，默认 7.2）
func usdCNYRate(db *gorm.DB) float64 {
	var setting store.Setting
	if err := db.Where("`key` = ?", "usd_cny_rate").First(&setting).Error; err != nil {
		return 7.2
	}
	if v, err := strconv.ParseFloat(setting.Value, 64); err == nil && v > 0.5 && v < 20 {
		return v
	}
	return 7.2
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
	FailedOnly bool // 仅看失败：error 非空（跨状态码——含 200+错误摘要的流内失败）
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
	if q.FailedOnly {
		tx = tx.Where("error IS NOT NULL AND error != ''")
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

// LatestUsage 最近生效流量（成功请求命中的渠道、线路与模型）
type LatestUsage struct {
	ID              int64  `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	ChannelID       int64  `json:"channelId"`
	ChannelName     string `json:"channelName,omitempty"`
	LineURL         string `json:"lineUrl,omitempty"` // 实际路由线路（base_url）
	Via             string `json:"via,omitempty"`     // 连接路径：direct | personal | proxy:{id}
	Model           string `json:"model"`
	UpstreamModel   string `json:"upstreamModel,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"` // 推理强度（v1.5.58，未开启为空）
	StatusCode      int    `json:"statusCode"`
}

// Stats 完整统计响应
type Stats struct {
	Summary   StatsSummary  `json:"summary"`
	ByUser    []StatsGroup  `json:"byUser,omitempty"` // 管理员全员统计：按用户分组（零用量用户也补齐展示）
	ByChannel []StatsGroup  `json:"byChannel"`
	ByModel   []StatsGroup  `json:"byModel"`
	ByKey     []StatsGroup  `json:"byKey"`
	ByEffort  []StatsGroup  `json:"byEffort"` // 按推理强度分组（未开启/历史数据归 '-' 行）
	Recent    []LatestUsage `json:"recent"`   // 最近生效流量（同渠道同模型去重，最新 5 个组合）
}

// QueryStats 用量/花费统计（userID 为 nil 时全员；since/until 为 Unix 秒，0 表示该侧不限；
// tokenID 非 nil 时仅统计该令牌产生的请求）
func QueryStats(db *gorm.DB, userID *int64, since, until int64, tokenID *int64) (*Stats, error) {
	if since <= 0 && until <= 0 {
		since = time.Now().AddDate(0, 0, -7).Unix()
	}
	base := func() *gorm.DB {
		tx := db.Model(&store.Log{})
		if since > 0 {
			tx = tx.Where("created_at >= ?", since)
		}
		if until > 0 {
			tx = tx.Where("created_at < ?", until)
		}
		if tokenID != nil {
			tx = tx.Where("token_id = ?", *tokenID)
		}
		return tx.Scopes(whereUser(userID))
	}

	var sum struct {
		Requests   int64
		Errors     int64
		Prompt     int64
		Completion int64
		Cost       float64
	}
	if err := base().Select(`
		COUNT(*) AS requests,
		SUM(CASE WHEN status_code >= 400 OR status_code IS NULL OR error IS NOT NULL THEN 1 ELSE 0 END) AS errors,
		COALESCE(SUM(prompt_tokens),0) AS prompt,
		COALESCE(SUM(completion_tokens),0) AS completion,
		COALESCE(SUM(input_cost),0)+COALESCE(SUM(output_cost),0) AS cost
	`).Scan(&sum).Error; err != nil {
		return nil, err
	}

	st := &Stats{
		Summary: StatsSummary{
			Requests:         sum.Requests,
			PromptTokens:     sum.Prompt,
			CompletionTokens: sum.Completion,
			Cost:             sum.Cost,
		},
	}
	if sum.Requests > 0 {
		st.Summary.ErrorRate = float64(sum.Errors) * 100 / float64(sum.Requests)
	}

	group := func(col string) []StatsGroup {
		var rows []StatsGroup
		base().Select(fmt.Sprintf(`
			IFNULL(CAST(%s AS TEXT),'-') AS dim,
			COUNT(*) AS requests,
			COALESCE(SUM(prompt_tokens),0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens),0) AS completion_tokens,
			COALESCE(SUM(input_cost),0)+COALESCE(SUM(output_cost),0) AS cost,
			SUM(CASE WHEN status_code >= 400 OR error IS NOT NULL THEN 1 ELSE 0 END) AS errors
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
	st.ByEffort = group("reasoning_effort")

	// 管理员全员统计（FR-M3）：按用户分组，并补齐零用量用户
	// （GROUP BY logs 不会为没产生过日志的成员产生行，从 users 表回填保证全员可见）
	var userNames map[int64]string
	if userID == nil {
		st.ByUser = group("user_id")
		var users []store.User
		db.Select("id, username").Order("id").Find(&users)
		userNames = make(map[int64]string, len(users))
		seen := make(map[string]bool, len(st.ByUser))
		for i := range st.ByUser {
			seen[st.ByUser[i].Dim] = true
		}
		for i := range users {
			userNames[users[i].ID] = users[i].Username
			if id := strconv.FormatInt(users[i].ID, 10); !seen[id] {
				st.ByUser = append(st.ByUser, StatsGroup{Dim: id}) // 零用量行
			}
		}
	}

	// 价目补算：写入时未定价（费用 NULL）的成功请求按当前价目补算并合入汇总与分组；
	// 补算后仍未命中价目的行才计入"部分未定价"（快照语义仅覆盖已定价行）
	chNames, keyNames, chParams := channelMaps(db, userID)
	extra, unpriced := recomputeUnpricedCost(db, base, chParams)
	st.Summary.Cost += extra.total
	st.Summary.Unpriced = unpriced > 0
	mergeCost(st.ByChannel, extra.byChannel)
	mergeCost(st.ByModel, extra.byModel)
	mergeCost(st.ByKey, extra.byKey)
	mergeCost(st.ByUser, extra.byUser)
	mergeCost(st.ByEffort, extra.byEffort)

	// 分组维度显示名称：渠道/密钥/用户 id → 名称（已删除的回退 #id）
	applyIDNames(st.ByChannel, chNames)
	applyIDNames(st.ByKey, keyNames)
	applyIDNames(st.ByUser, userNames)

	// 最近生效流量（不受统计窗口限制）：同渠道同模型只占一行——按（渠道, 模型）
	// 分组取最新一条成功日志，展示最近 5 个组合（含实际路由线路与连接路径）。
	// 成功口径与错误率一致（v1.5.55）：200 但带错误摘要（流内失败）不算成功
	var recent []struct {
		ID              int64
		CreatedAt       int64
		ChannelID       int64
		Model           string
		UpstreamModel   string
		ReasoningEffort string
		StatusCode      int
		LineURL         string
		Via             string
	}
	sub := db.Model(&store.Log{}).
		Select("MAX(id) AS id").
		Where("channel_id IS NOT NULL AND status_code < 400 AND error IS NULL").
		Scopes(whereUser(userID))
	if tokenID != nil {
		sub = sub.Where("token_id = ?", *tokenID)
	}
	sub = sub.Group("channel_id, model")
	err := db.Model(&store.Log{}).
		Where("id IN (?)", sub).
		Select("id, created_at, channel_id, COALESCE(model,'') AS model, COALESCE(upstream_model,'') AS upstream_model, COALESCE(reasoning_effort,'') AS reasoning_effort, COALESCE(status_code,0) AS status_code, COALESCE(line_url,'') AS line_url, COALESCE(via,'') AS via").
		Order("id DESC").Limit(5).
		Scan(&recent).Error
	if err != nil {
		return nil, err
	}
	for i := range recent {
		if recent[i].ChannelID == 0 {
			continue
		}
		st.Recent = append(st.Recent, LatestUsage{
			ID:              recent[i].ID,
			CreatedAt:       recent[i].CreatedAt,
			ChannelID:       recent[i].ChannelID,
			LineURL:         recent[i].LineURL,
			Via:             recent[i].Via,
			Model:           recent[i].Model,
			UpstreamModel:   recent[i].UpstreamModel,
			ReasoningEffort: recent[i].ReasoningEffort,
			StatusCode:      recent[i].StatusCode,
		})
	}
	return st, nil
}

// recomputeCosts 价目补算结果（维度 key 与 SQL 分组口径一致：渠道/密钥/用户为 id 文本，模型为名称）
type recomputeCosts struct {
	total     float64
	byChannel map[string]float64
	byModel   map[string]float64
	byKey     map[string]float64
	byUser    map[string]float64
	byEffort  map[string]float64
}

// recomputeUnpricedCost 用当前价目补算窗口内成功但未定价的行；返回补算费用与仍未定价行数。
// 渠道计价参数（模式/倍率/换算比）取当前值，渠道已删除时按 usd × 1 兜底。
func recomputeUnpricedCost(db *gorm.DB, base func() *gorm.DB, chParams map[int64]*store.Channel) (recomputeCosts, int64) {
	res := recomputeCosts{
		byChannel: map[string]float64{},
		byModel:   map[string]float64{},
		byKey:     map[string]float64{},
		byUser:    map[string]float64{},
		byEffort:  map[string]float64{},
	}
	var rows []store.Log
	if err := base().
		Select("user_id, channel_id, key_id, model, upstream_model, reasoning_effort, prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens").
		Where("input_cost IS NULL AND status_code IS NOT NULL AND status_code < 400").
		Find(&rows).Error; err != nil {
		return res, 0
	}
	if len(rows) == 0 {
		return res, 0
	}
	snap := pricingSnapshot(db)
	fx := snap.fx
	var unpriced int64
	for i := range rows {
		l := &rows[i]
		model, upstream := derefStr(l.Model), derefStr(l.UpstreamModel)
		p, ok := lookupPricing(snap.table, model, upstream)
		if !ok {
			unpriced++
			continue
		}
		mode, mult, ratio := "usd", 1.0, 0.0
		var ch *store.Channel
		if l.ChannelID != nil {
			ch = chParams[*l.ChannelID]
		}
		if ch != nil {
			mode, mult, ratio = ch.PricingMode, ch.PriceMultiplier, ch.CNYRatio
		}
		u := convert.Usage{
			PromptTokens:     int(derefI64(l.PromptTokens)),
			CompletionTokens: int(derefI64(l.CompletionTokens)),
			CachedTokens:     int(derefI64(l.CachedTokens)),
			CacheWriteTokens: int(derefI64(l.CacheWriteTokens)),
		}
		ic, oc := applyPricing(&p, mode, mult, ratio, fx, u)
		cost := ic + oc
		res.total += cost
		res.byChannel[dimOfID(l.ChannelID)] += cost
		res.byModel[dimOfStr(l.Model)] += cost
		res.byKey[dimOfID(l.KeyID)] += cost
		res.byUser[strconv.FormatInt(l.UserID, 10)] += cost
		res.byEffort[dimOfStr(l.ReasoningEffort)] += cost
	}
	return res, unpriced
}

// channelMaps 渠道/密钥 id → 名称与渠道计价参数（一次载入；用户视角只取自己的）
func channelMaps(db *gorm.DB, userID *int64) (chNames, keyNames map[int64]string, chParams map[int64]*store.Channel) {
	chNames, keyNames, chParams = map[int64]string{}, map[int64]string{}, map[int64]*store.Channel{}
	var chans []store.Channel
	q := db.Select("id, name, pricing_mode, price_multiplier, cny_ratio")
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	q.Find(&chans)
	for i := range chans {
		chNames[chans[i].ID] = chans[i].Name
		chParams[chans[i].ID] = &chans[i]
	}
	var keys []store.Key
	q = db.Select("id, name")
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	q.Find(&keys)
	for i := range keys {
		keyNames[keys[i].ID] = keys[i].Name
	}
	return chNames, keyNames, chParams
}

// applyIDNames 把 id 型分组维度替换为名称，无名称时回退 #id
func applyIDNames(rows []StatsGroup, names map[int64]string) {
	for i := range rows {
		if rows[i].Dim == "-" {
			continue
		}
		id, err := strconv.ParseInt(rows[i].Dim, 10, 64)
		if err != nil {
			continue
		}
		if n := names[id]; n != "" {
			rows[i].Dim = n
		} else {
			rows[i].Dim = "#" + rows[i].Dim
		}
	}
}

// mergeCost 把补算费用合入分组行（须在维度名称化之前调用）
func mergeCost(rows []StatsGroup, extra map[string]float64) {
	for i := range rows {
		rows[i].Cost += extra[rows[i].Dim]
	}
}

// dimOfID / dimOfStr 与分组 SQL 的 IFNULL 口径一致：NULL → "-"，其余原样
func dimOfID(p *int64) string {
	if p == nil {
		return "-"
	}
	return strconv.FormatInt(*p, 10)
}

func dimOfStr(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
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
