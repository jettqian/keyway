package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Options 存储打开选项；DataDir 为数据目录，特殊值 ":memory:" 表示内存库（测试用）
type Options struct {
	DataDir string
}

// Store SQLite 存储封装
type Store struct {
	db *gorm.DB
}

const (
	dbFileName    = "keyway.db"
	busyTimeoutMs = 5000
)

// Open 打开 DataDir 下的 keyway.db（启用 WAL、busy_timeout、foreign_keys），
// 执行 AutoMigrate 并补建 logs 索引
func Open(opts Options) (*Store, error) {
	var dsn string
	if opts.DataDir == ":memory:" {
		dsn = ":memory:" + dsnPragmas()
	} else {
		if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
		dsn = "file:" + filepath.ToSlash(filepath.Join(opts.DataDir, dbFileName)) + dsnPragmas()
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	if opts.DataDir == ":memory:" {
		// 内存库每个连接独立，限制单连接避免表互相不可见
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.SetMaxOpenConns(1)
		}
	}

	if err := db.AutoMigrate(allModels()...); err != nil {
		return nil, fmt.Errorf("迁移失败: %w", err)
	}
	if err := ensureLogsIndexes(db); err != nil {
		return nil, err
	}
	// restricted 列迁移后回填：启用集合非空的存量令牌必然是限定语义。
	// 幂等且安全：restricted=1 + 空集合（全部停用）不受影响；空集合 + restricted=0
	// （从未限定/主开关时代的"不限"遗留）保持不限
	db.Exec("UPDATE tokens SET restricted = 1 WHERE channel_ids_json IS NOT NULL AND channel_ids_json NOT IN ('', '[]')")
	// 价目来源回填（v1.5.69）：source 列引入前的存量条目无来源记录，按现行语义
	// 「价目表增长完全由管理员控制」统一记为手工维护；后续远程同步（目录关联）
	// 或管理端编辑会按实际来源覆盖。幂等：首启跑完即无 source='' 行
	db.Exec("UPDATE model_pricing SET source = 'manual' WHERE source = ''")
	return &Store{db: db}, nil
}

// dsnPragmas 每连接生效的 pragma 参数
func dsnPragmas() string {
	return fmt.Sprintf("?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		busyTimeoutMs)
}

func allModels() []any {
	return []any{
		&User{}, &Session{}, &Key{}, &ChannelTemplate{}, &Channel{},
		&LineStat{}, &Proxy{}, &ProxyUsage{}, &Token{}, &ModelPricing{},
		&CatalogModel{}, &Log{}, &InviteCode{}, &Setting{}, &BreakerState{},
	}
}

// ensureLogsIndexes logs 的 created_at 参与多个复合索引，模型标签无法表达，
// 按 DDL 原名补建
func ensureLogsIndexes(db *gorm.DB) error {
	for _, ddl := range []string{
		"CREATE INDEX IF NOT EXISTS idx_logs_time ON logs(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_logs_user ON logs(user_id, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_logs_channel ON logs(channel_id, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_logs_key ON logs(key_id, created_at)",
		"CREATE INDEX IF NOT EXISTS idx_logs_token ON logs(token_id, created_at)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("创建 logs 索引失败: %w", err)
		}
	}
	return nil
}

// DB 暴露底层 GORM 句柄（供后续仓储层使用）
func (s *Store) DB() *gorm.DB { return s.db }

// Close 关闭存储
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("获取底层连接失败: %w", err)
	}
	return sqlDB.Close()
}
