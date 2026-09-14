package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	// GatewayTokenPrefix 网关令牌前缀（DESIGN §4.2）
	GatewayTokenPrefix = "sk-keyway-"
	// gatewayTokenBodyLen 32B 随机数的 base62 长度（256bit ≈ 43 字符）
	gatewayTokenBodyLen = 43
	// base62Limit 拒绝采样阈值（62*4=248），保证取模均匀
	base62Limit = 248
	base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// GenerateGatewayToken 生成网关令牌：sk-keyway- + 43 字符 base62（32B 随机）
func GenerateGatewayToken() (string, error) {
	buf := make([]byte, 64)
	out := make([]byte, 0, gatewayTokenBodyLen)
	for len(out) < gatewayTokenBodyLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("读取随机数失败: %w", err)
		}
		for _, b := range buf {
			if b >= base62Limit {
				continue
			}
			out = append(out, base62Chars[b%62])
			if len(out) == gatewayTokenBodyLen {
				break
			}
		}
	}
	return GatewayTokenPrefix + string(out), nil
}

// HashToken 令牌 sha256 十六进制，用于 tokens.key_hash 的 O(1) 查找
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
