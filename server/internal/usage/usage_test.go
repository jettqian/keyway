package usage

import (
	"testing"
	"time"

	"keyway/internal/convert"
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
	//（重复只占一行、时间与线路取最新），另 1 条失败；用户 2：1 条成功
	logs := []store.Log{
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), LineURL: strp("https://old.example.com"), Via: strp("direct")},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200), LineURL: strp("https://b.example.com"), Via: strp("proxy:3")},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(3), Model: strp("m3"), StatusCode: intp(200)},
		// 同渠道同模型重复：只占一行，展示最新这条（350，线路同取最新）
		{CreatedAt: 350, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), LineURL: strp("https://a.example.com"), Via: strp("personal")},
		{CreatedAt: 400, UserID: 1, ChannelID: i64p(4), Model: strp("m4"), StatusCode: intp(200)},
		// 同渠道同模型重复：只占一行，展示最新这条（450，线路取最新、旧行线路不残留）
		{CreatedAt: 450, UserID: 1, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200), LineURL: strp("https://b2.example.com"), Via: strp("direct")},
		{CreatedAt: 500, UserID: 1, ChannelID: i64p(5), Model: strp("m5"), StatusCode: intp(200)},
		{CreatedAt: 600, UserID: 1, ChannelID: i64p(6), Model: strp("m6"), StatusCode: intp(200)},
		// 失败请求（不进最近生效流量）
		{CreatedAt: 700, UserID: 1, ChannelID: i64p(7), Model: strp("m7"), StatusCode: intp(500)},
		{CreatedAt: 800, UserID: 2, ChannelID: i64p(8), Model: strp("m8"), StatusCode: intp(200), LineURL: strp("https://c.example.com"), Via: strp("direct")},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	st1, err := QueryStats(db, &uid, 1, 1<<40, nil)
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
		lineURL string
		via     string
	}{
		{1, "m1", 350, "https://a.example.com", "personal"},
		{2, "m2", 450, "https://b2.example.com", "direct"},
	} {
		r := find(tc.channel, tc.model)
		if r == nil {
			t.Errorf("期望包含组合 (%d,%s)，实际 %+v", tc.channel, tc.model, st1.Recent)
		} else {
			if r.CreatedAt != tc.at {
				t.Errorf("组合 (%d,%s) 期望展示最新一条（时间 %d），实际 %d", tc.channel, tc.model, tc.at, r.CreatedAt)
			}
			if r.LineURL != tc.lineURL || r.Via != tc.via {
				t.Errorf("组合 (%d,%s) 期望展示最新一条的线路 %s / %s，实际 %s / %s", tc.channel, tc.model, tc.lineURL, tc.via, r.LineURL, r.Via)
			}
		}
	}
	// 无线路的历史日志回退空值（COALESCE 不产生 "null" 字符串）
	if r := find(6, "m6"); r != nil && (r.LineURL != "" || r.Via != "") {
		t.Errorf("无线路日志期望线路字段为空，实际 %s / %s", r.LineURL, r.Via)
	}

	uid2 := int64(2)
	st2, err := QueryStats(db, &uid2, 1, 1<<40, nil)
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

	st1, err := QueryStats(db, &uid, 50, 500, nil)
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
	st2, err := QueryStats(db, &uid, 50, 150, nil)
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

// 管理员全员统计：按用户分组——维度显示用户名、零用量用户补齐展示、
// 补算费用合入用户分组；用户视角不含 byUser
func TestQueryStatsAdminByUser(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 三个用户：alice / bob 有日志，carol 从未用过（GROUP BY logs 不会为其产生行）
	users := []store.User{
		{Username: "alice", PasswordHash: strp("x")},
		{Username: "bob", PasswordHash: strp("x")},
		{Username: "carol", PasswordHash: strp("x")},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 当前价目：m1 输入 $3/输出 $15 每百万
	if err := db.Create(&store.ModelPricing{Model: "m1", InputPerM: 3, OutputPerM: 15, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	logs := []store.Log{
		// alice：1 条已定价快照 $2 + 1 条写入时未定价（补算 1M×3 + 1M×15 = $18）
		{CreatedAt: 100, UserID: users[0].ID, Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(2)},
		{CreatedAt: 200, UserID: users[0].ID, Model: strp("m1"), StatusCode: intp(200), PromptTokens: i64p(1_000_000), CompletionTokens: i64p(1_000_000)},
		// bob：1 条失败（0 费用、1 错误）
		{CreatedAt: 300, UserID: users[1].ID, Model: strp("m1"), StatusCode: intp(500)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	stA, err := QueryStats(db, nil, 1, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := stA.Summary.Requests; got != 3 {
		t.Errorf("全员汇总期望 3 条请求，实际 %d", got)
	}
	if got := stA.Summary.Cost; got != 20 {
		t.Errorf("全员汇总期望费用 20（快照 2 + 补算 18），实际 %.4f", got)
	}
	find := func(dim string) *StatsGroup {
		for i := range stA.ByUser {
			if stA.ByUser[i].Dim == dim {
				return &stA.ByUser[i]
			}
		}
		return nil
	}
	if len(stA.ByUser) != 3 {
		t.Fatalf("按用户分组期望 3 行（含零用量 carol），实际 %d 行：%+v", len(stA.ByUser), stA.ByUser)
	}
	if g := find("alice"); g == nil {
		t.Errorf("期望维度显示用户名「alice」，实际 %+v", stA.ByUser)
	} else if g.Requests != 2 || g.Cost != 20 {
		t.Errorf("alice 期望 2 次 / $20（含补算 18），实际 %d 次 / $%.4f", g.Requests, g.Cost)
	}
	if g := find("bob"); g == nil {
		t.Errorf("期望包含用户「bob」，实际 %+v", stA.ByUser)
	} else if g.Requests != 1 || g.Errors != 1 || g.Cost != 0 {
		t.Errorf("bob 期望 1 次 / 1 错误 / $0，实际 %d 次 / %d 错误 / $%.4f", g.Requests, g.Errors, g.Cost)
	}
	if g := find("carol"); g == nil {
		t.Errorf("零用量用户 carol 期望补齐展示，实际 %+v", stA.ByUser)
	} else if g.Requests != 0 {
		t.Errorf("carol 期望 0 次请求，实际 %d", g.Requests)
	}

	// 用户视角不含 byUser（自己的统计里该维度无意义）
	st1, err := QueryStats(db, &users[0].ID, 1, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st1.ByUser) != 0 {
		t.Errorf("用户视角不应返回 byUser，实际 %+v", st1.ByUser)
	}
}

// 按令牌筛选：汇总/分组/最近生效流量全部限定到该令牌；
// 用户视角传他人令牌查不到数据（user_id 与 token_id 双条件天然隔离）
func TestQueryStatsByToken(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 当前价目：m1 输入 $3/输出 $15 每百万
	if err := db.Create(&store.ModelPricing{Model: "m1", InputPerM: 3, OutputPerM: 15, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}

	uid, uid2 := int64(1), int64(2)
	tk1, tk2, tk3 := int64(11), int64(12), int64(21)
	logs := []store.Log{
		// 用户 1 / 令牌 11：2 条成功（渠道 1×m1 已定价快照 $2；渠道 2×m1 未定价补算 $18）
		{CreatedAt: 100, UserID: uid, TokenID: &tk1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(2)},
		{CreatedAt: 200, UserID: uid, TokenID: &tk1, ChannelID: i64p(2), Model: strp("m1"), StatusCode: intp(200), PromptTokens: i64p(1_000_000), CompletionTokens: i64p(1_000_000)},
		// 用户 1 / 令牌 11：1 条失败（计入请求数与错误率，不进最近生效流量）
		{CreatedAt: 300, UserID: uid, TokenID: &tk1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(500)},
		// 用户 1 / 令牌 12：1 条成功
		{CreatedAt: 400, UserID: uid, TokenID: &tk2, ChannelID: i64p(3), Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(0.5)},
		// 用户 2 / 令牌 21：1 条成功（隔离验证用）
		{CreatedAt: 500, UserID: uid2, TokenID: &tk3, ChannelID: i64p(4), Model: strp("m1"), StatusCode: intp(200), InputCost: f64p(1)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 不筛令牌：用户 1 全部 4 条（含补算费用 18）
	stAll, err := QueryStats(db, &uid, 1, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := stAll.Summary.Requests; got != 4 {
		t.Fatalf("不筛令牌期望 4 条请求，实际 %d", got)
	}
	if got := stAll.Summary.Cost; got != 20.5 {
		t.Fatalf("不筛令牌期望费用 20.5（快照 2.5 + 补算 18），实际 %.4f", got)
	}

	// 筛令牌 11：3 条、费用 20（2 + 补算 18）、错误率 1/3
	stT1, err := QueryStats(db, &uid, 1, 1<<40, &tk1)
	if err != nil {
		t.Fatal(err)
	}
	if got := stT1.Summary.Requests; got != 3 {
		t.Errorf("令牌 11 期望 3 条请求，实际 %d", got)
	}
	if got := stT1.Summary.ErrorRate; got < 33.33 || got > 33.34 {
		t.Errorf("令牌 11 期望错误率 1/3≈33.33%%，实际 %.2f%%", got)
	}
	if got := stT1.Summary.Cost; got != 20 {
		t.Errorf("令牌 11 期望费用 20（快照 2 + 补算 18），实际 %.4f", got)
	}
	// 分组只含令牌 11 的渠道 1/2，不含令牌 12 的渠道 3（渠道未建表，维度回退 #id）
	dims := map[string]bool{}
	for _, g := range stT1.ByChannel {
		dims[g.Dim] = true
	}
	if !dims["#1"] || !dims["#2"] || dims["#3"] {
		t.Errorf("令牌 11 按渠道分组期望仅渠道 #1/#2，实际 %+v", stT1.ByChannel)
	}
	// 最近生效流量也限定该令牌（(1,m1) 与 (2,m1) 两组，失败与令牌 12 的组合不进）
	if len(stT1.Recent) != 2 || stT1.Recent[0].ChannelID != 2 || stT1.Recent[1].ChannelID != 1 {
		t.Errorf("令牌 11 最近生效流量期望渠道 2 / 1 两组，实际 %+v", stT1.Recent)
	}

	// 筛令牌 12：1 条、费用 0.5
	stT2, err := QueryStats(db, &uid, 1, 1<<40, &tk2)
	if err != nil {
		t.Fatal(err)
	}
	if got := stT2.Summary.Requests; got != 1 {
		t.Errorf("令牌 12 期望 1 条请求，实际 %d", got)
	}
	if got := stT2.Summary.Cost; got != 0.5 {
		t.Errorf("令牌 12 期望费用 0.5，实际 %.4f", got)
	}

	// 用户 1 视角筛他人令牌：双条件叠加，查不到任何数据
	stIso, err := QueryStats(db, &uid, 1, 1<<40, &tk3)
	if err != nil {
		t.Fatal(err)
	}
	if got := stIso.Summary.Requests; got != 0 {
		t.Errorf("用户 1 筛他人令牌期望 0 条请求，实际 %d", got)
	}
	if len(stIso.Recent) != 0 {
		t.Errorf("用户 1 筛他人令牌期望最近生效流量为空，实际 %+v", stIso.Recent)
	}

	// 管理员视角筛令牌 21：能看到用户 2 的该令牌数据
	stA, err := QueryStats(db, nil, 1, 1<<40, &tk3)
	if err != nil {
		t.Fatal(err)
	}
	if got := stA.Summary.Requests; got != 1 {
		t.Errorf("管理员筛令牌 21 期望 1 条请求，实际 %d", got)
	}
	if got := stA.Summary.Cost; got != 1 {
		t.Errorf("管理员筛令牌 21 期望费用 1，实际 %.4f", got)
	}
}

// 错误率口径（v1.5.55）：200 但带错误摘要（流内失败）计入错误数，
// 且不进入最近生效流量；纯 200 成功行不受影响
func TestQueryStats流内失败计错误(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	logs := []store.Log{
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), TotalMs: i64p(10)},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), Error: strp("流异常终止（未见终止标记）"), TotalMs: i64p(20)},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(2), Model: strp("m2"), StatusCode: intp(200), TotalMs: i64p(30)},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	stats, err := QueryStats(db, &uid, 1, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.Requests != 3 {
		t.Fatalf("期望 3 个请求，实际 %d", stats.Summary.Requests)
	}
	if stats.Summary.ErrorRate != 33.333333333333336 && stats.Summary.ErrorRate < 33 || stats.Summary.ErrorRate > 34 {
		t.Fatalf("期望错误率 ~33%%（1/3 流内失败），实际 %v", stats.Summary.ErrorRate)
	}
	// 分组错误数：渠道 1 应 1 错 1 成，渠道 2 应 0 错（测试库无渠道表数据，
	// 维度回退 #id 显示）
	byChannel := map[string]StatsGroup{}
	for _, g := range stats.ByChannel {
		byChannel[g.Dim] = g
	}
	if byChannel["#1"].Errors != 1 || byChannel["#1"].Requests != 2 {
		t.Errorf("渠道 1 期望 2 请求 1 错误，实际 %+v", byChannel["#1"])
	}
	if byChannel["#2"].Errors != 0 {
		t.Errorf("渠道 2 期望 0 错误，实际 %+v", byChannel["#2"])
	}
	// 最近生效流量：流内失败行不进最近生效流量——m1 应取 100 时刻的成功行，
	// m2 正常进入
	if len(stats.Recent) != 2 {
		t.Fatalf("期望 2 个最近生效组合，实际 %d: %+v", len(stats.Recent), stats.Recent)
	}
	for _, r := range stats.Recent {
		if r.Model == "m1" && r.CreatedAt != 100 {
			t.Errorf("m1 最近生效行应回退到成功行（CreatedAt=100），实际 %d", r.CreatedAt)
		}
	}
}

// 仅看失败过滤（v1.5.56）：error 非空跨状态码筛选——含 200+错误摘要的
// 流内失败行，与状态码筛选可叠加
func TestQueryLogs仅看失败(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	logs := []store.Log{
		{CreatedAt: 100, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200)},
		{CreatedAt: 200, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(200), Error: strp("流内错误事件 type=overloaded_error")},
		{CreatedAt: 300, UserID: 1, ChannelID: i64p(1), Model: strp("m1"), StatusCode: intp(503), Error: strp("上游 503")},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	uid := int64(1)
	got, total, err := QueryLogs(db, &uid, LogQuery{FailedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("期望 2 条失败行（503 与流内失败），实际 %d", total)
	}
	for _, l := range got {
		if l.Error == nil {
			t.Errorf("仅看失败不应返回无错误行: %+v", l)
		}
	}
}

// 目录「关联价目」参与计价（v1.5.63）：裸名未定价而关联名命中时按关联名价计算；
// 裸名人工条目优先于别名；未关联或关联名也未定价仍为未定价
func TestCatalogAliasPricing(t *testing.T) {
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	// 关联名条目（模拟 LiteLLM 带前缀名）与裸名人工条目
	if err := db.Create(&store.ModelPricing{Model: "provider/flashx", InputPerM: 0.37, CachedInputPerM: f64p(0.075), OutputPerM: 1.25, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "provider/bare", InputPerM: 5, OutputPerM: 5, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.ModelPricing{Model: "bare", InputPerM: 9, OutputPerM: 9, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	catalogs := []store.CatalogModel{
		{Name: "flashx", PricingModel: "provider/flashx", Enabled: 1, CreatedAt: now, UpdatedAt: now},
		{Name: "bare", PricingModel: "provider/bare", Enabled: 1, CreatedAt: now, UpdatedAt: now},
		{Name: "orphan", PricingModel: "provider/none", Enabled: 1, CreatedAt: now, UpdatedAt: now},
		{Name: "noassoc", PricingModel: "", Enabled: 1, CreatedAt: now, UpdatedAt: now},
	}
	for i := range catalogs {
		if err := db.Create(&catalogs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	u := convert.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	// 别名计价：0.37 + 1.25 = 1.62
	ic, oc := ComputeCost(db, "flashx", "", "usd", 1, 0, u)
	if ic == nil || *ic+*oc != 1.62 {
		t.Fatalf("关联价目应生效 $1.62，实际 %v / %v", ic, oc)
	}
	// 裸名人工条目优先：9 + 9 = 18（而非关联名 5+5=10）
	ic, oc = ComputeCost(db, "bare", "", "usd", 1, 0, u)
	if ic == nil || *ic+*oc != 18 {
		t.Fatalf("裸名人工条目应优先 $18，实际 %v / %v", ic, oc)
	}
	// 关联名未定价 → 未定价
	if ic, oc = ComputeCost(db, "orphan", "", "usd", 1, 0, u); ic != nil {
		t.Fatalf("关联名未定价应返回 nil，实际 %v", *ic)
	}
	// 未关联（pricing_model 空回退自身名）→ 未定价
	if ic, oc = ComputeCost(db, "noassoc", "", "usd", 1, 0, u); ic != nil {
		t.Fatalf("未关联目录模型应返回 nil，实际 %v", *ic)
	}
	// 入站名命中目录、上游名直接命中价目表 → 上游名优先（1 + 1 = 2，不走别名）
	if err := db.Create(&store.ModelPricing{Model: "upstream-x", InputPerM: 1, OutputPerM: 1, Currency: "USD"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.CatalogModel{Name: "alias-x", PricingModel: "provider/flashx", Enabled: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	InvalidatePricingCache(db) // 模拟目录/价目变更后的失效（生产由 TTL 或显式失效驱动）
	ic, oc = ComputeCost(db, "alias-x", "upstream-x", "usd", 1, 0, u)
	if ic == nil || *ic+*oc != 2 {
		t.Fatalf("上游名直接命中应优先 $2，实际 %v / %v", ic, oc)
	}
}
