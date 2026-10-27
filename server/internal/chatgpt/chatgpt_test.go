package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"keyway/internal/store"
)

// signJWT 构造测试用 JWT（payload base64url，不验签场景）
func signJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func testClaims(exp int64) map[string]any {
	return map[string]any{
		"email": "user@example.com",
		"exp":   exp,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-0123456789",
			"chatgpt_plan_type":  "plus",
		},
	}
}

// newTestManager 建 Manager + 模拟令牌端点；返回管理器与记录请求的句柄
func newTestManager(t *testing.T, st *store.Store, secret string, handler http.HandlerFunc) *Manager {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldURL := TokenURL
	TokenURL = srv.URL
	t.Cleanup(func() { TokenURL = oldURL })
	return NewManager(st, secret)
}

func tokenEndpoint(t *testing.T, mu *sync.Mutex, calls *int, access, refresh string, expiresIn int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls++
		mu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("code_verifier") == "" && r.PostForm.Get("grant_type") == "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"missing verifier"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"expires_in":    expiresIn,
		})
	}
}

func setupStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(store.Options{DataDir: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStartLoginAndParseCallback(t *testing.T) {
	st := setupStore(t)
	m := newTestManager(t, st, "secret", func(w http.ResponseWriter, r *http.Request) {})

	urlStr, err := m.StartLogin(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(urlStr, "auth.openai.com/oauth/authorize") ||
		!strings.Contains(urlStr, "code_challenge_method=S256") ||
		!strings.Contains(urlStr, oauthClientID) {
		t.Fatalf("授权链接参数不完整: %s", urlStr)
	}

	// 授权链接不是回调地址（缺 code），应被拒绝
	if _, _, err := parseCallback(urlStr + "&ignored=1"); err == nil {
		t.Fatal("授权链接不应被解析为回调")
	}

	m.mu.Lock()
	var state string
	for s := range m.pending {
		state = s
	}
	m.mu.Unlock()
	callback := redirectURI + "?code=abc123&state=" + state
	code, gotState, err := parseCallback(callback)
	if err != nil || code != "abc123" || gotState != state {
		t.Fatalf("回调解析失败: code=%s state=%s err=%v", code, gotState, err)
	}
	// 纯查询串形式也应可解析
	if _, _, err := parseCallback("?code=x&state=y"); err != nil {
		t.Fatalf("纯查询串解析失败: %v", err)
	}
	if _, _, err := parseCallback("http://localhost:1455/auth/callback?error=access_denied"); err == nil {
		t.Fatal("缺少 code/state 应报错")
	}
}

func TestCompleteLoginCreatesChatGPTKey(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	calls := 0
	var mu sync.Mutex
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	m := newTestManager(t, st, secret, tokenEndpoint(t, &mu, &calls, access, "rt-2", 86400))

	if _, err := m.StartLogin(7); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	var state string
	for s := range m.pending {
		state = s
	}
	m.mu.Unlock()

	k, info, err := m.CompleteLogin(context.Background(), 7, redirectURI+"?code=good&state="+state, "")
	if err != nil {
		t.Fatal(err)
	}
	if k.Kind != store.KeyKindChatGPT || k.UserID != 7 {
		t.Fatalf("密钥字段错误: %+v", k)
	}
	if !strings.HasPrefix(k.Name, "chatgpt-") {
		t.Fatalf("自动命名错误: %s", k.Name)
	}
	if info.Plan != "plus" || info.AccountID != "acct-0123456789" || info.Email != "user@example.com" {
		t.Fatalf("账号信息错误: %+v", info)
	}
	// 落库内容加密且可解回
	b, err := DecryptBundle(secret, k.ValueEnc)
	if err != nil {
		t.Fatal(err)
	}
	if b.AccessToken != access || b.RefreshToken != "rt-2" || b.AccountID != "acct-0123456789" {
		t.Fatalf("Bundle 内容错误: %+v", b)
	}
	// state 一次性
	if _, _, err := m.CompleteLogin(context.Background(), 7, redirectURI+"?code=again&state="+state, ""); err == nil {
		t.Fatal("state 复用应失败")
	}
	// 他人会话不可用
	if _, err := m.StartLogin(8); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for s, p := range m.pending {
		if p.UserID == 8 {
			state = s
		}
	}
	m.mu.Unlock()
	if _, _, err := m.CompleteLogin(context.Background(), 7, redirectURI+"?code=x&state="+state, ""); err == nil {
		t.Fatal("跨用户完成登录应失败")
	}
}

func TestEnsureFreshRefreshesNearExpiry(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	calls := 0
	var mu sync.Mutex
	access1 := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	access2 := signJWT(t, testClaims(time.Now().Add(48*time.Hour).Unix()))
	m := newTestManager(t, st, secret, tokenEndpoint(t, &mu, &calls, access2, "rt-3", 172800))

	enc, err := EncryptBundle(secret, &Bundle{
		AccessToken: access1, RefreshToken: "rt-old",
		AccountID: "acct-0123456789", ExpiresAt: time.Now().Add(-time.Minute).Unix(), // 已过期
	})
	if err != nil {
		t.Fatal(err)
	}
	k := &store.Key{UserID: 1, Name: "chatgpt-test", ValueEnc: enc, Kind: store.KeyKindChatGPT, Status: 1}
	if err := st.DB().Create(k).Error; err != nil {
		t.Fatal(err)
	}

	// 并发 5 个 EnsureFresh：应只刷新一次（singleflight），全部拿到新 token
	var wg sync.WaitGroup
	results := make([]string, 5)
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, _, err := m.EnsureFresh(context.Background(), k, nil)
			results[i], errs[i] = tok, err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("EnsureFresh[%d] 失败: %v", i, errs[i])
		}
		if results[i] != access2 {
			t.Fatalf("EnsureFresh[%d] 未拿到新令牌", i)
		}
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("并发下应只刷新一次，实际 %d 次", n)
	}
	// 新令牌落库
	var fresh store.Key
	if err := st.DB().First(&fresh, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	b, err := DecryptBundle(secret, fresh.ValueEnc)
	if err != nil {
		t.Fatal(err)
	}
	if b.RefreshToken != "rt-3" {
		t.Fatalf("轮转后的 refresh_token 未落库: %s", b.RefreshToken)
	}
	if fresh.LastError != nil {
		t.Fatalf("不应有 last_error: %v", *fresh.LastError)
	}
}

func TestEnsureFreshValidTokenNoRefresh(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	calls := 0
	var mu sync.Mutex
	access := signJWT(t, testClaims(time.Now().Add(24*time.Hour).Unix()))
	m := newTestManager(t, st, secret, tokenEndpoint(t, &mu, &calls, access, "rt", 86400))

	enc, err := EncryptBundle(secret, &Bundle{
		AccessToken: access, RefreshToken: "rt",
		AccountID: "acct-0123456789", ExpiresAt: time.Now().Add(time.Hour).Unix(), // 余量充足
	})
	if err != nil {
		t.Fatal(err)
	}
	k := &store.Key{UserID: 1, Name: "chatgpt-test", ValueEnc: enc, Kind: store.KeyKindChatGPT, Status: 1}
	if err := st.DB().Create(k).Error; err != nil {
		t.Fatal(err)
	}
	tok, acct, err := m.EnsureFresh(context.Background(), k, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != access || acct != "acct-0123456789" {
		t.Fatalf("有效令牌被改动: %s %s", tok, acct)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 0 {
		t.Fatalf("余量充足不应触发刷新，实际 %d 次", n)
	}
}

func TestRefreshFailureMarksKeyError(t *testing.T) {
	st := setupStore(t)
	secret := strings.Repeat("k", 32)
	m := newTestManager(t, st, secret, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	})

	enc, err := EncryptBundle(secret, &Bundle{
		AccessToken: "expired", RefreshToken: "revoked",
		AccountID: "acct-0123456789", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	k := &store.Key{UserID: 1, Name: "chatgpt-test", ValueEnc: enc, Kind: store.KeyKindChatGPT, Status: 1}
	if err := st.DB().Create(k).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.EnsureFresh(context.Background(), k, nil); err == nil {
		t.Fatal("刷新失败应报错")
	}
	var fresh store.Key
	if err := st.DB().First(&fresh, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.LastError == nil || !strings.Contains(*fresh.LastError, "refresh token revoked") {
		t.Fatalf("last_error 未记录上游摘要: %v", fresh.LastError)
	}
}

func TestParseJWTClaims(t *testing.T) {
	token := signJWT(t, testClaims(1234))
	c, err := parseJWTClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exp != 1234 || c.Auth == nil || c.Auth.PlanType != "plus" {
		t.Fatalf("claims 解析错误: %+v", c)
	}
	if _, err := parseJWTClaims("not-a-jwt"); err == nil {
		t.Fatal("非法 JWT 应报错")
	}
}
