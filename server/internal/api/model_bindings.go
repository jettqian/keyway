package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"keyway/internal/store"
)

// handleUpdateModelBindings 原子更新一个模型在多个渠道中的列表项。
func (s *Server) handleUpdateModelBindings(c *gin.Context) {
	var req struct {
		Name         string  `json:"name"`
		PreviousName string  `json:"previousName"`
		ChannelIDs   []int64 `json:"channelIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.PreviousName = strings.TrimSpace(req.PreviousName)
	if req.Name == "" || len(req.Name) > 255 {
		s.fail(c, http.StatusBadRequest, "模型名须为 1~255 字节")
		return
	}
	selected := make(map[int64]bool)
	for _, id := range req.ChannelIDs {
		selected[id] = true
	}
	invalidChannels := errors.New("包含不存在或不属于你的渠道")
	err := s.Store.DB().WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var channels []store.Channel
		if err := tx.Where("user_id = ?", currentUser(c).ID).Find(&channels).Error; err != nil {
			return err
		}
		owned := make(map[int64]bool)
		for _, ch := range channels {
			owned[ch.ID] = true
		}
		for id := range selected {
			if !owned[id] {
				return invalidChannels
			}
		}
		for _, ch := range channels {
			var models []string
			if err := json.Unmarshal([]byte(ch.ModelsJSON), &models); err != nil {
				return err
			}
			var mapping map[string]string
			if err := json.Unmarshal([]byte(ch.ModelMappingJSON), &mapping); err != nil {
				return err
			}
			if mapping == nil {
				mapping = make(map[string]string)
			}
			updated := make([]string, 0, len(models)+1)
			touched := selected[ch.ID]
			for _, model := range models {
				if req.PreviousName != "" && model == req.PreviousName {
					touched = true
					continue
				}
				if selected[ch.ID] && model == req.Name {
					continue
				}
				updated = append(updated, model)
			}
			if !touched {
				continue
			}
			if selected[ch.ID] {
				updated = append(updated, req.Name)
				if req.PreviousName != req.Name && mapping[req.PreviousName] != "" {
					mapping[req.Name] = mapping[req.PreviousName]
				}
			}
			if req.PreviousName != req.Name || !selected[ch.ID] {
				delete(mapping, req.PreviousName)
			}
			if err := tx.Model(&ch).Updates(map[string]any{"models_json": string(mustJSONStr(updated)), "model_mapping_json": string(mustJSONStr(mapping))}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, invalidChannels) {
		s.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "保存模型绑定失败")
		return
	}
	s.ok(c, gin.H{})
}
