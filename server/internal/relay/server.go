package relay

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"keyway/internal/auth"
	"keyway/internal/breaker"
	"keyway/internal/config"
	"keyway/internal/httpx"
	"keyway/internal/probe"
	"keyway/internal/proxyman"
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
	PM      *proxyman.Manager
	Cfg     config.Config
	Breaker *breaker.Engine

	pool httpx.Pool
	rr   map[int64]int64 // round_robin 渠道计数
	rrMu sync.Mutex
}

func NewServer(st *store.Store, secret string, a *auth.Service, r *routing.Service, w *usage.Writer, pm *proxyman.Manager, cfg config.Config, bk *breaker.Engine) *Server {
	return &Server{
		Store: st, Secret: secret, Auth: a, Routing: r, Logs: w, PM: pm, Cfg: cfg, Breaker: bk,
		pool: *httpx.NewPool(cfg.ResponseHeaderTimeoutSec),
		rr:   map[int64]int64{},
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
	return s.pool.Get(proxyURL)
}

// ---------- 尝试计划 ----------

type attempt struct {
	protocol string
	request  *http.Request
	rc       *routing.ResolvedChannel
	lineURL  string
	proxyURL string // "" = 直连
	via      string // direct | personal | proxy:{id}
	proxyID  int64  // 公共代理 ID（via 为 proxy:{id} 时非零）
	key      *store.Key
	trial    bool // 半开试探：执行前需原子认领（ClaimHalfOpen），仅放行单个组合
}

type linePath struct {
	line    string
	proxy   string
	via     string
	proxyID int64
}

// plan 生成组合序列：渠道（priority 降序）× 线路 × 路径 × 密钥（有序/轮询）
// 路径集合 = proxyman（直连→个人→公共代理，渠道 opt-in）；
// 线路×路径按 line_stats 探测数据排序：健康且新鲜者按延迟升序，未知按录入顺序，不健康殿后；
// 预算（FR-K5）按渠道粒度截断：单渠道组合耗尽（≤ cfg.AttemptBudget）→ 换下一候选渠道，
// 全部渠道耗尽 → 透传最后错误；冷却中的密钥排后（可用密钥优先）；
// 同一渠道同时命中 matched 与 defaults 时去重（第二轮重试必然同样失败）；
// 熔断过滤（FR-B1/FR-B2，v1.5.45）：view 中熔断且冷却未到期的渠道整渠道跳过；
// 冷却已到期（半开）的渠道只放行首个组合并标记 trial（执行前需认领）
func (s *Server) plan(matched, defaults []*routing.ResolvedChannel, model string, view breaker.View) []attempt {
	var out []attempt
	budget := s.Cfg.AttemptBudget
	if budget <= 0 {
		budget = 3
	}
	now := time.Now().Unix()
	seen := map[int64]bool{}
	for _, rc := range append(matched, defaults...) {
		if seen[rc.Channel.ID] {
			continue
		}
		seen[rc.Channel.ID] = true
		if view != nil && view.Open(rc.Channel.ID) && !view.Due(rc.Channel.ID, now) {
			continue // 熔断冷却中：跳过整渠道，流量稳定走备用渠道
		}
		keys := s.orderKeys(rc)
		if len(keys) == 0 {
			continue
		}
		paths := s.PM.Paths(rc.PersonalProxyURL, rc.Channel.AllowPublicProxy == 1)
		lines := rc.BaseURLs
		if rc.Channel.LineStrategy == "manual" {
			// manual：固定第一条线路且不做路径优选
			lines = lines[:1]
			paths = paths[:1]
		}
		var rawPaths []struct{ proxy, via string }
		ids := map[string]int64{}
		for _, p := range paths {
			rawPaths = append(rawPaths, struct{ proxy, via string }{p.ProxyURL, p.Via})
			if p.ProxyID != 0 {
				ids[p.Via] = p.ProxyID
			}
		}
		combos := orderCombos(s.Store.DB(), rc.Channel.ID, lines, rawPaths, probeIntervalMin(s.Cfg))
		var chOut []attempt
		for _, cb := range combos {
			for _, k := range keys {
				chOut = append(chOut, attempt{
					rc: rc, lineURL: cb.line, proxyURL: cb.proxy, via: cb.via,
					proxyID: ids[cb.via], key: k,
				})
			}
		}
		if len(chOut) > budget {
			chOut = chOut[:budget]
		}
		if view != nil && view.Due(rc.Channel.ID, now) && len(chOut) > 0 {
			// 半开试探：单组合 + 首选密钥，成功即关闭、流量切回
			chOut = chOut[:1]
			chOut[0].trial = true
		}
		out = append(out, chOut...)
	}
	return out
}

// breakerView 计算候选渠道在请求模型上的熔断视图；全部候选渠道都熔断时返回
// nil（旁路，FR-B3）：可用性优先——此时无处可切，行为应与未熔断一致，
// 旁路尝试成功即自动关闭熔断。调用方应先剔除因协议不兼容等必然不会尝试的
// 渠道，避免"唯一可用渠道熔断 + 其余渠道不可用"被误判为存在可用候选
func (s *Server) breakerView(candidates []*routing.ResolvedChannel, model string) breaker.View {
	if s.Breaker == nil || len(candidates) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(candidates))
	for _, rc := range candidates {
		ids = append(ids, rc.Channel.ID)
	}
	view := s.Breaker.View(ids, model)
	if len(view) == 0 {
		return nil
	}
	for _, rc := range candidates {
		if !view.Open(rc.Channel.ID) {
			return view // 存在未熔断候选，正常按视图过滤
		}
	}
	return nil // 全候选熔断 → 旁路
}

// orderCombos 按 line_stats 健康度与延迟排序组合（FR-S2）
func orderCombos(db *gorm.DB, channelID int64, lines []string, paths []struct{ proxy, via string }, intervalMin int) []linePath {
	type raw struct {
		lp    linePath
		order int
	}
	var all []raw
	for _, line := range lines {
		for _, p := range paths {
			all = append(all, raw{lp: linePath{line: line, proxy: p.proxy, via: p.via}, order: len(all)})
		}
	}
	stats := probe.LoadStats(db, channelID)
	// 新鲜阈值 = 3 个探测周期（跟随 ProbeIntervalMin 缩放，与 probe.Engine 基准频率一致）
	if intervalMin <= 0 {
		intervalMin = 10
	}
	fresh := time.Now().Unix() - int64(3*intervalMin*60)

	type scored struct {
		r    raw
		rank int64
	}
	list := make([]scored, 0, len(all))
	for _, r := range all {
		if st, ok := stats[probe.StatKey(r.lp.line, r.lp.via)]; ok && st.LastProbeAt != nil && *st.LastProbeAt > fresh {
			if st.Ok != nil && *st.Ok == 1 && st.LatencyMs != nil {
				list = append(list, scored{r, *st.LatencyMs}) // 健康新鲜：延迟升序
				continue
			}
			if st.Ok != nil && *st.Ok == 0 {
				list = append(list, scored{r, 2_000_000_000 + int64(r.order)}) // 不健康殿后
				continue
			}
		}
		list = append(list, scored{r, 1_000_000_000 + int64(r.order)}) // 未知：录入顺序
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].rank < list[j].rank })
	out := make([]linePath, 0, len(list))
	for _, s := range list {
		out = append(out, s.r.lp)
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

// probeIntervalMin 探测周期（分钟），供 orderCombos 新鲜度阈值计算；
// 与 probe.Engine.baseInterval 一致：<=0 时回落 10
func probeIntervalMin(cfg config.Config) int {
	if cfg.ProbeIntervalMin > 0 {
		return cfg.ProbeIntervalMin
	}
	return 10
}
