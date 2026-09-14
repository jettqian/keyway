package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
)

// hkdfSHA256 手写 RFC 5869 HKDF（SHA-256，Go 1.22 尚无 crypto/hkdf）；
// salt 传 nil 时按规范使用空盐（等价于 HashLen 个零字节）
func hkdfSHA256(ikm, salt, info []byte, length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("输出长度须为正数，实际 %d", length)
	}
	if length > 255*sha256.Size {
		return nil, fmt.Errorf("输出长度超上限 %d", 255*sha256.Size)
	}
	extract := hmac.New(sha256.New, salt)
	extract.Write(ikm)
	prk := extract.Sum(nil)

	okm := make([]byte, 0, length+sha256.Size)
	var t []byte
	for i := byte(1); len(okm) < length; i++ {
		expand := hmac.New(sha256.New, prk)
		expand.Write(t)
		expand.Write(info)
		expand.Write([]byte{i})
		t = expand.Sum(nil)
		okm = append(okm, t...)
	}
	return okm[:length], nil
}
