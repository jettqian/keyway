package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"keyway/internal/auth"
	"keyway/internal/probe"
	"keyway/internal/proxyman"
	"keyway/internal/store"
)

// Server 控制台 API
type Server struct {
	Store   *store.Store
	Secret  string
	Auth    *auth.Service
	Probe   *probe.Engine
	PM      *proxyman.Manager
	BaseURL string
}

func New(st *store.Store, secret string, a *auth.Service, p *probe.Engine, pm *proxyman.Manager, baseURL string) *Server {
	return &Server{Store: st, Secret: secret, Auth: a, Probe: p, PM: pm, BaseURL: baseURL}
}

const sessionCookie = "keyway_session"

func (s *Server) fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"message": msg})
}

func (s *Server) ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, data)
}

// ---------- 中间件 ----------

// SessionAuth 会话鉴权（/api）
func (s *Server) SessionAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookie)
		if err != nil || token == "" {
			s.fail(c, http.StatusUnauthorized, "未登录")
			c.Abort()
			return
		}
		if c.Request.Method != http.MethodGet && c.GetHeader("X-Keyway-CSRF") == "" {
			s.fail(c, http.StatusForbidden, "缺少 CSRF 头")
			c.Abort()
			return
		}
		u, err := s.Auth.UserFromSession(token)
		if err != nil {
			s.fail(c, http.StatusUnauthorized, err.Error())
			c.Abort()
			return
		}
		c.Set("user", u)
		c.Set("session", token)
		c.Next()
	}
}

// AdminAuth 管理员鉴权
func (s *Server) AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := currentUser(c)
		if u == nil || u.Role != 100 {
			s.fail(c, http.StatusForbidden, "需要管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}

func currentUser(c *gin.Context) *store.User {
	v, _ := c.Get("user")
	u, _ := v.(*store.User)
	return u
}

// ---------- 认证路由 ----------

func (s *Server) RegisterAuthRoutes(r *gin.RouterGroup) {
	r.POST("/auth/register", s.handleRegister)
	r.POST("/auth/login", s.handleLogin)
	r.POST("/auth/logout", s.handleLogout)
	r.GET("/auth/public-info", s.handlePublicInfo)
	r.GET("/auth/feishu/url", s.handleFeishuURL)
}

// HandleFeishuCallback 飞书 OAuth 回调（注册在根路由）
func (s *Server) HandleFeishuCallback(c *gin.Context) {
	base := s.requestBase(c)
	state := c.Query("state")
	if !s.validState(state) {
		c.Redirect(http.StatusFound, base+"/login?feishu=state_error")
		return
	}
	code := c.Query("code")
	if code == "" {
		c.Redirect(http.StatusFound, base+"/login?feishu=missing_code")
		return
	}
	u, token, err := s.Auth.FeishuCallback(code, base)
	if err != nil {
		c.Redirect(http.StatusFound, base+"/login?feishu=error")
		return
	}
	s.setSessionCookie(c, token)
	c.Redirect(http.StatusFound, base+"/")
	_ = u
}

// requestBase 对外地址：优先配置，其次从请求推导
func (s *Server) requestBase(c *gin.Context) string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/")
	}
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + c.Request.Host
}

func (s *Server) validState(state string) bool {
	// 透传给 auth 校验（HMAC+TTL）
	return s.Auth.VerifyFeishuState(state)
}

func (s *Server) handlePublicInfo(c *gin.Context) {
	feishu, _ := s.Store.GetSetting("feishu_enabled")
	mode, _ := s.Store.GetSetting("register_mode")
	if mode == "" {
		mode = "open"
	}
	s.ok(c, gin.H{"feishuEnabled": feishu == "1", "registerMode": mode})
}

// RegisterRoutes 会话内路由
func (s *Server) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/auth/me", s.handleMe)
	r.PUT("/auth/password", s.handleChangePassword)

	r.GET("/keys", s.handleListKeys)
	r.POST("/keys", s.handleCreateKey)
	r.PUT("/keys/:id", s.handleUpdateKey)
	r.PUT("/keys/:id/status", s.handleUpdateKeyStatus)
	r.DELETE("/keys/:id", s.handleDeleteKey)

	r.GET("/channels", s.handleListChannels)
	r.POST("/channels", s.handleCreateChannel)
	r.POST("/channels/from_template/:tid", s.handleCopyTemplate)
	r.PUT("/channels/:id", s.handleUpdateChannel)
	r.DELETE("/channels/:id", s.handleDeleteChannel)
	r.POST("/channels/:id/test", s.handleTestChannel)
	r.POST("/channels/:id/test_keys", s.handleTestKeys)

	r.GET("/templates", s.handleListTemplates)
	r.PUT("/models/bindings", s.handleUpdateModelBindings)
	r.GET("/models/catalog", s.handleListCatalogModels)

	r.GET("/tokens", s.handleListTokens)
	r.POST("/tokens", s.handleCreateToken)
	r.PUT("/tokens/:id", s.handleUpdateToken)
	r.POST("/tokens/:id/reveal", s.handleRevealToken)
	r.DELETE("/tokens/:id", s.handleRevokeToken)

	r.GET("/logs", s.handleLogs)
	r.GET("/logs/export", s.handleLogsExport)
	r.GET("/stats", s.handleStats)
	r.GET("/stats/export", s.handleStatsExport)

	admin := r.Group("/admin", s.AdminAuth())
	{
		admin.GET("/users", s.handleAdminUsers)
		admin.PUT("/users/:id/status", s.handleAdminUserStatus)
		admin.POST("/users/:id/reset_password", s.handleAdminResetPassword)
		admin.GET("/stats", s.handleAdminStats)
		admin.GET("/pricing", s.handleAdminPricing)
		admin.PUT("/pricing/:model", s.handleAdminUpdatePricing)
		admin.DELETE("/pricing/:model", s.handleAdminDeletePricing)
		admin.GET("/models", s.handleAdminListCatalogModels)
		admin.POST("/models", s.handleAdminCreateCatalogModel)
		admin.PUT("/models/:id", s.handleAdminUpdateCatalogModel)
		admin.DELETE("/models/:id", s.handleAdminDeleteCatalogModel)
		admin.POST("/models/import_pricing", s.handleAdminImportCatalogFromPricing)
		admin.GET("/proxies", s.handleAdminProxies)
		admin.POST("/proxies", s.handleAdminCreateProxy)
		admin.PUT("/proxies/:id", s.handleAdminUpdateProxy)
		admin.DELETE("/proxies/:id", s.handleAdminDeleteProxy)
		admin.GET("/proxy_usage", s.handleAdminProxyUsage)
		admin.GET("/templates", s.handleAdminTemplates)
		admin.POST("/templates", s.handleAdminCreateTemplate)
		admin.PUT("/templates/:id", s.handleAdminUpdateTemplate)
		admin.DELETE("/templates/:id", s.handleAdminDeleteTemplate)
		admin.GET("/settings", s.handleAdminSettings)
		admin.PUT("/settings", s.handleAdminUpdateSettings)
		admin.GET("/invites", s.handleAdminListInvites)
		admin.POST("/invites", s.handleAdminCreateInvites)
		admin.DELETE("/invites/:code", s.handleAdminDeleteInvite)
		admin.GET("/stats/export", s.handleAdminStatsExport)
	}
}

func (s *Server) setSessionCookie(c *gin.Context, token string) {
	c.SetCookie(sessionCookie, token, 7*24*3600, "/", "", false, true)
}

func (s *Server) handleRegister(c *gin.Context) {
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		InviteCode string `json:"inviteCode"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	u, token, err := s.Auth.Register(req.Username, req.Password, req.InviteCode)
	if err != nil {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	s.setSessionCookie(c, token)
	s.ok(c, gin.H{"user": userDTO(u)})
}

func (s *Server) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	u, token, err := s.Auth.Login(req.Username, req.Password)
	if err != nil {
		s.fail(c, http.StatusUnauthorized, err.Error())
		return
	}
	s.setSessionCookie(c, token)
	s.ok(c, gin.H{"user": userDTO(u)})
}

func (s *Server) handleLogout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookie); err == nil {
		s.Auth.Logout(token)
	}
	c.SetCookie(sessionCookie, "", -1, "/", "", false, true)
	s.ok(c, gin.H{})
}

func (s *Server) handleMe(c *gin.Context) {
	s.ok(c, gin.H{"user": userDTO(currentUser(c))})
}

func (s *Server) handleChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	if err := s.Auth.ChangePassword(currentUser(c).ID, req.OldPassword, req.NewPassword); err != nil {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	// 改密后吊销全部会话，要求重新登录
	if token, err := c.Cookie(sessionCookie); err == nil {
		s.Auth.Logout(token)
	}
	c.SetCookie(sessionCookie, "", -1, "/", "", false, true)
	s.ok(c, gin.H{})
}

func (s *Server) handleFeishuURL(c *gin.Context) {
	u, err := s.Auth.FeishuAuthorizeURL(s.requestBase(c))
	if err != nil {
		s.ok(c, gin.H{"url": ""})
		return
	}
	s.ok(c, gin.H{"url": u})
}

func userDTO(u *store.User) gin.H {
	dto := gin.H{
		"id":        u.ID,
		"username":  u.Username,
		"role":      u.Role,
		"status":    u.Status,
		"hasFeishu": u.FeishuUserID != nil,
		"createdAt": u.CreatedAt,
	}
	if u.LastLoginAt != nil {
		dto["lastLoginAt"] = *u.LastLoginAt
	}
	return dto
}

func trimOrEmpty(s string) string {
	return strings.TrimSpace(s)
}
