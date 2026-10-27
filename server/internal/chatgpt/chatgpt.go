// Package chatgpt ChatGPT 账号 OAuth 登录与令牌管理（FR-K9~K12，v1.5.81）。
//
// 复用 Codex CLI 的公开 OAuth client（PKCE public client）走
// "Sign in with ChatGPT"，取得 access/refresh token 后直接调用 ChatGPT 后端
// Responses 端点，消耗 Plus 订阅额度（与 API 余额隔离）。社区方案，违反 OpenAI
// ToS、有封号风险——登录向导明示，用户自担。
//
// 令牌以加密 JSON 存于 keys.value_enc（kind=chatgpt），复用 PurposeKey 加密；
// EnsureFresh 供转发/探测按需取有效 access_token（快过期自动刷新落库）。
package chatgpt

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// OpenAI OAuth 端点与 Codex CLI 公开 client 常量（对齐 codex-rs login 模块）
var (
	AuthorizeURL = "https://auth.openai.com/oauth/authorize"
	TokenURL     = "https://auth.openai.com/oauth/token"
)

const (
	oauthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	redirectURI   = "http://localhost:1455/auth/callback"
	oauthScope    = "openid profile email offline_access"

	// refreshMargin 剩余有效期不足该值即触发刷新
	refreshMargin = 5 * time.Minute
	// pendingTTL 待完成登录会话有效期（粘贴回调地址的人工窗口）
	pendingTTL = 10 * time.Minute
	// tokenHTTPTimeout 令牌交换/刷新请求超时
	tokenHTTPTimeout = 30 * time.Second
)

// Bundle keys.value_enc（kind=chatgpt）的明文结构
type Bundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	Plan         string `json:"plan"`
	Email        string `json:"email,omitempty"`
	ExpiresAt    int64  `json:"expires_at"` // unix 秒
}

// AccountInfo 登录完成后的展示信息（不含任何令牌明文）
type AccountInfo struct {
	AccountID string `json:"accountId"`
	Plan      string `json:"plan"`
	Email     string `json:"email,omitempty"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Manager ChatGPT 账号登录与令牌管理器
type Manager struct {
	store  *store.Store
	secret string
	client *http.Client // 默认 transport，尊重 HTTPS_PROXY（服务端出网走代理的场景）

	mu        sync.Mutex
	pending   map[string]pendingLogin // state → 待完成会话（一次性，TTL 过期清理）
	refreshMu sync.Mutex
	refreshes map[int64]*refreshCall // 按密钥 ID singleflight
}

type pendingLogin struct {
	Verifier  string
	UserID    int64
	ExpiresAt time.Time
}

type refreshCall struct {
	wg     sync.WaitGroup
	bundle Bundle
	err    error
}

// NewManager 构造管理器
func NewManager(st *store.Store, secret string) *Manager {
	return &Manager{
		store:     st,
		secret:    secret,
		client:    &http.Client{Timeout: tokenHTTPTimeout},
		pending:   map[string]pendingLogin{},
		refreshes: map[int64]*refreshCall{},
	}
}

// ---------- 登录流程 ----------

// StartLogin 生成 PKCE 授权链接；state 与发起用户绑定（complete 时校验归属）
func (m *Manager) StartLogin(userID int64) (string, error) {
	verifier, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("生成 code_verifier 失败: %w", err)
	}
	state, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("生成 state 失败: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	m.mu.Lock()
	m.gcLocked()
	m.pending[state] = pendingLogin{Verifier: verifier, UserID: userID, ExpiresAt: time.Now().Add(pendingTTL)}
	m.mu.Unlock()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", oauthClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", oauthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return AuthorizeURL + "?" + q.Encode(), nil
}

// CompleteLogin 用用户粘贴的回调地址完成登录并创建 kind=chatgpt 密钥。
// callbackUrl 接受完整 URL（localhost:1455 页面报错不影响复制地址栏）。
// name 为空时自动命名（chatgpt-<account_id 后 6 位>，重名加随机后缀）
func (m *Manager) CompleteLogin(userID int64, callbackURL, name string) (*store.Key, AccountInfo, error) {
	code, state, err := parseCallback(callbackURL)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	m.mu.Lock()
	p, ok := m.pending[state]
	if !ok {
		m.mu.Unlock()
		return nil, AccountInfo{}, errors.New("登录会话不存在或已过期，请重新发起")
	}
	delete(m.pending, state) // 一次性
	m.mu.Unlock()
	if p.UserID != userID {
		return nil, AccountInfo{}, errors.New("登录会话与当前用户不匹配")
	}
	if time.Now().After(p.ExpiresAt) {
		return nil, AccountInfo{}, errors.New("登录会话已过期，请重新发起")
	}

	tok, err := m.exchangeToken(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {oauthClientID},
		"code_verifier": {p.Verifier},
	})
	if err != nil {
		return nil, AccountInfo{}, fmt.Errorf("换取令牌失败: %w", err)
	}
	bundle, info, err := bundleFromToken(tok)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	if name == "" {
		name = defaultName(bundle.AccountID)
	}
	k, err := m.createKey(userID, name, bundle)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	return k, info, nil
}

// parseCallback 从粘贴内容提取 code 与 state（容忍整段 URL 或纯查询串）
func parseCallback(raw string) (code, state string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("回调地址为空")
	}
	if !strings.Contains(raw, "://") {
		raw = redirectURI + "?" + strings.TrimPrefix(raw, "?")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("回调地址无法解析: %w", err)
	}
	q := u.Query()
	code, state = q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		return "", "", errors.New("回调地址缺少 code/state，请完整复制登录后浏览器地址栏 URL")
	}
	return code, state, nil
}

// ---------- 令牌刷新 ----------

// EnsureFresh 返回密钥当前有效的 access_token 与 account_id；
// 剩余有效期不足 refreshMargin 时自动刷新并落库（按密钥 singleflight）
func (m *Manager) EnsureFresh(k *store.Key) (string, string, error) {
	b, err := DecryptBundle(m.secret, k.ValueEnc)
	if err != nil {
		return "", "", err
	}
	if time.Until(time.Unix(b.ExpiresAt, 0)) > refreshMargin {
		return b.AccessToken, b.AccountID, nil
	}
	nb, err := m.refresh(k, *b)
	if err != nil {
		return "", "", err
	}
	return nb.AccessToken, nb.AccountID, nil
}

// RefreshNow 强制刷新（控制台手动刷新端点用）
func (m *Manager) RefreshNow(k *store.Key) error {
	b, err := DecryptBundle(m.secret, k.ValueEnc)
	if err != nil {
		return err
	}
	_, err = m.refresh(k, *b)
	return err
}

// refresh 带并发收敛的令牌刷新：并发请求共享同一次刷新结果，避免
// refresh_token 轮转后旧值互相覆盖（先到先得，后来者读库重试一次）
func (m *Manager) refresh(k *store.Key, b Bundle) (Bundle, error) {
	m.refreshMu.Lock()
	if call, ok := m.refreshes[k.ID]; ok {
		m.refreshMu.Unlock()
		call.wg.Wait()
		return call.bundle, call.err
	}
	call := &refreshCall{}
	call.wg.Add(1)
	m.refreshes[k.ID] = call
	m.refreshMu.Unlock()

	call.bundle, call.err = m.doRefresh(k, b)
	call.wg.Done()

	m.refreshMu.Lock()
	delete(m.refreshes, k.ID)
	m.refreshMu.Unlock()
	return call.bundle, call.err
}

func (m *Manager) doRefresh(k *store.Key, b Bundle) (Bundle, error) {
	if b.RefreshToken == "" {
		return Bundle{}, errors.New("缺少 refresh_token，请在控制台重新登录 ChatGPT 账号")
	}
	tok, err := m.exchangeToken(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
		"client_id":     {oauthClientID},
	})
	if err != nil {
		m.markKeyError(k.ID, "令牌刷新失败: "+err.Error())
		return Bundle{}, fmt.Errorf("令牌刷新失败: %w", err)
	}
	nb := b // 刷新响应可能不含 refresh_token（未轮转），保留旧值
	if tok.RefreshToken != "" {
		nb.RefreshToken = tok.RefreshToken
	}
	nb.AccessToken = tok.AccessToken
	nb.ExpiresAt = time.Now().Unix() + tok.ExpiresIn
	if err := m.persist(k, nb); err != nil {
		return Bundle{}, err
	}
	m.clearKeyError(k.ID)
	return nb, nil
}

// ---------- 令牌交换与 JWT 解析 ----------

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (m *Manager) exchangeToken(form url.Values) (*tokenResponse, error) {
	resp, err := m.client.PostForm(TokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("请求令牌端点失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Description != "" {
			return nil, fmt.Errorf("令牌端点返回 %d: %s（%s）", resp.StatusCode, e.Error, e.Description)
		}
		return nil, fmt.Errorf("令牌端点返回 %d", resp.StatusCode)
	}
	var tok tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("解析令牌响应失败: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("令牌响应缺少 access_token")
	}
	return &tok, nil
}

// bundleFromToken 从令牌响应构造存储 Bundle 与展示信息；
// JWT 仅解 payload 提取 claim，不验签——签名校验由 OpenAI 后端在每次调用时完成，
// 网关只提取展示与转发所需字段
func bundleFromToken(tok *tokenResponse) (Bundle, AccountInfo, error) {
	claims, err := parseJWTClaims(tok.AccessToken)
	if err != nil {
		return Bundle{}, AccountInfo{}, fmt.Errorf("解析 access_token 失败: %w", err)
	}
	b := Bundle{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    claims.Exp,
	}
	if b.ExpiresAt == 0 {
		b.ExpiresAt = time.Now().Add(time.Hour).Unix() // 无 exp 时兜底 1h，到期即走刷新
	}
	if claims.Auth != nil {
		b.AccountID = claims.Auth.AccountID
		b.Plan = claims.Auth.PlanType
	}
	if b.AccountID == "" {
		return Bundle{}, AccountInfo{}, errors.New("令牌缺少 chatgpt_account_id（非 ChatGPT 账号令牌？）")
	}
	info := AccountInfo{AccountID: b.AccountID, Plan: b.Plan, Email: claims.Email, ExpiresAt: b.ExpiresAt}
	return b, info, nil
}

type jwtClaims struct {
	Email string   `json:"email"`
	Exp   int64    `json:"exp"`
	Auth  *jwtAuth `json:"https://api.openai.com/auth"`
}

type jwtAuth struct {
	AccountID string `json:"chatgpt_account_id"`
	PlanType  string `json:"chatgpt_plan_type"`
}

// parseJWTClaims 解析 JWT payload（base64url，容忍缺省 padding）
func parseJWTClaims(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("不是合法的 JWT 结构")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("JWT payload 解码失败: %w", err)
	}
	var c jwtClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("JWT payload 解析失败: %w", err)
	}
	return &c, nil
}

// ---------- 存取 ----------

// DecryptBundle 解密密钥值（api 列表 DTO / probe 复用）
func DecryptBundle(secret string, enc []byte) (*Bundle, error) {
	plain, err := crypto.Decrypt(secret, crypto.PurposeKey, enc)
	if err != nil {
		return nil, fmt.Errorf("解密 ChatGPT 令牌失败: %w", err)
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, fmt.Errorf("ChatGPT 令牌数据损坏: %w", err)
	}
	return &b, nil
}

// EncryptBundle 加密 Bundle 为 value_enc（登录创建与刷新落库共用）
func EncryptBundle(secret string, b *Bundle) ([]byte, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	return crypto.Encrypt(secret, crypto.PurposeKey, raw)
}

func (m *Manager) createKey(userID int64, name string, b Bundle) (*store.Key, error) {
	enc, err := EncryptBundle(m.secret, &b)
	if err != nil {
		return nil, fmt.Errorf("加密令牌失败: %w", err)
	}
	k := &store.Key{
		UserID: userID, Name: name, ValueEnc: enc,
		Kind: store.KeyKindChatGPT, Status: 1, CreatedAt: time.Now().Unix(),
	}
	if err := m.store.DB().Create(k).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			// 自动命名撞名：追加随机后缀重试一次（用户显式命名撞名则如实报错）
			k.Name = fmt.Sprintf("%s-%04x", name, time.Now().UnixNano()&0xffff)
			if err := m.store.DB().Create(k).Error; err != nil {
				return nil, fmt.Errorf("创建密钥失败: %w", err)
			}
			return k, nil
		}
		return nil, fmt.Errorf("创建密钥失败: %w", err)
	}
	return k, nil
}

// persist 刷新后落库新令牌
func (m *Manager) persist(k *store.Key, b Bundle) error {
	enc, err := EncryptBundle(m.secret, &b)
	if err != nil {
		return fmt.Errorf("加密令牌失败: %w", err)
	}
	return m.store.DB().Model(&store.Key{}).Where("id = ?", k.ID).Update("value_enc", enc).Error
}

func (m *Manager) markKeyError(keyID int64, msg string) {
	m.store.DB().Model(&store.Key{}).Where("id = ?", keyID).Update("last_error", msg)
}

func (m *Manager) clearKeyError(keyID int64) {
	m.store.DB().Model(&store.Key{}).Where("id = ?", keyID).Update("last_error", nil)
}

// ---------- 工具 ----------

// defaultName 自动命名：chatgpt-<account_id 后 6 位>
func defaultName(accountID string) string {
	if len(accountID) > 6 {
		accountID = accountID[len(accountID)-6:]
	}
	return "chatgpt-" + accountID
}

// randomToken n 字节随机数的 base64url（无 padding）编码
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// gcLocked 清理过期待完成会话（调用方持 m.mu）
func (m *Manager) gcLocked() {
	now := time.Now()
	for s, p := range m.pending {
		if now.After(p.ExpiresAt) {
			delete(m.pending, s)
		}
	}
}
