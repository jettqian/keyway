package usage

import (
	"testing"

	"keyway/internal/store"
)

func i64p(v int64) *int64    { return &v }
func strp(v string) *string { s := v; return &s }
func intp(v int) *int       { return &v }

// 最近生效流量 = 最新一条成功（status < 400 且有渠道）日志，不受统计窗口限制
func TestQueryStatsLatest(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	logs := []store.Log{
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200)},
		{CreatedAt: 200, UserID: 2, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200)},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(3), Model: strp("m3"), StatusCode: intp(500)},
		{CreatedAt: 400, UserID: 1, ChannelID: i64p(4), Model: strp("m4"), StatusCode: intp(200)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	st1, err := QueryStats(db, &uid, 7)
	if err != nil {
		t.Fatal(err)
	}
	if st1.Latest == nil {
		t.Fatal("期望返回最近生效流量，实际为空")
	}
	if st1.Latest.ChannelID != 4 || st1.Latest.Model != "m4" {
		t.Errorf("用户 1 期望最新成功流量为渠道 4 / m4，实际 %d / %s", st1.Latest.ChannelID, st1.Latest.Model)
	}

	uid2 := int64(2)
	st2, err := QueryStats(db, &uid2, 7)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Latest == nil || st2.Latest.ChannelID != 2 || st2.Latest.Model != "m2" {
		t.Errorf("用户 2 期望最新成功流量为渠道 2 / m2，实际 %+v", st2.Latest)
	}
}
