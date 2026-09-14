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
	"keyway/internal/config"
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
	probeEngine := probe.New(st, cfg.Secret, routingSvc, pm, cfg)
	apiSvc := api.New(st, cfg.Secret, authSvc, probeEngine, pm)
	relaySvc := relay.NewServer(st, cfg.Secret, authSvc, routingSvc, logWriter, pm, cfg)

	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		logWriter.Start(stopWriter)
		close(writerDone)
	}()

	probeStop := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		probeEngine.Start(probeStop)
		close(probeDone)
	}()

	pmStop := make(chan struct{})
	pmDone := make(chan struct{})
	go func() {
		pm.Start(pmStop)
		close(pmDone)
	}()

	if os.Getenv("KEYWAY_DEBUG") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/v1", relaySvc.TokenAuth())
	{
		v1.POST("/chat/completions", relaySvc.HandleOpenAIChat)
		v1.POST("/completions", relaySvc.HandleOpenAIPassthrough("/completions"))
		v1.POST("/embeddings", relaySvc.HandleOpenAIPassthrough("/embeddings"))
		v1.GET("/models", relaySvc.HandleModels)
		v1.POST("/messages", relaySvc.HandleAnthropicMessages)
		v1.POST("/messages/count_tokens", relaySvc.HandleAnthropicCountTokens)
	}

	apiGroup := r.Group("/api")
	apiSvc.RegisterAuthRoutes(apiGroup)
	sessioned := apiGroup.Group("", apiSvc.SessionAuth())
	apiSvc.RegisterRoutes(sessioned)

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
			close(probeStop)
			select {
			case <-probeDone:
			case <-time.After(5 * time.Second):
			}
			close(pmStop)
			select {
			case <-pmDone:
			case <-time.After(5 * time.Second):
			}
		},
	}, nil
}

func main() {
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
