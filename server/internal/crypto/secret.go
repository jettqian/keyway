package crypto

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// ParseSecret 解析主密钥 KEYWAY_SECRET：
// 接受 32 字节原文、64 位 hex（openssl rand -hex 32）或 base64
// （openssl rand -base64 32，解码后须为 32 字节）
func ParseSecret(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("密钥为空")
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("密钥格式无效：需 32 字节原文、hex 或 base64 编码，实际长度 %d", len(s))
}
