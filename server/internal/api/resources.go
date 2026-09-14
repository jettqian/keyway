package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// ---------- 密钥池 ----------

func keyDTO(k *store.Key) gin.H {
	dto := gin.H{
		"id":            k.ID,
		"name":          k.Name,
		"note":          k.Note,
		"status":        k.Status,
		"cooldownUntil": k.CooldownUntil,
		"createdAt":     k.CreatedAt,
	}
	if k.LastError != nil {
		dto["lastError"] = *k.LastError
	}
	return dto
}

func (s *Server) handleListKeys(c *gin.Context) {
	var keys []store.Key
	s.Store.DB().Where("user_id = ? ORDER BY id", currentUser(c).ID).Find(&keys)
	out := make([]gin.H, 0, len(keys))
	for i := range keys {
		out = append(out, keyDTO(&keys[i]))
	}
	s.ok(c, gin.H{"keys": out})
}

func (s *Server) handleCreateKey(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Note  string `json:"note"`
		Value string `json:"value"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" || trimOrEmpty(req.Value) == "" {
		s.fail(c, http.StatusBadRequest, "名称与密钥值不能为空")
		return
	}
	enc, err := crypto.Encrypt(s.Secret, "key", []byte(req.Value))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "加密失败")
		return
	}
	k := store.Key{
		UserID: currentUser(c).ID, Name: trimOrEmpty(req.Name), ValueEnc: enc,
		Note: req.Note, Status: 1, CreatedAt: time.Now().Unix(),
	}
	if err := s.Store.DB().Create(&k).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.fail(c, http.StatusBadRequest, "同名密钥已存在")
			return
		}
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	s.ok(c, gin.H{"key": keyDTO(&k)})
}

func (s *Server) handleUpdateKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var k store.Key
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&k).Error; err != nil {
		s.fail(c, http.StatusNotFound, "密钥不存在")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Note  string `json:"note"`
		Value string `json:"value"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" {
		s.fail(c, http.StatusBadRequest, "名称不能为空")
		return
	}
	updates := map[string]any{"name": trimOrEmpty(req.Name), "note": req.Note}
	if trimOrEmpty(req.Value) != "" {
		enc, err := crypto.Encrypt(s.Secret, "key", []byte(req.Value))
		if err != nil {
			s.fail(c, http.StatusInternalServerError, "加密失败")
			return
		}
		updates["value_enc"] = enc
		updates["cooldown_until"] = 0
		updates["last_error"] = nil
	}
	s.Store.DB().Model(&k).Updates(updates)
	s.ok(c, gin.H{"key": keyDTO(&k)})
}

func (s *Server) handleDeleteKey(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).Delete(&store.Key{})
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "密钥不存在")
		return
	}
	s.ok(c, gin.H{})
}

// ---------- 渠道 ----------

type channelInput struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	BaseURLs         []string          `json:"baseUrls"`
	KeyIDs           []int64           `json:"keyIds"`
	KeyStrategy      string            `json:"keyStrategy"`
	LineStrategy     string            `json:"lineStrategy"`
	ProxyURL         string            `json:"proxyUrl"`
	AllowPublicProxy bool              `json:"allowPublicProxy"`
	Models           []string          `json:"models"`
	ModelMapping     map[string]string `json:"modelMapping"`
	Priority         int               `json:"priority"`
	IsDefault        bool              `json:"isDefault"`
	Enabled          bool              `json:"enabled"`
}

func (s *Server) validateChannel(in *channelInput) string {
	if trimOrEmpty(in.Name) == "" {
		return "名称不能为空"
	}
	if in.Type != "openai" && in.Type != "anthropic" {
		return "类型必须为 openai 或 anthropic"
	}
	if n := len(nonEmpty(in.BaseURLs)); n < 1 || n > 5 {
		return "线路数量须为 1~5"
	}
	if len(in.KeyIDs) < 1 || len(in.KeyIDs) > 5 {
		return "绑定密钥数量须为 1~5"
	}
	if in.KeyStrategy == "" {
		in.KeyStrategy = "ordered"
	}
	if in.LineStrategy == "" {
		in.LineStrategy = "auto"
	}
	return ""
}

func (s *Server) applyChannelInput(ch *store.Channel, in *channelInput, copyFrom *int64) error {
	ch.Name = trimOrEmpty(in.Name)
	ch.Type = in.Type
	urls := nonEmpty(in.BaseURLs)
	ch.BaseURLsJSON = string(mustJSONStr(urls))
	ch.KeyIDsJSON = string(mustJSONStr(in.KeyIDs))
	ch.KeyStrategy = in.KeyStrategy
	ch.LineStrategy = in.LineStrategy
	ch.AllowPublicProxy = boolToInt(in.AllowPublicProxy)
	ch.ModelsJSON = string(mustJSONStr(nonEmpty(in.Models)))
	ch.ModelMappingJSON = string(mustJSONStr(in.ModelMapping))
	ch.Priority = in.Priority
	ch.IsDefault = boolToInt(in.IsDefault)
	ch.Enabled = boolToInt(in.Enabled)
	if copyFrom != nil {
		ch.CopiedFromTemplateID = copyFrom
	}
	if trimOrEmpty(in.ProxyURL) != "" {
		enc, err := crypto.Encrypt(s.Secret, "proxy", []byte(in.ProxyURL))
		if err != nil {
			return err
		}
		ch.ProxyURLEnc = enc
	}
	return nil
}

func (s *Server) handleListChannels(c *gin.Context) {
	var chans []store.Channel
	s.Store.DB().Where("user_id = ? ORDER BY priority DESC, id", currentUser(c).ID).Find(&chans)
	out := make([]gin.H, 0, len(chans))
	for i := range chans {
		out = append(out, channelDTO(&chans[i]))
	}
	s.ok(c, gin.H{"channels": out})
}

func (s *Server) handleCreateChannel(c *gin.Context) {
	var in channelInput
	if err := c.BindJSON(&in); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if msg := s.validateChannel(&in); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	// 校验密钥归属
	if !s.ownsKeys(currentUser(c).ID, in.KeyIDs) {
		s.fail(c, http.StatusBadRequest, "包含不属于你的密钥")
		return
	}
	ch := store.Channel{UserID: currentUser(c).ID, CreatedAt: time.Now().Unix()}
	if err := s.applyChannelInput(&ch, &in, nil); err != nil {
		s.fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	if err := s.Store.DB().Create(&ch).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	if ch.IsDefault == 1 {
		s.clearOtherDefaults(ch.UserID, ch.ID)
	}
	s.ok(c, gin.H{"channel": channelDTO(&ch)})
}

func (s *Server) handleUpdateChannel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var ch store.Channel
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&ch).Error; err != nil {
		s.fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	var in channelInput
	if err := c.BindJSON(&in); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if msg := s.validateChannel(&in); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	if !s.ownsKeys(currentUser(c).ID, in.KeyIDs) {
		s.fail(c, http.StatusBadRequest, "包含不属于你的密钥")
		return
	}
	if err := s.applyChannelInput(&ch, &in, nil); err != nil {
		s.fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	if trimOrEmpty(in.ProxyURL) == "" {
		// 留空表示不变更
		s.Store.DB().Model(&ch).Omit("proxy_url_enc").Updates(map[string]any{
			"name": ch.Name, "type": ch.Type, "base_urls_json": ch.BaseURLsJSON,
			"key_ids_json": ch.KeyIDsJSON, "key_strategy": ch.KeyStrategy, "line_strategy": ch.LineStrategy,
			"allow_public_proxy": ch.AllowPublicProxy, "models_json": ch.ModelsJSON,
			"model_mapping_json": ch.ModelMappingJSON, "priority": ch.Priority,
			"is_default": ch.IsDefault, "enabled": ch.Enabled,
		})
	} else {
		s.Store.DB().Save(&ch)
	}
	if ch.IsDefault == 1 {
		s.clearOtherDefaults(ch.UserID, ch.ID)
	}
	s.ok(c, gin.H{"channel": channelDTO(&ch)})
}

func (s *Server) handleDeleteChannel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).Delete(&store.Channel{})
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	s.ok(c, gin.H{})
}

// handleTestChannel 渠道连通性测试（线路×路径矩阵，FR-C2）
func (s *Server) handleTestChannel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var ch store.Channel
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&ch).Error; err != nil {
		s.fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	results, err := s.Probe.ProbeChannel(&ch)
	if err != nil {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(c, gin.H{"results": results})
}

// handleTestKeys 逐密钥测试（FR-K6）
func (s *Server) handleTestKeys(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var ch store.Channel
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&ch).Error; err != nil {
		s.fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	results, err := s.Probe.ProbeKeys(&ch)
	if err != nil {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	s.ok(c, gin.H{"results": results})
}

func (s *Server) handleCopyTemplate(c *gin.Context) {
	tid, _ := strconv.ParseInt(c.Param("tid"), 10, 64)
	var tpl store.ChannelTemplate
	if err := s.Store.DB().Where("id = ? AND enabled = 1", tid).First(&tpl).Error; err != nil {
		s.fail(c, http.StatusNotFound, "模板不存在或已停用")
		return
	}
	var urls, models []string
	json.Unmarshal([]byte(tpl.BaseURLsJSON), &urls)
	json.Unmarshal([]byte(tpl.ModelsJSON), &models)
	var mapping map[string]string
	json.Unmarshal([]byte(tpl.ModelMappingJSON), &mapping)
	if len(urls) == 0 || len(urls) > 5 {
		s.fail(c, http.StatusBadRequest, "模板线路配置无效")
		return
	}
	// 密钥留空由用户绑定：临时取用户第一把可用密钥，无则创建失败提示
	var firstKey store.Key
	if err := s.Store.DB().Where("user_id = ? AND status = 1", currentUser(c).ID).First(&firstKey).Error; err != nil {
		s.fail(c, http.StatusBadRequest, "请先在密钥池创建至少一把上游密钥再复制模板")
		return
	}
	ch := store.Channel{
		UserID:               currentUser(c).ID,
		CopiedFromTemplateID: &tpl.ID,
		Name:                 tpl.Name,
		Type:                 tpl.Type,
		BaseURLsJSON:         tpl.BaseURLsJSON,
		KeyIDsJSON:           string(mustJSONStr([]int64{firstKey.ID})),
		KeyStrategy:          "ordered",
		LineStrategy:         tpl.LineStrategy,
		AllowPublicProxy:     tpl.AllowPublicProxyDefault,
		ModelsJSON:           tpl.ModelsJSON,
		ModelMappingJSON:     tpl.ModelMappingJSON,
		Priority:             tpl.PriorityDefault,
		Enabled:              1,
		CreatedAt:            time.Now().Unix(),
	}
	if err := s.Store.DB().Create(&ch).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "复制失败")
		return
	}
	s.Store.DB().Model(&tpl).UpdateColumn("copy_count", tpl.CopyCount+1)
	s.ok(c, gin.H{"channel": channelDTO(&ch)})
}

// ---------- 模板（用户侧只读）----------

func templateDTO(t *store.ChannelTemplate) gin.H {
	var urls, models []string
	json.Unmarshal([]byte(t.BaseURLsJSON), &urls)
	json.Unmarshal([]byte(t.ModelsJSON), &models)
	var mapping map[string]string
	json.Unmarshal([]byte(t.ModelMappingJSON), &mapping)
	return gin.H{
		"id": t.ID, "name": t.Name, "type": t.Type,
		"baseUrls": urls, "lineStrategy": t.LineStrategy,
		"models": models, "modelMapping": mapping,
		"priorityDefault":         t.PriorityDefault,
		"allowPublicProxyDefault": t.AllowPublicProxyDefault == 1,
		"note":                    t.Note, "enabled": t.Enabled == 1, "copyCount": t.CopyCount,
		"updatedAt": t.UpdatedAt,
	}
}

func (s *Server) handleListTemplates(c *gin.Context) {
	var tpls []store.ChannelTemplate
	s.Store.DB().Where("enabled = 1 ORDER BY id").Find(&tpls)
	out := make([]gin.H, 0, len(tpls))
	for i := range tpls {
		out = append(out, templateDTO(&tpls[i]))
	}
	s.ok(c, gin.H{"templates": out})
}

// ---------- 令牌 ----------

func tokenDTO(t *store.Token) gin.H {
	dto := gin.H{
		"id": t.ID, "name": t.Name, "keyPrefix": t.KeyPrefix,
		"revoked": t.Revoked == 1, "createdAt": t.CreatedAt,
	}
	if t.ChannelID != nil {
		dto["channelId"] = *t.ChannelID
	}
	if t.ModelScope != nil {
		dto["modelScope"] = *t.ModelScope
	}
	if t.ExpiresAt != nil {
		dto["expiresAt"] = *t.ExpiresAt
	}
	return dto
}

func (s *Server) handleListTokens(c *gin.Context) {
	var tokens []store.Token
	s.Store.DB().Where("user_id = ? ORDER BY id DESC", currentUser(c).ID).Find(&tokens)
	out := make([]gin.H, 0, len(tokens))
	for i := range tokens {
		out = append(out, tokenDTO(&tokens[i]))
	}
	s.ok(c, gin.H{"tokens": out})
}

func (s *Server) handleCreateToken(c *gin.Context) {
	var req struct {
		Name       string `json:"name"`
		ChannelID  *int64 `json:"channelId"`
		ModelScope string `json:"modelScope"`
		ExpiresAt  *int64 `json:"expiresAt"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" {
		s.fail(c, http.StatusBadRequest, "名称不能为空")
		return
	}
	if req.ChannelID != nil {
		var ch store.Channel
		if err := s.Store.DB().Where("id = ? AND user_id = ?", *req.ChannelID, currentUser(c).ID).First(&ch).Error; err != nil {
			s.fail(c, http.StatusBadRequest, "限定渠道不存在")
			return
		}
	}
	plaintext, err := crypto.GenerateGatewayToken()
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "生成令牌失败")
		return
	}
	enc, err := crypto.Encrypt(s.Secret, "token", []byte(plaintext))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "加密失败")
		return
	}
	t := store.Token{
		UserID: currentUser(c).ID, Name: trimOrEmpty(req.Name),
		KeyEnc: enc, KeyPrefix: plaintext[:16], KeyHash: crypto.HashToken(plaintext),
		ChannelID: req.ChannelID, CreatedAt: time.Now().Unix(),
	}
	if req.ModelScope != "" {
		t.ModelScope = &req.ModelScope
	}
	t.ExpiresAt = req.ExpiresAt
	if err := s.Store.DB().Create(&t).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	s.ok(c, gin.H{"token": tokenDTO(&t), "plaintext": plaintext})
}

func (s *Server) handleRevokeToken(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).
		Update("revoked", 1)
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	s.ok(c, gin.H{})
}

// ---------- 辅助 ----------

func (s *Server) ownsKeys(userID int64, keyIDs []int64) bool {
	if len(keyIDs) == 0 {
		return false
	}
	var count int64
	s.Store.DB().Model(&store.Key{}).Where("user_id = ? AND id IN ?", userID, keyIDs).Count(&count)
	return count == int64(len(keyIDs))
}

func (s *Server) clearOtherDefaults(userID, keepID int64) {
	s.Store.DB().Model(&store.Channel{}).
		Where("user_id = ? AND id != ? AND is_default = 1", userID, keepID).
		Update("is_default", 0)
}

func channelDTO(ch *store.Channel) gin.H {
	var urls, models []string
	var keyIDs []int64
	json.Unmarshal([]byte(ch.BaseURLsJSON), &urls)
	json.Unmarshal([]byte(ch.ModelsJSON), &models)
	json.Unmarshal([]byte(ch.KeyIDsJSON), &keyIDs)
	var mapping map[string]string
	json.Unmarshal([]byte(ch.ModelMappingJSON), &mapping)
	dto := gin.H{
		"id": ch.ID, "name": ch.Name, "type": ch.Type,
		"baseUrls": urls, "keyIds": keyIDs,
		"keyStrategy": ch.KeyStrategy, "lineStrategy": ch.LineStrategy,
		"hasPersonalProxy": ch.ProxyURLEnc != nil,
		"allowPublicProxy": ch.AllowPublicProxy == 1,
		"models":           models, "modelMapping": mapping,
		"priority": ch.Priority, "isDefault": ch.IsDefault == 1,
		"enabled": ch.Enabled == 1, "createdAt": ch.CreatedAt,
	}
	if ch.CopiedFromTemplateID != nil {
		dto["copiedFromTemplateId"] = *ch.CopiedFromTemplateID
	}
	if ch.LastError != nil {
		dto["lastError"] = *ch.LastError
	}
	return dto
}

func mustJSONStr(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func nonEmpty(list []string) []string {
	var out []string
	for _, s := range list {
		if trimOrEmpty(s) != "" {
			out = append(out, trimOrEmpty(s))
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
