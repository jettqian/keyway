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

// 阈值语义：低于阈值只累计内存计数（无行），达到阈值落熔断行（冷却 = 基础周期）
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

// View 与半开认领：到期前认领失败，到期后仅一次认领成功（单飞）
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
	// 认领把冷却顺延到下一退避档（threshold=1，fc=1 → backoff(2)=2×300）：
	// View 不再 Due
	view = e.View([]int64{1}, "gpt-x")
	if view.Due(1, after.Unix()) {
		t.Fatal("认领后应视为冷却中")
	}
	if r := e.row(t, 1, "gpt-x"); r.CooldownUntil != after.Add(600*time.Second).Unix() {
		t.Fatalf("认领应顺延到下一退避档（600s）: %d", r.CooldownUntil)
	}
}

// 成功清零：熔断关闭后重新计数
func Test成功关闭并重新计数(t *testing.T) {
	e, _ := openEngine(t, 2, 300, 3600)
	e.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

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

// 手动恢复：单模型与整渠道两种粒度，均清内存计数
func Test手动恢复(t *testing.T) {
	e, _ := openEngine(t, 1, 300, 3600)
	e.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

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
	e.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

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

// 重启模拟：新引擎（本地计数归零）在同一存储上继续记录，退避进度以 DB 行的
// fail_count 为准推进——不降级计数、不缩短冷却、保留 opened_at
func Test重启后退避进度不降级(t *testing.T) {
	e, st := openEngine(t, 3, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }
	for i := 0; i < 5; i++ {
		e.RecordFailure(1, "gpt-x", "e") // fc=5，冷却 300×2²=1200s
	}
	r := e.row(t, 1, "gpt-x")
	if r.FailCount != 5 || r.CooldownUntil != base.Add(1200*time.Second).Unix() {
		t.Fatalf("前置条件不符: %+v", r)
	}

	// 重启：同一 DB、新引擎（内存计数清零）
	e2 := New(st.DB(), 3, 300, 3600)
	restart := base.Add(10 * time.Minute)
	e2.now = func() time.Time { return restart }
	for i := 0; i < 3; i++ { // 重新累积到阈值（本地 n=3）
		e2.RecordFailure(1, "gpt-x", "e")
	}
	r = e2.row(t, 1, "gpt-x")
	if r.FailCount != 6 {
		t.Fatalf("重启后 fail_count 应从 DB 行推进到 6，而非本地计数 3: %+v", r)
	}
	if r.CooldownUntil != restart.Add(2400*time.Second).Unix() {
		t.Fatalf("冷却应按 fail_count=6 退避（300×2³=2400s）: %d", r.CooldownUntil)
	}
	if r.OpenedAt != base.Unix() {
		t.Fatalf("opened_at 应保留首次开闸时刻: %+v", r)
	}
}

// 认领顺延到下一退避档：与试探失败后 RecordFailure 写入的值一致——
// 非计入型收尾（如 400 透传）不再把冷却打回基础周期，退避进度得以保持
func Test认领顺延至下一退避档(t *testing.T) {
	e, _ := openEngine(t, 3, 300, 3600)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		e.RecordFailure(1, "m", "e") // fc=3，冷却 300s
	}
	after := base.Add(301 * time.Second)
	e.now = func() time.Time { return after }
	if !e.ClaimHalfOpen(1, "m") {
		t.Fatal("到期认领应成功")
	}
	r := e.row(t, 1, "m")
	if r.CooldownUntil != after.Add(600*time.Second).Unix() {
		t.Fatalf("认领应把冷却顺延到下一退避档（600s），实际 %d", r.CooldownUntil)
	}
	// 试探失败（计入）后 RecordFailure 写入同一档位：进度一致
	e.RecordFailure(1, "m", "试探失败")
	r = e.row(t, 1, "m")
	if r.CooldownUntil != after.Add(600*time.Second).Unix() || r.FailCount != 4 {
		t.Fatalf("试探失败后的冷却应与认领顺延值一致: %+v", r)
	}
}

// 上限配置低于基础冷却时 clamp 到基础冷却（而非回落 3600），退避不再增长
func Test上限低于基础冷却时收敛(t *testing.T) {
	e, _ := openEngine(t, 3, 600, 100)
	base := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		e.RecordFailure(1, "m", "e")
	}
	if r := e.row(t, 1, "m"); r.CooldownUntil != base.Add(600*time.Second).Unix() {
		t.Fatalf("冷却上限应 clamp 到基础冷却 600s，实际到 %d", r.CooldownUntil)
	}
	for i := 0; i < 3; i++ {
		e.RecordFailure(1, "m", "e")
	}
	if r := e.row(t, 1, "m"); r.CooldownUntil != base.Add(600*time.Second).Unix() {
		t.Fatalf("退避应封顶在 600s，实际到 %d", r.CooldownUntil)
	}
}

// 清理不再服务的模型（渠道改配）：行与内存计数一并清理，空列表 = 清理全部
func Test清理不再服务的模型(t *testing.T) {
	e, _ := openEngine(t, 2, 300, 3600)
	e.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

	e.RecordFailure(1, "gpt-a", "e")
	e.RecordFailure(1, "gpt-a", "e")
	e.RecordFailure(1, "gpt-b", "e")
	e.RecordFailure(1, "gpt-b", "e")
	e.RecordFailure(1, "gpt-c", "e")
	e.RecordFailure(1, "gpt-c", "e")
	e.PruneModels(1, []string{"gpt-a", "gpt-d"})
	if r := e.row(t, 1, "gpt-a"); r == nil {
		t.Fatal("列表内的模型不应被清理")
	}
	for _, m := range []string{"gpt-b", "gpt-c"} {
		if r := e.row(t, 1, m); r != nil {
			t.Fatalf("列表外模型 %s 应被清理: %+v", m, r)
		}
	}
	// 内存计数同步清理：gpt-b 再失败一次不足以落行（计数已从零开始）
	e.RecordFailure(1, "gpt-b", "e")
	if r := e.row(t, 1, "gpt-b"); r != nil {
		t.Fatalf("清理后计数应清零，单次失败不应落行: %+v", r)
	}
	// 空列表 = 清理全部
	e.PruneModels(1, nil)
	if r := e.row(t, 1, "gpt-a"); r != nil {
		t.Fatalf("空列表应清理全部: %+v", r)
	}
}
