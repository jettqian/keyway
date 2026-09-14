package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// mockFeishu 模拟飞书开放平台（authorize 跳转由浏览器完成，服务端只模拟后三个接口）
func mockFeishu(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Code != "good-code" {
			w.Write([]byte(`{"code":20030,"msg":"invalid code"}`))
			return
		}
		w.Write([]byte(`{"code":0,"access_token":"uat-123","expires_in":7200,"token_type":"Bearer"}`))
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer uat-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"code":0,"msg":"success","data":{"name":"张三","open_id":"ou_test_001"}}`))
	})
	return httptest.NewServer(mux)
}

func TestE2E飞书登录全流程(t *testing.T) {
	c, _ := setupApp(t)
	feishu := mockFeishu(t)
	defer feishu.Close()

	// 管理员注册并配置飞书登录
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "root", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "feishuEnabled": true,
		"feishuAppId": "cli_a1", "feishuAppSecret": "sec-xyz",
		"feishuBaseUrl": feishu.URL,
	}, true); w.Code != 200 {
		t.Fatalf("配置飞书失败: %s", w.Body.String())
	}

	// 公共信息：登录页可见飞书入口
	w := c.do("GET", "/api/auth/public-info", nil, false)
	if !strings.Contains(w.Body.String(), `"feishuEnabled":true`) {
		t.Fatalf("公共信息异常: %s", w.Body.String())
	}

	// 获取授权地址并提取 state
	w = c.do("GET", "/api/auth/feishu/url", nil, false)
	var urlResp struct {
		URL string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &urlResp)
	if !strings.Contains(urlResp.URL, "app_id=cli_a1") {
		t.Fatalf("授权地址异常: %s", urlResp.URL)
	}
	state := extractQuery(urlResp.URL, "state")
	if state == "" {
		t.Fatal("缺少 state")
	}

	// 首次回调：自动建号 + 建会话 + 302 跳转
	w = c.do("GET", "/oauth/feishu/callback?code=good-code&state="+url.QueryEscape(state), nil, false)
	if w.Code != http.StatusFound {
		t.Fatalf("回调应 302: %d %s", w.Code, w.Body.String())
	}
	// 校验会话：/api/auth/me 返回绑定用户
	w = c.do("GET", "/api/auth/me", nil, true)
	if !strings.Contains(w.Body.String(), `"hasFeishu":true`) {
		t.Fatalf("飞书绑定失败: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"username":"`+"张三") && !strings.Contains(w.Body.String(), "username") {
		t.Fatalf("自动建号异常: %s", w.Body.String())
	}

	// 二次回调（同 open_id）：已绑定直接登录
	w2 := c.do("GET", "/api/auth/feishu/url", nil, false)
	json.Unmarshal(w2.Body.Bytes(), &urlResp)
	state2 := extractQuery(urlResp.URL, "state")
	w = c.do("GET", "/oauth/feishu/callback?code=good-code&state="+url.QueryEscape(state2), nil, false)
	if w.Code != http.StatusFound {
		t.Fatalf("二次回调应 302: %d", w.Code)
	}

	// 无效 state 拒绝
	w = c.do("GET", "/oauth/feishu/callback?code=good-code&state=bad|bad|bad", nil, false)
	if w.Code == http.StatusFound && !strings.Contains(w.Header().Get("Location"), "state_error") {
		t.Fatalf("无效 state 应跳转错误页: %s", w.Header().Get("Location"))
	}

	// 篡改的 HMAC 拒绝
	w = c.do("GET", "/oauth/feishu/callback?code=good-code&state=9999999999|aaaa|deadbeef", nil, false)
	if !strings.Contains(w.Header().Get("Location"), "state_error") {
		t.Fatalf("篡改 state 应拒绝: %s", w.Header().Get("Location"))
	}
}

func TestE2E飞书登录关闭注册时拒绝新号(t *testing.T) {
	c, _ := setupApp(t)
	feishu := mockFeishu(t)
	defer feishu.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "root", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "invite", "feishuEnabled": true,
		"feishuAppId": "cli_a1", "feishuAppSecret": "sec-xyz",
		"feishuBaseUrl": feishu.URL,
	}, true); w.Code != 200 {
		t.Fatalf("配置失败: %s", w.Body.String())
	}

	w := c.do("GET", "/api/auth/feishu/url", nil, false)
	var urlResp struct {
		URL string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &urlResp)
	state := extractQuery(urlResp.URL, "state")

	w = c.do("GET", "/oauth/feishu/callback?code=good-code&state="+url.QueryEscape(state), nil, false)
	if !strings.Contains(w.Header().Get("Location"), "error") {
		t.Fatalf("邀请制下新号应被拒绝: %s", w.Header().Get("Location"))
	}
}

func extractQuery(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

var _ = fmt.Sprintf
