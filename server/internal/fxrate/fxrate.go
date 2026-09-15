// Package fxrate 汇率同步：从免费公共 API 拉取 USD→CNY 汇率写入 settings.usd_cny_rate，
// 供人民币渠道（cny_ratio 模式）费用折算。多源顺序回退，仅 auto 模式下定时覆盖；
// manual 模式保留管理员固定值（管理员也可手动触发同步覆盖）
package fxrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"keyway/internal/store"
)

const (
	frankfurterURL = "https://api.frankfurter.dev/v1/latest?base=USD&symbols=CNY"
	jsdelivrURL    = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/usd.json"
	erapiURL       = "https://open.er-api.com/v6/latest/USD"

	// syncInterval 定时同步周期；startupDelay 首次同步延迟（错开启动高峰）
	syncInterval = 24 * time.Hour
	startupDelay = 30 * time.Second
	requestLimit = 1 << 20 // 响应体上限 1MB
	minValidRate = 0.5     // 与管理员手动设置的有效范围一致
	maxValidRate = 20
)

// Settings 键（usd_cny_rate 本体由 usage 层消费）
const (
	KeyRate      = "usd_cny_rate"
	KeyMode      = "usd_cny_rate_mode"       // manual（默认）/ auto
	KeyUpdatedAt = "usd_cny_rate_updated_at" // RFC3339
	KeySource    = "usd_cny_rate_source"     // frankfurter / jsdelivr / erapi / manual
	KeySourceURL = "usd_cny_rate_source_url" // 命中源的请求地址（manual 为空）
)

// ModeAuto 自动同步；ModeManual 管理员固定值
const (
	ModeAuto   = "auto"
	ModeManual = "manual"
)

type source struct {
	name  string
	url   string
	parse func([]byte) (float64, error)
}

// Engine 汇率同步引擎
type Engine struct {
	store   *store.Store
	client  *http.Client
	sources []source
	mu      sync.Mutex // 串行化写 settings（手动同步与定时循环可能并发）
}

// New 创建引擎；mainURL 非空时覆盖为单一同步源（测试或自建镜像用，无回退），
// 留空时按 官方 frankfurter → jsdelivr CDN → open.er-api.com 三源顺序回退
func New(st *store.Store, mainURL string) *Engine {
	e := &Engine{store: st, client: &http.Client{Timeout: 15 * time.Second}}
	if mainURL != "" {
		e.sources = []source{{name: "frankfurter", url: mainURL, parse: parseFrankfurter}}
		return e
	}
	e.sources = []source{
		{name: "frankfurter", url: frankfurterURL, parse: parseFrankfurter},
		{name: "jsdelivr", url: jsdelivrURL, parse: parseJsdelivr},
		{name: "erapi", url: erapiURL, parse: parseERAPI},
	}
	return e
}

// Mode 当前同步模式（缺省 manual）
func (e *Engine) Mode() string {
	if v, _ := e.store.GetSetting(KeyMode); v == ModeAuto {
		return ModeAuto
	}
	return ModeManual
}

// Result 单次同步结果（含命中源的请求地址，供页面展示来源）
type Result struct {
	Rate      float64
	Source    string
	SourceURL string
}

// Sync 依次尝试各源，返回首个有效汇率（0.5~20 之外视为源异常，继续回退）
func (e *Engine) Sync() (Result, error) {
	var errs []string
	for _, src := range e.sources {
		r, perr := fetchParse(e.client, src.url, src.parse)
		if perr == nil {
			return Result{Rate: r, Source: src.name, SourceURL: src.url}, nil
		}
		errs = append(errs, src.name+": "+perr.Error())
	}
	return Result{}, fmt.Errorf("全部汇率源拉取失败（%s）", joinErrors(errs))
}

// SyncNow 手动同步：拉取并立即写入（不区分模式，管理员点按钮即生效）
func (e *Engine) SyncNow() (Result, time.Time, error) {
	res, err := e.Sync()
	if err != nil {
		return Result{}, time.Time{}, err
	}
	return res, e.ApplyRate(res), nil
}

// Start 后台定时循环：启动 startupDelay 后先跑一次，此后每 syncInterval 一次；
// 仅 auto 模式下写入，manual 模式跳过（保留固定值）
func (e *Engine) Start(stop <-chan struct{}) {
	timer := time.NewTimer(startupDelay)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			e.runAuto()
			timer.Reset(syncInterval)
		}
	}
}

// runAuto 单次定时同步（auto 模式才有副作用）
func (e *Engine) runAuto() {
	if e.Mode() != ModeAuto {
		return
	}
	res, err := e.Sync()
	if err != nil {
		fmt.Printf("[keyway] 汇率同步失败: %v\n", err)
		return
	}
	e.ApplyRate(res)
	fmt.Printf("[keyway] 汇率已同步：1 USD = %.4f CNY（来源 %s）\n", res.Rate, res.Source)
}

// ApplyRate 写入汇率及元信息（含来源地址），返回写入时间；范围校验由调用方保证
// （fetchParse 与 SaveManualRate 均已在边界校验 0.5~20）
func (e *Engine) ApplyRate(res Result) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.store.SetSetting(KeyRate, strconv.FormatFloat(res.Rate, 'f', 4, 64))
	e.store.SetSetting(KeySource, res.Source)
	e.store.SetSetting(KeySourceURL, res.SourceURL)
	e.store.SetSetting(KeyUpdatedAt, now.Format(time.RFC3339))
	return now
}

// SaveManualRate 管理员固定值写入（manual 模式），带范围校验
func (e *Engine) SaveManualRate(rate float64) error {
	if rate < minValidRate || rate > maxValidRate {
		return fmt.Errorf("汇率须在 %.1f ~ %.1f 之间", minValidRate, float64(maxValidRate))
	}
	e.ApplyRate(Result{Rate: rate, Source: "manual"})
	return nil
}

func fetchParse(client *http.Client, url string, parse func([]byte) (float64, error)) (float64, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, requestLimit))
	if err != nil {
		return 0, err
	}
	rate, err := parse(body)
	if err != nil {
		return 0, err
	}
	if rate < minValidRate || rate > maxValidRate {
		return 0, fmt.Errorf("汇率 %.4f 超出有效范围", rate)
	}
	return rate, nil
}

// joinErrors 汇总各源错误（源最多 3 个，简单拼接足够）
func joinErrors(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "；"
		}
		out += e
	}
	return out
}

// ---------- 各源解析 ----------

// frankfurter: {"amount":1.0,"base":"USD","date":"...","rates":{"CNY":6.7084}}
func parseFrankfurter(body []byte) (float64, error) {
	var raw struct {
		Rates struct {
			CNY float64 `json:"CNY"`
		} `json:"rates"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("非法 JSON: %w", err)
	}
	if raw.Rates.CNY <= 0 {
		return 0, fmt.Errorf("缺少 CNY 汇率")
	}
	return raw.Rates.CNY, nil
}

// jsdelivr currency-api: {"date":"...","usd":{"cny":6.71,...}}（键为小写）
func parseJsdelivr(body []byte) (float64, error) {
	var raw struct {
		USD map[string]float64 `json:"usd"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("非法 JSON: %w", err)
	}
	rate, ok := raw.USD["cny"]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("缺少 CNY 汇率")
	}
	return rate, nil
}

// open.er-api.com: {"result":"success","rates":{"CNY":6.7,...}}
func parseERAPI(body []byte) (float64, error) {
	var raw struct {
		Result string             `json:"result"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("非法 JSON: %w", err)
	}
	if raw.Result != "success" {
		return 0, fmt.Errorf("接口返回 %q", raw.Result)
	}
	rate, ok := raw.Rates["CNY"]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("缺少 CNY 汇率")
	}
	return rate, nil
}
