package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// dist 内嵌前端构建产物（CI 构建时把 web/dist 拷贝到本目录）
//
//go:embed all:dist
var embedded embed.FS

var devDir = "web/dist"

// Register 注册前端静态路由：优先内嵌资源，本地开发回退 ./web/dist
func Register(r *gin.Engine) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		tryDevStatic(r)
		return
	}
	// 占位文件说明没有真实前端，走开发目录
	if data, _ := fs.ReadFile(sub, "index.html"); string(data) == "placeholder" {
		tryDevStatic(r)
		return
	}
	r.NoRoute(noRouteHandler(sub))
}

// noRouteHandler 生产（内嵌资源）模式的 NoRoute 处理：
// API 请求返回 404 JSON，其余回退 SPA 静态资源
func noRouteHandler(sub fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isAPIRequest(c) {
			c.JSON(http.StatusNotFound, gin.H{"message": "not found"})
			return
		}
		serveFS(c, sub, time.Time{})
	}
}

func tryDevStatic(r *gin.Engine) {
	if _, err := os.Stat(devDir); err != nil {
		return
	}
	r.NoRoute(func(c *gin.Context) {
		if isAPIRequest(c) {
			c.JSON(http.StatusNotFound, gin.H{"message": "not found"})
			return
		}
		p := c.Request.URL.Path
		if p == "/" {
			p = "/index.html"
		}
		http.ServeFile(c.Writer, c.Request, devDir+p)
	})
}

func serveFS(c *gin.Context, sub fs.FS, modTime time.Time) {
	p := strings.TrimPrefix(c.Request.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	f, err := sub.Open(p)
	if err != nil {
		// SPA 回退到 index.html
		f, err = sub.Open("index.html")
		if err != nil {
			c.String(http.StatusNotFound, "not found")
			return
		}
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		c.String(http.StatusInternalServerError, "资源不可读")
		return
	}
	http.ServeContent(c.Writer, c.Request, p, modTime, rs)
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/v1") || strings.HasPrefix(p, "/oauth")
}

// isAPIRequest 判定不应落入前端静态资源的请求：
// API 前缀路径，或任意非 GET/HEAD 方法（如中转端点 POST /responses，
// 客户端 base_url 不带 /v1 时路径无 API 前缀，误回 index.html 会让
// 客户端把 HTML 当协议响应解析而表现为“无响应”）
func isAPIRequest(c *gin.Context) bool {
	if isAPIPath(c.Request.URL.Path) {
		return true
	}
	return c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead
}
