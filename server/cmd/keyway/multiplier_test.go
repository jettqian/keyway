package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"keyway/internal/convert"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// Test渠道计价模式 usd 倍率 / cny_ratio 换算比（PRD v0.9）
func Test渠道计价模式(t *testing.T) {
	c, _ := setupApp(t)
	c.bootstrap(t, "http://upstream.invalid")

	if err := c.store.DB().Save(&store.ModelPricing{
		Model: "price-model", InputPerM: 1, OutputPerM: 2, Currency: "USD", UpdatedAt: time.Now().Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	u := convert.Usage{PromptTokens: 10000, CompletionTokens: 1000}

	// USD 模式 ×0.8：10k 输入 → $0.008
	ic, _ := usage.ComputeCost(c.store.DB(), "price-model", "price-model", "usd", 0.8, 0, u)
	if *ic < 0.0079 || *ic > 0.0081 {
		t.Fatalf("usd 倍率费用错误: %v（期望 0.008）", *ic)
	}

	// CNY 模式：$1 官方用量实收 ¥0.5（micu 类中转），默认汇率 7.2
	// 10k 输入官方价 $0.01 → 渠道实付 ¥0.005 → 统计 $0.005/7.2 ≈ 0.000694
	ic2, _ := usage.ComputeCost(c.store.DB(), "price-model", "price-model", "cny_ratio", 1, 0.5, u)
	want := 0.01 * 0.5 / 7.2
	if *ic2 < want-0.000001 || *ic2 > want+0.000001 {
		t.Fatalf("cny_ratio 费用错误: %v（期望 %v）", *ic2, want)
	}

	// 管理员改汇率后生效（走管理 API → fxrate.ApplyRate → 价目缓存失效）
	if w := c.do("PUT", "/api/admin/settings", map[string]any{
		"registerMode": "open", "exchangeRateMode": "manual", "exchangeRate": 7.0,
	}, true); w.Code != 200 {
		t.Fatalf("设置汇率失败: %s", w.Body.String())
	}
	ic3, _ := usage.ComputeCost(c.store.DB(), "price-model", "price-model", "cny_ratio", 1, 0.5, u)
	want3 := 0.01 * 0.5 / 7.0
	if *ic3 < want3-0.000001 || *ic3 > want3+0.000001 {
		t.Fatalf("汇率未生效: %v（期望 %v）", *ic3, want3)
	}
}

// TestE2E令牌多渠道绑定与开关 令牌限定多渠道，渠道级开关控制实际路由
func TestE2E令牌多渠道绑定与开关(t *testing.T) {
	c, _ := setupApp(t)
	up1 := seededUpstream(t, "chan-a", 0)
	up2 := seededUpstream(t, "chan-b", 0)
	defer up1.Close()
	defer up2.Close()

	if w := c.do("POST", "/api/auth/register", map[string]any{"username": "dave", "password": "password123"}, false); w.Code != 200 {
		t.Fatal("注册失败")
	}
	var keyResp struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
	}
	if w := c.do("POST", "/api/keys", map[string]any{"name": "k", "value": "upstream-key"}, true); w.Code != 200 {
		t.Fatal("建密钥失败")
	} else {
		json.Unmarshal(w.Body.Bytes(), &keyResp)
	}

	// 两个渠道服务同一模型，c1 优先级高
	var ids []int64
	for i, up := range []string{up1.URL, up2.URL} {
		w := c.do("POST", "/api/channels", map[string]any{
			"name": nameByIndex(i), "type": "openai",
			"baseUrls": []string{up},
			"keyIds":   []int64{keyResp.Key.ID},
			"models":   []string{"pick-model"},
			"priority": 10 - i, "enabled": true,
		}, true)
		if w.Code != 200 {
			t.Fatalf("建渠道失败: %s", w.Body.String())
		}
		var chResp struct {
			Channel struct {
				ID int64 `json:"id"`
			} `json:"channel"`
		}
		json.Unmarshal(w.Body.Bytes(), &chResp)
		ids = append(ids, chResp.Channel.ID)
	}

	// 令牌绑定两个渠道
	var tokResp struct {
		Plaintext string `json:"plaintext"`
	}
	if w := c.do("POST", "/api/tokens", map[string]any{
		"name": "multi", "channelIds": ids,
	}, true); w.Code != 200 {
		t.Fatalf("多渠道令牌失败: %s", w.Body.String())
	} else {
		json.Unmarshal(w.Body.Bytes(), &tokResp)
		c.token = tokResp.Plaintext
	}

	// 路由到高优先级 c1
	w := c.do("POST", "/v1/chat/completions", map[string]any{"model": "pick-model", "messages": []map[string]any{{"role": "user", "content": "hi"}}}, false)
	if !strings.Contains(w.Body.String(), "chan-a") {
		t.Fatalf("应路由到高优先级渠道: %s", w.Body.String())
	}

	// 关掉 c1 → 自动落到 c2（按需开关）
	if w := c.do("PUT", "/api/channels/"+itoa(ids[0]), map[string]any{
		"name": nameByIndex(0), "type": "openai",
		"baseUrls": []string{up1.URL},
		"keyIds":   []int64{keyResp.Key.ID},
		"models":   []string{"pick-model"},
		"priority": 10, "enabled": false,
	}, true); w.Code != 200 {
		t.Fatalf("关闭渠道失败: %s", w.Body.String())
	}
	w = c.do("POST", "/v1/chat/completions", map[string]any{"model": "pick-model", "messages": []map[string]any{{"role": "user", "content": "hi"}}}, false)
	if !strings.Contains(w.Body.String(), "chan-b") {
		t.Fatalf("关闭高优先后应路由到次优先级渠道: %s", w.Body.String())
	}
}

func nameByIndex(i int) string {
	if i == 0 {
		return "c-first"
	}
	return "c-second"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
