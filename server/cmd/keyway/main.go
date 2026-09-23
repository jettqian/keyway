package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/api"
	"keyway/internal/auth"
	"keyway/internal/breaker"
	"keyway/internal/config"
	"keyway/internal/fxrate"
	"keyway/internal/pricing"
	"keyway/internal/probe"
	"keyway/internal/proxyman"
	"keyway/internal/relay"
	"keyway/internal/routing"
	"keyway/internal/store"
	"keyway/internal/usage"
	"keyway/internal/webui"
)

// app 组装完成的应用实例
type app struct {
	engine *gin.Engine
	store  *store.Store
	pm     *proxyman.Manager
	stop   func()
}

// buildApp 完成全部装配（供 main 与测试复用）
func buildApp(cfg config.Config) (*app, error) {
	st, err := store.Open(store.Options{DataDir: cfg.DataDir})
	if err != nil {
		return nil, err
	}

	authSvc := auth.New(st, cfg.Secret)
	routingSvc := routing.New(st, cfg.Secret)
	logWriter := usage.NewWriter(st.DB())
	pm := proxyman.New(st, cfg.Secret)
	// 渠道×模型熔断器：relay 失败计数/半开试探恢复 + 探测/手动恢复关闭
	breakerEngine := breaker.New(st.DB(), cfg.BreakerFailThreshold, cfg.BreakerCooldownSec, cfg.BreakerCooldownMaxSec)
	// 探测器（v1.5.45 起无定时循环）：按需线路预热 + 测试按钮矩阵
	probeEngine := probe.New(st, cfg.Secret, routingSvc, pm, cfg, breakerEngine)
	// 汇率定时同步（USD→CNY，auto 模式下每 24h 覆盖，manual 保留固定值）
	fxEngine := fxrate.New(st, cfg.FxSourceURL)
	apiSvc := api.New(st, cfg.Secret, authSvc, probeEngine, pm, fxEngine, breakerEngine, cfg.BaseURL)
	relaySvc := relay.NewServer(st, cfg.Secret, authSvc, routingSvc, logWriter, pm, cfg, breakerEngine, probeEngine)

	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		logWriter.Start(stopWriter)
		close(writerDone)
	}()

	pmStop := make(chan struct{})
	pmDone := make(chan struct{})
	go func() {
		pm.Start(pmStop)
		close(pmDone)
	}()

	retentionStop := make(chan struct{})
	retentionDone := make(chan struct{})
	go func() {
		logWriter.StartRetention(retentionStop, cfg.LogRetentionDays)
		close(retentionDone)
	}()

	fxStop := make(chan struct{})
	fxDone := make(chan struct{})
	go func() {
		fxEngine.Start(fxStop)
		close(fxDone)
	}()

	// 官方价目定期同步（models.dev + LiteLLM 全量 upsert；KEYWAY_PRICING_SYNC_HOURS=0 关闭）
	pricingStop := make(chan struct{})
	pricingDone := make(chan struct{})
	if cfg.PricingSyncHours > 0 {
		go func() {
			pricing.StartSyncLoop(st.DB(), cfg.PricingSyncHours, pricingStop)
			close(pricingDone)
		}()
	} else {
		close(pricingDone)
	}

	if os.Getenv("KEYWAY_DEBUG") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 中转端点同时注册 /v1 与根路径两种前缀：客户端 base_url 带不带 /v1 均可直连，
	// 降低配置成本（OpenAI SDK 习惯带 /v1，Anthropic SDK 拼接 /messages 等）
	registerRelay := func(g *gin.RouterGroup) {
		g.POST("/chat/completions", relaySvc.HandleOpenAIChat)
		g.POST("/completions", relaySvc.HandleOpenAIPassthrough("/completions"))
		g.POST("/embeddings", relaySvc.HandleOpenAIPassthrough("/embeddings"))
		g.POST("/responses", relaySvc.HandleOpenAIResponses)
		g.GET("/models", relaySvc.HandleModels)
		g.POST("/messages", relaySvc.HandleAnthropicMessages)
		g.POST("/messages/count_tokens", relaySvc.HandleAnthropicCountTokens)
	}
	registerRelay(r.Group("/v1", relaySvc.TokenAuth()))
	registerRelay(r.Group("", relaySvc.TokenAuth()))

	apiGroup := r.Group("/api")
	apiSvc.RegisterAuthRoutes(apiGroup)
	sessioned := apiGroup.Group("", apiSvc.SessionAuth())
	apiSvc.RegisterRoutes(sessioned)

	// 飞书 OAuth 回调（根路由，浏览器跳转）
	r.GET("/oauth/feishu/callback", apiSvc.HandleFeishuCallback)

	webui.Register(r)

	return &app{
		engine: r,
		store:  st,
		pm:     pm,
		stop: func() {
			close(stopWriter)
			select {
			case <-writerDone:
			case <-time.After(5 * time.Second):
			}
			close(pmStop)
			select {
			case <-pmDone:
			case <-time.After(5 * time.Second):
			}
			close(retentionStop)
			select {
			case <-retentionDone:
			case <-time.After(5 * time.Second):
			}
			close(pricingStop)
			select {
			case <-pricingDone:
			case <-time.After(5 * time.Second):
			}
			close(fxStop)
			select {
			case <-fxDone:
			case <-time.After(5 * time.Second):
			}
		},
	}, nil
}

func main() {
	// docker healthcheck 模式：自检 /healthz 后退出（distroless 无 curl/wget）
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		cfg := config.Load()
		port := cfg.Port
		if port == 0 {
			port = 8080
		}
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		resp.Body.Close()
		os.Exit(0)
	}

	cfg := config.Load()
	if err := cfg.ValidateSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "KEYWAY_SECRET 未配置或无效（生成：openssl rand -base64 32）：%v\n", err)
		os.Exit(1)
	}
	a, err := buildApp(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer a.store.Close()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: a.engine,
	}
	go func() {
		fmt.Fprintf(os.Stderr, "keyway listening on %s\n", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "服务异常退出: %v\n", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "优雅退出失败: %v\n", err)
	}
	a.stop()
}
