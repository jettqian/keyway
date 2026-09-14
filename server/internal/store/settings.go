package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetSetting 读取配置值；不存在返回空串
func (s *Store) GetSetting(key string) (string, error) {
	var row Setting
	err := s.db.Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取配置 %s 失败: %w", key, err)
	}
	return row.Value, nil
}

// SetSetting 写入配置（upsert）
func (s *Store) SetSetting(key, value string) error {
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&Setting{Key: key, Value: value}).Error; err != nil {
		return fmt.Errorf("写入配置 %s 失败: %w", key, err)
	}
	return nil
}
