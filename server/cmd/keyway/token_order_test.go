package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 令牌限定渠道的顺序与启用集合分离：关闭渠道（从 channelIds 移除）不影响
// channelOrder（面板全量顺序，含已关闭渠道）——关闭的渠道保持原位。
func TestTokenChannelOrder与启用集合分离(t *testing.T) {
	c, upstream := setupApp(t)
	defer upstream.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "order", "password": "password123"}, false); w.Code != 200 {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	w := c.do("POST", "/api/keys", map[string]any{"name": "k1", "value": "upstream-key"}, true)
	if w.Code != 200 {
		t.Fatalf("建密钥失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &keyResp)

	createChannel := func(name string, priority int) int64 {
		w := c.do("POST", "/api/channels", map[string]any{
			"name": name, "type": "openai", "baseUrls": []string{upstream.URL},
			"keyIds": []int64{keyResp.Key.ID}, "models": []string{"m-" + name},
			"priority": priority, "enabled": true,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道 %s 失败: %d %s", name, w.Code, w.Body.String())
		}
		var chResp struct {
			Channel struct {
				ID int64 `json:"id"`
			} `json:"channel"`
		}
		json.Unmarshal(w.Body.Bytes(), &chResp)
		return chResp.Channel.ID
	}
	ch1, ch2, ch3 := createChannel("alpha", 30), createChannel("beta", 20), createChannel("gamma", 10)

	assertIDs := func(label string, got, want []int64) {
		t.Helper()
		format := func(ids []int64) string {
			parts := make([]string, len(ids))
			for i, id := range ids {
				parts[i] = fmt.Sprint(id)
			}
			return strings.Join(parts, ",")
		}
		if format(got) != format(want) {
			t.Fatalf("%s = %v，期望 %v", label, got, want)
		}
	}

	type tokenDTO struct {
		ID           int64   `json:"id"`
		ChannelIDs   []int64 `json:"channelIds"`
		ChannelOrder []int64 `json:"channelOrder"`
	}

	// 创建即带 channelIds：order 应等于选中集合
	var createResp struct {
		Token tokenDTO `json:"token"`
	}
	if w = c.do("POST", "/api/tokens", map[string]any{"name": "tk", "channelIds": []int64{ch1, ch2, ch3}}, true); w.Code != 200 {
		t.Fatalf("建令牌失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	tokenID := createResp.Token.ID
	assertIDs("创建后 channelIds", createResp.Token.ChannelIDs, []int64{ch1, ch2, ch3})
	assertIDs("创建后 channelOrder", createResp.Token.ChannelOrder, []int64{ch1, ch2, ch3})

	put := func(body map[string]any) tokenDTO {
		w := c.do("PUT", fmt.Sprintf("/api/tokens/%d", tokenID), body, true)
		if w.Code != 200 {
			t.Fatalf("更新令牌失败(%v): %d %s", body["channelIds"], w.Code, w.Body.String())
		}
		var resp struct {
			Token tokenDTO `json:"token"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Token
	}

	// 关闭 ch2/ch3：channelIds 只留 ch1，channelOrder 保留全量顺序（渠道原位）
	dto := put(map[string]any{"channelIds": []int64{ch1}, "channelOrder": []int64{ch1, ch2, ch3}})
	assertIDs("关闭后 channelIds", dto.ChannelIDs, []int64{ch1})
	assertIDs("关闭后 channelOrder", dto.ChannelOrder, []int64{ch1, ch2, ch3})

	// 旧客户端只传 channelIds：channelOrder 不被改动
	dto = put(map[string]any{"channelIds": []int64{ch1, ch2}})
	assertIDs("旧客户端 channelIds", dto.ChannelIDs, []int64{ch1, ch2})
	assertIDs("旧客户端 channelOrder", dto.ChannelOrder, []int64{ch1, ch2, ch3})

	// 重新打开 ch3：启用集合恢复全量，顺序不变
	dto = put(map[string]any{"channelIds": []int64{ch1, ch2, ch3}, "channelOrder": []int64{ch1, ch2, ch3}})
	assertIDs("恢复后 channelIds", dto.ChannelIDs, []int64{ch1, ch2, ch3})
	assertIDs("恢复后 channelOrder", dto.ChannelOrder, []int64{ch1, ch2, ch3})

	// 列表接口回显持久化结果
	var listResp struct {
		Tokens []tokenDTO `json:"tokens"`
	}
	if w = c.do("GET", "/api/tokens", nil, true); w.Code != 200 {
		t.Fatalf("列表失败: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Tokens) != 1 {
		t.Fatalf("令牌数量 = %d，期望 1", len(listResp.Tokens))
	}
	assertIDs("列表 channelIds", listResp.Tokens[0].ChannelIDs, []int64{ch1, ch2, ch3})
	assertIDs("列表 channelOrder", listResp.Tokens[0].ChannelOrder, []int64{ch1, ch2, ch3})

	// channelOrder 含不属于当前用户的渠道 → 400
	if w := c.do("PUT", fmt.Sprintf("/api/tokens/%d", tokenID), map[string]any{"channelOrder": []int64{999}}, true); w.Code != 400 {
		t.Fatalf("越权 channelOrder 应 400，得到 %d", w.Code)
	}
}
