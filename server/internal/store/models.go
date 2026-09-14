package store

// GORM 模型严格对齐 DESIGN §3 DDL：
// - JSON 列（base_urls_json 等）以 string 存储，由上层自行 marshal
// - DDL 中可空列用指针类型表达
// - 各模型显式声明 TableName，避免复数化歧义（如 proxy_usage）

// User 用户（密码或飞书登录）
type User struct {
	ID           int64   `gorm:"column:id;primaryKey;autoIncrement"`
	Username     string  `gorm:"column:username;not null;uniqueIndex"`
	PasswordHash *string `gorm:"column:password_hash"` // 飞书-only 用户为 NULL
	FeishuUserID *string `gorm:"column:feishu_user_id;uniqueIndex"`
	Role         int     `gorm:"column:role;not null;default:1"`   // 1 user / 100 admin
	Status       int     `gorm:"column:status;not null;default:1"` // 1 启用 / 2 禁用
	CreatedAt    int64   `gorm:"column:created_at"`
	LastLoginAt  *int64  `gorm:"column:last_login_at"`
}

func (User) TableName() string { return "users" }

// Session Web 会话（DB-backed，可即时吊销）
type Session struct {
	TokenHash string `gorm:"column:token_hash;primaryKey"`
	UserID    int64  `gorm:"column:user_id;not null"`
	ExpiresAt int64  `gorm:"column:expires_at;not null"`
}

func (Session) TableName() string { return "sessions" }

// Key 上游密钥池
type Key struct {
	ID            int64   `gorm:"column:id;primaryKey;autoIncrement"`
	UserID        int64   `gorm:"column:user_id;not null;uniqueIndex:idx_keys_user_name,priority:1"`
	Name          string  `gorm:"column:name;not null;uniqueIndex:idx_keys_user_name,priority:2"`
	ValueEnc      []byte  `gorm:"column:value_enc;not null"`
	Note          string  `gorm:"column:note;default:''"`
	Status        int     `gorm:"column:status;not null;default:1"` // 1 启用 / 2 禁用
	LastError     *string `gorm:"column:last_error"`
	CooldownUntil int64   `gorm:"column:cooldown_until;default:0"`
	CreatedAt     int64   `gorm:"column:created_at"`
}

func (Key) TableName() string { return "keys" }

// ChannelTemplate 预制渠道模板（管理员维护）
type ChannelTemplate struct {
	ID                      int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name                    string `gorm:"column:name;not null"`
	Type                    string `gorm:"column:type;not null"`           // openai|anthropic
	BaseURLsJSON            string `gorm:"column:base_urls_json;not null"` // 线路数组 ≤5
	LineStrategy            string `gorm:"column:line_strategy;not null;default:'auto'"`
	ModelsJSON              string `gorm:"column:models_json;not null"`
	ModelMappingJSON        string `gorm:"column:model_mapping_json;default:'{}'"`
	PriorityDefault         int    `gorm:"column:priority_default;not null;default:0"`
	AllowPublicProxyDefault int    `gorm:"column:allow_public_proxy_default;not null;default:0"`
	Note                    string `gorm:"column:note;default:''"`
	Enabled                 int    `gorm:"column:enabled;not null;default:1"`
	CopyCount               int    `gorm:"column:copy_count;not null;default:0"`
	UpdatedAt               int64  `gorm:"column:updated_at"`
}

func (ChannelTemplate) TableName() string { return "channel_templates" }

// Channel 用户渠道
type Channel struct {
	ID                   int64   `gorm:"column:id;primaryKey;autoIncrement"`
	UserID               int64   `gorm:"column:user_id;not null;index:idx_channels_user,priority:1"`
	CopiedFromTemplateID *int64  `gorm:"column:copied_from_template_id"` // 仅来源标记，不参与路由
	Name                 string  `gorm:"column:name;not null"`
	Type                 string  `gorm:"column:type;not null"`                           // openai|anthropic
	BaseURLsJSON         string  `gorm:"column:base_urls_json;not null"`                 // ≤5
	KeyIDsJSON           string  `gorm:"column:key_ids_json;not null"`                   // ≤5 有序
	KeyStrategy          string  `gorm:"column:key_strategy;not null;default:'ordered'"` // ordered|round_robin
	LineStrategy         string  `gorm:"column:line_strategy;not null;default:'auto'"`
	ProxyURLEnc          []byte  `gorm:"column:proxy_url_enc"` // 个人代理（可空）
	AllowPublicProxy     int     `gorm:"column:allow_public_proxy;not null;default:0"`
	ModelsJSON           string  `gorm:"column:models_json;not null"`
	ModelMappingJSON     string  `gorm:"column:model_mapping_json;default:'{}'"`
	Priority             int     `gorm:"column:priority;not null;default:0"`
	PriceMultiplier      float64 `gorm:"column:price_multiplier;not null;default:1"` // 渠道价格倍率（优惠渠道 <1）
	IsDefault            int     `gorm:"column:is_default;not null;default:0"`
	Enabled              int     `gorm:"column:enabled;not null;default:1;index:idx_channels_user,priority:2"`
	LastOkAt             *int64  `gorm:"column:last_ok_at"`
	LastError            *string `gorm:"column:last_error"`
	CreatedAt            int64   `gorm:"column:created_at"`
}

func (Channel) TableName() string { return "channels" }

// LineStat 探测结果（渠道×线路×路径）
type LineStat struct {
	ChannelID   int64   `gorm:"column:channel_id;primaryKey;autoIncrement:false"`
	LineURL     string  `gorm:"column:line_url;primaryKey"`
	Via         string  `gorm:"column:via;primaryKey"` // direct | personal | proxy:{id}
	LastProbeAt *int64  `gorm:"column:last_probe_at"`
	LatencyMs   *int64  `gorm:"column:latency_ms"`
	Ok          *int    `gorm:"column:ok"`
	LastError   *string `gorm:"column:last_error"`
}

func (LineStat) TableName() string { return "line_stats" }

// Proxy 管理员公共代理池
type Proxy struct {
	ID        int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string `gorm:"column:name;not null"`
	URLEnc    []byte `gorm:"column:url_enc;not null"`
	Enabled   int    `gorm:"column:enabled;not null;default:1"`
	Note      string `gorm:"column:note;default:''"`
	CreatedAt int64  `gorm:"column:created_at"`
}

func (Proxy) TableName() string { return "proxies" }

// ProxyUsage 公共代理按用户日流量（仅统计）
type ProxyUsage struct {
	UserID  int64  `gorm:"column:user_id;primaryKey;autoIncrement:false"`
	ProxyID int64  `gorm:"column:proxy_id;primaryKey"`
	Day     string `gorm:"column:day;primaryKey"` // YYYY-MM-DD
	Bytes   int64  `gorm:"column:bytes;not null;default:0"`
}

func (ProxyUsage) TableName() string { return "proxy_usage" }

// Token 网关令牌
type Token struct {
	ID         int64   `gorm:"column:id;primaryKey;autoIncrement"`
	UserID     int64   `gorm:"column:user_id;not null"`
	Name       string  `gorm:"column:name;not null"`
	KeyEnc     []byte  `gorm:"column:key_enc;not null"`              // 全文加密（支持界面回看）
	KeyPrefix  string  `gorm:"column:key_prefix;not null"`           // 展示与日志用
	KeyHash    string  `gorm:"column:key_hash;not null;uniqueIndex"` // sha256，认证 O(1) 查找
	ChannelID  *int64  `gorm:"column:channel_id"`                    // 限定渠道（可空）
	ModelScope *string `gorm:"column:model_scope"`                   // 模型前缀通配（可空）
	ExpiresAt  *int64  `gorm:"column:expires_at"`
	Revoked    int     `gorm:"column:revoked;not null;default:0"`
	CreatedAt  int64   `gorm:"column:created_at"`
}

func (Token) TableName() string { return "tokens" }

// ModelPricing 模型价目（每百万 token）
type ModelPricing struct {
	Model           string   `gorm:"column:model;primaryKey" json:"model"`
	InputPerM       float64  `gorm:"column:input_per_m;not null" json:"inputPerM"`
	CachedInputPerM *float64 `gorm:"column:cached_input_per_m" json:"cachedInputPerM"` // NULL 回退 input_per_m
	CacheWritePerM  *float64 `gorm:"column:cache_write_per_m" json:"cacheWritePerM"`   // NULL 回退 input_per_m
	OutputPerM      float64  `gorm:"column:output_per_m;not null" json:"outputPerM"`
	Currency        string   `gorm:"column:currency;not null;default:'USD'" json:"currency"`
	UpdatedAt       int64    `gorm:"column:updated_at" json:"updatedAt"`
}

func (ModelPricing) TableName() string { return "model_pricing" }

// Log 请求日志（异步批写）
type Log struct {
	ID               int64    `gorm:"column:id;primaryKey;autoIncrement"`
	CreatedAt        int64    `gorm:"column:created_at;not null"`
	UserID           int64    `gorm:"column:user_id;not null"`
	TokenID          *int64   `gorm:"column:token_id"`
	ChannelID        *int64   `gorm:"column:channel_id"`
	TemplateSourceID *int64   `gorm:"column:template_source_id"` // 复制来源模板（可空，仅统计）
	LineURL          *string  `gorm:"column:line_url"`
	Via              *string  `gorm:"column:via"`
	KeyID            *int64   `gorm:"column:key_id"`
	Protocol         *string  `gorm:"column:protocol"` // openai|anthropic（入站）
	Model            *string  `gorm:"column:model"`
	UpstreamModel    *string  `gorm:"column:upstream_model"`
	StatusCode       *int     `gorm:"column:status_code"`
	TtftMs           *int64   `gorm:"column:ttft_ms"`
	TotalMs          *int64   `gorm:"column:total_ms"`
	PromptTokens     *int64   `gorm:"column:prompt_tokens"`
	CompletionTokens *int64   `gorm:"column:completion_tokens"`
	CachedTokens     *int64   `gorm:"column:cached_tokens"`      // 缓存读 token（归一化）
	CacheWriteTokens *int64   `gorm:"column:cache_write_tokens"` // 缓存写 token（归一化）
	InputCost        *float64 `gorm:"column:input_cost"`         // 写入时快照；未定价为 NULL
	OutputCost       *float64 `gorm:"column:output_cost"`
	Error            *string  `gorm:"column:error"` // 截断 512B
}

func (Log) TableName() string { return "logs" }

// InviteCode 邀请码
type InviteCode struct {
	Code      string `gorm:"column:code;primaryKey"`
	CreatedBy *int64 `gorm:"column:created_by"`
	UsedBy    *int64 `gorm:"column:used_by"`
	UsedAt    *int64 `gorm:"column:used_at"`
}

func (InviteCode) TableName() string { return "invite_codes" }

// Setting 系统配置（key-value）
type Setting struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value"`
}

func (Setting) TableName() string { return "settings" }
