package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// newProdEngine 模拟生产模式（内嵌前端资源）的 NoRoute 处理
func newProdEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	sub, err := fs.Sub(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>spa</html>")},
	}, ".")
	if err != nil {
		panic(err)
	}
	r.NoRoute(noRouteHandler(sub))
	return r
}

// setupDevStatic 构造带 ./web/dist 的开发目录并注册前端静态路由
func setupDevStatic(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldWD, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "dist", "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWD) })
	r := gin.New()
	Register(r)
	return r
}

// 生产模式：未注册 POST 路径（如 /responses）必须 404 JSON，
// 不能 SPA 回退成 200 HTML（客户端会把 HTML 当协议响应解析而表现为“无响应”）
func Test生产模式POST未知路径返回404而非HTML(t *testing.T) {
	r := newProdEngine()
	req := httptest.NewRequest("POST", "/responses", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST 未注册路径应 404，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "<html>") {
		t.Fatalf("POST 未注册路径不应返回前端 HTML: %s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("应返回 JSON，实际 Content-Type: %s", ct)
	}
}

// 生产模式：GET 未知路径保留 SPA 回退（前端路由）
func Test生产模式GET未知路径SPA回退(t *testing.T) {
	r := newProdEngine()
	req := httptest.NewRequest("GET", "/some/spa/route", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html>spa</html>") {
		t.Fatalf("GET 未知路径应回退 index.html，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 生产模式：API 前缀路径始终 404
func Test生产模式API前缀始终404(t *testing.T) {
	r := newProdEngine()
	req := httptest.NewRequest("GET", "/api/not-exist", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<html>") {
		t.Fatalf("API 前缀应 404 JSON，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 开发模式：POST 未注册路径同样不返回前端资源
func Test开发模式POST未知路径返回404(t *testing.T) {
	r := setupDevStatic(t)
	req := httptest.NewRequest("POST", "/responses", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<html>") {
		t.Fatalf("POST 未注册路径应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}
