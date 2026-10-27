package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"keyway/internal/crypto"
	"keyway/internal/store"
)

// ---------- 用户配置导出/导入（FR-BK）----------

// configImportMaxBytes 导入文件体积上限：用户级配置（≤30 密钥、渠道/令牌若干）
// 正常远小于该值，防异常大文件滥用
const configImportMaxBytes = 4 << 20

// configExportFile 导出文件契约（version 1）：
// - 密钥以名称为引用键（每用户内唯一，跨实例可读），渠道的密钥绑定导出为 keyNames
// - 渠道以文件内 id（导出时的 DB id）为引用键，供令牌的渠道绑定重映射
// - mode 仅作信息标注；导入按字段存在性自适应（value/plaintext/proxyUrl 缺失即
//   按结构处理），因此同一文件允许混合（如含密钥值但不含令牌明文）
type configExportFile struct {
	App        string             `json:"app"`      // "keyway"
	Format     string             `json:"format"`   // "user-config"
	Version    int                `json:"version"`  // 1
	ExportedAt int64              `json:"exportedAt"`
	Mode       string             `json:"mode"` // full | structure
	Keys       []configExportKey  `json:"keys"`
	Channels   []configExportChan `json:"channels"`
	Tokens     []configExportTok  `json:"tokens"`
}

type configExportKey struct {
	Name   string `json:"name"`
	Note   string `json:"note"`
	Status int    `json:"status"`
	Value  string `json:"value,omitempty"` // 仅完整导出
}

type configExportChan struct {
	ID               int64             `json:"id"` // 文件内引用键（导出时 DB id）
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	BaseURLs         []string          `json:"baseUrls"`
	KeyNames         []string          `json:"keyNames"`
	KeyStrategy      string            `json:"keyStrategy"`
	LineStrategy     string            `json:"lineStrategy"`
	ProxyURL         string            `json:"proxyUrl,omitempty"` // 仅完整导出
	AllowPublicProxy bool              `json:"allowPublicProxy"`
	Models           []string          `json:"models"`
	ModelMapping     map[string]string `json:"modelMapping"`
	ForwardMode      string            `json:"forwardMode"`
	Priority         int               `json:"priority"`
	PriceMultiplier  float64           `json:"priceMultiplier"`
	PricingMode      string            `json:"pricingMode"`
	CNYRatio         float64           `json:"cnyRatio"`
	IsDefault        bool              `json:"isDefault"`
	Enabled          bool              `json:"enabled"`
}

type configExportTok struct {
	Name         string  `json:"name"`
	ModelScope   string  `json:"modelScope,omitempty"`
	ExpiresAt    *int64  `json:"expiresAt"`
	Restricted   bool    `json:"restricted"`
	ChannelIDs   []int64 `json:"channelIds"`   // 引用 channels[].id（文件内）
	ChannelOrder []int64 `json:"channelOrder"` // 同上
	Plaintext    string  `json:"plaintext,omitempty"` // 仅完整导出
}

// configImportResult 导入结果摘要（前端展示计数与警告）
type configImportResult struct {
	KeysCreated     int      `json:"keysCreated"`
	KeysReused      int      `json:"keysReused"`
	KeysMissing     int      `json:"keysMissing"`
	ChannelsCreated int      `json:"channelsCreated"`
	ChannelsReused  int      `json:"channelsReused"`
	ChannelsDrafted int      `json:"channelsDrafted"`
	ChannelsSkipped int      `json:"channelsSkipped"`
	TokensCreated   int      `json:"tokensCreated"`
	TokensSkipped   int      `json:"tokensSkipped"`
	Warnings        []string `json:"warnings"`
}

func (r *configImportResult) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// handleConfigExport 导出本人全部用户配置（密钥池 / 渠道 / 令牌）。
// secrets=1 为完整备份（解密包含上游密钥值、令牌明文、个人代理地址），
// 缺省为纯结构（不含任何明文，可分享）
func (s *Server) handleConfigExport(c *gin.Context) {
	u := currentUser(c)
	secrets := c.Query("secrets") == "1"
	mode := "structure"
	if secrets {
		mode = "full"
	}

	var keys []store.Key
	s.Store.DB().Where("user_id = ? ORDER BY id", u.ID).Find(&keys)
	keyNameByID := map[int64]string{}
	outKeys := make([]configExportKey, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		keyNameByID[k.ID] = k.Name
		ek := configExportKey{Name: k.Name, Note: k.Note, Status: k.Status}
		if secrets {
			if plain, err := crypto.Decrypt(s.Secret, crypto.PurposeKey, k.ValueEnc); err == nil {
				ek.Value = string(plain)
			}
		}
		outKeys = append(outKeys, ek)
	}

	var chans []store.Channel
	s.Store.DB().Where("user_id = ? ORDER BY id", u.ID).Find(&chans)
	outChans := make([]configExportChan, 0, len(chans))
	for i := range chans {
		ec := channelToExport(&chans[i], keyNameByID)
		if secrets && chans[i].ProxyURLEnc != nil {
			if plain, err := crypto.Decrypt(s.Secret, crypto.PurposeProxy, chans[i].ProxyURLEnc); err == nil {
				ec.ProxyURL = string(plain)
			}
		}
		outChans = append(outChans, ec)
	}

	// 已吊销令牌不导出：吊销即弃用，无迁移价值，且按明文重建等于复活
	var tokens []store.Token
	s.Store.DB().Where("user_id = ? AND revoked = 0 ORDER BY id", u.ID).Find(&tokens)
	outToks := make([]configExportTok, 0, len(tokens))
	for i := range tokens {
		t := &tokens[i]
		channelIDs := t.ChannelFilter()
		if channelIDs == nil {
			channelIDs = []int64{}
		}
		channelOrder := t.ChannelOrder()
		if channelOrder == nil {
			channelOrder = []int64{}
		}
		et := configExportTok{
			Name: t.Name, Restricted: t.Restricted == 1,
			ChannelIDs: channelIDs, ChannelOrder: channelOrder,
		}
		if t.ModelScope != nil {
			et.ModelScope = *t.ModelScope
		}
		et.ExpiresAt = t.ExpiresAt
		if secrets {
			if plain, err := crypto.Decrypt(s.Secret, crypto.PurposeToken, t.KeyEnc); err == nil {
				et.Plaintext = string(plain)
			}
		}
		outToks = append(outToks, et)
	}

	file := configExportFile{
		App: "keyway", Format: "user-config", Version: 1,
		ExportedAt: time.Now().Unix(), Mode: mode,
		Keys: outKeys, Channels: outChans, Tokens: outToks,
	}
	name := fmt.Sprintf("keyway-config-%s-%s.json", mode, time.Now().Format("20060102-150405"))
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.JSON(http.StatusOK, file)
}

func channelToExport(ch *store.Channel, keyNameByID map[int64]string) configExportChan {
	var urls, models []string
	var keyIDs []int64
	json.Unmarshal([]byte(ch.BaseURLsJSON), &urls)
	json.Unmarshal([]byte(ch.ModelsJSON), &models)
	json.Unmarshal([]byte(ch.KeyIDsJSON), &keyIDs)
	var mapping map[string]string
	json.Unmarshal([]byte(ch.ModelMappingJSON), &mapping)
	// 历史空配置可能存为 null；导出始终为可遍历集合
	if urls == nil {
		urls = []string{}
	}
	if models == nil {
		models = []string{}
	}
	if mapping == nil {
		mapping = map[string]string{}
	}
	keyNames := make([]string, 0, len(keyIDs))
	for _, id := range keyIDs {
		if n, ok := keyNameByID[id]; ok {
			keyNames = append(keyNames, n)
		}
	}
	return configExportChan{
		ID: ch.ID, Name: ch.Name, Type: ch.Type,
		BaseURLs: urls, KeyNames: keyNames,
		KeyStrategy: ch.KeyStrategy, LineStrategy: ch.LineStrategy,
		AllowPublicProxy: ch.AllowPublicProxy == 1,
		Models:           models, ModelMapping: mapping,
		ForwardMode: ch.ForwardMode, Priority: ch.Priority,
		PriceMultiplier: ch.PriceMultiplier, PricingMode: ch.PricingMode,
		CNYRatio: ch.CNYRatio, IsDefault: ch.IsDefault == 1, Enabled: ch.Enabled == 1,
	}
}

// handleConfigImport 导入用户配置（合并模式，重复导入幂等）：
// - 密钥：同名复用现有（不覆盖值）；含明文值且无同名的创建；无值且无同名的悬空
// - 渠道：同名跳过复用（不新建副本，令牌绑定重映射到现有渠道）；无同名的创建，
//   密钥绑定按名称重映射，全部悬空的渠道以草稿（停用）导入，绑定密钥后手动启用
// - 令牌：同名跳过；无同名的创建——含明文的按明文重建（实例迁移后 Agent 零改配，
//   key_hash 全局唯一，他人持有同值令牌时跳过），无明文的随机签发新值
// 整个导入在单事务内执行，任一硬错误整体回滚；条目级问题跳过并记入 warnings
func (s *Server) handleConfigImport(c *gin.Context) {
	u := currentUser(c)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, configImportMaxBytes+1))
	if err != nil {
		s.fail(c, http.StatusBadRequest, "读取文件失败")
		return
	}
	if len(body) > configImportMaxBytes {
		s.fail(c, http.StatusBadRequest, "文件过大（上限 4MB）")
		return
	}
	// 容错 UTF-8 BOM（部分编辑器习惯性写入）
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	var in configExportFile
	if err := json.Unmarshal(body, &in); err != nil {
		s.fail(c, http.StatusBadRequest, "不是合法的 JSON 文件")
		return
	}
	if in.App != "keyway" || in.Format != "user-config" || in.Version != 1 {
		s.fail(c, http.StatusBadRequest, "不是有效的 Keyway 用户配置文件")
		return
	}

	res := &configImportResult{Warnings: []string{}}
	tx := s.Store.DB().Begin()
	if tx.Error != nil {
		s.fail(c, http.StatusInternalServerError, "导入失败")
		return
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	if err := s.importUserConfig(tx, u.ID, &in, res); err != nil {
		s.fail(c, http.StatusInternalServerError, "导入失败: "+err.Error())
		return
	}
	if err := tx.Commit().Error; err != nil {
		s.fail(c, http.StatusInternalServerError, "导入失败")
		return
	}
	committed = true
	s.ok(c, gin.H{"result": res})
}

func (s *Server) importUserConfig(tx *gorm.DB, userID int64, in *configExportFile, res *configImportResult) error {
	now := time.Now().Unix()

	// 1. 密钥
	var existingKeys []store.Key
	if err := tx.Where("user_id = ?", userID).Find(&existingKeys).Error; err != nil {
		return err
	}
	keyIDByName := map[string]int64{}
	for i := range existingKeys {
		keyIDByName[existingKeys[i].Name] = existingKeys[i].ID
	}
	for _, ek := range in.Keys {
		name := trimOrEmpty(ek.Name)
		if name == "" {
			res.warn("跳过一个未命名密钥")
			continue
		}
		if _, exists := keyIDByName[name]; exists {
			res.KeysReused++ // 同名复用，不覆盖现有值
			continue
		}
		if trimOrEmpty(ek.Value) == "" {
			res.KeysMissing++ // 无明文（纯结构导出）且本地无同名，无法创建
			continue
		}
		enc, err := crypto.Encrypt(s.Secret, crypto.PurposeKey, []byte(trimOrEmpty(ek.Value)))
		if err != nil {
			return fmt.Errorf("加密密钥「%s」失败: %w", name, err)
		}
		status := ek.Status
		if status != 1 && status != 2 {
			status = 1
		}
		k := store.Key{UserID: userID, Name: name, ValueEnc: enc, Note: ek.Note, Status: status, CreatedAt: now}
		if err := tx.Create(&k).Error; err != nil {
			return fmt.Errorf("创建密钥「%s」失败: %w", name, err)
		}
		keyIDByName[name] = k.ID
		res.KeysCreated++
	}
	if res.KeysMissing > 0 {
		res.warn("%d 个密钥条目不含明文值（纯结构导出），未创建", res.KeysMissing)
	}

	// 2. 渠道（同名跳过复用：重复导入幂等，不产生副本）
	var existingChans []store.Channel
	if err := tx.Where("user_id = ?", userID).Find(&existingChans).Error; err != nil {
		return err
	}
	channelIDByName := map[string]int64{}
	for i := range existingChans {
		channelIDByName[existingChans[i].Name] = existingChans[i].ID
	}
	channelIDByOld := map[int64]int64{}
	for _, ec := range in.Channels {
		name := trimOrEmpty(ec.Name)
		if name == "" {
			res.ChannelsSkipped++
			res.warn("跳过一个未命名渠道")
			continue
		}
		if existingID, exists := channelIDByName[name]; exists {
			// 同名渠道复用现有：不新建副本；仍建立 id 映射，
			// 令牌的渠道绑定据此落到现有渠道
			if ec.ID != 0 {
				channelIDByOld[ec.ID] = existingID
			}
			res.ChannelsReused++
			continue
		}
		// 密钥绑定按名称重映射：悬空引用（密钥未导入且本地无同名）丢弃
		var keyIDs []int64
		seen := map[int64]bool{}
		for _, kn := range ec.KeyNames {
			kn = trimOrEmpty(kn)
			if kn == "" {
				continue
			}
			if id, ok := keyIDByName[kn]; ok && !seen[id] {
				seen[id] = true
				keyIDs = append(keyIDs, id)
			}
		}
		if len(keyIDs) > 5 {
			keyIDs = keyIDs[:5]
		}
		ci := channelInput{
			Name: ec.Name, Type: ec.Type, BaseURLs: ec.BaseURLs, KeyIDs: keyIDs,
			KeyStrategy: ec.KeyStrategy, LineStrategy: ec.LineStrategy,
			ProxyURL: ec.ProxyURL, AllowPublicProxy: ec.AllowPublicProxy,
			Models: ec.Models, ModelMapping: ec.ModelMapping,
			ForwardMode: ec.ForwardMode, Priority: ec.Priority,
			PriceMultiplier: ec.PriceMultiplier, PricingMode: ec.PricingMode,
			CNYRatio: ec.CNYRatio, IsDefault: ec.IsDefault, Enabled: ec.Enabled,
		}
		drafted := false
		if len(keyIDs) == 0 {
			// 无可用密钥：强制草稿（停用），绑定密钥后手动启用
			ci.Enabled = false
			drafted = true
		}
		if msg := s.validateChannel(&ci); msg != "" {
			res.ChannelsSkipped++
			res.warn("渠道「%s」校验失败已跳过：%s", name, msg)
			continue
		}
		if msg := validatePersonalProxy(ci.ProxyURL); msg != "" {
			res.ChannelsSkipped++
			res.warn("渠道「%s」校验失败已跳过：%s", name, msg)
			continue
		}
		ch := store.Channel{UserID: userID, CreatedAt: now}
		if err := s.applyChannelInput(&ch, &ci, nil); err != nil {
			return fmt.Errorf("处理渠道「%s」失败: %w", name, err)
		}
		if err := tx.Create(&ch).Error; err != nil {
			return fmt.Errorf("创建渠道「%s」失败: %w", ch.Name, err)
		}
		channelIDByName[ch.Name] = ch.ID // 文件内同名渠道随后续条目跳过复用
		if ec.ID != 0 {
			channelIDByOld[ec.ID] = ch.ID
		}
		if ch.IsDefault == 1 {
			tx.Model(&store.Channel{}).
				Where("user_id = ? AND id != ? AND is_default = 1", userID, ch.ID).
				Update("is_default", 0)
		}
		res.ChannelsCreated++
		if drafted {
			res.ChannelsDrafted++
		}
	}
	if res.ChannelsReused > 0 {
		res.warn("%d 个同名渠道已存在，跳过新建（复用现有配置）", res.ChannelsReused)
	}

	// 3. 令牌（同名跳过复用，重复导入幂等）
	var existingTokens []store.Token
	if err := tx.Where("user_id = ?", userID).Find(&existingTokens).Error; err != nil {
		return err
	}
	tokenNameSet := map[string]bool{}
	for i := range existingTokens {
		tokenNameSet[existingTokens[i].Name] = true
	}
	remapChannels := func(ids []int64, cap int) []int64 {
		out := make([]int64, 0, len(ids))
		seen := map[int64]bool{}
		for _, id := range ids {
			if nid, ok := channelIDByOld[id]; ok && !seen[nid] {
				seen[nid] = true
				out = append(out, nid)
			}
		}
		if len(out) > cap {
			out = out[:cap]
		}
		return out
	}
	seenHashes := map[string]bool{}
	for _, et := range in.Tokens {
		name := trimOrEmpty(et.Name)
		if name == "" {
			res.TokensSkipped++
			res.warn("跳过一个未命名令牌")
			continue
		}
		if tokenNameSet[name] {
			// 同名令牌复用现有：不新建、不覆盖（纯结构导出重复导入亦幂等）
			res.TokensSkipped++
			res.warn("同名令牌「%s」已存在，跳过", name)
			continue
		}
		plaintext := trimOrEmpty(et.Plaintext)
		if plaintext != "" {
			// 明文格式校验：前缀 + 足够长度（KeyPrefix 取前 16 字符）
			if !strings.HasPrefix(plaintext, crypto.GatewayTokenPrefix) || len(plaintext) < 16 {
				res.TokensSkipped++
				res.warn("令牌「%s」的明文格式无效已跳过", name)
				continue
			}
			hash := crypto.HashToken(plaintext)
			if seenHashes[hash] {
				res.TokensSkipped++
				res.warn("令牌「%s」的明文在文件中重复，仅导入首个", name)
				continue
			}
			var count int64
			if err := tx.Model(&store.Token{}).Where("key_hash = ?", hash).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				// 全局唯一冲突：他人持有同值令牌（本人重复导入已被同名拦截），跳过
				res.TokensSkipped++
				res.warn("令牌「%s」的明文已存在于本实例，跳过", name)
				continue
			}
			seenHashes[hash] = true
		} else {
			p, err := crypto.GenerateGatewayToken()
			if err != nil {
				return fmt.Errorf("生成令牌失败: %w", err)
			}
			plaintext = p
		}
		enc, err := crypto.Encrypt(s.Secret, crypto.PurposeToken, []byte(plaintext))
		if err != nil {
			return fmt.Errorf("加密令牌失败: %w", err)
		}
		filter := remapChannels(et.ChannelIDs, 20)
		order := remapChannels(et.ChannelOrder, 20)
		t := store.Token{
			UserID: userID, Name: name,
			KeyEnc: enc, KeyPrefix: plaintext[:16], KeyHash: crypto.HashToken(plaintext),
			ChannelIDsJSON: string(mustJSONStr(filter)), ChannelOrderJSON: string(mustJSONStr(order)),
			Restricted: boolToInt(et.Restricted), CreatedAt: now,
		}
		if scope := trimOrEmpty(et.ModelScope); scope != "" {
			t.ModelScope = &scope
		}
		t.ExpiresAt = et.ExpiresAt
		if err := tx.Create(&t).Error; err != nil {
			return fmt.Errorf("创建令牌「%s」失败: %w", t.Name, err)
		}
		tokenNameSet[name] = true // 文件内同名令牌随后续条目跳过
		res.TokensCreated++
	}
	return nil
}
