package crypto

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// 测试主密钥（hex 编码 32B）
var testSecret = strings.Repeat("1f", 32)

func TestParseSecret(t *testing.T) {
	// raw 32 字节
	if _, err := ParseSecret(strings.Repeat("x", 32)); err != nil {
		t.Fatalf("32 字节原文应通过: %v", err)
	}
	// hex 64 位
	if _, err := ParseSecret(testSecret); err != nil {
		t.Fatalf("hex 应通过: %v", err)
	}
	// base64（0x00-0x1f 共 32 字节）
	if _, err := ParseSecret("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="); err != nil {
		t.Fatalf("base64 应通过: %v", err)
	}
	for _, bad := range []string{"", "short", "zz", strings.Repeat("g", 63), "AAECAwQFBgcICQoLDA0ODxA="} {
		if _, err := ParseSecret(bad); err == nil {
			t.Errorf("无效密钥 %q 应报错", bad)
		}
	}
}

func TestHKDFRFC5869Vectors(t *testing.T) {
	ikm := bytes.Repeat([]byte{0x0b}, 22)
	// A.1：带 salt 与 info
	okm, err := hkdfSHA256(
		ikm,
		[]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c},
		[]byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9},
		42,
	)
	if err != nil {
		t.Fatalf("A.1 执行失败: %v", err)
	}
	want1 := "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"
	if got := hex.EncodeToString(okm); got != want1 {
		t.Fatalf("A.1 不匹配:\n got  %s\n want %s", got, want1)
	}
	// A.2：空盐、空 info
	okm, err = hkdfSHA256(ikm, nil, nil, 42)
	if err != nil {
		t.Fatalf("A.2 执行失败: %v", err)
	}
	want2 := "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8"
	if got := hex.EncodeToString(okm); got != want2 {
		t.Fatalf("A.2 不匹配:\n got  %s\n want %s", got, want2)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintexts := [][]byte{
		nil,
		[]byte(""),
		[]byte("sk-upstream-abc123"),
		bytes.Repeat([]byte{0x00, 0xff, 0x80}, 100),
	}
	for _, purpose := range []string{PurposeKey, PurposeToken, PurposeProxy} {
		for i, pt := range plaintexts {
			ct, err := Encrypt(testSecret, purpose, pt)
			if err != nil {
				t.Fatalf("purpose=%s 加密 #%d 失败: %v", purpose, i, err)
			}
			got, err := Decrypt(testSecret, purpose, ct)
			if err != nil {
				t.Fatalf("purpose=%s 解密 #%d 失败: %v", purpose, i, err)
			}
			if !bytes.Equal(got, pt) {
				t.Errorf("purpose=%s 明文 #%d 不一致: got %q want %q", purpose, i, got, pt)
			}
		}
	}
}

func TestEncryptWrongSecretOrPurpose(t *testing.T) {
	ct, err := Encrypt(testSecret, PurposeKey, []byte("secret-value"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	otherSecret := strings.Repeat("2e", 32)
	if _, err := Decrypt(otherSecret, PurposeKey, ct); err == nil {
		t.Error("错误密钥解密应失败")
	}
	if _, err := Decrypt(testSecret, PurposeToken, ct); err == nil {
		t.Error("错误用途解密应失败")
	}
	if _, err := Decrypt("", PurposeKey, ct); err == nil {
		t.Error("空密钥解密应失败")
	}
}

func TestEncryptNonceRandom(t *testing.T) {
	pt := []byte("same-plaintext")
	ct1, err := Encrypt(testSecret, PurposeToken, pt)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	ct2, err := Encrypt(testSecret, PurposeToken, pt)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("同明文两次加密密文应不同（nonce 应随机）")
	}
}

func TestDecryptTampered(t *testing.T) {
	ct, err := Encrypt(testSecret, PurposeProxy, []byte("proxy-url"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := Decrypt(testSecret, PurposeProxy, tampered); err == nil {
		t.Error("篡改密文解密应失败")
	}
	if _, err := Decrypt(testSecret, PurposeProxy, ct[:10]); err == nil {
		t.Error("截断密文解密应失败")
	}
}

func TestGenerateGatewayToken(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		tok, err := GenerateGatewayToken()
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if !strings.HasPrefix(tok, GatewayTokenPrefix) {
			t.Fatalf("前缀错误: %s", tok)
		}
		if len(tok) != len(GatewayTokenPrefix)+43 {
			t.Fatalf("长度错误: %d (%s)", len(tok), tok)
		}
		for _, c := range tok[len(GatewayTokenPrefix):] {
			if !strings.ContainsRune(base62Chars, c) {
				t.Fatalf("含非 base62 字符 %q: %s", c, tok)
			}
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("令牌重复: %s", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestHashToken(t *testing.T) {
	// sha256("abc") 标准向量
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("哈希不匹配: %s", got)
	}
	if HashToken("a") == HashToken("b") {
		t.Fatal("不同令牌哈希应不同")
	}
	if HashToken("abc") != HashToken("abc") {
		t.Fatal("哈希应稳定")
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("pw-123456")
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if err := CheckPassword(hash, "pw-123456"); err != nil {
		t.Fatalf("正确密码校验失败: %v", err)
	}
	if err := CheckPassword(hash, "wrong"); err == nil {
		t.Fatal("错误密码应校验失败")
	}
	if err := CheckPassword("not-a-bcrypt-hash", "pw-123456"); err == nil {
		t.Fatal("非法哈希应报错")
	}
}
