package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"keyway/internal/config"
	"keyway/internal/httpx"
	"keyway/internal/proxyman"
	"keyway/internal/routing"
	"keyway/internal/store"
)

const (
	scanTick   = 30 * time.Second
	maxBackoff = 60 * time.Minute
	matrixCap  = 20
	probeWait  = 15 * time.Second // 单次探测整体超时
)

// Result 线路×路径探测结果
type Result struct {
	LineURL   string `json:"lineUrl"`
	Via       string `json:"via"`
	OK        bool   `json:"ok"`
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

// Engine 后台探测器（DESIGN §6）
type Engine struct {
	store   *store.Store
	secret  string
	routing *routing.Service
	pm      *proxyman.Manager
	cfg     config.Config
	pool    *httpx.Pool

	mu       sync.Mutex
	next     map[int64]time.Time // 渠道 → 下次探测时间
	failMult map[int64]int       // 连续失败退避倍数
}

func New(st *store.Store, secret string, r *routing.Service, pm *proxyman.Manager, cfg config.Config) *Engine {
	return &Engine{
		store: st, secret: secret, routing: r, pm: pm, cfg: cfg,
		pool:     httpx.NewPool(),
		next:     map[int64]time.Time{},
		failMult: map[int64]int{},
	}
}

// Start 启动调度循环：初始随机铺开，30s 扫描到期渠道
func (e *Engine) Start(stop <-chan struct{}) {
	interval := e.baseInterval()
	var chans []store.Channel
	e.store.DB().Where("enabled = 1").Find(&chans)
	e.mu.Lock()
	for i := range chans {
		// 初始铺开：0~1 个周期内随机
		e.next[chans[i].ID] = time.Now().Add(time.Duration(rand.Float64() * float64(interval)))
	}
	e.mu.Unlock()

	ticker := time.NewTicker(scanTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			e.scanOnce()
		}
	}
}

func (e *Engine) scanOnce() {
	var chans []store.Channel
	e.store.DB().Where("enabled = 1").Find(&chans)
	now := time.Now()
	for i := range chans {
		ch := &chans[i]
		e.mu.Lock()
		due := e.next[ch.ID]
		e.mu.Unlock()
		if now.Before(due) {
			continue
		}
		results, err := e.ProbeChannel(ch)
		if err != nil {
			continue
		}
		anyOK := false
		for _, r := range results {
			if r.OK {
				anyOK = true
				break
			}
		}
		e.scheduleNext(ch.ID, anyOK)
	}
}

func (e *Engine) baseInterval() time.Duration {
	min := e.cfg.ProbeIntervalMin
	if min <= 0 {
		min = 10
	}
	return time.Duration(min) * time.Minute
}

// scheduleNext 成功恢复基准频率（±20% jitter）；失败指数退避（上限 60 分钟）
func (e *Engine) scheduleNext(id int64, ok bool) {
	base := e.baseInterval()
	e.mu.Lock()
	defer e.mu.Unlock()
	if ok {
		e.failMult[id] = 0
	} else {
		e.failMult[id]++
	}
	mult := int64(1) << min64(int64(e.failMult[id]), 5)
	wait := base * time.Duration(mult)
	if wait > maxBackoff {
		wait = maxBackoff
	}
	jitter := 0.8 + 0.4*rand.Float64()
	e.next[id] = time.Now().Add(time.Duration(float64(wait) * jitter))
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ProbeChannel 同步探测一个渠道的全部"线路 × 路径"组合并写 line_stats
func (e *Engine) ProbeChannel(ch *store.Channel) ([]Result, error) {
	rc, err := e.routing.ForChannel(ch)
	if err != nil {
		return nil, err
	}
	if len(rc.Keys) == 0 {
		return nil, fmt.Errorf("渠道无可用密钥")
	}
	model := firstModel(ch)
	if model == "" {
		return nil, fmt.Errorf("渠道未配置模型列表，无法探测")
	}
	key := pickKey(rc.Keys)
	keyPlain, err := routing.DecodeKeyValue(e.secret, key)
	if err != nil {
		return nil, err
	}

	// 路径集合：proxyman（直连 → 个人 → 公共代理，渠道 opt-in）
	paths := e.pm.Paths(rc.PersonalProxyURL, ch.AllowPublicProxy == 1)

	var results []Result
	for _, line := range rc.BaseURLs {
		for _, p := range paths {
			if len(results) >= matrixCap {
				break
			}
			results = append(results, e.probeOnce(ch, model, keyPlain, line, p.ProxyURL, p.Via))
		}
	}
	e.saveResults(ch.ID, results)
	return results, nil
}

// ProbeKeys 逐密钥测试（首线路直连路径）
func (e *Engine) ProbeKeys(ch *store.Channel) ([]KeyResult, error) {
	rc, err := e.routing.ForChannel(ch)
	if err != nil {
		return nil, err
	}
	model := firstModel(ch)
	if model == "" {
		return nil, fmt.Errorf("渠道未配置模型列表")
	}
	if len(rc.BaseURLs) == 0 {
		return nil, fmt.Errorf("渠道无线路")
	}
	line := rc.BaseURLs[0]
	var out []KeyResult
	for _, k := range rc.Keys {
		plain, err := routing.DecodeKeyValue(e.secret, k)
		if err != nil {
			out = append(out, KeyResult{KeyID: k.ID, Name: k.Name, Error: err.Error()})
			continue
		}
		res := e.probeOnce(ch, model, plain, line, "", "direct")
		out = append(out, KeyResult{
			KeyID: k.ID, Name: k.Name, OK: res.OK, LatencyMs: res.LatencyMs, Error: res.Error,
		})
	}
	return out, nil
}

// probeOnce 发送最小请求并计时
func (e *Engine) probeOnce(ch *store.Channel, model, keyPlain, line, proxyURL, via string) Result {
	result := Result{LineURL: line, Via: via}

	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 8,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
	})
	var target string
	if ch.Type == "anthropic" {
		target = strings.TrimSuffix(line, "/") + "/v1/messages"
	} else {
		target = strings.TrimSuffix(line, "/") + "/chat/completions"
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.Type == "anthropic" {
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
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = fmt.Sprintf("%s %s: %v", via, shortURL(line), err)
		return result
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		result.OK = true
	} else {
		result.Error = fmt.Sprintf("上游返回 %d", resp.StatusCode)
	}
	return result
}

// saveResults 结果落 line_stats + 更新渠道健康状态
func (e *Engine) saveResults(channelID int64, results []Result) {
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
	if len(results) > 0 {
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

func firstModel(ch *store.Channel) string {
	var models []string
	json.Unmarshal([]byte(ch.ModelsJSON), &models)
	for _, m := range models {
		if m != "" {
			return m
		}
	}
	return ""
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
