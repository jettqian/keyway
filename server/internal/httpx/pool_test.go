package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 慢响应头上游：body 写入前先睡 2s
func slowHeaderServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Write([]byte("ok"))
	}))
}

func TestPool响应头超时生效(t *testing.T) {
	srv := slowHeaderServer()
	defer srv.Close()

	p := NewPool(1) // 1s 响应头超时
	cl, err := p.Get("")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := cl.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("慢响应头上游应超时失败")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("应在响应头超时处切断而非等完上游: %v", elapsed)
	}
}

func TestPool响应头超时零为不限制(t *testing.T) {
	srv := slowHeaderServer()
	defer srv.Close()

	p := NewPool(0) // 0 = 不限制
	cl, err := p.Get("")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cl.Get(srv.URL)
	if err != nil {
		t.Fatalf("0 应表示不限制: %v", err)
	}
	resp.Body.Close()
}
