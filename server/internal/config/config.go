package config

import (
	"fmt"
	"os"
	"strconv"

	"keyway/internal/crypto"
)

type Config struct {
	Secret                   string
	DataDir                  string
	Port                     int
	BaseURL                  string
	ProbeIntervalMin         int
	AttemptBudget            int
	KeyCooldownSec           int
	DefaultMaxTokens         int
	BodyLimitMB              int
	IdleStreamTimeoutSec     int
	ResponseHeaderTimeoutSec int
	LogRetentionDays         int
	PricingSyncHours         int
	FxSourceURL              string
	BreakerFailThreshold     int
	BreakerCooldownSec       int
	BreakerCooldownMaxSec    int
}

func Load() Config {
	return Config{
		Secret:  envStr("KEYWAY_SECRET", ""),
		DataDir: envStr("KEYWAY_DATA_DIR", "./data"),
		Port:    envInt("KEYWAY_PORT", 8080),
		BaseURL: envStr("KEYWAY_BASE_URL", ""),
		// v1.5.45 起默认 30 分钟：渠道×模型熔断器接管请求路径的健康反馈
		// （失败计数/半开恢复），探测退居线路排序与状态展示，频率下调降低
		// 探测对上游额度的消耗（KEYWAY_PROBE_INTERVAL_MIN 可覆盖）
		ProbeIntervalMin:     envInt("KEYWAY_PROBE_INTERVAL_MIN", 30),
		AttemptBudget:        envInt("KEYWAY_ATTEMPT_BUDGET", 3),
		KeyCooldownSec:       envInt("KEYWAY_KEY_COOLDOWN_S", 60),
		DefaultMaxTokens:     envInt("KEYWAY_DEFAULT_MAX_TOKENS", 8192),
		BodyLimitMB:          envInt("KEYWAY_BODY_LIMIT_MB", 50),
		IdleStreamTimeoutSec: envInt("KEYWAY_IDLE_STREAM_TIMEOUT_S", 300),
		// 对齐 new-api RELAY_RESPONSE_HEADER_TIMEOUT（默认 1800s）：
		// 非流式长推理响应头可能远超 60s；0 = 不限制
		ResponseHeaderTimeoutSec: envInt("KEYWAY_RESPONSE_HEADER_TIMEOUT_S", 1800),
		LogRetentionDays:         envInt("KEYWAY_LOG_RETENTION_DAYS", 30),
		PricingSyncHours:         envInt("KEYWAY_PRICING_SYNC_HOURS", 24),
		FxSourceURL:              envStr("KEYWAY_FX_SOURCE_URL", ""),
		// 渠道×模型熔断器（v1.5.45）：连续 N 个请求耗尽该渠道该模型的组合
		// 尝试后熔断，冷却 S 秒后半开试探，失败指数退避（上限 MAX 秒）
		BreakerFailThreshold:  envInt("KEYWAY_BREAKER_FAIL_THRESHOLD", 3),
		BreakerCooldownSec:    envInt("KEYWAY_BREAKER_COOLDOWN_S", 300),
		BreakerCooldownMaxSec: envInt("KEYWAY_BREAKER_COOLDOWN_MAX_S", 3600),
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
