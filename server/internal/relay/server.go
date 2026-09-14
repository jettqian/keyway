package relay

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/auth"
	"keyway/internal/config"
	"keyway/internal/routing"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// Server /v1 中转入口
type Server struct {
	Store   *store.Store
	Secret  string
	Auth    *auth.Service
	Routing *routing.Service
	Logs    *usage.Writer
	Cfg     config.Config

	clients map[string]*http.Client
	mu      sync.Mutex
	rr      map[int64]int64 // round_robin 渠道计数
	rrMu    sync.Mutex
}

func NewServer(st *store.Store, secret string, a *auth.Service, r *routing.Service, w *usage.Writer, cfg config.Config) *Server {
	return &Server{
		Store: st, Secret: secret, Auth: a, Routing: r, Logs: w, Cfg: cfg,
		clients: map[string]*http.Client{},
		rr:      map[int64]int64{},
	}
}

// ---------- 鉴权中间件 ----------

// TokenAuth 网关令牌鉴权（Bearer / x-api-key 双兼容）
func (s *Server) TokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if raw == "" {
			raw = c.GetHeader("x-api-key")
		}
		tok, user, err := s.Auth.AuthenticateGatewayToken(raw)
		if err != nil {
			respondOpenAIError(c, http.StatusUnauthorized, err.Error())
			c.Abort()
			return
		}
		c.Set("token", tok)
		c.Set("user", user)
		c.Next()
	}
}

// getClient 按代理 URL 复用 HTTP 客户端（"" = 直连）
func (s *Server) getClient(proxyURL string) (*http.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cl, ok := s.clients[proxyURL]; ok {
		return cl, nil
	}
	transport := &http.Transport{
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if proxyURL != "" {
		u, err := parseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(u)
	}
	cl := &http.Client{Transport: transport}
	s.clients[proxyURL] = cl
	return cl, nil
}

// ---------- 尝试计划 ----------

type attempt struct {
	rc       *routing.ResolvedChannel
	lineURL  string
	proxyURL string // "" = 直连
	via      string // direct | personal
	key      *store.Key
}

// plan 生成组合序列：渠道（priority 降序）× 线路 × 路径（直连→个人代理）× 密钥（有序/轮询）
// 预算截断为 cfg.AttemptBudget；冷却中的密钥排后（可用密钥优先）
func (s *Server) plan(matched, defaults []*routing.ResolvedChannel) []attempt {
	var out []attempt
	for _, rc := range append(matched, defaults...) {
		keys := s.orderKeys(rc)
		if len(keys) == 0 {
			continue
		}
		paths := []struct{ proxy, via string }{{"", "direct"}}
		if rc.PersonalProxyURL != "" {
			paths = append(paths, struct{ proxy, via string }{rc.PersonalProxyURL, "personal"})
		}
		for _, line := range rc.BaseURLs {
			for _, p := range paths {
				for _, k := range keys {
					out = append(out, attempt{rc: rc, lineURL: line, proxyURL: p.proxy, via: p.via, key: k})
				}
			}
		}
	}
	budget := s.Cfg.AttemptBudget
	if budget <= 0 {
		budget = 3
	}
	if len(out) > budget {
		out = out[:budget]
	}
	return out
}

// orderKeys 密钥选择：ordered 顺序；round_robin 从计数位起轮转；冷却密钥排后
func (s *Server) orderKeys(rc *routing.ResolvedChannel) []*store.Key {
	keys := rc.Keys
	if rc.Channel.KeyStrategy == "round_robin" && len(keys) > 1 {
		s.rrMu.Lock()
		s.rr[rc.Channel.ID]++
		start := int(s.rr[rc.Channel.ID] % int64(len(keys)))
		s.rrMu.Unlock()
		rotated := make([]*store.Key, 0, len(keys))
		rotated = append(rotated, keys[start:]...)
		rotated = append(rotated, keys[:start]...)
		keys = rotated
	}
	now := time.Now().Unix()
	var hot, cooling []*store.Key
	for _, k := range keys {
		if k.CooldownUntil > now {
			cooling = append(cooling, k)
		} else {
			hot = append(hot, k)
		}
	}
	if len(hot) > 0 {
		return hot
	}
	return cooling
}

func parseProxyURL(u string) (*url.URL, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https", "socks5":
		return parsed, nil
	}
	return nil, fmt.Errorf("不支持的代理协议: %s", parsed.Scheme)
}
