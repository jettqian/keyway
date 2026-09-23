package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/store"
	"keyway/internal/usage"
)

// ---------- 全局模型目录（管理员预置，供渠道/模板表单点选） ----------

func catalogModelDTO(m *store.CatalogModel) gin.H {
	return gin.H{
		"id": m.ID, "name": m.Name, "pricingModel": m.PricingModel, "note": m.Note,
		"enabled": m.Enabled == 1, "updatedAt": m.UpdatedAt,
	}
}

// catalogModelDTOWithPricing 附带关联价目：按模型名精确匹配 model_pricing
// （费用计算同口径：未命中时该模型费用记未定价）
func catalogModelDTOWithPricing(m *store.CatalogModel, p *store.ModelPricing) gin.H {
	dto := catalogModelDTO(m)
	if p == nil {
		return dto
	}
	dto["inputPerM"] = p.InputPerM
	dto["outputPerM"] = p.OutputPerM
	dto["cachedInputPerM"] = p.CachedInputPerM
	dto["cacheWritePerM"] = p.CacheWritePerM
	return dto
}

// pricingMap 全量价目按模型名索引（目录页展示关联价用）
func (s *Server) pricingMap() map[string]*store.ModelPricing {
	var pricing []store.ModelPricing
	s.Store.DB().Find(&pricing)
	out := make(map[string]*store.ModelPricing, len(pricing))
	for i := range pricing {
		out[pricing[i].Model] = &pricing[i]
	}
	return out
}

// handleListCatalogModels 用户侧：启用的目录模型（点选数据源，只读；含关联单价）
func (s *Server) handleListCatalogModels(c *gin.Context) {
	var models []store.CatalogModel
	s.Store.DB().Where("enabled = 1 ORDER BY name").Find(&models)
	prices := s.pricingMap()
	out := make([]gin.H, 0, len(models))
	for i := range models {
		priceModel := models[i].PricingModel
		if priceModel == "" {
			priceModel = models[i].Name
		}
		out = append(out, catalogModelDTOWithPricing(&models[i], prices[priceModel]))
	}
	s.ok(c, gin.H{"models": out})
}

func (s *Server) handleAdminListCatalogModels(c *gin.Context) {
	var models []store.CatalogModel
	s.Store.DB().Order("name").Find(&models)
	prices := s.pricingMap()
	out := make([]gin.H, 0, len(models))
	for i := range models {
		priceModel := models[i].PricingModel
		if priceModel == "" {
			priceModel = models[i].Name
		}
		out = append(out, catalogModelDTOWithPricing(&models[i], prices[priceModel]))
	}
	s.ok(c, gin.H{"models": out})
}

func (s *Server) handleAdminCreateCatalogModel(c *gin.Context) {
	var req struct {
		Name         string  `json:"name"`
		PricingModel *string `json:"pricingModel"`
		Note         string  `json:"note"`
	}
	if err := c.BindJSON(&req); err != nil || trimOrEmpty(req.Name) == "" || len(req.Name) > 255 {
		s.fail(c, http.StatusBadRequest, "模型名须为 1~255 字节")
		return
	}
	pricingModel := ""
	if req.PricingModel != nil {
		pricingModel = trimOrEmpty(*req.PricingModel)
	}
	if pricingModel == "" {
		pricingModel = trimOrEmpty(req.Name)
	}
	m := store.CatalogModel{
		Name: trimOrEmpty(req.Name), PricingModel: pricingModel, Note: req.Note, Enabled: 1,
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
	usage.InvalidatePricingCache(s.Store.DB()) // 关联价目参与计价别名（v1.5.63）
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
		Name         string  `json:"name"`
		PricingModel *string `json:"pricingModel"`
		Note         string  `json:"note"`
		Enabled      *bool   `json:"enabled"`
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
	if req.PricingModel != nil {
		updates["pricing_model"] = trimOrEmpty(*req.PricingModel)
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
	usage.InvalidatePricingCache(s.Store.DB()) // 名称/关联价目变化影响计价别名
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
	usage.InvalidatePricingCache(s.Store.DB()) // 移除目录条目即移除计价别名
	s.ok(c, gin.H{})
}
