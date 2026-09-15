package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/crypto"
	"keyway/internal/httpx"
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

// handleUpdateKeyStatus 启用/停用密钥（停用后不参与渠道轮换）
func (s *Server) handleUpdateKeyStatus(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Status int `json:"status"`
	}
	if err := c.BindJSON(&req); err != nil || (req.Status != 1 && req.Status != 2) {
		s.fail(c, http.StatusBadRequest, "status 须为 1（启用）或 2（停用）")
		return
	}
	res := s.Store.DB().Model(&store.Key{}).
		Where("id = ? AND user_id = ?", id, currentUser(c).ID).Update("status", req.Status)
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
	ForwardMode      string            `json:"forwardMode"`
	Priority         int               `json:"priority"`
	PriceMultiplier  float64           `json:"priceMultiplier"`
	PricingMode      string            `json:"pricingMode"`
	CNYRatio         float64           `json:"cnyRatio"`
	IsDefault        bool              `json:"isDefault"`
	Enabled          bool              `json:"enabled"`
}

func (s *Server) validateChannel(in *channelInput) string {
	if trimOrEmpty(in.Name) == "" {
		return "名称不能为空"
	}
	if in.ForwardMode == "" {
		in.ForwardMode = "passthrough"
	}
	if in.ForwardMode != "passthrough" && in.ForwardMode != "convert" {
		return "转发模式必须为 passthrough 或 convert"
	}
	// 协议类型只在跨协议转换时才需要；透明转发沿用入站协议，无需选择
	if in.ForwardMode == "convert" && in.Type != "openai" && in.Type != "anthropic" {
		return "跨协议转换模式必须指定目标协议（openai 或 anthropic）"
	}
	if n := len(nonEmpty(in.BaseURLs)); n < 1 || n > 5 {
		return "线路数量须为 1~5"
	}
	// 启用中的渠道必须绑定密钥；草稿（停用）允许 0 把，便于先建后绑
	if in.Enabled {
		if len(in.KeyIDs) < 1 || len(in.KeyIDs) > 5 {
			return "启用中的渠道须绑定 1~5 把密钥（停用状态可作为草稿保存）"
		}
	} else if len(in.KeyIDs) > 5 {
		return "绑定密钥数量须为 0~5"
	}
	if in.KeyStrategy == "" {
		in.KeyStrategy = "ordered"
	}
	if in.LineStrategy == "" {
		in.LineStrategy = "auto"
	}
	if in.PricingMode == "" {
		in.PricingMode = "usd"
	}
	if in.PricingMode != "usd" && in.PricingMode != "cny_ratio" {
		return "计价模式须为 usd 或 cny_ratio"
	}
	if in.PriceMultiplier < 0 {
		return "价格倍率不能为负"
	}
	if in.PricingMode == "cny_ratio" && in.CNYRatio <= 0 {
		return "人民币渠道须填写换算比（$1 官方用量实收人民币金额）"
	}
	return ""
}

func (s *Server) applyChannelInput(ch *store.Channel, in *channelInput, copyFrom *int64) error {
	ch.Name = trimOrEmpty(in.Name)
	ch.Type = in.Type
	urls := nonEmpty(in.BaseURLs)
	ch.BaseURLsJSON = string(mustJSONStr(urls))
	if in.KeyIDs == nil {
		in.KeyIDs = []int64{}
	}
	ch.KeyIDsJSON = string(mustJSONStr(in.KeyIDs))
	ch.KeyStrategy = in.KeyStrategy
	ch.LineStrategy = in.LineStrategy
	ch.AllowPublicProxy = boolToInt(in.AllowPublicProxy)
	ch.ModelsJSON = string(mustJSONStr(nonEmpty(in.Models)))
	ch.ModelMappingJSON = string(mustJSONStr(in.ModelMapping))
	ch.ForwardMode = in.ForwardMode
	ch.Priority = in.Priority
	if in.PriceMultiplier > 0 {
		ch.PriceMultiplier = in.PriceMultiplier
	} else {
		ch.PriceMultiplier = 1
	}
	if in.PricingMode == "cny_ratio" {
		ch.PricingMode = "cny_ratio"
		ch.CNYRatio = in.CNYRatio
	} else {
		ch.PricingMode = "usd"
		ch.CNYRatio = 0
	}
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
	if msg := validatePersonalProxy(in.ProxyURL); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	// 校验密钥归属
	if len(in.KeyIDs) > 0 && !s.ownsKeys(currentUser(c).ID, in.KeyIDs) {
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
	if msg := validatePersonalProxy(in.ProxyURL); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	if len(in.KeyIDs) > 0 && !s.ownsKeys(currentUser(c).ID, in.KeyIDs) {
		s.fail(c, http.StatusBadRequest, "包含不属于你的密钥")
		return
	}
	if err := s.applyChannelInput(&ch, &in, nil); err != nil {
		s.fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	var saveErr error
	if trimOrEmpty(in.ProxyURL) == "" {
		// 留空表示不变更
		saveErr = s.Store.DB().Model(&ch).Omit("proxy_url_enc").Updates(map[string]any{
			"name": ch.Name, "type": ch.Type, "base_urls_json": ch.BaseURLsJSON,
			"key_ids_json": ch.KeyIDsJSON, "key_strategy": ch.KeyStrategy, "line_strategy": ch.LineStrategy,
			"allow_public_proxy": ch.AllowPublicProxy, "models_json": ch.ModelsJSON,
			"model_mapping_json": ch.ModelMappingJSON, "forward_mode": ch.ForwardMode, "priority": ch.Priority,
			"price_multiplier": ch.PriceMultiplier, "pricing_mode": ch.PricingMode, "cny_ratio": ch.CNYRatio,
			"is_default": ch.IsDefault, "enabled": ch.Enabled,
		}).Error
	} else {
		saveErr = s.Store.DB().Save(&ch).Error
	}
	if saveErr != nil {
		s.fail(c, http.StatusInternalServerError, "保存失败")
		return
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
	// 复制为草稿：不绑定密钥、不启用，用户编辑绑定密钥后再启用。
	// 用 map 显式列值创建，绕过 GORM 对带 default 标签零值字段的跳过（Enabled=0 会被 default:1 覆盖）
	userID := currentUser(c).ID
	if err := s.Store.DB().Model(&store.Channel{}).Create(map[string]any{
		"user_id":                 userID,
		"copied_from_template_id": tpl.ID,
		"name":                    tpl.Name,
		"type":                    tpl.Type,
		"base_urls_json":          tpl.BaseURLsJSON,
		"key_ids_json":            "[]",
		"key_strategy":            "ordered",
		"line_strategy":           tpl.LineStrategy,
		"allow_public_proxy":      tpl.AllowPublicProxyDefault,
		"models_json":             tpl.ModelsJSON,
		"model_mapping_json":      tpl.ModelMappingJSON,
		"priority":                tpl.PriorityDefault,
		"price_multiplier":        1,
		"is_default":              0,
		"enabled":                 0,
		"created_at":              time.Now().Unix(),
	}).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "复制失败")
		return
	}
	var ch store.Channel
	if err := s.Store.DB().Where("user_id = ? AND copied_from_template_id = ?", userID, tpl.ID).
		Order("id DESC").First(&ch).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "回读草稿失败")
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
		"channelIds": t.ChannelFilter(), "channelOrder": t.ChannelOrder(),
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
		Name       string  `json:"name"`
		ChannelID  *int64  `json:"channelId"`
		ChannelIDs []int64 `json:"channelIds"`
		ModelScope string  `json:"modelScope"`
		ExpiresAt  *int64  `json:"expiresAt"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" {
		s.fail(c, http.StatusBadRequest, "名称不能为空")
		return
	}
	// 渠道限定集合 = channelIds ∪ 旧 channelId；校验归属
	filter := req.ChannelIDs
	if req.ChannelID != nil {
		exists := false
		for _, id := range filter {
			if id == *req.ChannelID {
				exists = true
			}
		}
		if !exists {
			filter = append(filter, *req.ChannelID)
		}
	}
	if len(filter) > 20 {
		s.fail(c, http.StatusBadRequest, "限定渠道数量过多（≤20）")
		return
	}
	if len(filter) > 0 {
		var count int64
		s.Store.DB().Model(&store.Channel{}).
			Where("user_id = ? AND id IN ?", currentUser(c).ID, filter).Count(&count)
		if count != int64(len(filter)) {
			s.fail(c, http.StatusBadRequest, "包含不存在或不属于你的渠道")
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
		ChannelIDsJSON: string(mustJSONStr(filter)), ChannelOrderJSON: string(mustJSONStr(filter)), CreatedAt: time.Now().Unix(),
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

// handleRevokeToken 吊销令牌（保留记录可回看，立即失效）
func (s *Server) handleRevokeToken(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Model(&store.Token{}).
		Where("id = ? AND user_id = ?", id, currentUser(c).ID).
		Update("revoked", 1)
	if res.Error != nil {
		s.fail(c, http.StatusInternalServerError, "吊销失败")
		return
	}
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	s.ok(c, gin.H{})
}

// handleDeleteToken 删除令牌记录（吊销后清理；记录删除后令牌自然失效）
func (s *Server) handleDeleteToken(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).Delete(&store.Token{})
	if res.Error != nil {
		s.fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	s.ok(c, gin.H{})
}

// handleUpdateToken 更新令牌（名称 / 限定渠道集合；channelIds 为启用集合（顺序即路由
// 优先级），channelOrder 为面板配置顺序（含已关闭渠道，纯 UI）——二者分离，开关渠道
// 不再改变顺序）
func (s *Server) handleUpdateToken(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var t store.Token
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&t).Error; err != nil {
		s.fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	var req struct {
		Name         *string  `json:"name"`
		ChannelIDs   *[]int64 `json:"channelIds"`
		ChannelOrder *[]int64 `json:"channelOrder"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	uniqInt64 := func(filter []int64) []int64 {
		seen := map[int64]bool{}
		uniq := make([]int64, 0, len(filter))
		for _, cid := range filter {
			if cid > 0 && !seen[cid] {
				seen[cid] = true
				uniq = append(uniq, cid)
			}
		}
		return uniq
	}
	ownsAll := func(uniq []int64) bool {
		if len(uniq) == 0 {
			return true
		}
		var count int64
		s.Store.DB().Model(&store.Channel{}).
			Where("user_id = ? AND id IN ?", currentUser(c).ID, uniq).Count(&count)
		return count == int64(len(uniq))
	}
	updates := map[string]any{}
	if req.Name != nil {
		name := trimOrEmpty(*req.Name)
		if name == "" {
			s.fail(c, http.StatusBadRequest, "名称不能为空")
			return
		}
		updates["name"] = name
	}
	if req.ChannelIDs != nil {
		filter := *req.ChannelIDs
		if filter == nil {
			filter = []int64{}
		}
		uniq := uniqInt64(filter)
		if len(uniq) > 20 {
			s.fail(c, http.StatusBadRequest, "限定渠道数量过多（≤20）")
			return
		}
		if !ownsAll(uniq) {
			s.fail(c, http.StatusBadRequest, "包含不存在或不属于你的渠道")
			return
		}
		updates["channel_ids_json"] = string(mustJSONStr(uniq))
		// 更新集合时清除旧单渠道字段，避免两者合并产生歧义
		updates["channel_id"] = nil
	}
	if req.ChannelOrder != nil {
		order := *req.ChannelOrder
		if order == nil {
			order = []int64{}
		}
		uniq := uniqInt64(order)
		if len(uniq) > 20 {
			s.fail(c, http.StatusBadRequest, "限定渠道数量过多（≤20）")
			return
		}
		if !ownsAll(uniq) {
			s.fail(c, http.StatusBadRequest, "包含不存在或不属于你的渠道")
			return
		}
		updates["channel_order_json"] = string(mustJSONStr(uniq))
	}
	if len(updates) > 0 {
		if err := s.Store.DB().Model(&t).Updates(updates).Error; err != nil {
			s.fail(c, http.StatusInternalServerError, "更新失败")
			return
		}
		if err := s.Store.DB().Where("id = ?", t.ID).First(&t).Error; err != nil {
			s.fail(c, http.StatusInternalServerError, "读取更新结果失败")
			return
		}
	}
	s.ok(c, gin.H{"token": tokenDTO(&t)})
}

// handleRevealToken 返回令牌所有者保存的完整令牌。
func (s *Server) handleRevealToken(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var t store.Token
	if err := s.Store.DB().Where("id = ? AND user_id = ?", id, currentUser(c).ID).First(&t).Error; err != nil {
		s.fail(c, http.StatusNotFound, "令牌不存在")
		return
	}
	plaintext, err := crypto.Decrypt(s.Secret, "token", t.KeyEnc)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "令牌解密失败")
		return
	}
	s.ok(c, gin.H{"plaintext": string(plaintext)})
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

func validatePersonalProxy(raw string) string {
	raw = trimOrEmpty(raw)
	if raw == "" {
		return ""
	}
	if _, err := httpx.ParseProxyURL(raw); err != nil {
		return "个人代理地址无效: " + err.Error()
	}
	return ""
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
	// 历史空配置可能存为 null；接口始终返回可遍历的集合。
	if urls == nil {
		urls = []string{}
	}
	if models == nil {
		models = []string{}
	}
	if keyIDs == nil {
		keyIDs = []int64{}
	}
	if mapping == nil {
		mapping = map[string]string{}
	}
	dto := gin.H{
		"id": ch.ID, "name": ch.Name, "type": ch.Type,
		"baseUrls": urls, "keyIds": keyIDs,
		"keyStrategy": ch.KeyStrategy, "lineStrategy": ch.LineStrategy,
		"hasPersonalProxy": ch.ProxyURLEnc != nil,
		"allowPublicProxy": ch.AllowPublicProxy == 1,
		"models":           models, "modelMapping": mapping,
		"forwardMode": ch.ForwardMode,
		"priority":    ch.Priority, "priceMultiplier": ch.PriceMultiplier,
		"pricingMode": ch.PricingMode, "cnyRatio": ch.CNYRatio,
		"isDefault": ch.IsDefault == 1,
		"enabled":   ch.Enabled == 1, "createdAt": ch.CreatedAt,
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
	out := make([]string, 0, len(list))
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
