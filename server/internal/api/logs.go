package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/crypto"
	"keyway/internal/fxrate"
	"keyway/internal/httpx"
	"keyway/internal/pricing"
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
			"reasoningEffort": l.ReasoningEffort,
			"inputCost":       l.InputCost, "outputCost": l.OutputCost,
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
	since, until := statsRange(c)
	st, err := usage.QueryStats(s.Store.DB(), &u.ID, since, until, queryTokenID(c))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.fillRecentChannelNames(st)
	s.ok(c, st)
}

func (s *Server) handleAdminStats(c *gin.Context) {
	since, until := statsRange(c)
	st, err := usage.QueryStats(s.Store.DB(), nil, since, until, queryTokenID(c))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.fillRecentChannelNames(st)
	s.ok(c, st)
}

// ---------- 渠道×模型链路状态（FR-B7） ----------

// handleStatsLinks GET /api/stats/links：本人渠道的链路状态（统计页「链路状态」）
func (s *Server) handleStatsLinks(c *gin.Context) {
	u := currentUser(c)
	since, until := statsRange(c)
	links, err := s.linksResponse(&u.ID, since, until)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.ok(c, gin.H{"links": links})
}

// handleAdminStatsLinks GET /api/admin/stats/links：全量渠道的链路状态（管理端
// 全局视角，聚合所有用户的请求；行含渠道所有者）
func (s *Server) handleAdminStatsLinks(c *gin.Context) {
	since, until := statsRange(c)
	links, err := s.linksResponse(nil, since, until)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	s.ok(c, gin.H{"links": links})
}

// linksResponse 组装链路状态：聚合日志（userID nil = 全员）、补充渠道名/所有者、
// 叠加当前熔断快照。渠道已删的行剔除；熔断中的组合即使窗口内零尝试（熔断的
// 本意就是无流量）也补零行展示——状态可见性不依赖流量
func (s *Server) linksResponse(userID *int64, since, until int64) ([]usage.LinkStat, error) {
	links, err := usage.QueryLinks(s.Store.DB(), userID, since, until)
	if err != nil {
		return nil, err
	}
	var chans []store.Channel
	q := s.Store.DB()
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	if err := q.Find(&chans).Error; err != nil {
		return nil, err
	}
	chByID := make(map[int64]*store.Channel, len(chans))
	ids := make([]int64, 0, len(chans))
	for i := range chans {
		chByID[chans[i].ID] = &chans[i]
		ids = append(ids, chans[i].ID)
	}
	// 管理端补所有者用户名
	ownerByID := map[int64]string{}
	if userID == nil && len(ids) > 0 {
		var users []store.User
		s.Store.DB().Find(&users)
		for i := range users {
			ownerByID[users[i].ID] = users[i].Username
		}
	}
	// 当前熔断快照（本人/全量渠道）
	type bkKey struct {
		ch int64
		m  string
	}
	brkBy := map[bkKey]*store.BreakerState{}
	if len(ids) > 0 {
		var rows []store.BreakerState
		s.Store.DB().Where("channel_id IN ?", ids).Find(&rows)
		for i := range rows {
			brkBy[bkKey{rows[i].ChannelID, rows[i].Model}] = &rows[i]
		}
	}
	snap := func(b *store.BreakerState) *usage.LinkBreaker {
		if b == nil {
			return nil
		}
		return &usage.LinkBreaker{FailCount: b.FailCount, CooldownUntil: b.CooldownUntil, LastError: b.LastError}
	}
	out := make([]usage.LinkStat, 0, len(links)+len(brkBy))
	seen := make(map[bkKey]bool, len(links))
	for _, l := range links {
		ch, ok := chByID[l.ChannelID]
		if !ok {
			continue // 渠道已删
		}
		l.ChannelName = ch.Name
		if userID == nil {
			l.Owner = ownerByID[ch.UserID]
		}
		k := bkKey{l.ChannelID, l.Model}
		l.Breaker = snap(brkBy[k])
		seen[k] = true
		out = append(out, l)
	}
	// 补零行：熔断中但窗口内零尝试
	for k, b := range brkBy {
		if seen[k] {
			continue
		}
		ch, ok := chByID[k.ch]
		if !ok {
			continue
		}
		l := usage.LinkStat{
			ChannelID: k.ch, Model: k.m, ChannelName: ch.Name,
			LastAt: b.UpdatedAt, Breaker: snap(b),
		}
		if userID == nil {
			l.Owner = ownerByID[ch.UserID]
		}
		out = append(out, l)
	}
	return out, nil
}

// queryTokenID 解析可选的令牌筛选参数（tokenId，数值 id；空或非法返回 nil）
func queryTokenID(c *gin.Context) *int64 {
	if v := c.Query("tokenId"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			return &id
		}
	}
	return nil
}

// statsRange 解析统计时间窗：优先 start/end（YYYY-MM-DD，end 含当天），
// 未提供时回退 days（默认 7，向后兼容）
func statsRange(c *gin.Context) (since, until int64) {
	var start, end time.Time
	if v := c.Query("start"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			start = t
		}
	}
	if v := c.Query("end"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			end = t
		}
	}
	if !start.IsZero() || !end.IsZero() {
		if !start.IsZero() {
			since = start.Unix()
		}
		if !end.IsZero() {
			until = end.Add(24 * time.Hour).Unix()
		}
		return since, until
	}
	days := queryInt(c, "days", 7)
	if days < 1 {
		days = 7
	}
	return time.Now().AddDate(0, 0, -days).Unix(), 0
}

// fillRecentChannelNames 为最近生效流量批量补充渠道名（日志行已按用户隔离；
// 渠道已删除时回退 #id）
func (s *Server) fillRecentChannelNames(st *usage.Stats) {
	if st == nil || len(st.Recent) == 0 {
		return
	}
	ids := make([]int64, 0, len(st.Recent))
	for i := range st.Recent {
		ids = append(ids, st.Recent[i].ChannelID)
	}
	var chans []store.Channel
	s.Store.DB().Select("id, name").Where("id IN ?", ids).Find(&chans)
	names := make(map[int64]string, len(chans))
	for i := range chans {
		names[chans[i].ID] = chans[i].Name
	}
	for i := range st.Recent {
		if n := names[st.Recent[i].ChannelID]; n != "" {
			st.Recent[i].ChannelName = n
		} else {
			st.Recent[i].ChannelName = "#" + strconv.FormatInt(st.Recent[i].ChannelID, 10)
		}
	}
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
	var rows []store.ModelPricing
	s.Store.DB().Order("model").Find(&rows)
	// 附带远程同步元信息：最近一次同步时间与同步源链接（管理页展示）
	s.ok(c, gin.H{
		"pricing":  rows,
		"syncedAt": pricing.SyncedAt(s.Store.DB()),
		"sources":  pricing.Sources(),
	})
}

func (s *Server) handleAdminUpdatePricing(c *gin.Context) {
	model := c.Param("model")
	var p store.ModelPricing
	if err := c.BindJSON(&p); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	p.Model = model // URL 参数为权威
	p.UpdatedAt = time.Now().Unix()
	if err := s.Store.DB().Save(&p).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "保存价目失败")
		return
	}
	usage.InvalidatePricingCache(s.Store.DB())
	s.ok(c, gin.H{"pricing": p})
}

func (s *Server) handleAdminDeletePricing(c *gin.Context) {
	model := c.Param("model")
	if err := s.Store.DB().Where("model = ?", model).Delete(&store.ModelPricing{}).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	usage.InvalidatePricingCache(s.Store.DB())
	s.ok(c, gin.H{})
}

// handleAdminSyncPricing 手动触发官方价目同步（LiteLLM + OpenRouter，只补缺不覆盖已有条目）
func (s *Server) handleAdminSyncPricing(c *gin.Context) {
	res, err := pricing.SyncRemote(s.Store.DB(), 60*time.Second)
	if err != nil {
		s.fail(c, http.StatusBadGateway, err.Error())
		return
	}
	s.ok(c, gin.H{"result": res})
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

func (s *Server) handleAdminCreateProxy(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Note string `json:"note"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" || trimOrEmpty(req.URL) == "" {
		s.fail(c, http.StatusBadRequest, "名称与代理地址不能为空")
		return
	}
	enc, err := encryptProxy(s.Secret, req.URL)
	if err != nil {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	p := store.Proxy{Name: trimOrEmpty(req.Name), URLEnc: enc, Note: req.Note, Enabled: 1, CreatedAt: time.Now().Unix()}
	if err := s.Store.DB().Create(&p).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	s.PM.Reload()
	s.ok(c, gin.H{"proxy": gin.H{"id": p.ID, "name": p.Name, "enabled": true, "note": p.Note}})
}

func (s *Server) handleAdminUpdateProxy(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var p store.Proxy
	if err := s.Store.DB().First(&p, id).Error; err != nil {
		s.fail(c, http.StatusNotFound, "代理不存在")
		return
	}
	var req struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Note    string `json:"note"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	updates := map[string]any{}
	if trimOrEmpty(req.Name) != "" {
		updates["name"] = trimOrEmpty(req.Name)
	}
	updates["note"] = req.Note
	if trimOrEmpty(req.URL) != "" {
		enc, err := encryptProxy(s.Secret, req.URL)
		if err != nil {
			s.fail(c, http.StatusBadRequest, err.Error())
			return
		}
		updates["url_enc"] = enc
	}
	if req.Enabled != nil {
		updates["enabled"] = boolToInt(*req.Enabled)
	}
	if err := s.Store.DB().Model(&p).Updates(updates).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "更新代理失败")
		return
	}
	s.PM.Reload()
	s.ok(c, gin.H{})
}

func (s *Server) handleAdminDeleteProxy(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	res := s.Store.DB().Delete(&store.Proxy{}, id)
	if res.Error != nil {
		s.fail(c, http.StatusInternalServerError, "删除代理失败")
		return
	}
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "代理不存在")
		return
	}
	s.PM.Reload()
	s.ok(c, gin.H{})
}

// handleAdminProxyUsage 公共代理按用户流量统计（仅统计，FR-P2）
func (s *Server) handleAdminProxyUsage(c *gin.Context) {
	type row struct {
		UserID    int64  `json:"userId"`
		Username  string `json:"username"`
		ProxyID   int64  `json:"proxyId"`
		ProxyName string `json:"proxyName"`
		Day       string `json:"day"`
		Bytes     int64  `json:"bytes"`
	}
	var rows []row
	s.Store.DB().Raw(`
		SELECT pu.user_id AS user_id, COALESCE(u.username,'-') AS username,
		       pu.proxy_id AS proxy_id, COALESCE(p.name,'-') AS proxy_name,
		       pu.day AS day, pu.bytes AS bytes
		FROM proxy_usage pu
		LEFT JOIN users u ON u.id = pu.user_id
		LEFT JOIN proxies p ON p.id = pu.proxy_id
		ORDER BY pu.day DESC, pu.bytes DESC
		LIMIT 500
	`).Scan(&rows)
	s.ok(c, gin.H{"usage": rows})
}

// ---------- 管理员：预制模板 CRUD ----------

type templateInput struct {
	Name                    string            `json:"name"`
	Type                    string            `json:"type"`
	BaseURLs                []string          `json:"baseUrls"`
	LineStrategy            string            `json:"lineStrategy"`
	Models                  []string          `json:"models"`
	ModelMapping            map[string]string `json:"modelMapping"`
	PriorityDefault         int               `json:"priorityDefault"`
	AllowPublicProxyDefault bool              `json:"allowPublicProxyDefault"`
	Note                    string            `json:"note"`
	Enabled                 bool              `json:"enabled"`
}

func (t *templateInput) validate() string {
	if trimOrEmpty(t.Name) == "" {
		return "名称不能为空"
	}
	// 协议类型不再对外暴露：模板默认透明转发，复制出的渠道沿用入站协议；
	// 兼容历史模板已存的 openai/anthropic 值，其余一律按空（透明）处理
	if t.Type != "openai" && t.Type != "anthropic" {
		t.Type = ""
	}
	if n := len(nonEmpty(t.BaseURLs)); n < 1 || n > 5 {
		return "线路数量须为 1~5"
	}
	if len(nonEmpty(t.Models)) < 1 {
		return "至少配置一个模型"
	}
	if t.LineStrategy == "" {
		t.LineStrategy = "auto"
	}
	return ""
}

func (s *Server) handleAdminCreateTemplate(c *gin.Context) {
	var in templateInput
	if err := c.BindJSON(&in); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if msg := in.validate(); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	tpl := store.ChannelTemplate{
		Name: trimOrEmpty(in.Name), Type: in.Type,
		BaseURLsJSON:            string(mustJSONStr(nonEmpty(in.BaseURLs))),
		LineStrategy:            in.LineStrategy,
		ModelsJSON:              string(mustJSONStr(nonEmpty(in.Models))),
		ModelMappingJSON:        string(mustJSONStr(in.ModelMapping)),
		PriorityDefault:         in.PriorityDefault,
		AllowPublicProxyDefault: boolToInt(in.AllowPublicProxyDefault),
		Note:                    in.Note,
		Enabled:                 boolToInt(in.Enabled),
		UpdatedAt:               time.Now().Unix(),
	}
	if err := s.Store.DB().Create(&tpl).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	s.ok(c, gin.H{"template": templateDTO(&tpl)})
}

func (s *Server) handleAdminUpdateTemplate(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var tpl store.ChannelTemplate
	if err := s.Store.DB().First(&tpl, id).Error; err != nil {
		s.fail(c, http.StatusNotFound, "模板不存在")
		return
	}
	var in templateInput
	if err := c.BindJSON(&in); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if msg := in.validate(); msg != "" {
		s.fail(c, http.StatusBadRequest, msg)
		return
	}
	tpl.Name = trimOrEmpty(in.Name)
	tpl.Type = in.Type
	tpl.BaseURLsJSON = string(mustJSONStr(nonEmpty(in.BaseURLs)))
	tpl.LineStrategy = in.LineStrategy
	tpl.ModelsJSON = string(mustJSONStr(nonEmpty(in.Models)))
	tpl.ModelMappingJSON = string(mustJSONStr(in.ModelMapping))
	tpl.PriorityDefault = in.PriorityDefault
	tpl.AllowPublicProxyDefault = boolToInt(in.AllowPublicProxyDefault)
	tpl.Note = in.Note
	tpl.Enabled = boolToInt(in.Enabled)
	tpl.UpdatedAt = time.Now().Unix()
	if err := s.Store.DB().Save(&tpl).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "更新模板失败")
		return
	}
	s.ok(c, gin.H{"template": templateDTO(&tpl)})
}

func (s *Server) handleAdminDeleteTemplate(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	// 已复制渠道不受影响（copied_from_template_id 悬挂标记）
	res := s.Store.DB().Delete(&store.ChannelTemplate{}, id)
	if res.Error != nil {
		s.fail(c, http.StatusInternalServerError, "删除模板失败")
		return
	}
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "模板不存在")
		return
	}
	s.ok(c, gin.H{})
}

func encryptProxy(secret, url string) ([]byte, error) {
	if _, err := httpx.ParseProxyURL(url); err != nil {
		return nil, err
	}
	return crypto.Encrypt(secret, "proxy", []byte(url))
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
	RegisterMode    string  `json:"registerMode"`
	FeishuEnabled   bool    `json:"feishuEnabled"`
	FeishuAppID     string  `json:"feishuAppId"`
	FeishuAppSecret string  `json:"feishuAppSecret,omitempty"` // 只写，不回显
	FeishuHasSecret bool    `json:"feishuHasSecret"`
	FeishuBaseURL   string  `json:"feishuBaseUrl"`
	ExchangeRate    float64 `json:"exchangeRate"` // USD→CNY，人民币渠道费用折算用
	// 汇率模式：auto = 定时同步（每 24h 拉取公共 API 覆盖）；manual = 管理员固定值
	ExchangeRateMode      string `json:"exchangeRateMode,omitempty"`
	ExchangeRateSource    string `json:"exchangeRateSource,omitempty"`    // frankfurter/jsdelivr/erapi/manual
	ExchangeRateSourceURL string `json:"exchangeRateSourceUrl,omitempty"` // 命中源的请求地址
	ExchangeRateUpdatedAt string `json:"exchangeRateUpdatedAt,omitempty"` // RFC3339
}

// settingsDTO 从 settings 表组装当前生效的管理员设置（GET 与 PUT 回显共用）
func (s *Server) settingsDTO() adminSettingsDTO {
	mode := s.Fx.Mode()
	updatedAt, _ := s.Store.GetSetting(fxrate.KeyUpdatedAt)
	source, _ := s.Store.GetSetting(fxrate.KeySource)
	sourceURL, _ := s.Store.GetSetting(fxrate.KeySourceURL)
	rate := 7.2
	if v, err := strconv.ParseFloat(mustSetting(s.Store, fxrate.KeyRate), 64); err == nil && v > 0 {
		rate = v
	}
	return adminSettingsDTO{
		ExchangeRate:          rate,
		ExchangeRateMode:      mode,
		ExchangeRateSource:    source,
		ExchangeRateSourceURL: sourceURL,
		ExchangeRateUpdatedAt: updatedAt,
	}
}

func (s *Server) handleAdminSettings(c *gin.Context) {
	mode, _ := s.Store.GetSetting("register_mode")
	if mode == "" {
		mode = "open"
	}
	feishuEnabled, _ := s.Store.GetSetting("feishu_enabled")
	appID, _ := s.Store.GetSetting("feishu_app_id")
	baseURL, _ := s.Store.GetSetting("feishu_base_url")
	secretEnc, _ := s.Store.GetSetting("feishu_app_secret")
	dto := s.settingsDTO()
	dto.RegisterMode = mode
	dto.FeishuEnabled = feishuEnabled == "1"
	dto.FeishuAppID = appID
	dto.FeishuHasSecret = secretEnc != ""
	dto.FeishuBaseURL = baseURL
	s.ok(c, gin.H{"settings": dto})
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
	set := func(key, value string) bool {
		if err := s.Store.SetSetting(key, value); err != nil {
			s.fail(c, http.StatusInternalServerError, "保存设置失败")
			return false
		}
		return true
	}
	if !set("register_mode", dto.RegisterMode) {
		return
	}
	if dto.FeishuEnabled {
		if !set("feishu_enabled", "1") {
			return
		}
	} else {
		if !set("feishu_enabled", "0") {
			return
		}
	}
	if dto.FeishuAppID != "" {
		if !set("feishu_app_id", dto.FeishuAppID) {
			return
		}
	}
	if dto.FeishuBaseURL != "" {
		if !set("feishu_base_url", dto.FeishuBaseURL) {
			return
		}
	}
	if dto.FeishuAppSecret != "" {
		if err := s.Auth.SaveFeishuSecret(dto.FeishuAppSecret); err != nil {
			s.fail(c, http.StatusInternalServerError, "保存飞书 Secret 失败")
			return
		}
		dto.FeishuAppSecret = ""
	}
	// 汇率模式：空串不改（兼容旧客户端）；切 auto 由后台定时同步覆盖，切 manual 用固定值
	mode := s.Fx.Mode()
	if dto.ExchangeRateMode != "" {
		if dto.ExchangeRateMode != fxrate.ModeAuto && dto.ExchangeRateMode != fxrate.ModeManual {
			s.fail(c, http.StatusBadRequest, "exchangeRateMode 须为 auto/manual")
			return
		}
		mode = dto.ExchangeRateMode
		if !set(fxrate.KeyMode, mode) {
			return
		}
	}
	if mode == fxrate.ModeManual && dto.ExchangeRate >= 0.5 && dto.ExchangeRate <= 20 {
		if err := s.Fx.SaveManualRate(dto.ExchangeRate); err != nil {
			s.fail(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	// 回显不含密钥，汇率相关字段按库内实际生效值回显
	resp := s.settingsDTO()
	resp.RegisterMode = dto.RegisterMode
	resp.FeishuEnabled = dto.FeishuEnabled
	resp.FeishuAppID = dto.FeishuAppID
	secretEnc, _ := s.Store.GetSetting("feishu_app_secret")
	resp.FeishuHasSecret = secretEnc != ""
	resp.FeishuBaseURL = dto.FeishuBaseURL
	s.ok(c, gin.H{"settings": resp})
}

// handleAdminSyncExchangeRate 手动同步汇率：apply=true 拉取并立即写入；
// apply=false 仅预览（manual 模式下供表单填充固定值）
func (s *Server) handleAdminSyncExchangeRate(c *gin.Context) {
	var req struct {
		Apply bool `json:"apply"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	res, err := s.Fx.Sync()
	if err != nil {
		s.fail(c, http.StatusBadGateway, "汇率源拉取失败："+err.Error())
		return
	}
	result := gin.H{"rate": res.Rate, "source": res.Source, "sourceUrl": res.SourceURL, "applied": false}
	if req.Apply {
		at := s.Fx.ApplyRate(res)
		result["applied"] = true
		result["updatedAt"] = at.Format(time.RFC3339)
	}
	s.ok(c, gin.H{"result": result})
}

func mustSetting(st *store.Store, key string) string {
	v, _ := st.GetSetting(key)
	return v
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
