package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.DataDir != "./data" || cfg.Port != 8080 || cfg.ProbeIntervalMin != 30 ||
		cfg.AttemptBudget != 3 || cfg.KeyCooldownSec != 60 || cfg.DefaultMaxTokens != 8192 ||
		cfg.BodyLimitMB != 50 || cfg.IdleStreamTimeoutSec != 300 || cfg.LogRetentionDays != 30 ||
		cfg.ResponseHeaderTimeoutSec != 1800 ||
		cfg.BreakerFailThreshold != 3 || cfg.BreakerCooldownSec != 300 || cfg.BreakerCooldownMaxSec != 3600 {
		t.Fatalf("默认值不符合 DESIGN §12: %+v", cfg)
	}
}

func TestValidateSecret(t *testing.T) {
	// 缺失
	if err := (Config{}).ValidateSecret(); err == nil || !strings.Contains(err.Error(), "openssl") {
		t.Fatalf("缺失密钥应提示生成命令: %v", err)
	}
	// 长度 33（无法通过 32B/hex/base64 校验）
	if err := (Config{Secret: strings.Repeat("a", 33)}).ValidateSecret(); err == nil {
		t.Fatal("无效密钥应报错")
	}
	// 合法：32 字节原文
	if err := (Config{Secret: strings.Repeat("a", 32)}).ValidateSecret(); err != nil {
		t.Fatalf("32 字节密钥应通过: %v", err)
	}
	// 合法：hex
	if err := (Config{Secret: strings.Repeat("1f", 32)}).ValidateSecret(); err != nil {
		t.Fatalf("hex 密钥应通过: %v", err)
	}
}
