package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 导出文件契约（与 api.configExportFile 对齐的测试侧镜像）
type backupFile struct {
	App      string `json:"app"`
	Format   string `json:"format"`
	Version  int    `json:"version"`
	Mode     string `json:"mode"`
	Keys     []struct {
		Name  string `json:"name"`
		Note  string `json:"note"`
		Value string `json:"value"`
	} `json:"keys"`
	Channels []struct {
		ID       int64    `json:"id"`
		Name     string   `json:"name"`
		BaseURLs []string `json:"baseUrls"`
		KeyNames []string `json:"keyNames"`
		ProxyURL string   `json:"proxyUrl"`
		Models   []string `json:"models"`
		Enabled  bool     `json:"enabled"`
	} `json:"channels"`
	Tokens []struct {
		Name      string `json:"name"`
		Plaintext string `json:"plaintext"`
	} `json:"tokens"`
}

type importResult struct {
	Result struct {
		KeysCreated     int      `json:"keysCreated"`
		KeysReused      int      `json:"keysReused"`
		KeysMissing     int      `json:"keysMissing"`
		ChannelsCreated int      `json:"channelsCreated"`
		ChannelsReused  int      `json:"channelsReused"`
		ChannelsDrafted int      `json:"channelsDrafted"`
		ChannelsSkipped int      `json:"channelsSkipped"`
		TokensCreated   int      `json:"tokensCreated"`
		TokensSkipped   int      `json:"tokensSkipped"`
		Warnings        []string `json:"warnings"`
	} `json:"result"`
}

func exportConfig(t *testing.T, c *ctx, secrets bool) backupFile {
	t.Helper()
	path := "/api/config/export"
	if secrets {
		path += "?secrets=1"
	}
	w := c.do("GET", path, nil, true)
	if w.Code != 200 {
		t.Fatalf("导出失败: %d %s", w.Code, w.Body.String())
	}
	var f backupFile
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatalf("导出文件不是合法 JSON: %v", err)
	}
	if f.App != "keyway" || f.Format != "user-config" || f.Version != 1 {
		t.Fatalf("导出文件头异常: %+v", f)
	}
	return f
}

// 完整导出包含明文，纯结构导出不含任何明文（FR-BK1）
func TestE2E配置导出双模式(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)

	full := exportConfig(t, c, true)
	if full.Mode != "full" {
		t.Fatalf("完整导出 mode = %s", full.Mode)
	}
	if len(full.Keys) != 1 || full.Keys[0].Name != "k1" || full.Keys[0].Value != "upstream-key" {
		t.Fatalf("完整导出密钥异常: %+v", full.Keys)
	}
	if len(full.Channels) != 1 || len(full.Channels[0].KeyNames) != 1 || full.Channels[0].KeyNames[0] != "k1" {
		t.Fatalf("完整导出渠道密钥引用异常: %+v", full.Channels)
	}
	if len(full.Tokens) != 1 || full.Tokens[0].Plaintext != c.token {
		t.Fatalf("完整导出令牌明文异常: %+v", full.Tokens)
	}

	structure := exportConfig(t, c, false)
	if structure.Mode != "structure" {
		t.Fatalf("纯结构导出 mode = %s", structure.Mode)
	}
	if len(structure.Keys) != 1 || structure.Keys[0].Value != "" {
		t.Fatalf("纯结构导出不应包含密钥明文: %+v", structure.Keys)
	}
	if len(structure.Tokens) != 1 || structure.Tokens[0].Plaintext != "" {
		t.Fatalf("纯结构导出不应包含令牌明文: %+v", structure.Tokens)
	}
	if structure.Channels[0].ProxyURL != "" {
		t.Fatalf("纯结构导出不应包含代理明文")
	}
}

// 跨实例迁移（FR-BK2）：完整导出 → 新实例导入 → 旧令牌/密钥/渠道全部就位，
// Agent 零改配
func TestE2E配置导入跨实例迁移(t *testing.T) {
	c1, upstream := setupApp(t)
	defer upstream.Close()
	aliceToken := c1.bootstrap(t, upstream.URL)
	file := exportConfig(t, c1, true)

	// 实例 2：独立存储，bob 注册后导入 alice 的完整备份
	c2, upstream2 := setupApp(t)
	defer upstream2.Close()
	if w := c2.do("POST", "/api/auth/register", map[string]any{"username": "bob", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	w := c2.do("POST", "/api/config/import", file, true)
	if w.Code != 200 {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	var r importResult
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.Result.KeysCreated != 1 || r.Result.ChannelsCreated != 1 || r.Result.TokensCreated != 1 {
		t.Fatalf("导入计数异常: %+v", r.Result)
	}
	if r.Result.KeysReused != 0 || r.Result.ChannelsDrafted != 0 || r.Result.TokensSkipped != 0 {
		t.Fatalf("全新实例不应有复用/草稿/跳过: %+v", r.Result)
	}

	// alice 的旧令牌在实例 2 上直接可用（作为 bob 鉴权），且渠道密钥值正确
	c2.token = aliceToken
	w = c2.do("POST", "/v1/chat/completions", map[string]any{
		"model": "test-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "upstream-ok") {
		t.Fatalf("迁移后旧令牌请求失败: %d %s", w.Code, w.Body.String())
	}
}

// 同实例合并导入（FR-BK3）：同名密钥/渠道复用、令牌同名跳过——重复导入幂等，
// 不产生副本
func TestE2E配置导入同实例合并(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)
	file := exportConfig(t, c, true)

	w := c.do("POST", "/api/config/import", file, true)
	if w.Code != 200 {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	var r importResult
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.Result.KeysCreated != 0 || r.Result.KeysReused != 1 {
		t.Fatalf("同名密钥应复用: %+v", r.Result)
	}
	if r.Result.ChannelsCreated != 0 || r.Result.ChannelsReused != 1 {
		t.Fatalf("同名渠道应复用不建副本: %+v", r.Result)
	}
	if r.Result.TokensCreated != 0 || r.Result.TokensSkipped != 1 {
		t.Fatalf("同名令牌应跳过: %+v", r.Result)
	}
	if len(r.Result.Warnings) == 0 {
		t.Fatalf("跳过项应有警告: %+v", r.Result)
	}

	// 重复导入后渠道数量不变（无副本堆积）
	w = c.do("GET", "/api/channels", nil, true)
	var chResp struct {
		Channels []struct {
			Name string `json:"name"`
		} `json:"channels"`
	}
	json.Unmarshal(w.Body.Bytes(), &chResp)
	if len(chResp.Channels) != 1 || chResp.Channels[0].Name != "c1" {
		t.Fatalf("重复导入不应产生渠道副本: %+v", chResp.Channels)
	}
}

// 纯结构导入（FR-BK1）：无密钥明文 → 渠道以草稿导入、令牌随机签发新值
func TestE2E配置导入纯结构(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)
	file := exportConfig(t, c, false)

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "bob", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	w := c.do("POST", "/api/config/import", file, true)
	if w.Code != 200 {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	var r importResult
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.Result.KeysCreated != 0 || r.Result.KeysMissing != 1 {
		t.Fatalf("无明文密钥不应创建: %+v", r.Result)
	}
	if r.Result.ChannelsCreated != 1 || r.Result.ChannelsDrafted != 1 {
		t.Fatalf("无密钥渠道应以草稿导入: %+v", r.Result)
	}
	if r.Result.TokensCreated != 1 {
		t.Fatalf("令牌应按结构新建: %+v", r.Result)
	}

	// 草稿渠道停用且未绑密钥；令牌为新随机值（不等于原令牌）
	w = c.do("GET", "/api/channels", nil, true)
	var chResp struct {
		Channels []struct {
			Name    string `json:"name"`
			KeyIDs  []int64 `json:"keyIds"`
			Enabled bool   `json:"enabled"`
		} `json:"channels"`
	}
	json.Unmarshal(w.Body.Bytes(), &chResp)
	if len(chResp.Channels) != 1 || chResp.Channels[0].Enabled || len(chResp.Channels[0].KeyIDs) != 0 {
		t.Fatalf("纯结构导入渠道应为无密钥草稿: %+v", chResp.Channels)
	}
	w = c.do("GET", "/api/tokens", nil, true)
	var tokResp struct {
		Tokens []struct {
			KeyPrefix string `json:"keyPrefix"`
		} `json:"tokens"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokResp)
	if len(tokResp.Tokens) != 1 {
		t.Fatalf("应有一个令牌: %+v", tokResp.Tokens)
	}
	if tokResp.Tokens[0].KeyPrefix == c.token[:16] {
		t.Fatalf("纯结构导入的令牌不应沿用原明文")
	}

	// 重复导入纯结构文件：全部同名跳过，幂等无副本（密钥无明文仍无法创建）
	w = c.do("POST", "/api/config/import", file, true)
	if w.Code != 200 {
		t.Fatalf("重复导入失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.Result.KeysCreated != 0 || r.Result.KeysMissing != 1 {
		t.Fatalf("重复导入密钥计数异常: %+v", r.Result)
	}
	if r.Result.ChannelsCreated != 0 || r.Result.ChannelsReused != 1 {
		t.Fatalf("重复导入渠道应跳过复用: %+v", r.Result)
	}
	if r.Result.TokensCreated != 0 || r.Result.TokensSkipped != 1 {
		t.Fatalf("重复导入令牌应跳过: %+v", r.Result)
	}
	w = c.do("GET", "/api/channels", nil, true)
	json.Unmarshal(w.Body.Bytes(), &chResp)
	if len(chResp.Channels) != 1 {
		t.Fatalf("重复导入不应产生渠道副本: %+v", chResp.Channels)
	}
}

// 非法文件拒绝（FR-BK4）：非 Keyway 配置文件 / 非法 JSON
func TestE2E配置导入非法文件(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()
	c.bootstrap(t, upstream.URL)

	if w := c.do("POST", "/api/config/import", map[string]any{"foo": 1}, true); w.Code != 400 {
		t.Fatalf("非 Keyway 文件应 400: %d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config/import", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Keyway-CSRF", "1")
	for _, ck := range c.jar.list() {
		req.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	c.e.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("非法 JSON 应 400: %d %s", w.Code, w.Body.String())
	}
}
