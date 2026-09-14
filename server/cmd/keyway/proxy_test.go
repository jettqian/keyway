package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// forwardProxy 极简 HTTP 正向代理：转发绝对形式请求并打标记头
func forwardProxy(t *testing.T, marker string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "" || !strings.HasPrefix(r.RequestURI, "http") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req, err := http.NewRequest(r.Method, r.RequestURI, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("X-Via-Proxy", marker)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
}

// echoUpstream 回显代理标记头
func echoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		via := r.Header.Get("X-Via-Proxy")
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"via:` + via + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"echoBody":"` + lenMarker(body) + `"}`))
	}))
}

func lenMarker(b []byte) string { return strings.ReplaceAll(string(b), `"`, "'") }

func TestE2E公共代理优选与流量统计(t *testing.T) {
	c, _ := setupApp(t)
	upstream := echoUpstream(t)
	defer upstream.Close()
	proxy := forwardProxy(t, "p1")
	defer proxy.Close()

	// 注册（自动管理员）+ 密钥 + 渠道（allowPublicProxy=true）
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "carol", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	if w := c.do("POST", "/api/keys", map[string]any{"name": "k1", "value": "any-key"}, true); w.Code != 200 {
		t.Fatal("建密钥失败")
	} else {
		json.Unmarshal(w.Body.Bytes(), &keyResp)
	}

	// 管理员建公共代理
	if w := c.do("POST", "/api/admin/proxies", map[string]any{
		"name": "公共出口", "url": proxy.URL, "note": "测试",
	}, true); w.Code != 200 {
		t.Fatalf("建公共代理失败: %s", w.Body.String())
	}

	// 建渠道：上游直连也可达（本地），但通过 line_stats 让直连不健康 → 强制走公共代理
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "pub", "type": "openai",
		"baseUrls":         []string{upstream.URL},
		"keyIds":           []int64{keyResp.Key.ID},
		"models":           []string{"proxy-model"},
		"allowPublicProxy": true,
		"priority":         5, "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建渠道失败: %s", w.Body.String())
	} else {
		var chResp struct {
			Channel struct {
				ID int64 `json:"id"`
			} `json:"channel"`
		}
		json.Unmarshal(w.Body.Bytes(), &chResp)
		c.channelID = chResp.Channel.ID
	}

	// 签令牌
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	if w := c.do("POST", "/api/tokens", map[string]any{"name": "t1"}, true); w.Code != 200 {
		t.Fatal("签令牌失败")
	} else {
		json.Unmarshal(w.Body.Bytes(), &tokResp)
		c.token = tokResp.Plaintext
	}

	// 直连标记不健康、公共代理标记健康 → 请求走公共代理
	c.seedStats(t, c.channelID, upstream.URL, false, 900)
	// 公共代理路径（via=proxy:1）标记健康
	now := timeNow()
	lat := int64(5)
	ok1 := 1
	stat := lineStatRow(c.channelID, upstream.URL, "proxy:1", &now, &lat, &ok1)
	if err := c.store.DB().Create(&stat).Error; err != nil {
		t.Fatal(err)
	}

	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "proxy-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if !strings.Contains(w.Body.String(), "via:p1") {
		t.Fatalf("应经公共代理转发: %s", w.Body.String())
	}

	// 流量统计：proxy_usage 落库（手动 Flush）
	c.flushPM(t)
	var rows []struct {
		UserID  int64 `json:"userId"`
		ProxyID int64 `json:"proxyId"`
		Bytes   int64 `json:"bytes"`
	}
	c.store.DB().Raw("SELECT user_id, proxy_id, bytes FROM proxy_usage").Scan(&rows)
	if len(rows) != 1 || rows[0].ProxyID != 1 || rows[0].Bytes == 0 {
		t.Fatalf("流量统计异常: %+v", rows)
	}

	// 管理员查代理流量接口
	w = c.do("GET", "/api/admin/proxy_usage", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "bytes") {
		t.Fatalf("代理流量接口异常: %s", w.Body.String())
	}
}

func TestE2E管理员模板CRUD与复制(t *testing.T) {
	c, upstream := setupApp(t)

	// 管理员建模板
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "root", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	if w := c.do("POST", "/api/admin/templates", map[string]any{
		"name": "官方 OpenAI", "type": "openai",
		"baseUrls":        []string{upstream.URL},
		"models":          []string{"tpl-model"},
		"priorityDefault": 8, "allowPublicProxyDefault": false,
		"note": "测试模板", "enabled": true,
	}, true); w.Code != 200 {
		t.Fatalf("建模板失败: %s", w.Body.String())
	}

	// 用户侧可见模板
	w := c.do("GET", "/api/templates", nil, true)
	if !strings.Contains(w.Body.String(), "官方 OpenAI") {
		t.Fatalf("用户侧模板不可见: %s", w.Body.String())
	}

	// 复制模板：草稿（无密钥、停用），无需先建密钥
	w = c.do("POST", "/api/channels/from_template/1", nil, true)
	if w.Code != 200 {
		t.Fatalf("复制模板失败: %s", w.Body.String())
	}
	var copyResp struct {
		Channel struct {
			ID      int64   `json:"id"`
			Name    string  `json:"name"`
			KeyIDs  []int64 `json:"keyIds"`
			Enabled bool    `json:"enabled"`
		} `json:"channel"`
	}
	json.Unmarshal(w.Body.Bytes(), &copyResp)
	if copyResp.Channel.Name != "官方 OpenAI" {
		t.Fatalf("复制渠道名异常: %s", w.Body.String())
	}
	if len(copyResp.Channel.KeyIDs) != 0 || copyResp.Channel.Enabled {
		t.Fatalf("复制应为无密钥的停用草稿: %s", w.Body.String())
	}
	// 草稿不参与路由：该模型请求仍应 404（未配置其他渠道）
	if w := c.do("POST", "/api/tokens", map[string]any{"name": "t1"}, true); w.Code != 200 {
		t.Fatal("签令牌失败")
	} else {
		var tokResp struct {
			Plaintext string `json:"plaintext"`
		}
		json.Unmarshal(w.Body.Bytes(), &tokResp)
		c.token = tokResp.Plaintext
	}
	w = c.do("POST", "/v1/chat/completions", map[string]any{"model": "tpl-model"}, false)
	if w.Code != 404 {
		t.Fatalf("草稿不应参与路由: %d", w.Code)
	}

	// 模板停用后不再接受新复制，但不影响已复制渠道
	if w := c.do("PUT", "/api/admin/templates/1", map[string]any{
		"name": "官方 OpenAI", "type": "openai",
		"baseUrls": []string{upstream.URL},
		"models":   []string{"tpl-model"},
		"note":     "停用", "enabled": false,
	}, true); w.Code != 200 {
		t.Fatalf("更新模板失败: %s", w.Body.String())
	}
	w = c.do("POST", "/api/channels/from_template/1", nil, true)
	if w.Code == 200 {
		t.Fatal("停用模板不应接受复制")
	}

	// 删除模板：已复制渠道不受影响
	if w := c.do("DELETE", "/api/admin/templates/1", nil, true); w.Code != 200 {
		t.Fatalf("删除模板失败: %s", w.Body.String())
	}
	var count int64
	c.store.DB().Model(channelModel()).Where("id = ?", copyResp.Channel.ID).Count(&count)
	if count != 1 {
		t.Fatal("删除模板不应影响已复制渠道")
	}
}
