package usage

import (
	"testing"

	"keyway/internal/store"
)

// 渠道×模型链路聚合：尝试口径（失败切换的中间尝试也计入）、成功率、平均耗时、
// 用户隔离与全员（管理端）口径、时间窗过滤
func TestQueryLinks(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	logs := []store.Log{
		// (1, m1)：2 成功 + 1 失败（失败切换的中间尝试），总耗时 100+300+200
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(100)},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(500), TotalMs: i64p(300)},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(200)},
		// (1, m2)：网络错误（status NULL）也计一次失败尝试
		{CreatedAt: 400, UserID: 1, ChannelID: i64p(1), Model: strp("m2"), StatusCode: nil, TotalMs: i64p(50)},
		// 用户 2 的 (2, m1)
		{CreatedAt: 500, UserID: 2, ChannelID: i64p(2), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(80)},
		// 窗口外（应被过滤）
		{CreatedAt: 10, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(999)},
		// 无渠道/无模型的日志（探测等）不参与
		{CreatedAt: 600, UserID: 1, ChannelID: nil, Model: strp("m9"), StatusCode: intp(200)},
		{CreatedAt: 600, UserID: 1, ChannelID: i64p(1), Model: nil, StatusCode: intp(200)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	links, err := QueryLinks(db, &uid, 50, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("用户 1 期望 2 个链路组合，实际 %d: %+v", len(links), links)
	}
	// 按尝试数降序：(1,m1)=3 在前
	if links[0].ChannelID != 1 || links[0].Model != "m1" {
		t.Fatalf("首行应为 (1,m1)，实际 %+v", links[0])
	}
	l := links[0]
	if l.Attempts != 3 || l.OK != 2 {
		t.Errorf("(1,m1) 期望 3 次尝试 2 次成功，实际 %d/%d", l.Attempts, l.OK)
	}
	if l.ErrorRate != 33 {
		t.Errorf("(1,m1) 期望错误率 33%%，实际 %d", l.ErrorRate)
	}
	if l.AvgMs != 200 {
		t.Errorf("(1,m1) 期望平均耗时 200ms，实际 %d", l.AvgMs)
	}
	if l.LastAt != 300 {
		t.Errorf("(1,m1) 期望最近尝试 300，实际 %d", l.LastAt)
	}
	if links[1].ChannelID != 1 || links[1].Model != "m2" || links[1].Attempts != 1 || links[1].OK != 0 || links[1].ErrorRate != 100 {
		t.Errorf("(1,m2) 期望 1 次尝试全失败，实际 %+v", links[1])
	}

	// 全员（管理端）：含用户 2 的 (2,m1)
	all, err := QueryLinks(db, nil, 50, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("全员期望 3 个链路组合，实际 %d: %+v", len(all), all)
	}
	// 窗口过滤：起点 50 排除 CreatedAt=10 的行，但 (1,m1) 其余尝试仍在
	narrow, err := QueryLinks(db, &uid, 250, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != 2 {
		t.Fatalf("窄窗口期望仍含 2 个组合，实际 %d", len(narrow))
	}
	if narrow[0].ChannelID != 1 || narrow[0].Model != "m1" || narrow[0].Attempts != 1 {
		t.Errorf("窄窗口 (1,m1) 期望只剩 1 次尝试，实际 %+v", narrow[0])
	}
}

// 流内失败的成功口径（v1.5.55）：200 但带错误摘要（流内错误事件）按失败计，
// 不再被算进 ok——否则链路状态错误率 0% 与熔断状态自相矛盾
func TestQueryLinks流内失败按失败计(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	logs := []store.Log{
		// 渠道 1：一次正常成功 + 一次 200 带错误摘要（流内失败）
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(100)},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), Error: strp("流内错误事件 type=overloaded_error"), TotalMs: i64p(200)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	links, err := QueryLinks(db, &uid, 50, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("期望 1 个链路组合，实际 %d", len(links))
	}
	l := links[0]
	if l.Attempts != 2 || l.OK != 1 {
		t.Errorf("期望 2 次尝试 1 次成功（流内失败不算成功），实际 %d/%d", l.Attempts, l.OK)
	}
	if l.ErrorRate != 50 {
		t.Errorf("期望错误率 50%%，实际 %d", l.ErrorRate)
	}
}
