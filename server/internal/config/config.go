package config

import (
	"fmt"
	"os"
	"strconv"

	"keyway/internal/crypto"
)

type Config struct {
	Secret               string
	DataDir              string
	Port                 int
	BaseURL              string
	ProbeIntervalMin     int
	AttemptBudget        int
	KeyCooldownSec       int
	DefaultMaxTokens     int
	BodyLimitMB          int
	IdleStreamTimeoutSec int
	LogRetentionDays     int
	PricingSyncHours     int
}

func Load() Config {
	return Config{
		Secret:               envStr("KEYWAY_SECRET", ""),
		DataDir:              envStr("KEYWAY_DATA_DIR", "./data"),
		Port:                 envInt("KEYWAY_PORT", 8080),
		BaseURL:              envStr("KEYWAY_BASE_URL", ""),
		ProbeIntervalMin:     envInt("KEYWAY_PROBE_INTERVAL_MIN", 10),
		AttemptBudget:        envInt("KEYWAY_ATTEMPT_BUDGET", 3),
		KeyCooldownSec:       envInt("KEYWAY_KEY_COOLDOWN_S", 60),
		DefaultMaxTokens:     envInt("KEYWAY_DEFAULT_MAX_TOKENS", 8192),
		BodyLimitMB:          envInt("KEYWAY_BODY_LIMIT_MB", 50),
		IdleStreamTimeoutSec: envInt("KEYWAY_IDLE_STREAM_TIMEOUT_S", 300),
		LogRetentionDays:     envInt("KEYWAY_LOG_RETENTION_DAYS", 30),
		PricingSyncHours:     envInt("KEYWAY_PRICING_SYNC_HOURS", 24),
	}
}

// ValidateSecret 校验主密钥（DESIGN §4.3）：缺失或格式错误时返回带生成命令提示的错误
func (c Config) ValidateSecret() error {
	if c.Secret == "" {
		return fmt.Errorf("KEYWAY_SECRET 未设置；可用 `openssl rand -base64 32` 生成 32 字节主密钥并配置到环境变量")
	}
	if _, err := crypto.ParseSecret(c.Secret); err != nil {
		return fmt.Errorf("KEYWAY_SECRET 无效（需 32 字节，base64/hex 编码）: %w", err)
	}
	return nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
