package pricing

import (
	"encoding/json"
	"testing"
)

// LiteLLM 解析：仅保留 chat 模式且输入输出价均大于 0 的条目；缓存价 0 归 NULL
func TestParseLiteLLM(t *testing.T) {
	body := []byte(`{
		"claude-sonnet-4-6": {"mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "cache_read_input_token_cost": 0.0000003, "cache_creation_input_token_cost": 0.00000375, "litellm_provider": "anthropic"},
		"text-embedding-3-small": {"mode": "embedding", "input_cost_per_token": 0.00000002, "output_cost_per_token": 0},
		"some-free-model": {"mode": "chat", "input_cost_per_token": 0, "output_cost_per_token": 0},
		"glm-5.3": {"mode": "chat", "input_cost_per_token": 0.00000111, "output_cost_per_token": 0.00000389, "litellm_provider": "zhipu"}
	}`)
	out, err := parseLiteLLM(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(out))
	}
	byName := map[string]ModelPrice{}
	for _, p := range out {
		byName[p.Model] = p
	}
	an := byName["claude-sonnet-4-6"]
	if an.InputPerM != 3 || an.OutputPerM != 15 {
		t.Errorf("输入/输出换算错误：%v/%v", an.InputPerM, an.OutputPerM)
	}
	if an.CachedInputPerM == nil || *an.CachedInputPerM != 0.3 {
		t.Errorf("缓存读错误：%v", an.CachedInputPerM)
	}
	if an.CacheWritePerM == nil || *an.CacheWritePerM != 3.75 {
		t.Errorf("缓存写错误：%v", an.CacheWritePerM)
	}
	glm := byName["glm-5.3"]
	if glm.CachedInputPerM != nil || glm.CacheWritePerM != nil {
		t.Errorf("无缓存价时应为 NULL：%v %v", glm.CachedInputPerM, glm.CacheWritePerM)
	}
}

// OpenRouter 解析：仅保留 text->text 且输入输出价可解析且大于 0 的条目
func TestParseOpenRouter(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openai/gpt-4o","architecture":{"modality":"text->text"},"pricing":{"prompt":"0.0000025","completion":"0.00001","input_cache_read":"0.00000125","input_cache_write":"0.000001875"}},
		{"id":"openai/gpt-4o-audio","architecture":{"modality":"text+audio->text"},"pricing":{"prompt":"0.0000025","completion":"0.0001"}},
		{"id":"meta-llama/llama-3.1-8b-instruct:free","architecture":{"modality":"text->text"},"pricing":{"prompt":"0","completion":"0"}},
		{"id":"no-pricing","architecture":{"modality":"text->text"}}
	]}`)
	out, err := parseOpenRouter(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(out))
	}
	p := out[0]
	if p.Model != "openai/gpt-4o" || p.InputPerM != 2.5 || p.OutputPerM != 10 {
		t.Errorf("映射错误：%+v", p)
	}
	if p.CachedInputPerM == nil || *p.CachedInputPerM != 1.25 {
		t.Errorf("缓存读错误：%v", p.CachedInputPerM)
	}
	if p.CacheWritePerM == nil || *p.CacheWritePerM != 1.875 {
		t.Errorf("缓存写错误：%v", p.CacheWritePerM)
	}
}

// 非法 JSON 报错而非 panic
func TestParseInvalid(t *testing.T) {
	if _, err := parseLiteLLM([]byte("not json")); err == nil {
		t.Error("LiteLLM 非法 JSON 应报错")
	}
	if _, err := parseOpenRouter([]byte("not json")); err == nil {
		t.Error("OpenRouter 非法 JSON 应报错")
	}
	var _ = json.Marshal // 保留 import
}
