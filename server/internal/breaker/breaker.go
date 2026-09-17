// Package breaker 渠道×模型熔断器（DESIGN §5.4，FR-B1~FR-B6）。
//
// 动机：失败切换是按请求重放的——第一个渠道挂掉后每个新请求都会先在它身上
// 耗尽整组尝试再切到备用渠道，既浪费上游请求，也令流量在渠道间反复振荡、
// 拆散上游侧 prompt 缓存。熔断器在「渠道 × 模型」维度记住失败：
//
//   - 连续 N 个请求（默认 3）把该渠道该模型的组合全部尝试耗尽 → 熔断（open），
//     路由整渠道跳过，流量**长期稳定**停留在备用渠道（缓存命中率优先，
//     不做流量试探——试探会在渠道间反复弹跳，恰好破坏粘性）；
//   - 切回只经两条路径：后台探测成功（探测覆盖渠道模型列表前 3 个，与
//     「测试渠道」按钮同源）自动关闭；或前端手动恢复；
//   - 该模型的全部候选渠道都熔断时，relay 侧直接旁路熔断（行为与无熔断一致，
//     可用性优先，旁路期间真实流量成功同样关闭熔断）。
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
	db        *gorm.DB
	threshold int // 连续失败多少个请求后熔断

	mu     sync.Mutex
	counts map[key]int // 低于阈值的连续失败计数（内存，成功即清零）

	now func() time.Time // 可注入时钟（测试）
}

type key struct {
	ChannelID int64
	Model     string
}

// New 构造熔断引擎；failThreshold < 1 时回落默认值 3
func New(db *gorm.DB, failThreshold int) *Engine {
	if failThreshold < 1 {
		failThreshold = 3
	}
	return &Engine{
		db:        db,
		threshold: failThreshold,
		counts:    map[key]int{},
		now:       time.Now,
	}
}

// View 一次请求的候选渠道熔断视图（仅熔断中的渠道有条目）
type View map[int64]struct{}

// Open 渠道是否处于熔断中
func (v View) Open(channelID int64) bool {
	_, ok := v[channelID]
	return ok
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
		v[rows[i].ChannelID] = struct{}{}
	}
	return v
}

// RecordFailure 记一次「渠道×模型」失败：一个请求把该渠道该模型的组合尝试
// 耗尽（或以 404/透传型 4xx 失败收尾）时调用一次。达到阈值即写入熔断行；
// 行一经写入即长期有效，直到探测成功 / 手动恢复 / 旁路流量成功才关闭
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
		ChannelID: channelID,
		Model:     model,
		FailCount: n,
		OpenedAt:  now,
		LastError: truncate(errMsg, 500),
		UpdatedAt: now,
	}
	// UPSERT：首次写入落 opened_at；后续只刷新计数与最近错误
	e.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"fail_count": st.FailCount,
			"last_error": st.LastError,
			"updated_at": st.UpdatedAt,
		}),
	}).Create(&st)
}

// RecordSuccess 记一次成功：删除熔断行并清零连续失败计数（关闭熔断，
// 流量切回）。探测成功、手动恢复、全候选旁路期间的真实流量成功走这里
func (e *Engine) RecordSuccess(channelID int64, model string) {
	if e == nil || channelID == 0 || model == "" {
		return
	}
	e.mu.Lock()
	delete(e.counts, key{channelID, model})
	e.mu.Unlock()
	e.db.Where("channel_id = ? AND model = ?", channelID, model).Delete(&store.BreakerState{})
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
