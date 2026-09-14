package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"keyway/internal/store"
)

// seededUpstream 双上游：标记型响应 + 可控延迟
func seededUpstream(t *testing.T, marker string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + marker + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
}

// seedStats 手工写入 line_stats（模拟探测器产出）
func (c *ctx) seedStats(t *testing.T, channelID int64, lineURL string, ok bool, latencyMs int64) {
	t.Helper()
	now := time.Now().Unix()
	okInt := 0
	if ok {
		okInt = 1
	}
	lat := latencyMs
	stat := store.LineStat{
		ChannelID: channelID, LineURL: lineURL, Via: "direct",
		LastProbeAt: &now, LatencyMs: &lat, Ok: &okInt,
	}
	if err := c.store.DB().Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
}

// ---------- proxy_test.go 辅助 ----------

func timeNow() int64 { return time.Now().Unix() }

func lineStatRow(channelID int64, lineURL, via string, now, lat *int64, ok *int) store.LineStat {
	return store.LineStat{
		ChannelID: channelID, LineURL: lineURL, Via: via,
		LastProbeAt: now, LatencyMs: lat, Ok: ok,
	}
}

func channelModel() store.Channel { return store.Channel{} }

// flushPM 手动刷新公共代理流量统计（测试用）
func (c *ctx) flushPM(t *testing.T) {
	t.Helper()
	c.pm.Flush()
}

func TestE2E测试按钮矩阵(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	w := c.do("POST", "/api/channels/1/test", nil, true)
	if w.Code != 200 {
		t.Fatalf("测试按钮失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []struct {
			LineURL   string `json:"lineUrl"`
			Via       string `json:"via"`
			OK        bool   `json:"ok"`
			LatencyMs int64  `json:"latencyMs"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 1 {
		t.Fatalf("矩阵结果数异常: %s", w.Body.String())
	}
	if !resp.Results[0].OK || resp.Results[0].Via != "direct" {
		t.Fatalf("探测结果异常: %s", w.Body.String())
	}
	// line_stats 应已落库
	var count int64
	c.store.DB().Model(&store.LineStat{}).Count(&count)
	if count != 1 {
		t.Fatalf("line_stats 未落库: %d", count)
	}
}

func TestE2E线路优选走低延迟(t *testing.T) {
	c, _ := setupApp(t)
	slow := seededUpstream(t, "slow-line", 150*time.Millisecond)
	fast := seededUpstream(t, "fast-line", 0)
	defer slow.Close()
	defer fast.Close()

	// 注册与建渠道：两条线路（录入顺序 slow 在前）
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "bob", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	if w := c.do("POST", "/api/keys", map[string]any{"name": "k1", "value": "upstream-key"}, true); w.Code != 200 {
		t.Fatal("建密钥失败")
	} else {
		json.Unmarshal(w.Body.Bytes(), &keyResp)
	}
	if w := c.do("POST", "/api/channels", map[string]any{
		"name": "multi-line", "type": "openai",
		"baseUrls": []string{slow.URL, fast.URL},
		"keyIds":   []int64{keyResp.Key.ID},
		"models":   []string{"pick-model"}, "priority": 5, "enabled": true,
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

	// 签发网关令牌
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	if w := c.do("POST", "/api/tokens", map[string]any{"name": "t1"}, true); w.Code != 200 {
		t.Fatalf("签令牌失败: %s", w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &tokResp)
		c.token = tokResp.Plaintext
	}

	// 未有探测数据：按录入顺序走 slow
	w := c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "pick-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if !strings.Contains(w.Body.String(), "slow-line") {
		t.Fatalf("无探测数据应按录入顺序: %s", w.Body.String())
	}

	// 写入探测数据：slow 不健康、fast 健康 → 走 fast
	c.seedStats(t, c.channelID, slow.URL, false, 900)
	c.seedStats(t, c.channelID, fast.URL, true, 5)
	w = c.do("POST", "/v1/chat/completions", map[string]any{
		"model": "pick-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if !strings.Contains(w.Body.String(), "fast-line") {
		t.Fatalf("探测数据应优选 fast 线路: %s", w.Body.String())
	}
}

func TestE2E逐密钥测试(t *testing.T) {
	c, upstream := setupApp(t)
	c.bootstrap(t, upstream.URL)

	w := c.do("POST", "/api/channels/1/test_keys", nil, true)
	if w.Code != 200 {
		t.Fatalf("逐密钥测试失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []struct {
			KeyID int64 `json:"keyId"`
			OK    bool  `json:"ok"`
		} `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("密钥测试结果异常: %s", w.Body.String())
	}
}
