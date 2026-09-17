package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// linksViaAPI GET /api/stats/links 或 /api/admin/stats/links（会话）
func (c *ctx) linksViaAPI(t *testing.T, path string) []map[string]any {
	t.Helper()
	w := c.do("GET", path, nil, true)
	if w.Code != 200 {
		t.Fatalf("查询链路状态失败（%s）: %d %s", path, w.Code, w.Body.String())
	}
	var resp struct {
		Links []map[string]any `json:"links"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	return resp.Links
}

func findLink(links []map[string]any, model string) map[string]any {
	for _, l := range links {
		if l["model"] == model {
			return l
		}
	}
	return nil
}

// TestE2E链路状态 渠道×模型链路状态（FR-B7）：用户只见自己渠道，管理端全量
// （含所有者），熔断状态叠加，熔断中但窗口内零尝试的组合补零行展示
func TestE2E链路状态(t *testing.T) {
	c, _ := setupApp(t)
	up := newBreakerUpstream(map[string]int{"m-broken": 503})
	defer up.srv.Close()

	// 首个注册用户 = 管理员，建自己的渠道并发一次请求（验证用户隔离）
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "linkadmin", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册管理员失败: %d %s", w.Code, w.Body.String())
	}
	setupBreaker(t, c, []string{up.srv.URL}, nil, []string{"m-admin"})
	if w := c.chat(t, "m-admin"); w.Code != 200 {
		t.Fatalf("管理员渠道请求应成功: %d", w.Code)
	}
	adminJar := c.jar

	// 普通用户 linku：m-ok 正常、m-broken 三次失败熔断（单渠道 → 旁路透传 503）
	c.jar = newSimpleJar()
	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "linku", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册 linku 失败: %d %s", w.Code, w.Body.String())
	}
	setupBreaker(t, c, []string{up.srv.URL}, nil, []string{"m-ok", "m-broken"})
	for i := 0; i < 2; i++ {
		if w := c.chat(t, "m-ok"); w.Code != 200 {
			t.Fatalf("m-ok 请求 %d 应成功: %d", i, w.Code)
		}
	}
	for i := 0; i < 3; i++ {
		if w := c.chat(t, "m-broken"); w.Code != 503 {
			t.Fatalf("m-broken 请求 %d 应透传 503: %d", i, w.Code)
		}
	}

	// linku 视角：只有自己的渠道（看不到管理员的 m-admin），熔断状态叠加。
	// 日志异步批写（1s 节拍），轮询等待落库
	var links []map[string]any
	for i := 0; i < 40; i++ {
		if links = c.linksViaAPI(t, "/api/stats/links"); len(links) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(links) != 2 {
		t.Fatalf("linku 应见 2 条链路（不含管理员渠道），实际 %d: %v", len(links), links)
	}
	ok := findLink(links, "m-ok")
	if ok == nil || ok["attempts"].(float64) != 2 || ok["ok"].(float64) != 2 || ok["errorRate"].(float64) != 0 {
		t.Fatalf("m-ok 链路不符: %v", ok)
	}
	if _, has := ok["owner"]; has {
		t.Fatalf("用户视角不应含 owner 字段: %v", ok)
	}
	broken := findLink(links, "m-broken")
	if broken == nil || broken["attempts"].(float64) != 3 || broken["ok"].(float64) != 0 || broken["errorRate"].(float64) != 100 {
		t.Fatalf("m-broken 链路不符: %v", broken)
	}
	bk, _ := broken["breaker"].(map[string]any)
	if bk == nil || bk["failCount"].(float64) != 3 {
		t.Fatalf("m-broken 应叠加熔断快照（failCount=3）: %v", broken)
	}
	if name, _ := broken["channelName"].(string); name != "brk-1" {
		t.Fatalf("链路应含渠道名: %v", broken)
	}

	// 熔断中但窗口内零尝试（窗口设在未来）：仍以零尝试行可见
	future := c.linksViaAPI(t, "/api/stats/links?start=2099-01-01")
	if len(future) != 1 || future[0]["model"] != "m-broken" || future[0]["attempts"].(float64) != 0 {
		t.Fatalf("未来窗口应只剩 m-broken 零尝试行: %v", future)
	}
	if _, _ = future[0]["breaker"].(map[string]any); future[0]["breaker"] == nil {
		t.Fatalf("零尝试行应带熔断快照: %v", future[0])
	}

	// 管理端：全量（含管理员自己的渠道），行含所有者
	c.jar = adminJar
	admin := c.linksViaAPI(t, "/api/admin/stats/links")
	if len(admin) != 3 {
		t.Fatalf("管理端应见全量 3 条链路，实际 %d: %v", len(admin), admin)
	}
	if l := findLink(admin, "m-admin"); l == nil || l["owner"] != "linkadmin" {
		t.Fatalf("管理端 m-admin 行应含所有者 linkadmin: %v", l)
	}
	if l := findLink(admin, "m-broken"); l == nil || l["owner"] != "linku" || l["breaker"] == nil {
		t.Fatalf("管理端 m-broken 行应含所有者与熔断态: %v", l)
	}
	// 管理员自身的用户视角同样只见自己的渠道
	mine := c.linksViaAPI(t, "/api/stats/links")
	if len(mine) != 1 || mine[0]["model"] != "m-admin" {
		t.Fatalf("管理员用户视角应只见自己的 m-admin: %v", mine)
	}
}

// TestE2E链路状态删除渠道后不可见 渠道删除后其链路行不再出现（含熔断补零行）
func TestE2E链路状态删除渠道后不可见(t *testing.T) {
	c, _ := setupApp(t)
	up := newBreakerUpstream(map[string]int{"m-x": 503})
	defer up.srv.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "linkdel", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	setupBreaker(t, c, []string{up.srv.URL}, nil, []string{"m-x"})
	ch1 := c.firstChannelID(t)
	for i := 0; i < 3; i++ {
		c.chat(t, "m-x")
	}
	// 日志异步批写（1s 节拍），轮询等待落库
	var links []map[string]any
	for i := 0; i < 40; i++ {
		if links = c.linksViaAPI(t, "/api/stats/links"); len(links) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(links) != 1 {
		t.Fatalf("删除前应见 1 条链路: %v", links)
	}
	if w := c.do("DELETE", fmt.Sprintf("/api/channels/%d", ch1), nil, true); w.Code != 200 {
		t.Fatalf("删除渠道失败: %d %s", w.Code, w.Body.String())
	}
	if links := c.linksViaAPI(t, "/api/stats/links"); len(links) != 0 {
		t.Fatalf("删除渠道后链路（含熔断补零行）应不可见: %v", links)
	}
}
