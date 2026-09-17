package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"keyway/internal/store"
)

// ---------- 渠道×模型熔断（FR-B5/FR-B6，v1.5.45） ----------

// handleListBreakers GET /api/breakers：本人渠道的熔断状态列表（渠道名关联）。
// 只返回熔断中的行（无行 = 关闭）
func (s *Server) handleListBreakers(c *gin.Context) {
	var chans []store.Channel
	if err := s.Store.DB().Where("user_id = ?", currentUser(c).ID).Find(&chans).Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "查询渠道失败")
		return
	}
	ids := make([]int64, 0, len(chans))
	nameByID := make(map[int64]string, len(chans))
	for i := range chans {
		ids = append(ids, chans[i].ID)
		nameByID[chans[i].ID] = chans[i].Name
	}
	out := []gin.H{}
	if len(ids) > 0 {
		var rows []store.BreakerState
		if err := s.Store.DB().Where("channel_id IN ?", ids).Order("channel_id, model").Find(&rows).Error; err != nil {
			s.fail(c, http.StatusInternalServerError, "查询熔断状态失败")
			return
		}
		for i := range rows {
			r := &rows[i]
			out = append(out, gin.H{
				"channelId":     r.ChannelID,
				"channelName":   nameByID[r.ChannelID],
				"model":         r.Model,
				"failCount":     r.FailCount,
				"openedAt":      r.OpenedAt,
				"cooldownUntil": r.CooldownUntil,
				"lastError":     r.LastError,
				"updatedAt":     r.UpdatedAt,
			})
		}
	}
	s.ok(c, gin.H{"breakers": out})
}

// handleResetBreaker POST /api/breakers/reset {channelId, model?}：手动恢复——
// 删除熔断状态并清零失败计数，下一个请求即恢复路由；model 为空 = 恢复该渠道
// 全部模型。仅渠道所有者可操作
func (s *Server) handleResetBreaker(c *gin.Context) {
	var req struct {
		ChannelID int64  `json:"channelId"`
		Model     string `json:"model"`
	}
	if err := c.BindJSON(&req); err != nil || req.ChannelID == 0 {
		s.fail(c, http.StatusBadRequest, "channelId 不能为空")
		return
	}
	var ch store.Channel
	if err := s.Store.DB().Where("id = ? AND user_id = ?", req.ChannelID, currentUser(c).ID).First(&ch).Error; err != nil {
		s.fail(c, http.StatusNotFound, "渠道不存在")
		return
	}
	s.Breaker.Reset(req.ChannelID, trimOrEmpty(req.Model))
	s.ok(c, gin.H{})
}
