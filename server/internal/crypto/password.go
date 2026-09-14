package crypto

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword 生成 bcrypt 密码哈希
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(hash), nil
}

// CheckPassword 校验密码与 bcrypt 哈希是否匹配
func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
