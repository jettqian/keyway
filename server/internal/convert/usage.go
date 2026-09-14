package convert

import (
	"encoding/json"
	"math"
	"strings"
)

// DefaultMaxTokens OI→AN 方向 max_tokens 缺省值（Anthropic 必填）
const DefaultMaxTokens = 8192

// Usage 归一化用量结构（DESIGN §8.2.1）
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// NormalizeOpenAIUsage OpenAI usage 归一化：
// prompt_tokens 已含缓存部分；cached 取 prompt_tokens_details.cached_tokens
// （兼容 DeepSeek 旧字段 prompt_cache_hit_tokens）；无缓存写。
func NormalizeOpenAIUsage(u *OpenAIUsage) Usage {
	if u == nil {
		return Usage{}
	}
	cached := 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if cached == 0 {
		cached = u.PromptCacheHitTokens
	}
	return Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CachedTokens:     cached,
		CacheWriteTokens: 0,
	}
}

// NormalizeAnthropicUsage Anthropic usage 归一化：
// input / cache_read / cache_creation 三段互斥，总输入 = 三段之和。
func NormalizeAnthropicUsage(u *AnthropicUsage) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		PromptTokens:     u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CompletionTokens: u.OutputTokens,
		CachedTokens:     u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

// ---------- 内容抽取辅助 ----------

// openAITexts 抽取 OpenAI 消息内容中的全部文本段
func openAITexts(content any) []string {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []OpenAIContentPart:
		var out []string
		for _, p := range v {
			if p.Type == "text" && p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range v {
			raw, _ := json.Marshal(item)
			var p OpenAIContentPart
			if json.Unmarshal(raw, &p) == nil && p.Type == "text" && p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	}
	return nil
}

// openAIBlocks 把 OpenAI 消息内容转为 anthropic 内容块（text/image）
func openAIBlocks(content any) []AnthropicContentBlock {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: v}}
	case []OpenAIContentPart:
		return openAIPartBlocks(v)
	case []any:
		parts := make([]OpenAIContentPart, 0, len(v))
		for _, item := range v {
			raw, _ := json.Marshal(item)
			var p OpenAIContentPart
			if json.Unmarshal(raw, &p) == nil {
				parts = append(parts, p)
			}
		}
		return openAIPartBlocks(parts)
	}
	return nil
}

func openAIPartBlocks(parts []OpenAIContentPart) []AnthropicContentBlock {
	var out []AnthropicContentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				out = append(out, AnthropicContentBlock{Type: "text", Text: p.Text})
			}
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			if src := imageSourceFromURL(p.ImageURL.URL); src != nil {
				out = append(out, AnthropicContentBlock{Type: "image", Source: src})
			}
		}
	}
	return out
}

// imageSourceFromURL openai image_url → anthropic source；
// data: URI 解析 media_type 与 base64 数据，http(s) 直传
func imageSourceFromURL(u string) *AnthropicImageSource {
	if strings.HasPrefix(u, "data:") {
		// data:image/png;base64,xxxx
		rest := strings.TrimPrefix(u, "data:")
		var media, data string
		if i := strings.Index(rest, ","); i >= 0 {
			meta := rest[:i]
			data = rest[i+1:]
			media = strings.TrimSuffix(meta, ";base64")
		} else {
			return nil
		}
		if media == "" {
			return nil
		}
		return &AnthropicImageSource{Type: "base64", MediaType: media, Data: data}
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return &AnthropicImageSource{Type: "url", URL: u}
	}
	return nil
}

// imageURLFromSource anthropic source → openai image_url
func imageURLFromSource(s *AnthropicImageSource) string {
	if s == nil {
		return ""
	}
	switch s.Type {
	case "url":
		return s.URL
	case "base64":
		return "data:" + s.MediaType + ";base64," + s.Data
	}
	return ""
}

// anthropicSystemText 抽取 system 字段文本（string 或 blocks）
func anthropicSystemText(sys any) string {
	switch v := sys.(type) {
	case string:
		return v
	case []AnthropicSystemBlock:
		var texts []string
		for _, b := range v {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	case []any:
		var texts []string
		for _, item := range v {
			raw, _ := json.Marshal(item)
			var b AnthropicSystemBlock
			if json.Unmarshal(raw, &b) == nil && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// anthropicBlocks 归一化消息 content 为块列表
func anthropicBlocks(content any) []AnthropicContentBlock {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: v}}
	case []AnthropicContentBlock:
		return v
	case []any:
		raw, _ := json.Marshal(v)
		var blocks []AnthropicContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			return blocks
		}
	}
	return nil
}

// toolResultText 抽取 tool_result 的文本内容
func toolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []AnthropicContentBlock:
		var texts []string
		for _, b := range v {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	case []any:
		raw, _ := json.Marshal(v)
		var blocks []AnthropicContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			return toolResultText(blocks)
		}
	}
	return ""
}

// marshalCompact 紧凑序列化（失败返回 "{}"）
func marshalCompact(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// EstimateTokens Anthropic 请求本地近似 token 数（含 system 与工具定义）
func EstimateTokens(req *AnthropicMessagesRequest) int {
	total := 0.0
	count := func(s string) {
		if s != "" {
			total += math.Ceil(float64(len([]rune(s))) / 3.6)
		}
	}
	count(anthropicSystemText(req.System))
	for _, m := range req.Messages {
		for _, b := range anthropicBlocks(m.Content) {
			switch b.Type {
			case "text", "thinking":
				count(b.Text + b.Thinking)
			case "tool_result":
				count(toolResultText(b.Content))
			case "tool_use":
				count(b.Name)
				count(string(b.Input))
			}
		}
	}
	for _, t := range req.Tools {
		count(t.Name + t.Description)
		count(string(marshalCompact(t.InputSchema)))
	}
	return int(total)
}
