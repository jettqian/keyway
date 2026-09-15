package relay

import (
	"testing"
	"time"

	"keyway/internal/config"
	"keyway/internal/store"
)

// TestOrderCombos新鲜度跟随探测周期 回归：新鲜度阈值 = 3×探测周期，应随
// ProbeIntervalMin 缩放（PRD v1.0 ④），而非硬编码 30 分钟——周期调大后
// 较旧的探测结果仍应参与延迟优选，否则所有结果永远"过期"、优选失效
func TestOrderCombos新鲜度跟随探测周期(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// 线路 a：45 分钟前探测成功、延迟 100ms；线路 b：刚探测成功、延迟 500ms
	old := time.Now().Unix() - 45*60
	ok1, lat1 := 1, int64(100)
	st.DB().Create(&store.LineStat{
		ChannelID: 1, LineURL: "https://a", Via: "direct",
		LastProbeAt: &old, Ok: &ok1, LatencyMs: &lat1,
	})
	fresh := time.Now().Unix()
	ok2, lat2 := 1, int64(500)
	st.DB().Create(&store.LineStat{
		ChannelID: 1, LineURL: "https://b", Via: "direct",
		LastProbeAt: &fresh, Ok: &ok2, LatencyMs: &lat2,
	})

	lines := []string{"https://a", "https://b"}
	paths := []struct{ proxy, via string }{{"", "direct"}}

	// 周期 60 分钟（窗口 180 分钟）：45 分钟前的结果仍新鲜 → 按延迟升序，a 在前
	got := orderCombos(st.DB(), 1, lines, paths, probeIntervalMin(config.Config{ProbeIntervalMin: 60}))
	if got[0].line != "https://a" {
		t.Fatalf("周期60分钟：45分钟前的低延迟结果应视为新鲜排前，got %s", got[0].line)
	}
	// 周期 10 分钟（窗口 30 分钟）：45 分钟前的结果过期 → 降级未知档，b 优先
	got = orderCombos(st.DB(), 1, lines, paths, probeIntervalMin(config.Config{}))
	if got[0].line != "https://b" {
		t.Fatalf("周期10分钟：过期结果应降级为未知档，got %s", got[0].line)
	}
}
