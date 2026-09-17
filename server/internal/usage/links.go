package usage

import (
	"time"

	"gorm.io/gorm"

	"keyway/internal/store"
)

// LinkStat 渠道×模型链路状态（FR-B7）：聚合窗口内该组合的全部**上游尝试**
// （v1.5.42 起逐次尝试各记一条日志，口径与统计页一致——一个客户端请求失败
// 切换 N 次即 N 条）。ChannelName/Owner/Breaker 由 API 层补充
type LinkStat struct {
	ChannelID   int64        `json:"channelId"`
	Model       string       `json:"model"`
	Attempts    int64        `json:"attempts"` // 上游尝试次数（含失败切换的中间尝试）
	OK          int64        `json:"ok"`       // 2xx/3xx 尝试次数
	ErrorRate   int64        `json:"errorRate"`
	AvgMs       int64        `json:"avgMs"`  // 尝试平均耗时（失败尝试也计时）
	LastAt      int64        `json:"lastAt"` // 最近一次尝试（Unix 秒）
	ChannelName string       `json:"channelName,omitempty"`
	Owner       string       `json:"owner,omitempty"`   // 管理端：渠道所有者
	Breaker     *LinkBreaker `json:"breaker,omitempty"` // 当前熔断中（无 = 关闭）
}

// LinkBreaker 链路上的熔断状态快照（与渠道页明细同源）
type LinkBreaker struct {
	FailCount     int    `json:"failCount"`
	CooldownUntil int64  `json:"cooldownUntil"`
	LastError     string `json:"lastError"`
}

// QueryLinks 渠道×模型链路状态聚合（userID 为 nil 时全员——管理端全局视角；
// since/until 为 Unix 秒，0 表示该侧不限）。只聚合真实落过日志的组合，
// 熔断中的渠道×模型即使窗口内零尝试也会由 API 层补零行展示
func QueryLinks(db *gorm.DB, userID *int64, since, until int64) ([]LinkStat, error) {
	if since <= 0 && until <= 0 {
		since = time.Now().AddDate(0, 0, -7).Unix()
	}
	tx := db.Model(&store.Log{}).
		Where("channel_id IS NOT NULL AND model IS NOT NULL AND model != ''")
	if since > 0 {
		tx = tx.Where("created_at >= ?", since)
	}
	if until > 0 {
		tx = tx.Where("created_at < ?", until)
	}
	tx = tx.Scopes(whereUser(userID))
	var rows []struct {
		ChannelID int64
		Model     string
		Attempts  int64
		OK        int64
		AvgMs     int64
		LastAt    int64
	}
	if err := tx.Select(`
		channel_id,
		model,
		COUNT(*) AS attempts,
		SUM(CASE WHEN status_code IS NOT NULL AND status_code < 400 THEN 1 ELSE 0 END) AS ok,
		CAST(AVG(total_ms) AS INTEGER) AS avg_ms,
		MAX(created_at) AS last_at
	`).Group("channel_id, model").Order("attempts DESC").Limit(500).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]LinkStat, 0, len(rows))
	for _, r := range rows {
		l := LinkStat{
			ChannelID: r.ChannelID, Model: r.Model,
			Attempts: r.Attempts, OK: r.OK, AvgMs: r.AvgMs, LastAt: r.LastAt,
		}
		if l.Attempts > 0 {
			l.ErrorRate = (l.Attempts - l.OK) * 100 / l.Attempts
		}
		out = append(out, l)
	}
	return out, nil
}
