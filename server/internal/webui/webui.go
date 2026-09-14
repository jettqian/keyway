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
	r.NoRoute(func(c *gin.Context) {
		if isAPIPath(c.Request.URL.Path) {
			c.JSON(http.StatusNotFound, gin.H{"message": "not found"})
			return
		}
		serveFS(c, sub, time.Time{})
	})
}

func tryDevStatic(r *gin.Engine) {
	if _, err := os.Stat(devDir); err != nil {
		return
	}
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if isAPIPath(p) {
			c.JSON(http.StatusNotFound, gin.H{"message": "not found"})
			return
		}
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
