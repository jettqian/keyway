package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCookie安全属性(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sameSite http.SameSite
	}{
		{sessionCookie, http.SameSiteStrictMode},
		{feishuStateCookie, http.SameSiteLaxMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "https://keyway.example.com/", nil)
			(&Server{}).setCookie(c, tc.name, "test", 300)
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != tc.sameSite {
				t.Fatalf("Cookie 安全属性错误: %+v", cookies)
			}
		})
	}
}
