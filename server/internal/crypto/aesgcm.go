package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// 用途常量：区分不同加密列的派生密钥（DESIGN §4.3）
const (
	PurposeKey   = "key"   // keys.value_enc
	PurposeToken = "token" // tokens.key_enc
	PurposeProxy = "proxy" // channels.proxy_url_enc、proxies.url_enc
)

const (
	aesKeySize  = 32 // AES-256
	gcmNonceLen = 12
	// hkdfInfoPrefix HKDF info 前缀，做用途域分隔
	hkdfInfoPrefix = "keyway:"
)

// deriveKey 由主密钥经 HKDF-SHA256 派生用途密钥
func deriveKey(secret, purpose string) ([]byte, error) {
	if purpose == "" {
		return nil, errors.New("用途不能为空")
	}
	ikm, err := ParseSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("解析主密钥失败: %w", err)
	}
	info := append([]byte(hkdfInfoPrefix), purpose...)
	return hkdfSHA256(ikm, nil, info, aesKeySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt AES-256-GCM 加密：密钥由 HKDF(主密钥, purpose) 派生，
// 随机 12B nonce 前置于密文（同明文两次加密结果不同）
func Encrypt(secret, purpose string, plaintext []byte) ([]byte, error) {
	key, err := deriveKey(secret, purpose)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成随机 nonce 失败: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt 解密 Encrypt 产出的密文（nonce 前置格式）
func Decrypt(secret, purpose string, ciphertext []byte) ([]byte, error) {
	key, err := deriveKey(secret, purpose)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	if len(ciphertext) < gcmNonceLen+gcm.Overhead() {
		return nil, errors.New("密文长度无效")
	}
	nonce, body := ciphertext[:gcmNonceLen], ciphertext[gcmNonceLen:]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("解密失败: %w", err)
	}
	return plaintext, nil
}
