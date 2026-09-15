package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

const sessionTTL = 7 * 24 * time.Hour

var (
	ErrInvalidCredential = errors.New("用户名或密码错误")
	ErrUserDisabled      = errors.New("账号已被禁用")
	ErrRegisterClosed    = errors.New("注册已关闭")
	ErrInvalidInvite     = errors.New("邀请码无效或已被使用")
	ErrUnauthorized      = errors.New("未登录或会话已过期")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)

type Service struct {
	store      *store.Store
	secret     string
	registerMu sync.Mutex
}

func New(st *store.Store, secret string) *Service {
	return &Service{store: st, secret: secret}
}

// ---------- Web 会话 ----------

// CreateSession 为用户创建会话，返回原始 token（写入 Cookie）
func (s *Service) CreateSession(userID int64) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话失败: %w", err)
	}
	token := hex.EncodeToString(buf)
	sess := store.Session{
		TokenHash: crypto.HashToken(token),
		UserID:    userID,
		ExpiresAt: time.Now().Add(sessionTTL).Unix(),
	}
	if err := s.store.DB().Create(&sess).Error; err != nil {
		return "", fmt.Errorf("保存会话失败: %w", err)
	}
	return token, nil
}

// UserFromSession 校验会话并返回用户
func (s *Service) UserFromSession(token string) (*store.User, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	var sess store.Session
	if err := s.store.DB().Where("token_hash = ?", crypto.HashToken(token)).First(&sess).Error; err != nil {
		return nil, ErrUnauthorized
	}
	if sess.ExpiresAt < time.Now().Unix() {
		s.store.DB().Delete(&sess)
		return nil, ErrUnauthorized
	}
	var u store.User
	if err := s.store.DB().First(&u, sess.UserID).Error; err != nil {
		return nil, ErrUnauthorized
	}
	if u.Status != 1 {
		return nil, ErrUserDisabled
	}
	return &u, nil
}

func (s *Service) Logout(token string) {
	if token == "" {
		return
	}
	s.store.DB().Where("token_hash = ?", crypto.HashToken(token)).Delete(&store.Session{})
}

func (s *Service) DeleteUserSessions(userID int64) {
	s.store.DB().Where("user_id = ?", userID).Delete(&store.Session{})
}

// ---------- 注册与登录 ----------

func (s *Service) Register(username, password, inviteCode string) (*store.User, string, error) {
	// 注册涉及邀请码消费和首个管理员引导，必须在单进程内串行化。
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	if !usernameRe.MatchString(username) {
		return nil, "", fmt.Errorf("用户名需为 3-32 位字母/数字/下划线/横线")
	}
	if len(password) < 8 {
		return nil, "", fmt.Errorf("密码至少 8 位")
	}
	mode, _ := s.store.GetSetting("register_mode")
	if mode == "" {
		mode = "open"
	}
	if mode == "closed" {
		return nil, "", ErrRegisterClosed
	}

	var invite *store.InviteCode
	if mode == "invite" {
		if inviteCode == "" {
			return nil, "", ErrInvalidInvite
		}
		invite = &store.InviteCode{}
		if err := s.store.DB().Where("code = ? AND used_by IS NULL", inviteCode).First(invite).Error; err != nil {
			return nil, "", ErrInvalidInvite
		}
	}

	hash, err := crypto.HashPassword(password)
	if err != nil {
		return nil, "", err
	}
	u := store.User{
		Username:     username,
		PasswordHash: &hash,
		Role:         1,
		Status:       1,
		CreatedAt:    time.Now().Unix(),
	}
	if err := s.store.DB().Transaction(func(tx *gorm.DB) error {
		// 首个用户自动成为管理员（引导）；注册锁与事务共同避免并发重复提升。
		var count int64
		if err := tx.Model(&store.User{}).Count(&count).Error; err != nil {
			return fmt.Errorf("查询用户数量失败: %w", err)
		}
		if count == 0 {
			u.Role = 100
		}
		if err := tx.Create(&u).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("用户名已存在")
			}
			return fmt.Errorf("创建用户失败: %w", err)
		}
		if invite != nil {
			now := time.Now().Unix()
			res := tx.Model(&store.InviteCode{}).
				Where("code = ? AND used_by IS NULL", invite.Code).
				Updates(map[string]any{"used_by": u.ID, "used_at": now})
			if res.Error != nil {
				return fmt.Errorf("消费邀请码失败: %w", res.Error)
			}
			if res.RowsAffected != 1 {
				return ErrInvalidInvite
			}
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	token, err := s.CreateSession(u.ID)
	if err != nil {
		return nil, "", err
	}
	return &u, token, nil
}

func (s *Service) Login(username, password string) (*store.User, string, error) {
	var u store.User
	if err := s.store.DB().Where("username = ?", username).First(&u).Error; err != nil {
		return nil, "", ErrInvalidCredential
	}
	if u.Status != 1 {
		return nil, "", ErrUserDisabled
	}
	if u.PasswordHash == nil {
		return nil, "", fmt.Errorf("该账号未设置密码，请使用飞书登录")
	}
	if err := crypto.CheckPassword(*u.PasswordHash, password); err != nil {
		return nil, "", ErrInvalidCredential
	}
	now := time.Now().Unix()
	s.store.DB().Model(&u).Update("last_login_at", now)
	token, err := s.CreateSession(u.ID)
	if err != nil {
		return nil, "", err
	}
	return &u, token, nil
}

func (s *Service) ChangePassword(userID int64, oldPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("新密码至少 8 位")
	}
	var u store.User
	if err := s.store.DB().First(&u, userID).Error; err != nil {
		return ErrInvalidCredential
	}
	if u.PasswordHash != nil {
		if err := crypto.CheckPassword(*u.PasswordHash, oldPassword); err != nil {
			return ErrInvalidCredential
		}
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.DB().Model(&u).Update("password_hash", hash).Error; err != nil {
		return err
	}
	s.DeleteUserSessions(userID)
	return nil
}

// ---------- 网关令牌认证（/v1）----------

// AuthenticateGatewayToken 校验 sk-keyway- 令牌，返回令牌与所属用户
func (s *Service) AuthenticateGatewayToken(raw string) (*store.Token, *store.User, error) {
	if raw == "" {
		return nil, nil, ErrUnauthorized
	}
	var tok store.Token
	if err := s.store.DB().Where("key_hash = ?", crypto.HashToken(raw)).First(&tok).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrUnauthorized
		}
		return nil, nil, fmt.Errorf("查询令牌失败: %w", err)
	}
	if tok.Revoked == 1 {
		return nil, nil, ErrUnauthorized
	}
	if tok.ExpiresAt != nil && *tok.ExpiresAt < time.Now().Unix() {
		return nil, nil, ErrUnauthorized
	}
	var u store.User
	if err := s.store.DB().First(&u, tok.UserID).Error; err != nil {
		return nil, nil, ErrUnauthorized
	}
	if u.Status != 1 {
		return nil, nil, ErrUserDisabled
	}
	return &tok, &u, nil
}
