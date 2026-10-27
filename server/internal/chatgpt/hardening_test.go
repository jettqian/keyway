package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"keyway/internal/store"
)

// seedKey 落一把 chatgpt 密钥并返回（调用方持有的即"请求开始时读出的"副本）
func seedKey(t *testing.T, st *store.Store, secret string, b Bundle) *store.Key {
	t.Helper()
	enc, err := EncryptBundle(secret, &b)
	if err != nil {
		t.Fatal(err)
	}
	k := &store.Key{UserID: 1, Name: "chatgpt-test", ValueEnc: enc, Kind: store.KeyKindChatGPT, Status: 1}
	if err := st.DB().Create(k).Error; err != nil {
		t.Fatal(err)
	}
	return k
}

func storedBundle(t *testing.T, st *store.Store, secret string, id int64) (*Bundle, store.Key) {
	t.Helper()
	var k store.Key
	if err := st.DB().First(&k, id).Error; err != nil {
		t.Fatal(err)
	}
	b, err := DecryptBundle(secret, k.ValueEnc)
	if err != nil {
		t.Fatal(err)
	}
	return b, k
}

// 调用方持有的密钥行早于上一次刷新落库：库中令牌已是新的 → 不再刷新、直接用库中令牌
// （否则会拿已被轮换掉的 refresh_token 再刷一次，触发服务端复用检测）
func TestRefreshReadsLatestStoredBundle(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	calls := 0
	var mu sync.Mutex
	m := newTestManager(t, st, secret, tokenEndpoint(t, &mu, &calls, "unused", "unused", 3600))

	stale := seedKey(t, st, secret, Bundle{
		AccessToken: "old-access", RefreshToken: "rt-rotated-away",
		AccountID: "acct-0123456789", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	})
	// 另一个请求已完成刷新并落库
	if err := m.persist(stale.ID, Bundle{
		AccessToken: "new-access", RefreshToken: "rt-latest",
		AccountID: "acct-0123456789", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	tok, _, err := m.EnsureFresh(context.Background(), stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "new-access" {
		t.Fatalf("应返回库中最新令牌，实际 %q", tok)
	}
	if calls != 0 {
		t.Fatalf("库中令牌已新鲜，不应再请求令牌端点，实际 %d 次", calls)
	}
}

// 库中令牌也已过期时，刷新必须用库中最新的 refresh_token，而不是调用方手里的旧值
func TestRefreshUsesLatestRefreshToken(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	var gotRT string
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotRT = r.PostForm.Get("refresh_token")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt-next", "expires_in": 3600})
	})
	stale := seedKey(t, st, secret, Bundle{
		AccessToken: "a0", RefreshToken: "rt-old", AccountID: "acct", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err := m.persist(stale.ID, Bundle{
		AccessToken: "a1", RefreshToken: "rt-latest", AccountID: "acct", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.EnsureFresh(context.Background(), stale, nil); err != nil {
		t.Fatal(err)
	}
	if gotRT != "rt-latest" {
		t.Fatalf("应使用库中最新 refresh_token，实际 %q", gotRT)
	}
}

// 刷新失败后退避：期内不再请求令牌端点；密钥同步冷却；4xx 长退避、5xx 短退避；
// 手动刷新无视退避，成功后撤销刷新失败设下的冷却
func TestRefreshFailureBackoff(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		backoff  time.Duration
		wantHint bool // last_error 是否提示重新登录
	}{
		{"令牌端点 5xx 短退避", http.StatusBadGateway, refreshRetryBackoff, false},
		{"invalid_grant 长退避", http.StatusBadRequest, refreshDeadBackoff, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := setupStore(t)
			secret := strings.Repeat("k", 32)
			var mu sync.Mutex
			calls, fail := 0, true
			access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
			m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				f := fail
				mu.Unlock()
				if f {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"boom"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt2", "expires_in": 3600})
			})
			k := seedKey(t, st, secret, Bundle{
				AccessToken: "a", RefreshToken: "rt", AccountID: "acct", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
			})

			for i := 0; i < 3; i++ {
				if _, _, err := m.EnsureFresh(context.Background(), k, nil); err == nil {
					t.Fatal("刷新失败应报错")
				}
			}
			if calls != 1 {
				t.Fatalf("退避期内不应重复请求令牌端点，实际 %d 次", calls)
			}
			_, row := storedBundle(t, st, secret, k.ID)
			want := time.Now().Add(tc.backoff).Unix()
			if row.CooldownUntil < want-5 || row.CooldownUntil > want+5 {
				t.Fatalf("密钥冷却应约为 %v，实际 cooldown_until=%d（期望≈%d）", tc.backoff, row.CooldownUntil, want)
			}
			if row.LastError == nil || strings.Contains(*row.LastError, "重新登录") != tc.wantHint {
				t.Fatalf("last_error 提示不符: %v", row.LastError)
			}

			mu.Lock()
			fail = false
			mu.Unlock()
			if err := m.RefreshNow(context.Background(), k); err != nil {
				t.Fatalf("手动刷新应无视退避并成功: %v", err)
			}
			if calls != 2 {
				t.Fatalf("手动刷新应请求令牌端点，实际累计 %d 次", calls)
			}
			b, row := storedBundle(t, st, secret, k.ID)
			if b.AccessToken != access || row.CooldownUntil != 0 || row.LastError != nil {
				t.Fatalf("手动刷新成功后应落新令牌并清除冷却与错误: cooldown=%d err=%v", row.CooldownUntil, row.LastError)
			}
		})
	}
}

// 刷新成功只撤销刷新失败设下的冷却，更晚到期的 429/402 冷却保留
func TestRefreshSuccessKeepsQuotaCooldown(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	fail := true
	m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt2", "expires_in": 3600})
	})
	k := seedKey(t, st, secret, Bundle{AccessToken: "a", RefreshToken: "rt", AccountID: "acct", ExpiresAt: 1})
	if _, _, err := m.EnsureFresh(context.Background(), k, nil); err == nil {
		t.Fatal("刷新失败应报错")
	}
	quota := time.Now().Add(5 * time.Hour).Unix()
	st.DB().Model(&store.Key{}).Where("id = ?", k.ID).Update("cooldown_until", quota)
	fail = false
	if err := m.RefreshNow(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if _, row := storedBundle(t, st, secret, k.ID); row.CooldownUntil != quota {
		t.Fatalf("额度冷却不应被刷新成功清除: %d", row.CooldownUntil)
	}
}

// 调用方取消只放弃等待：刷新在后台跑完并落库（中断可能丢失已被轮换的 refresh_token）
func TestRefreshSurvivesCallerCancel(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	entered, release := make(chan struct{}), make(chan struct{})
	m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt2", "expires_in": 3600})
	})
	k := seedKey(t, st, secret, Bundle{AccessToken: "a", RefreshToken: "rt", AccountID: "acct", ExpiresAt: 1})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, _, err := m.EnsureFresh(ctx, k, nil)
		errc <- err
	}()
	<-entered
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应立即返回 context.Canceled，实际 %v", err)
	}

	// 第二个调用方加入同一次进行中的刷新，等它完成
	done := make(chan struct{})
	go func() {
		defer close(done)
		if tok, _, err := m.EnsureFresh(context.Background(), k, nil); err != nil || tok != access {
			t.Errorf("加入进行中刷新应拿到新令牌: %v", err)
		}
	}()
	close(release)
	<-done
	if b, _ := storedBundle(t, st, secret, k.ID); b.RefreshToken != "rt2" {
		t.Fatalf("调用方取消后刷新仍应落库，实际 refresh_token=%q", b.RefreshToken)
	}
}

// 刷新响应缺 expires_in 时取 JWT exp（缺省到期=当前时刻会让每个请求都刷新）
func TestRefreshExpiryFromJWT(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	exp := time.Now().Add(48 * time.Hour).Unix()
	access := signJWT(t, testClaims(exp))
	m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access})
	})
	k := seedKey(t, st, secret, Bundle{AccessToken: "a", RefreshToken: "rt", AccountID: "acct", ExpiresAt: 1})
	if _, _, err := m.EnsureFresh(context.Background(), k, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := storedBundle(t, st, secret, k.ID)
	if b.ExpiresAt != exp {
		t.Fatalf("到期时间应取 JWT exp=%d，实际 %d", exp, b.ExpiresAt)
	}
	if b.RefreshToken != "rt" {
		t.Fatalf("响应未轮转 refresh_token 时应保留旧值，实际 %q", b.RefreshToken)
	}
	if b.Email != "user@example.com" {
		t.Fatalf("刷新应补全邮箱，实际 %q", b.Email)
	}
}

func pendingState(m *Manager, userID int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s, p := range m.pending {
		if p.UserID == userID {
			return s
		}
	}
	return ""
}

// 登录：邮箱落库；显式命名撞名如实报错，自动命名撞名加后缀
func TestCompleteLoginNaming(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	var mu sync.Mutex
	calls := 0
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	m := newTestManager(t, st, secret, tokenEndpoint(t, &mu, &calls, access, "rt", 3600))
	login := func(name string) (*store.Key, error) {
		if _, err := m.StartLogin(1); err != nil {
			t.Fatal(err)
		}
		k, _, err := m.CompleteLogin(context.Background(), 1, redirectURI+"?code=c&state="+pendingState(m, 1), name)
		return k, err
	}

	first, err := login("")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := storedBundle(t, st, secret, first.ID); b.Email != "user@example.com" {
		t.Fatalf("邮箱应落库，实际 %q", b.Email)
	}
	second, err := login("")
	if err != nil {
		t.Fatalf("自动命名撞名应加后缀重试: %v", err)
	}
	if second.Name == first.Name || !strings.HasPrefix(second.Name, first.Name+"-") {
		t.Fatalf("自动命名后缀异常: %s / %s", first.Name, second.Name)
	}
	if _, err := login("mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := login("mine"); err == nil || !strings.Contains(err.Error(), "同名密钥已存在") {
		t.Fatalf("显式命名撞名应如实报错，实际 %v", err)
	}
}

// 单用户待完成登录会话有上限，超出淘汰最早的；不影响其他用户
func TestStartLoginCapsPendingPerUser(t *testing.T) {
	m := NewManager(setupStore(t), "secret")
	if _, err := m.StartLogin(2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPendingPerUser+3; i++ {
		if _, err := m.StartLogin(1); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := map[int64]int{}
	for _, p := range m.pending {
		n[p.UserID]++
	}
	if n[1] != maxPendingPerUser || n[2] != 1 {
		t.Fatalf("待完成会话计数异常: %v", n)
	}
}

func TestIsBackendURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://chatgpt.com/backend-api/codex", true},
		{"https://ChatGPT.com/backend-api/codex/", true},
		{"http://chatgpt.com/backend-api/codex", false}, // 明文 http 不发令牌
		{"https://chatgpt.com.evil.example/backend-api/codex", false},
		{"https://evil.example/?u=https://chatgpt.com", false},
		{"https://api.openai.com/v1", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if got := IsBackendURL(tc.url); got != tc.want {
			t.Errorf("IsBackendURL(%q) = %v，期望 %v", tc.url, got, tc.want)
		}
	}
}

func TestPrepareRequest(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"字符串 input 展开并补 store", `{"input":"hi"}`,
			`{"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"store":false}`},
		{"数组 input 原样保留", `{"input":[{"type":"message"}],"store":false}`, `{"input":[{"type":"message"}],"store":false}`},
		{"客户端显式 store 保留原值", `{"input":[],"store":true}`, `{"input":[],"store":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(tc.in), &m); err != nil {
				t.Fatal(err)
			}
			PrepareRequest(m)
			got, _ := json.Marshal(m)
			if string(got) != tc.want {
				t.Fatalf("got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestApplyHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("originator", "opencode")
	ApplyHeaders(h, "acct-1")
	if h.Get("chatgpt-account-id") != "acct-1" || h.Get("OpenAI-Beta") != "responses=experimental" {
		t.Fatalf("缺省头未补齐: %v", h)
	}
	if h.Get("originator") != "opencode" {
		t.Fatalf("客户端已带的头不应被改写: %q", h.Get("originator"))
	}
}

func TestUsageLimitReset(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name    string
		body    string
		wantSec int64
		wantOK  bool
	}{
		{"resets_in_seconds", `{"error":{"type":"usage_limit_reached","resets_in_seconds":7200}}`, 7200, true},
		{"resets_at", `{"error":{"type":"usage_limit_reached","resets_at":` + itoa(now+600) + `}}`, 600, true},
		{"无重置时间", `{"error":{"type":"usage_limit_reached"}}`, 0, true},
		{"普通限流", `{"error":{"type":"rate_limit_exceeded"}}`, 0, false},
		{"非 JSON", `<html>`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sec, ok := UsageLimitReset([]byte(tc.body))
			if ok != tc.wantOK || sec < tc.wantSec-2 || sec > tc.wantSec {
				t.Fatalf("got (%d,%v)，期望 (%d,%v)", sec, ok, tc.wantSec, tc.wantOK)
			}
		})
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
