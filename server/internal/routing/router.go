package routing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// ResolvedChannel 路由解析结果：渠道 + 有序密钥行 + 线路 + 解密后的个人代理
type ResolvedChannel struct {
	Channel          *store.Channel
	Keys             []*store.Key // 按绑定顺序
	BaseURLs         []string
	PersonalProxyURL string // 解密后；空串表示未配置
}

// Service 路由服务
type Service struct {
	store  *store.Store
	secret string
}

// New 构造路由服务
func New(st *store.Store, secret string) *Service {
	return &Service{store: st, secret: secret}
}

// DecodeKeyValue 解密某把密钥的明文值
func DecodeKeyValue(secret string, k *store.Key) (string, error) {
	b, err := crypto.Decrypt(secret, "key", k.ValueEnc)
	if err != nil {
		return "", fmt.Errorf("解密密钥失败: %w", err)
	}
	return string(b), nil
}

// Resolve 按模型名解析用户可用渠道（priority 降序）；
// channelID 非空时强制限定该渠道；modelScope 非空时按前缀通配过滤。
// 无命中时回落默认渠道（模型名透传）；仍无则返回空列表。
func (s *Service) Resolve(userID int64, model string, channelID *int64, modelScope string) ([]*ResolvedChannel, []*ResolvedChannel, error) {
	var chans []store.Channel
	q := s.store.DB().Where("user_id = ? AND enabled = 1", userID)
	if channelID != nil {
		q = q.Where("id = ?", *channelID)
	}
	if err := q.Find(&chans).Error; err != nil {
		return nil, nil, fmt.Errorf("查询渠道失败: %w", err)
	}

	var matched, defaults []*ResolvedChannel
	for i := range chans {
		ch := &chans[i]
		if modelScope != "" && !matchScope(modelScope, model) {
			continue
		}
		rc, err := s.buildResolved(ch)
		if err != nil {
			continue
		}
		var models []string
		json.Unmarshal([]byte(ch.ModelsJSON), &models)
		for _, m := range models {
			if m == model {
				matched = append(matched, rc)
				break
			}
		}
		if ch.IsDefault == 1 {
			defaults = append(defaults, rc)
		}
	}

	// priority 降序稳定排序
	sortByPriority(matched)
	return matched, defaults, nil
}

// AllEnabledModels 用户所有启用渠道模型名并集（/v1/models 用）
func (s *Service) AllEnabledModels(userID int64) ([]string, error) {
	var chans []store.Channel
	if err := s.store.DB().Where("user_id = ? AND enabled = 1", userID).Find(&chans).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for i := range chans {
		var models []string
		json.Unmarshal([]byte(chans[i].ModelsJSON), &models)
		for _, m := range models {
			if m != "" && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// ForChannel 构造指定渠道的解析结果（探测/测试用）
func (s *Service) ForChannel(ch *store.Channel) (*ResolvedChannel, error) {
	return s.buildResolved(ch)
}

func (s *Service) buildResolved(ch *store.Channel) (*ResolvedChannel, error) {
	var urls []string
	if err := json.Unmarshal([]byte(ch.BaseURLsJSON), &urls); err != nil || len(urls) == 0 {
		return nil, fmt.Errorf("渠道 %d 线路配置无效", ch.ID)
	}
	var keyIDs []int64
	json.Unmarshal([]byte(ch.KeyIDsJSON), &keyIDs)

	rc := &ResolvedChannel{Channel: ch, BaseURLs: urls}
	if len(keyIDs) > 0 {
		var keys []store.Key
		if err := s.store.DB().Where("user_id = ? AND status = 1", ch.UserID).Find(&keys).Error; err == nil {
			byID := map[int64]*store.Key{}
			for i := range keys {
				byID[keys[i].ID] = &keys[i]
			}
			for _, id := range keyIDs {
				if k, ok := byID[id]; ok {
					rc.Keys = append(rc.Keys, k)
				}
			}
		}
	}
	if len(rc.Keys) == 0 {
		return nil, fmt.Errorf("渠道 %d 无可用密钥", ch.ID)
	}
	if ch.ProxyURLEnc != nil {
		if b, err := crypto.Decrypt(s.secret, "proxy", ch.ProxyURLEnc); err == nil {
			rc.PersonalProxyURL = string(b)
		}
	}
	return rc, nil
}

// UpdateKeyCooldown 429 后设置密钥冷却
func (s *Service) UpdateKeyCooldown(keyID int64, seconds int64) {
	if seconds < 1 {
		seconds = 60
	}
	s.store.DB().Model(&store.Key{}).Where("id = ?", keyID).
		Update("cooldown_until", time.Now().Unix()+seconds)
}

// MarkKeyError 记录密钥最近错误
func (s *Service) MarkKeyError(keyID int64, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	s.store.DB().Model(&store.Key{}).Where("id = ?", keyID).Update("last_error", msg)
}

// MarkChannelStatus 更新渠道最近成功/错误状态
func (s *Service) MarkChannelStatus(channelID int64, ok bool, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	now := time.Now().Unix()
	updates := map[string]any{}
	if ok {
		updates["last_ok_at"] = now
		updates["last_error"] = nil
	} else {
		updates["last_error"] = msg
	}
	s.store.DB().Model(&store.Channel{}).Where("id = ?", channelID).Updates(updates)
}

// matchScope 模型前缀通配："claude-*" 前缀匹配，无 * 精确匹配，空串放行
func matchScope(scope, model string) bool {
	if scope == "" {
		return true
	}
	if strings.HasSuffix(scope, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(scope, "*"))
	}
	return scope == model
}

func sortByPriority(list []*ResolvedChannel) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Channel.Priority > list[j-1].Channel.Priority; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
