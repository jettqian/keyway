package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/store"
)

// ---------- 全局模型目录（管理员预置，供渠道/模板表单点选） ----------

func catalogModelDTO(m *store.CatalogModel) gin.H {
	return gin.H{
		"id": m.ID, "name": m.Name, "note": m.Note,
		"enabled": m.Enabled == 1, "updatedAt": m.UpdatedAt,
	}
}

// handleListCatalogModels 用户侧：启用的目录模型（点选数据源，只读）
func (s *Server) handleListCatalogModels(c *gin.Context) {
	var models []store.CatalogModel
	s.Store.DB().Where("enabled = 1 ORDER BY name").Find(&models)
	out := make([]gin.H, 0, len(models))
	for i := range models {
		out = append(out, catalogModelDTO(&models[i]))
	}
	s.ok(c, gin.H{"models": out})
}

func (s *Server) handleAdminListCatalogModels(c *gin.Context) {
	var models []store.CatalogModel
	s.Store.DB().Order("name").Find(&models)
	out := make([]gin.H, 0, len(models))
	for i := range models {
		out = append(out, catalogModelDTO(&models[i]))
	}
	s.ok(c, gin.H{"models": out})
}

func (s *Server) handleAdminCreateCatalogModel(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" || len(req.Name) > 255 {
		s.fail(c, http.StatusBadRequest, "模型名须为 1~255 字节")
		return
	}
	m := store.CatalogModel{
		Name: trimOrEmpty(req.Name), Note: req.Note, Enabled: 1,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}
	if err := s.Store.DB().Create(&m).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.fail(c, http.StatusBadRequest, "同名模型已存在")
			return
		}
		s.fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	s.ok(c, gin.H{"model": catalogModelDTO(&m)})
}

func (s *Server) handleAdminUpdateCatalogModel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var m store.CatalogModel
	if err := s.Store.DB().First(&m, id).Error; err != nil {
		s.fail(c, http.StatusNotFound, "目录模型不存在")
		return
	}
	var req struct {
		Name    string `json:"name"`
		Note    string `json:"note"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.BindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "非法请求体")
		return
	}
	updates := map[string]any{"updated_at": time.Now().Unix()}
	if trimOrEmpty(req.Name) != "" {
		if len(req.Name) > 255 {
			s.fail(c, http.StatusBadRequest, "模型名须为 1~255 字节")
			return
		}
		updates["name"] = trimOrEmpty(req.Name)
	}
	updates["note"] = req.Note
	if req.Enabled != nil {
		updates["enabled"] = boolToInt(*req.Enabled)
	}
	if err := s.Store.DB().Model(&m).Updates(updates).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.fail(c, http.StatusBadRequest, "同名模型已存在")
			return
		}
		s.fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	s.Store.DB().First(&m, id)
	s.ok(c, gin.H{"model": catalogModelDTO(&m)})
}

func (s *Server) handleAdminDeleteCatalogModel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	// 目录仅是点选数据源，删除不影响已引用它的渠道配置
	res := s.Store.DB().Delete(&store.CatalogModel{}, id)
	if res.RowsAffected == 0 {
		s.fail(c, http.StatusNotFound, "目录模型不存在")
		return
	}
	s.ok(c, gin.H{})
}

// handleAdminImportCatalogFromPricing 从价目表一键导入缺失的模型名（管理员预置的快捷方式）
func (s *Server) handleAdminImportCatalogFromPricing(c *gin.Context) {
	var pricing []store.ModelPricing
	s.Store.DB().Order("model").Find(&pricing)
	var existing []store.CatalogModel
	s.Store.DB().Find(&existing)
	seen := make(map[string]bool, len(existing))
	for i := range existing {
		seen[existing[i].Name] = true
	}
	now := time.Now().Unix()
	imported := 0
	for i := range pricing {
		name := trimOrEmpty(pricing[i].Model)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if err := s.Store.DB().Create(&store.CatalogModel{
			Name: name, Enabled: 1, CreatedAt: now, UpdatedAt: now,
		}).Error; err == nil {
			imported++
		}
	}
	s.ok(c, gin.H{"imported": imported})
}
