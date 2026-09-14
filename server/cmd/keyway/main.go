// keyway 入口：配置加载、存储初始化、HTTP 服务与优雅退出
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/config"
	"keyway/internal/store"
)

// shutdownTimeout 优雅退出等待上限
const shutdownTimeout = 10 * time.Second

func main() {
	cfg := config.Load()
	if err := cfg.ValidateSecret(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}

	st, err := store.Open(store.Options{DataDir: cfg.DataDir})
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化存储失败:", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: newRouter(),
	}

	go func() {
		log.Printf("keyway 监听 %s（数据目录 %s）", srv.Addr, cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	// 优雅退出：SIGINT/SIGTERM → Shutdown（等待在途请求完成）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "优雅退出失败:", err)
	}
	log.Println("keyway 已退出")
}

// newRouter 构建 gin 路由（后续里程碑在 /v1、/api 挂载各服务）
func newRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return r
}
