// 上游错误来源标注（FR-R10，v1.5.78）：客户端拿到的上游 429/5xx 此前与 keyway
// 自身错误、熔断状态完全不可区分（渠道唯一时旁路放行，熔断与否错误体一模一样）。
// 现在透传前给 JSON 错误结构的 message 加统一前缀，并透传上游 Retry-After 头，
// 让 Agent 侧能直接读出「哪个渠道·哪条线路·是否熔断中」。
package relay

import (
	"bytes"
	"encoding/json"
	"net/url"
)

// srcTag 来源标注前缀：[keyway·渠道名·线路host(·熔断中)]
func srcTag(src string, fused bool) string {
	tag := "keyway·" + src
	if fused {
		tag += "·熔断中"
	}
	return "[" + tag + "] "
}

// lineHost 线路 URL 的 host（解析失败回退原文）
func lineHost(lineURL string) string {
	if u, err := url.Parse(lineURL); err == nil && u.Host != "" {
		return u.Host
	}
	return lineURL
}

// attemptSrc 一次尝试的来源标注（渠道名·线路 host）
func attemptSrc(a attempt) string {
	return a.rc.Channel.Name + "·" + lineHost(a.lineURL)
}

// annotateUpstreamError 在上游错误体的 message 前缀来源标注。仅当 body 是 JSON
// 对象且含 error.message / 顶层 message 字符串时改写；其余（非 JSON、数组、
// 无 message）一律保持原文，不破坏透传语义。数字经 UseNumber 保精度
func annotateUpstreamError(body []byte, src string, fused bool) []byte {
	if src == "" || len(body) == 0 {
		return body
	}
	prefix := srcTag(src, fused)
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return body
	}
	if errObj, ok := raw["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok {
			errObj["message"] = prefix + msg
			return remarshal(raw, body)
		}
		return body
	}
	if msg, ok := raw["message"].(string); ok {
		raw["message"] = prefix + msg
		return remarshal(raw, body)
	}
	return body
}

// remarshal 重新序列化；失败时回退原文（标注是尽力而为，不影响透传）
func remarshal(v any, fallback []byte) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return out
}
