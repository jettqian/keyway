// Package breaker 渠道×模型熔断器（DESIGN §5.4，FR-B1~FR-B6）。
//
// 动机：失败切换是按请求重放的——第一个渠道挂掉后每个新请求都会先在它身上
// 耗尽整组尝试再切到备用渠道，既浪费上游请求，也令流量在渠道间反复振荡、
// 拆散上游侧 prompt 缓存。熔断器在「渠道 × 模型」维度记住失败：
//
//   - 连续 N 个请求（默认 3）把该渠道该模型的组合全部尝试耗尽 → 熔断（open），
//     冷却期内路由整渠道跳过，流量稳定停留在备用渠道（缓存粘性优先）；
//   - 冷却到期后进入半开：下一个请求原子认领一次试探资格，仅放行单个组合
//     （成功 → 立即关闭、流量切回；失败 → 重新熔断并以指数退避拉长冷却）；
//   - 该模型的全部候选渠道都熔断时，relay 侧直接旁路熔断（行为与无熔断一致，
//     可用性优先，旁路期间真实流量成功同样关闭熔断）；
//   - 「测试渠道」按钮的探测成功与前端手动恢复同样关闭熔断（探测已无定时
//     循环，恢复以流量试探为主）。
//
// 状态存储：breaker_states 表只保存熔断中的行（无行 = 关闭），重启后关闭的
// 熔断需要重新累积失败；低于阈值的连续失败计数只存内存。
package breaker

import (
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"keyway/internal/store"
)

// Engine 熔断引擎（单实例内共享；所有方法并发安全，nil 接收者安全）
type Engine struct {
	db          *gorm.DB
	threshold   int           // 连续失败多少个请求后熔断
	cooldown    time.Duration // 基础冷却（半开试探间隔）
	cooldownMax time.Duration // 退避上限

	mu     sync.Mutex
	counts map[key]int // 低于阈值的连续失败计数（内存，成功即清零）

	now func() time.Time // 可注入时钟（测试）
}

type key struct {
	ChannelID int64
	Model     string
}

// New 构造熔断引擎；参数非法时回落默认值（threshold 3 / cooldown 600s / max 3600s）
func New(db *gorm.DB, failThreshold, cooldownSec, cooldownMaxSec int) *Engine {
	if failThreshold < 1 {
		failThreshold = 3
	}
	if cooldownSec < 1 {
		cooldownSec = 600
	}
	if cooldownMaxSec < cooldownSec {
		cooldownMaxSec = 3600
		if cooldownMaxSec < cooldownSec {
			cooldownMaxSec = cooldownSec
		}
	}
	return &Engine{
		db:          db,
		threshold:   failThreshold,
		cooldown:    time.Duration(cooldownSec) * time.Second,
		cooldownMax: time.Duration(cooldownMaxSec) * time.Second,
		counts:      map[key]int{},
		now:         time.Now,
	}
}

// Entry 单个渠道的熔断视图
type Entry struct {
	CooldownUntil int64 // 冷却到期时间（Unix 秒）；≤ now 表示可半开试探
}

// View 一次请求的候选渠道熔断视图（仅熔断中的渠道有条目）
type View map[int64]Entry

// Open 渠道是否处于熔断中
func (v View) Open(channelID int64) bool {
	_, ok := v[channelID]
	return ok
}

// Due 渠道熔断中且冷却已到期（可半开试探）
func (v View) Due(channelID int64, now int64) bool {
	ent, ok := v[channelID]
	return ok && ent.CooldownUntil <= now
}

// View 查询一组候选渠道在指定模型上的熔断视图；无熔断时返回 nil
func (e *Engine) View(channelIDs []int64, model string) View {
	if e == nil || len(channelIDs) == 0 || model == "" {
		return nil
	}
	var rows []store.BreakerState
	if err := e.db.Where("channel_id IN ? AND model = ?", channelIDs, model).Find(&rows).Error; err != nil {
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	v := View{}
	for i := range rows {
		v[rows[i].ChannelID] = Entry{CooldownUntil: rows[i].CooldownUntil}
	}
	return v
}

// RecordFailure 记一次「渠道×模型」失败：一个请求把该渠道该模型的组合尝试
// 耗尽（或以 404/透传型 4xx 失败收尾）时调用一次。达到阈值即写入熔断行并
// 按连续失败次数指数退避（cooldown × 2^n，上限 cooldownMax）
func (e *Engine) RecordFailure(channelID int64, model, errMsg string) {
	if e == nil || channelID == 0 || model == "" {
		return
	}
	e.mu.Lock()
	k := key{channelID, model}
	e.counts[k]++
	n := e.counts[k]
	e.mu.Unlock()
	if n < e.threshold {
		return
	}
	now := e.now().Unix()
	st := store.BreakerState{
		ChannelID:     channelID,
		Model:         model,
		FailCount:     n,
		OpenedAt:      now,
		CooldownUntil: now + int64(e.backoff(n)/time.Second),
		LastError:     truncate(errMsg, 500),
		UpdatedAt:     now,
	}
	// UPSERT：首次写入保留 opened_at；后续只刷新计数、退避与最近错误
	e.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"fail_count":     st.FailCount,
			"cooldown_until": st.CooldownUntil,
			"last_error":     st.LastError,
			"updated_at":     st.UpdatedAt,
		}),
	}).Create(&st)
}

// RecordSuccess 记一次成功：删除熔断行并清零连续失败计数（关闭熔断，
// 流量切回）。半开试探成功、探测成功、手动恢复、旁路流量成功走这里
func (e *Engine) RecordSuccess(channelID int64, model string) {
	if e == nil || channelID == 0 || model == "" {
		return
	}
	e.mu.Lock()
	delete(e.counts, key{channelID, model})
	e.mu.Unlock()
	e.db.Where("channel_id = ? AND model = ?", channelID, model).Delete(&store.BreakerState{})
}

// ClaimHalfOpen 原子认领一次半开试探：仅当冷却已到期时成功，并把冷却窗口
// 顺延一个周期（试探期间并发请求不再进入该渠道；试探失败会以更大退避覆盖，
// 成功则整行删除）。认领发生在试探真正执行前，避免高优先级渠道持续成功时
// 无谓消耗低优先级渠道的试探窗口
func (e *Engine) ClaimHalfOpen(channelID int64, model string) bool {
	if e == nil || channelID == 0 || model == "" {
		return false
	}
	now := e.now().Unix()
	res := e.db.Model(&store.BreakerState{}).
		Where("channel_id = ? AND model = ? AND cooldown_until <= ?", channelID, model, now).
		Update("cooldown_until", now+int64(e.cooldown/time.Second))
	return res.Error == nil && res.RowsAffected == 1
}

// Reset 手动恢复（前端「恢复」按钮）：删除熔断行并清零计数，下一个请求即
// 恢复路由；model 为空 = 恢复该渠道全部模型
func (e *Engine) Reset(channelID int64, model string) {
	if e == nil || channelID == 0 {
		return
	}
	q := e.db.Where("channel_id = ?", channelID)
	if model != "" {
		q = q.Where("model = ?", model)
	}
	q.Delete(&store.BreakerState{})
	e.mu.Lock()
	for k := range e.counts {
		if k.ChannelID == channelID && (model == "" || k.Model == model) {
			delete(e.counts, k)
		}
	}
	e.mu.Unlock()
}

// backoff 第 n 次连续失败（n ≥ threshold）的冷却时长：
// cooldown × 2^(n-threshold)，指数封顶 2^5，绝对上限 cooldownMax
func (e *Engine) backoff(n int) time.Duration {
	over := n - e.threshold
	if over < 0 {
		over = 0
	}
	if over > 5 {
		over = 5
	}
	wait := e.cooldown << uint(over)
	if wait > e.cooldownMax || wait <= 0 { // 移位溢出防护
		wait = e.cooldownMax
	}
	return wait
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
