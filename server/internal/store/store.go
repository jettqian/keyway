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
// 执行 AutoMigrate、补建 logs 索引并写入内置价目种子
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
	if err := seedModelPricing(db); err != nil {
		return nil, err
	}
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
		&Log{}, &InviteCode{}, &Setting{},
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
