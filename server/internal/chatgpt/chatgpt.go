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
	"context"
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
	// BackendHosts ChatGPT 后端允许的主机名：chatgpt 密钥的令牌只允许发往这些主机
	// （渠道绑定校验 + 出站兜底，v1.5.82）；测试可覆盖为本地 httptest 主机
	BackendHosts = []string{"chatgpt.com"}
)

const (
	oauthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	redirectURI   = "http://localhost:1455/auth/callback"
	oauthScope    = "openid profile email offline_access"

	// refreshMargin 剩余有效期不足该值即触发刷新
	refreshMargin = 5 * time.Minute
	// pendingTTL 待完成登录会话有效期（粘贴回调地址的人工窗口）
	pendingTTL = 10 * time.Minute
	// maxPendingPerUser 单用户待完成登录会话上限（超出淘汰最早的，防内存表被刷满）
	maxPendingPerUser = 5
	// tokenHTTPTimeout 令牌交换/刷新请求超时
	tokenHTTPTimeout = 30 * time.Second
	// refreshRetryBackoff 刷新暂时失败（网络/令牌端点 5xx）后的退避：期内直接返回
	// 上次错误，不再发起网络请求——否则令牌端点不可达时每个请求都要卡满超时
	refreshRetryBackoff = time.Minute
	// refreshDeadBackoff 令牌端点明确拒绝（4xx，如 invalid_grant）后的退避：
	// refresh_token 已失效，只能重新登录，长退避避免反复打令牌端点
	refreshDeadBackoff = time.Hour
)

// ErrNotBackendURL chatgpt 令牌被要求发往非 ChatGPT 后端主机（拒绝发送，防令牌外泄）
var ErrNotBackendURL = errors.New("ChatGPT 账号令牌只允许发往 ChatGPT 后端")

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
	refreshes map[int64]*refreshCall   // 按密钥 ID singleflight
	failures  map[int64]refreshFailure // 按密钥 ID 的刷新失败退避
}

type pendingLogin struct {
	Verifier  string
	UserID    int64
	ExpiresAt time.Time
}

// refreshCall 一次进行中的刷新；done 关闭后 bundle/err 可读
type refreshCall struct {
	done   chan struct{}
	bundle Bundle
	err    error
}

type refreshFailure struct {
	until time.Time
	err   error
}

// NewManager 构造管理器
func NewManager(st *store.Store, secret string) *Manager {
	return &Manager{
		store:     st,
		secret:    secret,
		client:    &http.Client{Timeout: tokenHTTPTimeout},
		pending:   map[string]pendingLogin{},
		refreshes: map[int64]*refreshCall{},
		failures:  map[int64]refreshFailure{},
	}
}

// ---------- 后端识别与请求规范化 ----------

// IsBackendURL 地址是否指向 ChatGPT 后端（https + BackendHosts 内的主机名；
// 测试覆盖的本地主机允许 http）
func IsBackendURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range BackendHosts {
		if host == h {
			return u.Scheme == "https" || host == "127.0.0.1" || host == "localhost"
		}
	}
	return false
}

// PrepareRequest ChatGPT 后端的 Responses 请求体规范化（就地修改）：
//   - 缺省 store 时补 store:false（后端要求存储关闭；Codex 客户端本就携带）；
//   - input 为字符串时展开为标准 message 数组（后端只收数组，官方 API 两种都收）
func PrepareRequest(m map[string]any) {
	if _, ok := m["store"]; !ok {
		m["store"] = false
	}
	if in, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": in,
			}},
		}}
	}
}

// ApplyHeaders 出站 ChatGPT 后端的必需请求头：chatgpt-account-id 恒设；Codex
// 协议头（OpenAI-Beta、originator）仅缺省时补齐——客户端已带则透传原值，不改写
// 客户端已有的任何头（非 Codex 客户端直连后端的功能必需项，不做指纹伪装）
func ApplyHeaders(h http.Header, accountID string) {
	h.Set("chatgpt-account-id", accountID)
	if h.Get("OpenAI-Beta") == "" {
		h.Set("OpenAI-Beta", "responses=experimental")
	}
	if h.Get("originator") == "" {
		h.Set("originator", "codex_cli_rs")
	}
}

// UsageLimitReset 解析 429 错误体里的订阅额度重置时间（秒）。
// ok=true 表示这是订阅额度耗尽（error.type=usage_limit_reached）；
// seconds 优先取 resets_in_seconds，其次 resets_at（unix 秒）换算；都缺省时为 0，
// 由调用方回落长冷却
func UsageLimitReset(body []byte) (seconds int64, ok bool) {
	var e struct {
		Error struct {
			Type            string `json:"type"`
			ResetsInSeconds int64  `json:"resets_in_seconds"`
			ResetsAt        int64  `json:"resets_at"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Type != "usage_limit_reached" {
		return 0, false
	}
	if e.Error.ResetsInSeconds > 0 {
		return e.Error.ResetsInSeconds, true
	}
	if d := e.Error.ResetsAt - time.Now().Unix(); e.Error.ResetsAt > 0 && d > 0 {
		return d, true
	}
	return 0, true
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
	m.capUserPendingLocked(userID)
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
func (m *Manager) CompleteLogin(ctx context.Context, userID int64, callbackURL, name string) (*store.Key, AccountInfo, error) {
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

	tok, err := m.exchangeToken(ctx, m.client, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {oauthClientID},
		"code_verifier": {p.Verifier},
	})
	if err != nil {
		return nil, AccountInfo{}, fmt.Errorf("换取令牌失败: %w", err)
	}
	bundle, err := bundleFromToken(tok)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	k, err := m.createKey(userID, name, bundle)
	if err != nil {
		return nil, AccountInfo{}, err
	}
	info := AccountInfo{AccountID: bundle.AccountID, Plan: bundle.Plan, Email: bundle.Email, ExpiresAt: bundle.ExpiresAt}
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
// 剩余有效期不足 refreshMargin 时自动刷新并落库（按密钥 singleflight）。
// client 为刷新请求使用的 HTTP 客户端（转发传入渠道当前路径的代理客户端，
// nil = 默认客户端，尊重 HTTPS_PROXY）；ctx 只约束本次等待——刷新本身在后台
// 跑完并落库，不随请求取消中断（中断可能丢失已被服务端轮换的 refresh_token）
func (m *Manager) EnsureFresh(ctx context.Context, k *store.Key, client *http.Client) (string, string, error) {
	b, err := DecryptBundle(m.secret, k.ValueEnc)
	if err != nil {
		return "", "", err
	}
	if time.Until(time.Unix(b.ExpiresAt, 0)) > refreshMargin {
		return b.AccessToken, b.AccountID, nil
	}
	nb, err := m.refresh(ctx, k.ID, client, false)
	if err != nil {
		return "", "", err
	}
	return nb.AccessToken, nb.AccountID, nil
}

// RefreshNow 强制刷新（控制台手动刷新端点用；无视失败退避）
func (m *Manager) RefreshNow(ctx context.Context, k *store.Key) error {
	_, err := m.refresh(ctx, k.ID, nil, true)
	return err
}

// refresh 带并发收敛与失败退避的令牌刷新：
//   - 退避期内（非强制）直接返回上次失败，不发网络请求；
//   - 同一密钥并发调用共享一次刷新（singleflight），刷新在后台 goroutine 执行；
//   - 调用方 ctx 取消只放弃等待，不中断刷新
func (m *Manager) refresh(ctx context.Context, keyID int64, client *http.Client, force bool) (Bundle, error) {
	m.refreshMu.Lock()
	if f, ok := m.failures[keyID]; ok && !force && time.Now().Before(f.until) {
		m.refreshMu.Unlock()
		return Bundle{}, f.err
	}
	call, ok := m.refreshes[keyID]
	if !ok {
		call = &refreshCall{done: make(chan struct{})}
		m.refreshes[keyID] = call
		go m.runRefresh(keyID, client, force, call)
	}
	m.refreshMu.Unlock()

	select {
	case <-call.done:
		return call.bundle, call.err
	case <-ctx.Done():
		return Bundle{}, fmt.Errorf("等待令牌刷新被取消: %w", ctx.Err())
	}
}

func (m *Manager) runRefresh(keyID int64, client *http.Client, force bool, call *refreshCall) {
	b, err := m.doRefresh(keyID, client, force)
	m.refreshMu.Lock()
	delete(m.refreshes, keyID)
	if err != nil {
		backoff := refreshRetryBackoff
		var te *tokenEndpointError
		if errors.As(err, &te) && te.Status >= 400 && te.Status < 500 {
			backoff = refreshDeadBackoff
		}
		until := time.Now().Add(backoff)
		m.failures[keyID] = refreshFailure{until: until, err: err}
		// 密钥同步冷却：转发组合把它排到可用密钥之后
		m.store.DB().Model(&store.Key{}).Where("id = ? AND cooldown_until < ?", keyID, until.Unix()).
			Update("cooldown_until", until.Unix())
	} else if f, ok := m.failures[keyID]; ok {
		delete(m.failures, keyID)
		// 撤销刷新失败设下的冷却（更晚到期的冷却来自 429/402，保留）
		m.store.DB().Model(&store.Key{}).Where("id = ? AND cooldown_until <= ?", keyID, f.until.Unix()).
			Update("cooldown_until", 0)
	}
	m.refreshMu.Unlock()
	call.bundle, call.err = b, err
	close(call.done)
}

// doRefresh 以库中**最新**令牌刷新：调用方持有的密钥行可能早于上一次刷新的落库
// （请求开始时读出、刷新完成后才走到这里），用其中已被轮换掉的 refresh_token
// 刷新会触发服务端复用检测、整组令牌作废。非强制刷新时若库中令牌已是新的，
// 直接返回而不再刷新
func (m *Manager) doRefresh(keyID int64, client *http.Client, force bool) (Bundle, error) {
	var k store.Key
	if err := m.store.DB().First(&k, keyID).Error; err != nil {
		return Bundle{}, fmt.Errorf("读取密钥失败: %w", err)
	}
	b, err := DecryptBundle(m.secret, k.ValueEnc)
	if err != nil {
		return Bundle{}, err
	}
	if !force && time.Until(time.Unix(b.ExpiresAt, 0)) > refreshMargin {
		return *b, nil // 其他请求已刷新落库
	}
	if b.RefreshToken == "" {
		return Bundle{}, errors.New("缺少 refresh_token，请在控制台重新登录 ChatGPT 账号")
	}
	if client == nil {
		client = m.client
	}
	ctx, cancel := context.WithTimeout(context.Background(), tokenHTTPTimeout)
	defer cancel()
	tok, err := m.exchangeToken(ctx, client, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
		"client_id":     {oauthClientID},
	})
	if err != nil {
		msg := "令牌刷新失败: " + err.Error()
		var te *tokenEndpointError
		if errors.As(err, &te) && te.Status >= 400 && te.Status < 500 {
			msg += "（refresh_token 已失效，请删除后重新登录 ChatGPT 账号）"
		}
		m.markKeyError(keyID, msg)
		return Bundle{}, fmt.Errorf("令牌刷新失败: %w", err)
	}
	nb := *b // 刷新响应可能不含 refresh_token（未轮转），保留旧值
	if tok.RefreshToken != "" {
		nb.RefreshToken = tok.RefreshToken
	}
	nb.AccessToken = tok.AccessToken
	nb.ExpiresAt = expiryOf(tok)
	if email := emailOf(tok); email != "" {
		nb.Email = email
	}
	if err := m.persist(keyID, nb); err != nil {
		return Bundle{}, err
	}
	m.clearKeyError(keyID)
	return nb, nil
}

// ---------- 令牌交换与 JWT 解析 ----------

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// tokenEndpointError 令牌端点返回的非 200 响应（4xx = 授权本身失效，需重登）
type tokenEndpointError struct {
	Status      int
	Code        string
	Description string
}

func (e *tokenEndpointError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("令牌端点返回 %d: %s（%s）", e.Status, e.Code, e.Description)
	}
	return fmt.Sprintf("令牌端点返回 %d", e.Status)
}

func (m *Manager) exchangeToken(ctx context.Context, client *http.Client, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("构造令牌请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求令牌端点失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		te := &tokenEndpointError{Status: resp.StatusCode}
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) == nil {
			te.Code, te.Description = e.Error, e.Description
		}
		return nil, te
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

// bundleFromToken 从登录令牌响应构造存储 Bundle；
// JWT 仅解 payload 提取 claim，不验签——签名校验由 OpenAI 后端在每次调用时完成，
// 网关只提取展示与转发所需字段
func bundleFromToken(tok *tokenResponse) (Bundle, error) {
	claims, err := parseJWTClaims(tok.AccessToken)
	if err != nil {
		return Bundle{}, fmt.Errorf("解析 access_token 失败: %w", err)
	}
	b := Bundle{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    expiryOf(tok),
		Email:        emailOf(tok),
	}
	if claims.Auth != nil {
		b.AccountID = claims.Auth.AccountID
		b.Plan = claims.Auth.PlanType
	}
	if b.AccountID == "" {
		return Bundle{}, errors.New("令牌缺少 chatgpt_account_id（非 ChatGPT 账号令牌？）")
	}
	return b, nil
}

// expiryOf access_token 到期时刻：JWT exp 优先，其次 expires_in，都缺省兜底 1h
// （缺省到期 = 当前时刻会让此后每个请求都触发刷新）
func expiryOf(tok *tokenResponse) int64 {
	if c, err := parseJWTClaims(tok.AccessToken); err == nil && c.Exp > 0 {
		return c.Exp
	}
	if tok.ExpiresIn > 0 {
		return time.Now().Unix() + tok.ExpiresIn
	}
	return time.Now().Add(time.Hour).Unix()
}

// emailOf 账号邮箱：access_token 顶层 email 或 profile claim，其次 id_token 的 email
func emailOf(tok *tokenResponse) string {
	if c, err := parseJWTClaims(tok.AccessToken); err == nil {
		if c.Email != "" {
			return c.Email
		}
		if c.Profile != nil && c.Profile.Email != "" {
			return c.Profile.Email
		}
	}
	if tok.IDToken != "" {
		if c, err := parseJWTClaims(tok.IDToken); err == nil {
			return c.Email
		}
	}
	return ""
}

type jwtClaims struct {
	Email   string      `json:"email"`
	Exp     int64       `json:"exp"`
	Auth    *jwtAuth    `json:"https://api.openai.com/auth"`
	Profile *jwtProfile `json:"https://api.openai.com/profile"`
}

type jwtAuth struct {
	AccountID string `json:"chatgpt_account_id"`
	PlanType  string `json:"chatgpt_plan_type"`
}

type jwtProfile struct {
	Email string `json:"email"`
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

// createKey 落 kind=chatgpt 密钥。name 为空时自动命名，撞名追加随机后缀重试一次；
// 用户显式命名撞名如实报错
func (m *Manager) createKey(userID int64, name string, b Bundle) (*store.Key, error) {
	auto := name == ""
	if auto {
		name = defaultName(b.AccountID)
	}
	enc, err := EncryptBundle(m.secret, &b)
	if err != nil {
		return nil, fmt.Errorf("加密令牌失败: %w", err)
	}
	k := &store.Key{
		UserID: userID, Name: name, ValueEnc: enc,
		Kind: store.KeyKindChatGPT, Status: 1, CreatedAt: time.Now().Unix(),
	}
	err = m.store.DB().Create(k).Error
	if err != nil && auto && strings.Contains(err.Error(), "UNIQUE") {
		k.ID = 0
		k.Name = fmt.Sprintf("%s-%04x", name, time.Now().UnixNano()&0xffff)
		err = m.store.DB().Create(k).Error
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("同名密钥已存在：%s", name)
		}
		return nil, fmt.Errorf("创建密钥失败: %w", err)
	}
	return k, nil
}

// persist 刷新后落库新令牌
func (m *Manager) persist(keyID int64, b Bundle) error {
	enc, err := EncryptBundle(m.secret, &b)
	if err != nil {
		return fmt.Errorf("加密令牌失败: %w", err)
	}
	if err := m.store.DB().Model(&store.Key{}).Where("id = ?", keyID).Update("value_enc", enc).Error; err != nil {
		return fmt.Errorf("刷新后的令牌落库失败: %w", err)
	}
	return nil
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

// capUserPendingLocked 用户待完成会话达上限时淘汰最早发起的（调用方持 m.mu）
func (m *Manager) capUserPendingLocked(userID int64) {
	for {
		n, oldest := 0, ""
		var oldestAt time.Time
		for s, p := range m.pending {
			if p.UserID != userID {
				continue
			}
			n++
			if oldest == "" || p.ExpiresAt.Before(oldestAt) {
				oldest, oldestAt = s, p.ExpiresAt
			}
		}
		if n < maxPendingPerUser {
			return
		}
		delete(m.pending, oldest)
	}
}
