package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm/clause"

	"keyway/internal/crypto"
)

// 测试用主密钥（32 字节）
var testSecret = strings.Repeat("k", 32)

func sPtr(s string) *string { return &s }
func iPtr(i int) *int       { return &i }
func i64Ptr(i int64) *int64 { return &i }

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// expectedColumns DESIGN §3 DDL 期望列（按声明顺序）
var expectedColumns = map[string][]string{
	"users":             {"id", "username", "password_hash", "feishu_user_id", "role", "status", "created_at", "last_login_at"},
	"sessions":          {"token_hash", "user_id", "expires_at"},
	"keys":              {"id", "user_id", "name", "value_enc", "note", "status", "last_error", "cooldown_until", "created_at"},
	"channel_templates": {"id", "name", "type", "base_urls_json", "line_strategy", "models_json", "model_mapping_json", "priority_default", "allow_public_proxy_default", "note", "enabled", "copy_count", "updated_at"},
	"channels":          {"id", "user_id", "copied_from_template_id", "name", "type", "base_urls_json", "key_ids_json", "key_strategy", "line_strategy", "proxy_url_enc", "allow_public_proxy", "models_json", "model_mapping_json", "forward_mode", "priority", "price_multiplier", "pricing_mode", "cny_ratio", "is_default", "enabled", "last_ok_at", "last_error", "created_at"},
	"line_stats":        {"channel_id", "line_url", "via", "last_probe_at", "latency_ms", "ok", "last_error"},
	"proxies":           {"id", "name", "url_enc", "enabled", "note", "created_at"},
	"proxy_usage":       {"user_id", "proxy_id", "day", "bytes"},
	"tokens":            {"id", "user_id", "name", "key_enc", "key_prefix", "key_hash", "channel_id", "channel_ids_json", "channel_order_json", "restricted", "model_scope", "expires_at", "revoked", "created_at"},
	"model_pricing":     {"model", "input_per_m", "cached_input_per_m", "cache_write_per_m", "output_per_m", "currency", "updated_at"},
	"logs":              {"id", "created_at", "user_id", "token_id", "channel_id", "template_source_id", "line_url", "via", "key_id", "protocol", "model", "upstream_model", "status_code", "ttft_ms", "total_ms", "prompt_tokens", "completion_tokens", "cached_tokens", "cache_write_tokens", "reasoning_effort", "input_cost", "output_cost", "error"},
	"invite_codes":      {"code", "created_by", "used_by", "used_at"},
	"settings":          {"key", "value"},
	"breaker_states":    {"channel_id", "model", "fail_count", "opened_at", "cooldown_until", "last_error", "updated_at"},
}

func TestSchemaAlignment(t *testing.T) {
	st := openTestStore(t)

	for table, want := range expectedColumns {
		var got []string
		if err := st.db.Raw("SELECT name FROM pragma_table_info(?) ORDER BY cid", table).Scan(&got).Error; err != nil {
			t.Fatalf("查询表 %s 列失败: %v", table, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("表 %s 列与 DDL 不一致:\n got  %v\n want %v", table, got, want)
		}
	}

	// 索引存在性
	wantIdx := map[string]bool{
		"idx_channels_user":  false,
		"idx_logs_time":      false,
		"idx_logs_user":      false,
		"idx_logs_channel":   false,
		"idx_logs_key":       false,
		"idx_keys_user_name": false,
	}
	var idx []string
	if err := st.db.Raw("SELECT name FROM sqlite_master WHERE type='index'").Scan(&idx).Error; err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}
	for _, name := range idx {
		if _, ok := wantIdx[name]; ok {
			wantIdx[name] = true
		}
	}
	for name, found := range wantIdx {
		if !found {
			t.Errorf("缺少索引 %s", name)
		}
	}

	// 复合主键顺序
	for table, want := range map[string][]string{
		"line_stats":     {"channel_id", "line_url", "via"},
		"proxy_usage":    {"user_id", "proxy_id", "day"},
		"breaker_states": {"channel_id", "model"},
	} {
		var got []string
		if err := st.db.Raw("SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk", table).Scan(&got).Error; err != nil {
			t.Fatalf("查询表 %s 主键失败: %v", table, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("表 %s 复合主键不一致: got %v want %v", table, got, want)
		}
	}

	// pragma 生效
	var mode string
	if err := st.db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q err=%v，期望 wal", mode, err)
	}
	var fk int
	if err := st.db.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d err=%v，期望 1", fk, err)
	}
	var bt int
	if err := st.db.Raw("PRAGMA busy_timeout").Scan(&bt).Error; err != nil || bt != busyTimeoutMs {
		t.Errorf("busy_timeout = %d err=%v，期望 %d", bt, err, busyTimeoutMs)
	}
}

func TestOpenMemory(t *testing.T) {
	st, err := Open(Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	defer st.Close()

	var tables []string
	if err := st.db.Raw("SELECT name FROM sqlite_master WHERE type='table'").Scan(&tables).Error; err != nil {
		t.Fatalf("查询表失败: %v", err)
	}
	for table := range expectedColumns {
		if !slices.Contains(tables, table) {
			t.Errorf("内存库缺少表 %s", table)
		}
	}
	// 简单写入读回
	if err := st.db.Create(&Setting{Key: "k", Value: "v"}).Error; err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	v, err := st.GetSetting("k")
	if err != nil || v != "v" {
		t.Fatalf("读回失败: v=%q err=%v", v, err)
	}
}

func TestUserCRUD(t *testing.T) {
	st := openTestStore(t)

	hash, err := crypto.HashPassword("pw-123456")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	u := &User{Username: "alice", PasswordHash: &hash}
	if err := st.db.Create(u).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if u.ID == 0 || u.Role != 1 || u.Status != 1 || u.CreatedAt <= 0 {
		t.Fatalf("默认值不符合 DDL: id=%d role=%d status=%d created_at=%d", u.ID, u.Role, u.Status, u.CreatedAt)
	}

	var got User
	if err := st.db.Where("username = ?", "alice").First(&got).Error; err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if got.PasswordHash == nil || crypto.CheckPassword(*got.PasswordHash, "pw-123456") != nil {
		t.Fatal("密码哈希回读/校验失败")
	}
	if got.LastLoginAt != nil {
		t.Fatalf("last_login_at 初始应为 NULL，实际 %v", *got.LastLoginAt)
	}

	now := time.Now().Unix()
	if err := st.db.Model(&got).Update("last_login_at", now).Error; err != nil {
		t.Fatalf("更新登录时间失败: %v", err)
	}
	var got2 User
	st.db.First(&got2, got.ID)
	if got2.LastLoginAt == nil || *got2.LastLoginAt != now {
		t.Fatal("last_login_at 更新未生效")
	}

	// username 唯一
	if err := st.db.Create(&User{Username: "alice"}).Error; err == nil {
		t.Fatal("username 唯一约束未生效")
	}
	// feishu_user_id 唯一，允许多个 NULL
	fid := "ou_123"
	st.db.Create(&User{Username: "bob", FeishuUserID: &fid})
	st.db.Create(&User{Username: "carol"})
	st.db.Create(&User{Username: "dave"})
	if err := st.db.Create(&User{Username: "erin", FeishuUserID: &fid}).Error; err == nil {
		t.Fatal("feishu_user_id 唯一约束未生效")
	}
}

func TestKeyCRUD(t *testing.T) {
	st := openTestStore(t)

	plain := "sk-upstream-abcdef"
	enc, err := crypto.Encrypt(testSecret, crypto.PurposeKey, []byte(plain))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	k := &Key{UserID: 1, Name: "主力", ValueEnc: enc}
	if err := st.db.Create(k).Error; err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if k.Status != 1 || k.CooldownUntil != 0 || k.Note != "" {
		t.Fatalf("默认值不符合 DDL: %+v", k)
	}

	var got Key
	if err := st.db.First(&got, k.ID).Error; err != nil {
		t.Fatalf("查询密钥失败: %v", err)
	}
	dec, err := crypto.Decrypt(testSecret, crypto.PurposeKey, got.ValueEnc)
	if err != nil || string(dec) != plain {
		t.Fatalf("密文解密不一致: %q err=%v", dec, err)
	}

	// UNIQUE(user_id, name)：同用户重名冲突，异用户同名允许
	if err := st.db.Create(&Key{UserID: 1, Name: "主力", ValueEnc: enc}).Error; err == nil {
		t.Fatal("(user_id, name) 唯一约束未生效")
	}
	if err := st.db.Create(&Key{UserID: 2, Name: "主力", ValueEnc: enc}).Error; err != nil {
		t.Fatalf("异用户同名应允许: %v", err)
	}
}

func TestChannelCRUD(t *testing.T) {
	st := openTestStore(t)

	ch := &Channel{
		UserID: 7, Name: "c1", Type: "openai",
		BaseURLsJSON: `["https://api.example.com"]`, KeyIDsJSON: `[1,2]`,
		ModelsJSON: `["gpt-4o"]`,
	}
	if err := st.db.Create(ch).Error; err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	// DDL 默认值（enabled 默认 0 = 草稿，不参与路由）
	if ch.KeyStrategy != "ordered" || ch.LineStrategy != "auto" || ch.AllowPublicProxy != 0 ||
		ch.Priority != 0 || ch.IsDefault != 0 || ch.Enabled != 0 || ch.ModelMappingJSON != "{}" {
		t.Fatalf("默认值不符合 DDL: %+v", ch)
	}
	if ch.ProxyURLEnc != nil || ch.LastOkAt != nil || ch.LastError != nil || ch.CopiedFromTemplateID != nil {
		t.Fatalf("可空列初始应为 NULL: %+v", ch)
	}

	// 走 idx_channels_user 的查询
	var got []Channel
	if err := st.db.Where("user_id = ? AND enabled = ?", 7, 0).Find(&got).Error; err != nil || len(got) != 1 {
		t.Fatalf("按用户查询渠道失败: %v len=%d", err, len(got))
	}
	if err := st.db.Model(ch).Updates(map[string]any{"priority": 9, "enabled": 2}).Error; err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}
	if err := st.db.Delete(&Channel{}, ch.ID).Error; err != nil {
		t.Fatalf("删除渠道失败: %v", err)
	}
	var n int64
	st.db.Model(&Channel{}).Count(&n)
	if n != 0 {
		t.Fatalf("删除后计数应为 0，实际 %d", n)
	}
}

func TestTokenCRUD(t *testing.T) {
	st := openTestStore(t)

	tok, err := crypto.GenerateGatewayToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	enc, err := crypto.Encrypt(testSecret, crypto.PurposeToken, []byte(tok))
	if err != nil {
		t.Fatalf("加密令牌失败: %v", err)
	}
	tk := &Token{
		UserID: 1, Name: "默认", KeyEnc: enc,
		KeyPrefix: tok[:12], KeyHash: crypto.HashToken(tok),
	}
	if err := st.db.Create(tk).Error; err != nil {
		t.Fatalf("创建令牌失败: %v", err)
	}

	// key_hash O(1) 查找
	var got Token
	if err := st.db.Where("key_hash = ?", crypto.HashToken(tok)).First(&got).Error; err != nil {
		t.Fatalf("按 key_hash 查找失败: %v", err)
	}
	dec, err := crypto.Decrypt(testSecret, crypto.PurposeToken, got.KeyEnc)
	if err != nil || string(dec) != tok {
		t.Fatalf("令牌密文解密不一致: err=%v", err)
	}

	// key_hash 唯一
	if err := st.db.Create(&Token{
		UserID: 2, Name: "重复", KeyEnc: enc,
		KeyPrefix: "sk-keyway-x", KeyHash: crypto.HashToken(tok),
	}).Error; err == nil {
		t.Fatal("key_hash 唯一约束未生效")
	}

	// 吊销
	if err := st.db.Model(&got).Update("revoked", 1).Error; err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	var got2 Token
	st.db.First(&got2, got.ID)
	if got2.Revoked != 1 {
		t.Fatal("吊销未生效")
	}
}

func TestLogCRUD(t *testing.T) {
	st := openTestStore(t)

	lg := &Log{
		UserID: 1, Protocol: sPtr("openai"), Model: sPtr("gpt-4o"),
		UpstreamModel: sPtr("gpt-4o"), StatusCode: iPtr(200),
		TtftMs: i64Ptr(120), TotalMs: i64Ptr(3400),
		PromptTokens: i64Ptr(1000), CompletionTokens: i64Ptr(200),
		CachedTokens: i64Ptr(800), CacheWriteTokens: i64Ptr(0),
	}
	if err := st.db.Create(lg).Error; err != nil {
		t.Fatalf("创建日志失败: %v", err)
	}
	if lg.ID == 0 || lg.CreatedAt <= 0 {
		t.Fatalf("id/created_at 未填充: %+v", lg)
	}
	// 未定价：费用与关联维度为 NULL
	if lg.InputCost != nil || lg.OutputCost != nil || lg.ChannelID != nil || lg.KeyID != nil {
		t.Fatalf("可空列应为 NULL: %+v", lg)
	}

	var rows []Log
	if err := st.db.Where("user_id = ? AND created_at BETWEEN ? AND ?", 1, 1, lg.CreatedAt).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("按用户+时间查询失败: %v len=%d", err, len(rows))
	}
	got := rows[0]
	if *got.CachedTokens != 800 || *got.CacheWriteTokens != 0 || *got.StatusCode != 200 {
		t.Fatalf("缓存字段回读不一致: %+v", got)
	}
	if got.Error != nil || got.LineURL != nil || got.Via != nil {
		t.Fatalf("未填列应为 NULL: %+v", got)
	}
}

func TestSettings(t *testing.T) {
	st := openTestStore(t)

	if v, err := st.GetSetting("not-exist"); err != nil || v != "" {
		t.Fatalf("不存在的配置应返回空串: v=%q err=%v", v, err)
	}
	if err := st.SetSetting("site.name", "keyway"); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if v, err := st.GetSetting("site.name"); err != nil || v != "keyway" {
		t.Fatalf("读回配置失败: v=%q err=%v", v, err)
	}
	if err := st.SetSetting("site.name", "keyway-2"); err != nil {
		t.Fatalf("覆盖配置失败: %v", err)
	}
	if v, _ := st.GetSetting("site.name"); v != "keyway-2" {
		t.Fatalf("覆盖未生效: %q", v)
	}
}

func TestPricingSeed(t *testing.T) {
	st := openTestStore(t)

	var n int64
	st.db.Model(&ModelPricing{}).Count(&n)
	if n < 10 {
		t.Fatalf("内置价目不足 10 条，实际 %d", n)
	}

	var p ModelPricing
	if err := st.db.First(&p, "model = ?", "claude-sonnet-4-20250514").Error; err != nil {
		t.Fatalf("查询价目失败: %v", err)
	}
	if p.InputPerM != 3 || p.OutputPerM != 15 || p.Currency != "USD" ||
		p.CachedInputPerM == nil || *p.CachedInputPerM != 0.3 ||
		p.CacheWritePerM == nil || *p.CacheWritePerM != 3.75 {
		t.Fatalf("claude 价目不符: %+v", p)
	}

	var pm ModelPricing
	if err := st.db.First(&pm, "model = ?", "kimi-k3").Error; err != nil {
		t.Fatalf("查询 kimi 价目失败: %v", err)
	}
	if pm.InputPerM != 2.78 || pm.OutputPerM != 13.89 {
		t.Fatalf("kimi-k3 折算价不符: %+v", pm)
	}

	// 管理员改价后重开，种子不得覆盖
	dir := t.TempDir()
	st2, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	if err := st2.db.Model(&ModelPricing{}).Where("model = ?", "gpt-4o").Update("input_per_m", 9.9).Error; err != nil {
		t.Fatalf("改价失败: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("关闭存储失败: %v", err)
	}

	st3, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatalf("重开存储失败: %v", err)
	}
	defer st3.Close()
	var p3 ModelPricing
	if err := st3.db.First(&p3, "model = ?", "gpt-4o").Error; err != nil {
		t.Fatalf("重查价目失败: %v", err)
	}
	if p3.InputPerM != 9.9 {
		t.Fatalf("重开后种子覆盖了管理员改价: %+v", p3)
	}
	var n3 int64
	st3.db.Model(&ModelPricing{}).Count(&n3)
	if n3 != n {
		t.Fatalf("重开后价目条数变化: %d != %d", n3, n)
	}
}

func TestCompositePKUpsert(t *testing.T) {
	st := openTestStore(t)

	ls := &LineStat{ChannelID: 1, LineURL: "https://a.example.com", Via: "direct",
		LatencyMs: i64Ptr(120), Ok: iPtr(1)}
	if err := st.db.Create(ls).Error; err != nil {
		t.Fatalf("写入 line_stats 失败: %v", err)
	}
	// 同主键 upsert：更新而非新增
	ls2 := &LineStat{ChannelID: 1, LineURL: "https://a.example.com", Via: "direct",
		LatencyMs: i64Ptr(90), Ok: iPtr(1)}
	if err := st.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(ls2).Error; err != nil {
		t.Fatalf("更新 line_stats 失败: %v", err)
	}
	var n int64
	st.db.Model(&LineStat{}).Count(&n)
	if n != 1 {
		t.Fatalf("line_stats 复合主键未生效: 计数 %d", n)
	}
	var got LineStat
	st.db.First(&got, "channel_id = ?", 1)
	if got.LatencyMs == nil || *got.LatencyMs != 90 {
		t.Fatalf("line_stats 更新未生效: %+v", got)
	}

	pu := &ProxyUsage{UserID: 1, ProxyID: 2, Day: "2026-09-14", Bytes: 1024}
	if err := st.db.Create(pu).Error; err != nil {
		t.Fatalf("写入 proxy_usage 失败: %v", err)
	}
	if err := st.db.Exec("UPDATE proxy_usage SET bytes = bytes + 512 WHERE user_id = 1 AND proxy_id = 2 AND day = '2026-09-14'").Error; err != nil {
		t.Fatalf("更新 proxy_usage 失败: %v", err)
	}
	var pu2 ProxyUsage
	if err := st.db.First(&pu2, "user_id = ? AND proxy_id = ?", 1, 2).Error; err != nil {
		t.Fatalf("查询 proxy_usage 失败: %v", err)
	}
	if pu2.Bytes != 1536 {
		t.Fatalf("proxy_usage 累加不符: %d", pu2.Bytes)
	}
}
