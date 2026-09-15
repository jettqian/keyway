package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// 飞书 OAuth（PRD FR-A5，DESIGN §10）：
// 授权码流程 → user_access_token → user_info(open_id) → 建会话/自动建号

const feishuStateTTL = 5 * time.Minute

// FeishuConfig 飞书登录配置（settings 表）
type FeishuConfig struct {
	Enabled   bool
	AppID     string
	AppSecret string
	BaseURL   string // 默认 https://open.feishu.cn（测试可覆盖）
}

func (s *Service) feishuConfig() FeishuConfig {
	cfg := FeishuConfig{BaseURL: "https://open.feishu.cn"}
	if v, _ := s.store.GetSetting("feishu_enabled"); v == "1" {
		cfg.Enabled = true
	}
	if v, _ := s.store.GetSetting("feishu_app_id"); v != "" {
		cfg.AppID = v
	}
	if v, _ := s.store.GetSetting("feishu_base_url"); v != "" {
		cfg.BaseURL = strings.TrimSuffix(v, "/")
	}
	if enc, _ := s.store.GetSetting("feishu_app_secret"); enc != "" {
		if b, err := crypto.Decrypt(s.secret, "setting", []byte(enc)); err == nil {
			cfg.AppSecret = string(b)
		}
	}
	return cfg
}

// SaveFeishuSecret 管理端保存加密后的 App Secret
func (s *Service) SaveFeishuSecret(secret string) error {
	enc, err := crypto.Encrypt(s.secret, "setting", []byte(secret))
	if err != nil {
		return err
	}
	return s.store.SetSetting("feishu_app_secret", string(enc))
}

// FeishuAuthorizeURL 构造授权跳转地址（state = 过期时间|随机数|HMAC）
func (s *Service) FeishuAuthorizeURL(baseURL string) (string, error) {
	cfg := s.feishuConfig()
	if !cfg.Enabled || cfg.AppID == "" || cfg.AppSecret == "" {
		return "", fmt.Errorf("飞书登录未启用或未配置")
	}
	if baseURL == "" {
		return "", fmt.Errorf("缺少对外访问地址（KEYWAY_BASE_URL）")
	}
	state := s.signState()
	q := url.Values{}
	q.Set("app_id", cfg.AppID)
	q.Set("redirect_uri", baseURL+"/oauth/feishu/callback")
	q.Set("state", state)
	return cfg.BaseURL + "/open-apis/authen/v1/authorize?" + q.Encode(), nil
}

func (s *Service) signState() string {
	buf := make([]byte, 8)
	rand.Read(buf)
	nonce := hex.EncodeToString(buf)
	exp := time.Now().Add(feishuStateTTL).Unix()
	payload := fmt.Sprintf("%d|%s", exp, nonce)
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyState(state string) bool {
	parts := strings.Split(state, "|")
	if len(parts) != 3 {
		return false
	}
	var exp int64
	fmt.Sscanf(parts[0], "%d", &exp)
	if time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(parts[0] + "|" + parts[1]))
	return hmac.Equal([]byte(parts[2]), []byte(hex.EncodeToString(mac.Sum(nil))))
}

// VerifyFeishuState 暴露给 API 层校验回调 state
func (s *Service) VerifyFeishuState(state string) bool { return s.verifyState(state) }

// FeishuCallback 授权码换用户：已绑定直接建会话；未绑定且注册开放则自动建号
func (s *Service) FeishuCallback(code, baseURL string) (*store.User, string, error) {
	cfg := s.feishuConfig()
	if !cfg.Enabled || cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, "", fmt.Errorf("飞书登录未启用")
	}
	openID, name, err := s.feishuFetchUser(cfg, code, baseURL)
	if err != nil {
		return nil, "", err
	}
	var u store.User
	if err := s.store.DB().Where("feishu_user_id = ?", openID).First(&u).Error; err == nil {
		if u.Status != 1 {
			return nil, "", ErrUserDisabled
		}
		s.healFeishuUsername(&u, name)
		token, err := s.CreateSession(u.ID)
		return &u, token, err
	}
	// 未绑定：注册策略放行才自动建号
	mode, _ := s.store.GetSetting("register_mode")
	if mode != "" && mode != "open" {
		return nil, "", fmt.Errorf("当前注册策略不允许新用户通过飞书登录")
	}
	username := feishuUsername(name)
	if s.usernameTaken(username) {
		username = username + "-" + randSuffix()
	}
	u = store.User{
		Username:     username,
		FeishuUserID: &openID,
		Role:         1,
		Status:       1,
		CreatedAt:    time.Now().Unix(),
	}
	if err := s.store.DB().Create(&u).Error; err != nil {
		return nil, "", fmt.Errorf("自动建号失败: %w", err)
	}
	token, err := s.CreateSession(u.ID)
	return &u, token, err
}

// feishuFetchUser 授权码 → app_access_token → user_access_token → user_info
func (s *Service) feishuFetchUser(cfg FeishuConfig, code, baseURL string) (openID, name string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}

	// 1. user_access_token（v2 授权码换令牌）
	tokenBody, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     cfg.AppID,
		"client_secret": cfg.AppSecret,
		"code":          code,
		"redirect_uri":  baseURL + "/oauth/feishu/callback",
	})
	resp, err := client.Post(cfg.BaseURL+"/open-apis/authen/v2/oauth/token",
		"application/json", strings.NewReader(string(tokenBody)))
	if err != nil {
		return "", "", fmt.Errorf("飞书令牌接口不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tokenResp struct {
		Code        int    `json:"code"`
		Msg         string `json:"msg"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &tokenResp); err != nil {
		return "", "", fmt.Errorf("飞书令牌响应异常: %s", shortStr(string(raw)))
	}
	if tokenResp.Code != 0 || tokenResp.AccessToken == "" {
		return "", "", fmt.Errorf("飞书令牌获取失败: code=%d msg=%s", tokenResp.Code, tokenResp.Msg)
	}

	// 2. user_info
	req, _ := http.NewRequest(http.MethodGet, cfg.BaseURL+"/open-apis/authen/v1/user_info", nil)
	req.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	resp2, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("飞书用户信息接口不可达: %w", err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	var infoResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Name   string `json:"name"`
			OpenID string `json:"open_id"`
		} `json:"data"`
		// 兼容无 data 包裹的返回形态
		Name   string `json:"name"`
		OpenID string `json:"open_id"`
	}
	if err := json.Unmarshal(raw2, &infoResp); err != nil {
		return "", "", fmt.Errorf("飞书用户信息响应异常: %s", shortStr(string(raw2)))
	}
	if infoResp.Code != 0 {
		return "", "", fmt.Errorf("飞书用户信息失败: code=%d msg=%s", infoResp.Code, infoResp.Msg)
	}
	name, openID = infoResp.Data.Name, infoResp.Data.OpenID
	if openID == "" {
		name, openID = infoResp.Name, infoResp.OpenID
	}
	if openID == "" {
		return "", "", fmt.Errorf("飞书未返回 open_id")
	}
	return openID, name, nil
}

func (s *Service) usernameTaken(username string) bool {
	var count int64
	s.store.DB().Model(&store.User{}).Where("username = ?", username).Count(&count)
	return count > 0
}

// feishuUsername 飞书昵称直接作用户名（保留中文等 Unicode），截断到 32 字符；
// 昵称为空时回退占位名，由重名后缀逻辑保证唯一
func feishuUsername(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "feishu-user"
	}
	r := []rune(name)
	if len(r) > 32 {
		return string(r[:32])
	}
	return name
}

// healFeishuUsername 历史版本把非 ASCII 昵称清洗成空、回退占位名 feishu-user；
// 已绑定用户再次登录时若仍是占位名，则自愈为当前飞书昵称（被占用则保持不变）
func (s *Service) healFeishuUsername(u *store.User, name string) {
	if u.Username != "feishu-user" {
		return
	}
	want := feishuUsername(name)
	if want == "feishu-user" || s.usernameTaken(want) {
		return
	}
	if err := s.store.DB().Model(&store.User{}).Where("id = ?", u.ID).
		Update("username", want).Error; err == nil {
		u.Username = want
	}
}

func randSuffix() string {
	buf := make([]byte, 3)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func shortStr(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
