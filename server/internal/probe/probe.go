package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"keyway/internal/breaker"
	"keyway/internal/config"
	"keyway/internal/httpx"
	"keyway/internal/proxyman"
	"keyway/internal/routing"
	"keyway/internal/store"
)

const (
	matrixCap = 20
	probeWait = 30 * time.Second // 单次探测（组合）总超时：覆盖 responses/messages 排队 + 首事件等待
	// 单组合探测最多尝试的模型数（回退控制上游请求成本，DESIGN §6）
	probeModelCap = 3
)

// Result 线路×路径探测结果；OK 时 Model 记录判定健康的模型
// （探测按渠道模型列表依序回退，任一模型成功即组合健康——该模型即恢复信号，
// 用于联动关闭渠道×模型熔断，FR-B4）
type Result struct {
	LineURL   string `json:"lineUrl"`
	Via       string `json:"via"`
	OK        bool   `json:"ok"`
	Model     string `json:"model,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error"`
}

// KeyResult 逐密钥测试结果
type KeyResult struct {
	KeyID     int64  `json:"keyId"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error"`
}

// Engine 探测器（DESIGN §6，v1.5.45 起无定时循环）：
//   - 按需线路预热（MaybeWarmup）：请求路由到渠道且 line_stats 缺失/过期时
//     异步补一次线路质量探测，流量驱动、无流量零成本；
//   - 「测试渠道」/「逐密钥测试」按钮的即时矩阵（诊断用，成功联动关闭熔断）
type Engine struct {
	store    *store.Store
	secret   string
	routing  *routing.Service
	pm       *proxyman.Manager
	cfg      config.Config
	pool     *httpx.Pool
	breakers *breaker.Engine // 探测成功联动关闭渠道×模型熔断（可空）

	mu   sync.Mutex
	next map[int64]time.Time // 渠道 → 下次预热检查时间（节流，防并发重复预热）
}

func New(st *store.Store, secret string, r *routing.Service, pm *proxyman.Manager, cfg config.Config, breakers *breaker.Engine) *Engine {
	return &Engine{
		store: st, secret: secret, routing: r, pm: pm, cfg: cfg,
		pool:     httpx.NewPool(cfg.ResponseHeaderTimeoutSec),
		breakers: breakers,
		next:     map[int64]time.Time{},
	}
}

// warmupInterval 预热节拍（分钟）；≤0 = 按需预热关闭（KEYWAY_PROBE_INTERVAL_MIN=0）
func (e *Engine) warmupInterval() int {
	return e.cfg.ProbeIntervalMin
}

// MaybeWarmup 按需线路预热（FR-S1，v1.5.45 取代定时探测）：请求路由到该渠道
// 且 line_stats 缺失或整体过期（> 3 个预热周期）时，异步补一次线路质量探测。
// 预热每条线路×路径只发一个最小请求（渠道首个模型、首选端点形态），宽松判定：
// 任意 <500 的非 HTML HTTP 响应都记为线路通并取其延迟——4xx 是模型/密钥维度
// 问题，不代表线路差；网络错误、HTML 回退、5xx 记为不通。
// 有流量的渠道按流量节拍保持新鲜，无流量渠道零成本；next 节流保证并发请求
// 不重复触发。前置校验（无模型/无线路）不消耗节流窗口——渠道配置补齐后
// 下一个请求即可触发，不必等一个新鲜度周期。manual 线路策略不参与优选，无需预热
func (e *Engine) MaybeWarmup(ch *store.Channel) {
	if ch == nil || ch.LineStrategy == "manual" {
		return
	}
	interval := e.warmupInterval()
	if interval <= 0 {
		return
	}
	if len(probeModels(ch)) == 0 {
		return // 未配置模型列表：无法预热，不占窗口
	}
	var lines []string
	if json.Unmarshal([]byte(ch.BaseURLsJSON), &lines) != nil || len(lines) == 0 {
		return // 无线路：无法预热，不占窗口
	}
	freshness := time.Duration(3*interval) * time.Minute
	now := time.Now()
	e.mu.Lock()
	if now.Before(e.next[ch.ID]) {
		e.mu.Unlock()
		return
	}
	// 认领节流窗口；已有新鲜数据（如刚点过「测试」）则对齐到其过期时刻
	e.next[ch.ID] = now.Add(freshness)
	e.mu.Unlock()

	stats := LoadStats(e.store.DB(), ch.ID)
	latest := int64(0)
	for _, st := range stats {
		if st.LastProbeAt != nil && *st.LastProbeAt > latest {
			latest = *st.LastProbeAt
		}
	}
	if latest > 0 && now.Unix() < latest+int64(freshness/time.Second) {
		e.mu.Lock()
		e.next[ch.ID] = time.Unix(latest+int64(freshness/time.Second), 0)
		e.mu.Unlock()
		return
	}
	go e.warmup(ch)
}

// warmup 预热一个渠道的全部线路×路径组合（并发、单模型、宽松判定），
// 结果 UPSERT line_stats 供路由排序（orderCombos）使用
func (e *Engine) warmup(ch *store.Channel) {
	rc, err := e.routing.ForChannel(ch)
	if err != nil {
		return
	}
	if len(rc.Keys) == 0 {
		return
	}
	models := probeModels(ch)
	if len(models) == 0 || len(rc.BaseURLs) == 0 {
		return
	}
	key := pickKey(rc.Keys)
	keyPlain, err := routing.DecodeKeyValue(e.secret, key)
	if err != nil {
		return
	}
	paths := e.pm.Paths(rc.PersonalProxyURL, ch.AllowPublicProxy == 1)
	type combo struct{ line, proxyURL, via string }
	var combos []combo
	for _, line := range rc.BaseURLs {
		for _, p := range paths {
			if len(combos) >= matrixCap {
				break
			}
			combos = append(combos, combo{line, p.ProxyURL, p.Via})
		}
	}
	if len(combos) == 0 {
		return
	}
	model := models[0] // 单模型：线路质量与模型无关，任一响应的延迟都代表线路
	shape := probeShapes(ch, model)[0]
	results := make([]Result, len(combos))
	var wg sync.WaitGroup
	for i, c := range combos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), probeWait)
			defer cancel()
			results[i] = e.probeOne(ctx, ch, routing.ApplyModelMapping(ch, model), keyPlain, c.line, c.proxyURL, c.via, shape, true)
		}()
	}
	wg.Wait()
	e.saveResults(ch.ID, results, false) // 预热只落 line_stats，不刷渠道级健康
}

// ProbeChannel 并发探测一个渠道的全部"线路 × 路径"组合并写 line_stats
func (e *Engine) ProbeChannel(ch *store.Channel) ([]Result, error) {
	rc, err := e.routing.ForChannel(ch)
	if err != nil {
		return nil, err
	}
	if len(rc.Keys) == 0 {
		return nil, fmt.Errorf("渠道无可用密钥")
	}
	models := probeModels(ch)
	if len(models) == 0 {
		return nil, fmt.Errorf("渠道未配置模型列表，无法探测")
	}
	key := pickKey(rc.Keys)
	keyPlain, err := routing.DecodeKeyValue(e.secret, key)
	if err != nil {
		return nil, err
	}

	// 路径集合：proxyman（直连 → 个人 → 公共代理，渠道 opt-in）
	paths := e.pm.Paths(rc.PersonalProxyURL, ch.AllowPublicProxy == 1)

	type target struct{ line, proxyURL, via string }
	var targets []target
	for _, line := range rc.BaseURLs {
		for _, p := range paths {
			if len(targets) >= matrixCap {
				break
			}
			targets = append(targets, target{line, p.ProxyURL, p.Via})
		}
	}
	// 组合并发探测（≤20 个独立 HTTP 请求），消除串行等待（否则最坏 20×15s）
	results := make([]Result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.probeOnce(ch, models, keyPlain, t.line, t.proxyURL, t.via)
		}()
	}
	wg.Wait()
	e.saveResults(ch.ID, results, true)
	// 探测成功即关闭对应模型的熔断（FR-B4 恢复信号：与真实转发完全一致的
	// 最小流式请求已走通，足以证明渠道×模型可用；「测试渠道」按钮走本路径，
	// 点击即联动恢复矩阵覆盖到的模型）
	if e.breakers != nil {
		for _, r := range results {
			if r.OK && r.Model != "" {
				e.breakers.RecordSuccess(ch.ID, r.Model)
			}
		}
	}
	return results, nil
}

// ProbeKeys 并发逐密钥测试（首线路直连路径）
func (e *Engine) ProbeKeys(ch *store.Channel) ([]KeyResult, error) {
	rc, err := e.routing.ForChannel(ch)
	if err != nil {
		return nil, err
	}
	models := probeModels(ch)
	if len(models) == 0 {
		return nil, fmt.Errorf("渠道未配置模型列表")
	}
	if len(rc.BaseURLs) == 0 {
		return nil, fmt.Errorf("渠道无线路")
	}
	line := rc.BaseURLs[0]
	out := make([]KeyResult, len(rc.Keys))
	var wg sync.WaitGroup
	for i, k := range rc.Keys {
		plain, err := routing.DecodeKeyValue(e.secret, k)
		if err != nil {
			out[i] = KeyResult{KeyID: k.ID, Name: k.Name, Error: err.Error()}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := e.probeOnce(ch, models, plain, line, "", "direct")
			out[i] = KeyResult{
				KeyID: k.ID, Name: k.Name, OK: res.OK, LatencyMs: res.LatencyMs, Error: res.Error,
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// probeShapes 探测端点形态尝试序列（形态 = 端点 + 鉴权 + 最小请求体）：
//   - anthropic：POST /v1/messages + x-api-key（Claude Code / Claude SDK 主力端点）；
//   - openai-responses：POST /v1/responses + Bearer（Responses API，Codex 客户端
//     主力端点；仅提供 responses 形态 provider 的 team 网关上 chat/completions 恒 403）；
//   - openai：POST /v1/chat/completions + Bearer（传统 Chat Completions，多为老
//     客户端/SDK 使用，agent 流量占比低——v1.5.26 起降级为回退形态）；
//
// 序列规则（**responses/messages 优先，chat 靠后**——对齐真实主力 agent 流量）：
//   - claude* → messages → responses → chat；
//   - 其余（gpt 等）→ responses → chat → messages；
//   - convert 模式上游协议由渠道显式指定，只在该协议族内排序（配错族就应报失败）；
//   - passthrough 沿用入站协议、无法预知，按模型名族推断首选并依序回退——兼容
//     claude 模型走 openai 兼容中转、gpt 模型走 responses-only 网关等场景。
//     渠道上的 type 字段在 passthrough 下不参与转发（relay 忽略），探测同样不依赖它
func probeShapes(ch *store.Channel, model string) []string {
	if ch.ForwardMode == "convert" && ch.Type != "" {
		if ch.Type == "anthropic" {
			return []string{"anthropic"}
		}
		return []string{"openai-responses", "openai"}
	}
	if strings.HasPrefix(model, "claude") {
		return []string{"anthropic", "openai-responses", "openai"}
	}
	return []string{"openai-responses", "openai", "anthropic"}
}

// shapeLabel 形态的展示名（错误信息与结果摘要用端点路径更直观）
func shapeLabel(shape string) string {
	switch shape {
	case "anthropic":
		return "/v1/messages"
	case "openai-responses":
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}

// probeOnce 发送最小请求并计时；模型 × 端点形态两级回退（v1.5.23/24）：
// 候选模型依序尝试，每个模型按其形态判定序列（见 probeShapes）继续回退，
// 任一成功即该组合健康；全部失败时汇报各模型末次错误摘要（含上游响应体 message）。
// 整个组合共享 probeWait 总超时（所有回退请求用同一 deadline，上限确定）
func (e *Engine) probeOnce(ch *store.Channel, models []string, keyPlain, line, proxyURL, via string) Result {
	result := Result{LineURL: line, Via: via}
	var modelErrs []string
	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	start := time.Now()
	for _, m := range models {
		upstreamModel := routing.ApplyModelMapping(ch, m)
		var shapeErrs []string
		var last Result
		for _, shape := range probeShapes(ch, m) {
			last = e.probeOne(ctx, ch, upstreamModel, keyPlain, line, proxyURL, via, shape, false)
			if last.OK {
				last.Model = m // 记录判定健康的模型（熔断恢复联动用）
				return last
			}
			shapeErrs = append(shapeErrs, fmt.Sprintf("%s：%s", shapeLabel(shape), last.Error))
			if ctx.Err() != nil {
				break
			}
		}
		modelErrs = append(modelErrs, fmt.Sprintf("%s（%s）", m, strings.Join(shapeErrs, "；")))
		if ctx.Err() != nil {
			break
		}
	}
	result.LatencyMs = time.Since(start).Milliseconds()
	result.Error = strings.Join(modelErrs, "；")
	return result
}

// probeOne 按指定形态发送最小**流式**请求并计时；错误信息附带上游响应体摘要。
// 流式探测（v1.5.26）：响应头/首事件（response.created、message_start、首个
// data 块）在推理开始前即返回，读到首字节即判通并立即断开止损——非流式下
// responses/messages 需等完整推理（慢思考模型首 token 10s+，15s 总超时内
// 完不成会误判失败），且探测成本更高。
// loose = 线路预热语义（FR-S1，v1.5.45）：4xx 也记为通并取延迟——4xx 是
// 模型/密钥维度问题，不代表线路差；仅网络错误、HTML 回退、5xx 记为不通
func (e *Engine) probeOne(ctx context.Context, ch *store.Channel, model, keyPlain, line, proxyURL, via, shape string, loose bool) Result {
	result := Result{LineURL: line, Via: via}

	var body []byte
	var path string
	if shape == "anthropic" {
		path = "/messages"
		body, _ = json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 8,
			"stream":     true,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		})
	} else if shape == "openai-responses" {
		path = "/responses"
		body, _ = json.Marshal(map[string]any{
			"model":             model,
			"input":             "ping",
			"max_output_tokens": 16,
			"stream":            true,
		})
	} else {
		path = "/chat/completions"
		body, _ = json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 8,
			"stream":     true,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		})
	}
	target := httpx.UpstreamEndpoint(line, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if shape == "anthropic" {
		req.Header.Set("x-api-key", keyPlain)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+keyPlain)
	}

	client, err := e.pool.Get(proxyURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("%s %s: %v", via, shortURL(line), err)
		return result
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		// 2xx 却返回 HTML：SPA 回退，端点在该线路不存在
		if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			result.LatencyMs = time.Since(start).Milliseconds()
			resp.Body.Close()
			result.Error = "上游返回 HTML（端点不存在）"
			return result
		}
		// 读首字节确认事件流已开始（毫秒级，不等推理），随即断开止损
		one := make([]byte, 1)
		io.ReadFull(resp.Body, one)
		result.LatencyMs = time.Since(start).Milliseconds()
		resp.Body.Close()
		result.OK = true
		return result
	}
	// 预热语义：4xx 是模型/密钥维度问题，线路本身通，延迟仍有效
	if loose && resp.StatusCode >= 400 && resp.StatusCode < 500 &&
		!strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		one := make([]byte, 1)
		io.ReadFull(resp.Body, one)
		result.LatencyMs = time.Since(start).Milliseconds()
		resp.Body.Close()
		result.OK = true
		return result
	}
	// 失败：读错误响应体摘要
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	result.LatencyMs = time.Since(start).Milliseconds()
	resp.Body.Close()
	if summary := UpstreamErrorSummary(respBody); summary != "" {
		result.Error = fmt.Sprintf("上游返回 %d：%s", resp.StatusCode, summary)
	} else {
		result.Error = fmt.Sprintf("上游返回 %d", resp.StatusCode)
	}
	return result
}

// UpstreamErrorSummary 提取上游错误响应体的 message 字段
// （openai/anthropic/new_api 等通用 {"error":{"message":...}} 结构），
// 解析不出时回退原文前段；截断到 120 rune 防止撑爆 UI 与 last_error 列。
// relay 失败尝试的日志摘要复用同一解析（v1.5.42）
func UpstreamErrorSummary(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Error.Message != "" {
			return truncateRunes(e.Error.Message, 120)
		}
		if e.Message != "" {
			return truncateRunes(e.Message, 120)
		}
		return ""
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	return truncateRunes(s, 120)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// saveResults 结果落 line_stats；updateChannel 时同步刷新渠道级健康
// （last_ok_at/last_error）——仅全矩阵诊断（「测试渠道」）使用：其 OK 判定
// 是严格语义（真实 2xx 走通）。线路预热是宽松判定（4xx 也记通），只度量
// 线路质量、不代表渠道健康，不碰渠道级字段
func (e *Engine) saveResults(channelID int64, results []Result, updateChannel bool) {
	now := time.Now().Unix()
	anyOK := false
	for _, r := range results {
		ok := 0
		if r.OK {
			ok = 1
			anyOK = true
		}
		lat := r.LatencyMs
		lastErr := r.Error
		if lastErr == "" {
			lastErr = " "
		}
		stat := store.LineStat{
			ChannelID: channelID, LineURL: r.LineURL, Via: r.Via,
			LastProbeAt: &now, LatencyMs: &lat, Ok: &ok, LastError: &lastErr,
		}
		if r.OK {
			stat.LastError = nil
		}
		e.store.DB().Clauses(clause.OnConflict{UpdateAll: true}).Create(&stat)
	}
	if len(results) > 0 && updateChannel {
		if anyOK {
			e.store.DB().Model(&store.Channel{}).Where("id = ?", channelID).
				Updates(map[string]any{"last_ok_at": now, "last_error": nil})
		} else {
			last := results[len(results)-1].Error
			e.store.DB().Model(&store.Channel{}).Where("id = ?", channelID).
				Update("last_error", last)
		}
	}
}

// LoadStats 读取渠道探测数据（relay 优选排序用）
func LoadStats(db *gorm.DB, channelID int64) map[string]*store.LineStat {
	var stats []store.LineStat
	db.Where("channel_id = ?", channelID).Find(&stats)
	out := map[string]*store.LineStat{}
	for i := range stats {
		out[statKey(stats[i].LineURL, stats[i].Via)] = &stats[i]
	}
	return out
}

func statKey(line, via string) string { return line + "\x00" + via }

func StatKey(line, via string) string { return statKey(line, via) }

// probeModels 探测候选模型：渠道模型列表前 probeModelCap 个非空项。
// 上游常出现"部分模型分组无渠道/provider 未启用"（如 new_api 的
// model_not_found、team 网关的 provider 路由），固定测第一个模型会把
// 可用渠道误判为不健康，故按序回退尝试
func probeModels(ch *store.Channel) []string {
	var models []string
	json.Unmarshal([]byte(ch.ModelsJSON), &models)
	var out []string
	for _, m := range models {
		if m == "" {
			continue
		}
		out = append(out, m)
		if len(out) >= probeModelCap {
			break
		}
	}
	return out
}

func pickKey(keys []*store.Key) *store.Key {
	now := time.Now().Unix()
	for _, k := range keys {
		if k.CooldownUntil <= now {
			return k
		}
	}
	return keys[0]
}

func shortURL(u string) string {
	if len(u) > 48 {
		return u[:48] + "…"
	}
	return u
}
