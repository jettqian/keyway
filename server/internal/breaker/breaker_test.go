package breaker

import (
	"testing"
	"time"

	"keyway/internal/store"
)

func openEngine(t *testing.T, threshold int) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := New(st.DB(), threshold)
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
	e, _ := openEngine(t, 3)
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
	if r.LastError != "err-4" {
		t.Fatalf("应记录最近错误: %q", r.LastError)
	}
	// 渠道 2 只失败 1 次：不熔断
	if r := e.row(t, 2, "gpt-x"); r != nil {
		t.Fatalf("渠道 2 不应熔断: %+v", r)
	}
	// 后续失败继续刷新计数与最近错误（opened_at 保留首次时间）
	next := base.Add(time.Minute)
	e.now = func() time.Time { return next }
	e.RecordFailure(1, "gpt-x", "err-5")
	r = e.row(t, 1, "gpt-x")
	if r == nil || r.FailCount != 4 || r.OpenedAt != base.Unix() || r.LastError != "err-5" {
		t.Fatalf("熔断行应延续: %+v", r)
	}
}

// 成功清零：熔断关闭后重新计数
func Test成功关闭并重新计数(t *testing.T) {
	e, _ := openEngine(t, 2)
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
	e, _ := openEngine(t, 1)
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
	e, _ := openEngine(t, 1)
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

// OpenModels 按 updated_at 升序（最久未触达优先），Touch 后移排队（LRU 轮转）
func TestOpenModels轮转(t *testing.T) {
	e, _ := openEngine(t, 1)
	base := time.Unix(1_800_000_000, 0)
	now := base
	e.now = func() time.Time { return now }

	e.RecordFailure(1, "a", "e") // updated_at = t0
	now = base.Add(10 * time.Second)
	e.RecordFailure(1, "b", "e") // updated_at = t0+10
	now = base.Add(20 * time.Second)
	e.RecordFailure(1, "c", "e") // updated_at = t0+20
	got := e.OpenModels(1)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("应按 updated_at 升序: %v", got)
	}
	// 补测失败 Touch 后 a 排到队尾
	now = base.Add(30 * time.Second)
	e.Touch(1, "a")
	got = e.OpenModels(1)
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Fatalf("Touch 后应排队后移: %v", got)
	}
	// 其他渠道不受影响
	if got := e.OpenModels(2); len(got) != 0 {
		t.Fatalf("无熔断渠道应返回空: %v", got)
	}
}
