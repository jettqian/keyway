package breaker

import (
	"testing"
	"time"

	"keyway/internal/store"
)

func openEngine(t *testing.T, threshold, cooldownSec, cooldownMaxSec int) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := New(st.DB(), threshold, cooldownSec, cooldownMaxSec)
	return e, st
}

func (e *Engine) row(t *testing.T, channelID int64, model string) *store.BreakerState {
	t.Helper()
	var r store.BreakerState
	if err := e.db.Where("channel_id = ? AND model = ?", channelID, model).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// 阈值语义：低于阈值只累计内存计数（无行），达到阈值落熔断行
func Test阈值内不熔断达到阈值熔断(t *testing.T) {
	e, _ := openEngine(t, 3, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }

	e.RecordFailure(1, "gpt-x", "err-1")
	e.RecordFailure(2, "gpt-x", "err-2")
	if r := e.row(t, 1, "gpt-x"); r != nil {
		t.Fatalf("第 1 次失败不应熔断: %+v", r)
	}
	e.RecordFailure(1, "gpt-x", "err-3")
	if r := e.row(t, 1, "gpt-x"); r != nil {
		t.Fatalf("第 2 次失败不应熔断: %+v", r)
	}
	e.RecordFailure(1, "gpt-x", "err-4")
	r := e.row(t, 1, "gpt-x")
	if r == nil {
		t.Fatal("第 3 次连续失败应熔断")
	}
	if r.FailCount != 3 || r.OpenedAt != base.Unix() {
		t.Fatalf("熔断行字段不符: %+v", r)
	}
	if r.CooldownUntil != base.Add(300*time.Second).Unix() {
		t.Fatalf("首次熔断冷却应为基础周期: %d", r.CooldownUntil)
	}
	if r.LastError != "err-4" {
		t.Fatalf("应记录最近错误: %q", r.LastError)
	}
	// 渠道 2 只失败 1 次：不熔断
	if r := e.row(t, 2, "gpt-x"); r != nil {
		t.Fatalf("渠道 2 不应熔断: %+v", r)
	}
}

// 成功清零：熔断关闭后重新计数，opened_at 保留首次时间
func Test成功关闭并重新计数(t *testing.T) {
	e, _ := openEngine(t, 2, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }

	e.RecordFailure(1, "gpt-x", "e1")
	e.RecordFailure(1, "gpt-x", "e2")
	if r := e.row(t, 1, "gpt-x"); r == nil {
		t.Fatal("应已熔断")
	}
	e.RecordSuccess(1, "gpt-x")
	if r := e.row(t, 1, "gpt-x"); r != nil {
		t.Fatalf("成功后应关闭熔断: %+v", r)
	}
	// 重新累计：1 次失败不熔断（计数已清零）
	e.RecordFailure(1, "gpt-x", "e3")
	if r := e.row(t, 1, "gpt-x"); r != nil {
		t.Fatalf("清零后单次失败不应熔断: %+v", r)
	}
	e.RecordFailure(1, "gpt-x", "e4")
	r := e.row(t, 1, "gpt-x")
	if r == nil || r.FailCount != 2 {
		t.Fatalf("再次达到阈值应熔断: %+v", r)
	}
}

// 指数退避：连续失败翻倍冷却，封顶 cooldownMax
func Test指数退避封顶(t *testing.T) {
	e, _ := openEngine(t, 2, 100, 450)
	base := time.Unix(1_800_000_000, 0)
	now := base
	e.now = func() time.Time { return now }

	e.RecordFailure(1, "m", "e1")
	e.RecordFailure(1, "m", "e2") // 达阈值：100s
	if r := e.row(t, 1, "m"); r.CooldownUntil != base.Add(100*time.Second).Unix() {
		t.Fatalf("首次冷却应 100s: %d", r.CooldownUntil)
	}
	now = now.Add(10 * time.Second)
	e.RecordFailure(1, "m", "e3") // ×2 = 200s
	if r := e.row(t, 1, "m"); r.CooldownUntil != now.Add(200*time.Second).Unix() {
		t.Fatalf("第二次冷却应 200s: %d", r.CooldownUntil)
	}
	now = now.Add(10 * time.Second)
	e.RecordFailure(1, "m", "e4") // ×4 = 400s
	if r := e.row(t, 1, "m"); r.CooldownUntil != now.Add(400*time.Second).Unix() {
		t.Fatalf("第三次冷却应 400s: %d", r.CooldownUntil)
	}
	now = now.Add(10 * time.Second)
	e.RecordFailure(1, "m", "e5") // ×8 = 800s → 封顶 450s
	if r := e.row(t, 1, "m"); r.CooldownUntil != now.Add(450*time.Second).Unix() {
		t.Fatalf("冷却应封顶 450s: %d", r.CooldownUntil)
	}
}

// View 与半开认领：到期前认领失败，到期后仅一次认领成功
func Test半开认领单飞(t *testing.T) {
	e, _ := openEngine(t, 1, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }

	e.RecordFailure(1, "gpt-x", "e1")
	view := e.View([]int64{1, 2}, "gpt-x")
	if !view.Open(1) {
		t.Fatal("渠道 1 应处于熔断")
	}
	if view.Open(2) {
		t.Fatal("渠道 2 不应处于熔断")
	}
	if view.Due(1, base.Unix()) {
		t.Fatal("冷却未到期不应 Due")
	}
	if e.ClaimHalfOpen(1, "gpt-x") {
		t.Fatal("冷却未到期认领应失败")
	}
	// 时间推进到冷却之后
	after := base.Add(301 * time.Second)
	e.now = func() time.Time { return after }
	view = e.View([]int64{1}, "gpt-x")
	if !view.Due(1, after.Unix()) {
		t.Fatal("冷却到期应 Due")
	}
	if !e.ClaimHalfOpen(1, "gpt-x") {
		t.Fatal("到期后首次认领应成功")
	}
	if e.ClaimHalfOpen(1, "gpt-x") {
		t.Fatal("认领后并发请求不应再次进入试探")
	}
	// 认领把冷却顺延一个周期：View 不再 Due
	view = e.View([]int64{1}, "gpt-x")
	if view.Due(1, after.Unix()) {
		t.Fatal("认领后应视为冷却中")
	}
}

// 手动恢复：单模型与整渠道两种粒度，均清内存计数
func Test手动恢复(t *testing.T) {
	e, _ := openEngine(t, 1, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }

	e.RecordFailure(1, "gpt-a", "e")
	e.RecordFailure(1, "gpt-b", "e")
	e.Reset(1, "gpt-a")
	if r := e.row(t, 1, "gpt-a"); r != nil {
		t.Fatalf("单模型恢复未生效: %+v", r)
	}
	if r := e.row(t, 1, "gpt-b"); r == nil {
		t.Fatal("其他模型不应被恢复")
	}
	// gpt-b 计数仍在：再来一次失败仍在熔断（行刷新）
	e.RecordFailure(1, "gpt-b", "e2")
	if r := e.row(t, 1, "gpt-b"); r == nil || r.FailCount != 2 {
		t.Fatalf("未恢复的模型计数应延续: %+v", r)
	}
	e.Reset(1, "")
	if r := e.row(t, 1, "gpt-b"); r != nil {
		t.Fatalf("整渠道恢复未生效: %+v", r)
	}
}

// 模型维度隔离：同一渠道不同模型互不影响
func Test模型维度隔离(t *testing.T) {
	e, _ := openEngine(t, 1, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }

	e.RecordFailure(1, "gpt-x", "e")
	view := e.View([]int64{1}, "gpt-x")
	if !view.Open(1) {
		t.Fatal("gpt-x 应熔断")
	}
	view = e.View([]int64{1}, "gpt-y")
	if view != nil {
		t.Fatalf("gpt-y 不应有熔断视图: %v", view)
	}
}
