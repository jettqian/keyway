package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/crypto"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// generatePassword 生成一次性重置密码
func generatePassword() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashPassword(p string) (string, error) {
	return crypto.HashPassword(p)
}

// ---------- 日志 ----------

func (s *Server) handleLogs(c *gin.Context) {
	u := currentUser(c)
	q := usage.LogQuery{
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", 20),
	}
	if v := c.Query("start"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			q.Start = t.Unix()
		}
	}
	if v := c.Query("end"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			q.End = t.Add(24 * time.Hour).Unix()
		}
	}
	if v := c.Query("channelId"); v != "" {
		id, _ := strconv.ParseInt(v, 10, 64)
		q.ChannelID = &id
	}
	q.Model = c.Query("model")
	if v := c.Query("statusCode"); v != "" {
		code, _ := strconv.Atoi(v)
		q.StatusCode = &code
	}
	logs, total, err := usage.QueryLogs(s.Store.DB(), &u.ID, q)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	// 渠道名映射
	names := s.channelNames(u.ID)
	out := make([]gin.H, 0, len(logs))
	for i := range logs {
		l := &logs[i]
		dto := gin.H{
			"id": l.ID, "createdAt": l.CreatedAt,
			"channelId": l.ChannelID, "lineUrl": l.LineURL, "via": l.Via,
			"keyId": l.KeyID, "protocol": l.Protocol,
			"model": l.Model, "upstreamModel": l.UpstreamModel,
			"statusCode": l.StatusCode, "ttftMs": l.TtftMs, "totalMs": l.TotalMs,
			"promptTokens": l.PromptTokens, "completionTokens": l.CompletionTokens,
			"cachedTokens": l.CachedTokens, "cacheWriteTokens": l.CacheWriteTokens,
			"inputCost": l.InputCost, "outputCost": l.OutputCost,
			"error": l.Error,
		}
		if l.ChannelID != nil {
			dto["channelName"] = names[*l.ChannelID]
		}
		out = append(out, dto)
	}
	s.ok(c, gin.H{"logs": out, "total": total})
}

// ---------- 统计 ----------

func (s *Server) handleStats(c *gin.Context) {
	u := currentUser(c)
	st, err := usage.QueryStats(s.Store.DB(), &u.ID, queryInt(c, "days", 7))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.ok(c, st)
}

func (s *Server) handleAdminStats(c *gin.Context) {
	st, err := usage.QueryStats(s.Store.DB(), nil, queryInt(c, "days", 7))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.ok(c, st)
}

// ---------- 管理员：用户 ----------

func (s *Server) handleAdminUsers(c *gin.Context) {
	var users []store.User
	s.Store.DB().Order("id").Find(&users)
	out := make([]gin.H, 0, len(users))
	for i := range users {
		out = append(out, userDTO(&users[i]))
	}
	s.ok(c, gin.H{"users": out})
}

func (s *Server) handleAdminUserStatus(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Status int `json:"status"`
	}
	if err := c.BindJSON(&req); err != nil || (req.Status != 1 && req.Status != 2) {
		s.fail(c, http.StatusBadRequest, "status 须为 1（启用）或 2（禁用）")
		return
	}
	if req.Status == 2 {
		s.Auth.DeleteUserSessions(id)
	}
	res := s.Store.DB().Model(&store.User{}).Where("id = ?", id).Update("status", req.Status)
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	s.ok(c, gin.H{})
}

func (s *Server) handleAdminResetPassword(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	password, err := generatePassword()
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "生成密码失败")
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "哈希失败")
		return
	}
	res := s.Store.DB().Model(&store.User{}).Where("id = ?", id).Update("password_hash", hash)
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	s.Auth.DeleteUserSessions(id)
	s.ok(c, gin.H{"password": password})
}

// ---------- 管理员：价目表 ----------

func (s *Server) handleAdminPricing(c *gin.Context) {
	var pricing []store.ModelPricing
	s.Store.DB().Order("model").Find(&pricing)
	s.ok(c, gin.H{"pricing": pricing})
}

func (s *Server) handleAdminUpdatePricing(c *gin.Context) {
	model := c.Param("model")
	var p store.ModelPricing
	if err := c.BindJSON(&p); err != nil || p.Model == "" {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	p.Model = model
	p.UpdatedAt = time.Now().Unix()
	s.Store.DB().Save(&p)
	s.ok(c, gin.H{"pricing": p})
}

// ---------- 管理员：代理池 / 模板 / 设置 ----------

func (s *Server) handleAdminProxies(c *gin.Context) {
	var proxies []store.Proxy
	s.Store.DB().Order("id").Find(&proxies)
	out := make([]gin.H, 0, len(proxies))
	for i := range proxies {
		out = append(out, gin.H{
			"id": proxies[i].ID, "name": proxies[i].Name,
			"enabled": proxies[i].Enabled == 1, "note": proxies[i].Note,
			"hasUrl": proxies[i].URLEnc != nil,
		})
	}
	s.ok(c, gin.H{"proxies": out})
}

func (s *Server) handleAdminTemplates(c *gin.Context) {
	var tpls []store.ChannelTemplate
	s.Store.DB().Order("id").Find(&tpls)
	out := make([]gin.H, 0, len(tpls))
	for i := range tpls {
		out = append(out, templateDTO(&tpls[i]))
	}
	s.ok(c, gin.H{"templates": out})
}

type adminSettingsDTO struct {
	RegisterMode  string `json:"registerMode"`
	FeishuEnabled bool   `json:"feishuEnabled"`
	FeishuAppID   string `json:"feishuAppId"`
}

func (s *Server) handleAdminSettings(c *gin.Context) {
	mode, _ := s.Store.GetSetting("register_mode")
	if mode == "" {
		mode = "open"
	}
	feishuEnabled, _ := s.Store.GetSetting("feishu_enabled")
	appID, _ := s.Store.GetSetting("feishu_app_id")
	s.ok(c, gin.H{"settings": adminSettingsDTO{
		RegisterMode:  mode,
		FeishuEnabled: feishuEnabled == "1",
		FeishuAppID:   appID,
	}})
}

func (s *Server) handleAdminUpdateSettings(c *gin.Context) {
	var dto adminSettingsDTO
	if err := c.BindJSON(&dto); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if dto.RegisterMode != "open" && dto.RegisterMode != "invite" && dto.RegisterMode != "closed" {
		s.fail(c, http.StatusBadRequest, "registerMode 须为 open/invite/closed")
		return
	}
	s.Store.SetSetting("register_mode", dto.RegisterMode)
	if dto.FeishuEnabled {
		s.Store.SetSetting("feishu_enabled", "1")
	} else {
		s.Store.SetSetting("feishu_enabled", "0")
	}
	if dto.FeishuAppID != "" {
		s.Store.SetSetting("feishu_app_id", dto.FeishuAppID)
	}
	s.ok(c, gin.H{"settings": dto})
}

// ---------- 辅助 ----------

func (s *Server) channelNames(userID int64) map[int64]string {
	var chans []store.Channel
	s.Store.DB().Where("user_id = ?", userID).Find(&chans)
	out := map[int64]string{}
	for i := range chans {
		out[chans[i].ID] = chans[i].Name
	}
	return out
}

func queryInt(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
