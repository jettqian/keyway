package usage

import (
	"testing"

	"keyway/internal/store"
)

func i64p(v int64) *int64     { return &v }
func strp(v string) *string   { s := v; return &s }
func intp(v int) *int         { return &v }
func f64p(v float64) *float64 { return &v }

// 最近生效流量 = 成功（status < 400 且有渠道）日志按（渠道,模型）去重后的最新 5 个组合，
// 每组只占一行（取该组最新一条），不受统计窗口限制
func TestQueryStatsRecent(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 用户 1：6 个不同（渠道,模型）组合，其中 (1,m1)、(2,m2) 各重复一次
	//（重复只占一行、时间取最新），另 1 条失败；用户 2：1 条成功
	logs := []store.Log{
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200)},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200)},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(3), Model: strp("m3"), StatusCode: intp(200)},
		// 同渠道同模型重复：只占一行，展示最新这条（350）
		{CreatedAt: 350, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200)},
		{CreatedAt: 400, UserID: 1, ChannelID: i64p(4), Model: strp("m4"), StatusCode: intp(200)},
		// 同渠道同模型重复：只占一行，展示最新这条（450）
		{CreatedAt: 450, UserID: 1, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200)},
		{CreatedAt: 500, UserID: 1, ChannelID: i64p(5), Model: strp("m5"), StatusCode: intp(200)},
		{CreatedAt: 600, UserID: 1, ChannelID: i64p(6), Model: strp("m6"), StatusCode: intp(200)},
		// 失败请求（不进最近生效流量）
		{CreatedAt: 700, UserID: 1, ChannelID: i64p(7), Model: strp("m7"), StatusCode: intp(500)},
		{CreatedAt: 800, UserID: 2, ChannelID: i64p(8), Model: strp("m8"), StatusCode: intp(200)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	st1, err := QueryStats(db, &uid, 1, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(st1.Recent) != 5 {
		t.Fatalf("用户 1 期望去重后最近 5 个组合，实际 %d 条", len(st1.Recent))
	}
	if st1.Recent[0].ChannelID != 6 || st1.Recent[0].Model != "m6" {
		t.Errorf("期望首行为最新组合渠道 6 / m6，实际 %d / %s", st1.Recent[0].ChannelID, st1.Recent[0].Model)
	}
	for i := 1; i < len(st1.Recent); i++ {
		if st1.Recent[i-1].ID <= st1.Recent[i].ID {
			t.Errorf("期望按日志倒序排列，实际第 %d 行 id=%d 不大于次行 id=%d", i-1, st1.Recent[i-1].ID, st1.Recent[i].ID)
		}
	}
	find := func(channel int64, model string) *LatestUsage {
		for i := range st1.Recent {
			if st1.Recent[i].ChannelID == channel && st1.Recent[i].Model == model {
				return &st1.Recent[i]
			}
		}
		return nil
	}
	// 去重后 6 个组合只剩 5 行，最早的 (3,m3) 被挤出
	if find(3, "m3") != nil {
		t.Errorf("期望最早的组合 (3,m3) 被 5 行上限挤出，实际 %+v", st1.Recent)
	}
	// 重复组合只占一行且取最新一条
	for _, tc := range []struct {
		channel int64
		model   string
		at      int64
	}{
		{1, "m1", 350},
		{2, "m2", 450},
	} {
		r := find(tc.channel, tc.model)
		if r == nil {
			t.Errorf("期望包含组合 (%d,%s)，实际 %+v", tc.channel, tc.model, st1.Recent)
		} else if r.CreatedAt != tc.at {
			t.Errorf("组合 (%d,%s) 期望展示最新一条（时间 %d），实际 %d", tc.channel, tc.model, tc.at, r.CreatedAt)
		}
	}

	uid2 := int64(2)
	st2, err := QueryStats(db, &uid2, 1, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Recent) != 1 || st2.Recent[0].ChannelID != 8 || st2.Recent[0].Model != "m8" {
		t.Errorf("用户 2 期望最近生效流量为渠道 8 / m8，实际 %+v", st2.Recent)
	}
}

// 统计分组显示渠道/密钥名称、时间窗过滤、未定价费用按当前价目补算
func TestQueryStatsNamesBackfillRange(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	ch := store.Channel{UserID: 1, Name: "渠道A", Type: "openai", BaseURLsJSON: "[]", KeyIDsJSON: "[]", ModelsJSON: "[]"}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	key := store.Key{UserID: 1, Name: "主号", ValueEnc: []byte("enc")}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	// 当前价目：m1 输入 $3/输出 $15 每百万
	if err := db.Create(&store.ModelPricing{Model: "m1", InputPerM: 3, OutputPerM: 15, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}

	uid := int64(1)
	uid2 := int64(2)
	logs := []store.Log{
		// 0：已定价快照 $2
		{CreatedAt: 50, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(2)},
		// 1：写入时未定价（NULL），当前价目补算 = 1M×3 + 1M×15 = $18
		{CreatedAt: 100, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(200), PromptTokens: i64p(1_000_000), CompletionTokens: i64p(1_000_000)},
		// 2：已定价快照 $0.5
		{CreatedAt: 200, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(0.5)},
		// 3：当前仍未定价
		{CreatedAt: 300, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m-x"), StatusCode: intp(200)},
		// 4：渠道已删除（id 42），快照 $0.25
		{CreatedAt: 400, UserID: uid, ChannelID: i64p(42), KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(0.25)},
		// 5：失败请求（不计费用、不进最近生效流量）
		{CreatedAt: 450, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(500)},
		// 6：其他用户（隔离）
		{CreatedAt: 150, UserID: uid2, ChannelID: i64p(2), KeyID: i64p(2), Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(1)},
		// 7：窗口外
		{CreatedAt: 1000, UserID: uid, ChannelID: &ch.ID, KeyID: &key.ID, Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(9)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	st1, err := QueryStats(db, &uid, 50, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got := st1.Summary.Requests; got != 6 {
		t.Errorf("窗口内期望 6 条请求，实际 %d", got)
	}
	if got := st1.Summary.ErrorRate; got < 16.66 || got > 16.67 {
		t.Errorf("期望错误率 1/6≈16.67%%，实际 %.2f%%", got)
	}
	// 费用 = 快照(2+0.5+0.25) + 补算 18
	if got := st1.Summary.Cost; got != 20.75 {
		t.Errorf("期望费用 20.75（含未定价补算 18），实际 %.4f", got)
	}
	if !st1.Summary.Unpriced {
		t.Error("m-x 当前仍未定价，期望 Unpriced=true")
	}

	find := func(rows []StatsGroup, dim string) *StatsGroup {
		for i := range rows {
			if rows[i].Dim == dim {
				return &rows[i]
			}
		}
		return nil
	}
	if g := find(st1.ByChannel, "渠道A"); g == nil {
		t.Errorf("按渠道分组期望维度显示名称「渠道A」，实际 %+v", st1.ByChannel)
	} else if g.Cost != 20.5 || g.Requests != 5 {
		t.Errorf("渠道A 期望 5 次 / $20.5，实际 %d 次 / $%.4f", g.Requests, g.Cost)
	}
	if g := find(st1.ByChannel, "#42"); g == nil {
		t.Errorf("已删除渠道期望回退显示「#42」，实际 %+v", st1.ByChannel)
	}
	if g := find(st1.ByKey, "主号"); g == nil {
		t.Errorf("按密钥分组期望维度显示名称「主号」，实际 %+v", st1.ByKey)
	} else if g.Cost != 20.75 || g.Requests != 6 {
		t.Errorf("主号 期望 6 次 / $20.75，实际 %d 次 / $%.4f", g.Requests, g.Cost)
	}
	if g := find(st1.ByModel, "m1"); g == nil {
		t.Errorf("按模型分组期望包含 m1，实际 %+v", st1.ByModel)
	} else if g.Cost != 20.75 {
		t.Errorf("m1 期望 $20.75（含渠道 #42 的快照 0.25），实际 $%.4f", g.Cost)
	}

	// 窄窗口（50~150）：仅日志 0 与 1，全部可定价 → 不再显示"部分未定价"
	st2, err := QueryStats(db, &uid, 50, 150)
	if err != nil {
		t.Fatal(err)
	}
	if got := st2.Summary.Requests; got != 2 {
		t.Errorf("窄窗口期望 2 条请求，实际 %d", got)
	}
	if got := st2.Summary.Cost; got != 20 {
		t.Errorf("窄窗口期望费用 20（快照 2 + 补算 18），实际 %.4f", got)
	}
	if st2.Summary.Unpriced {
		t.Error("窄窗口内模型均已定价，期望 Unpriced=false")
	}

	// 用户 2 数据不泄漏到用户 1
	if len(st1.ByChannel) != 2 || find(st1.ByChannel, "#2") != nil {
		t.Errorf("用户 1 分组不应包含其他用户的渠道，实际 %+v", st1.ByChannel)
	}
}
