package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/store"
	"keyway/internal/usage"
)

// ---------- CSV 导出（FR-M3）----------

func writeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	// UTF-8 BOM 便于 Excel 识别
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(c.Writer)
	w.Write(header)
	for _, r := range rows {
		w.Write(r)
	}
	w.Flush()
}

func (s *Server) handleLogsExport(c *gin.Context) {
	u := currentUser(c)
	q := usage.LogQuery{Page: 1, PageSize: 10000, Model: c.Query("model")}
	if v := c.Query("channelId"); v != "" {
		id, _ := strconv.ParseInt(v, 10, 64)
		q.ChannelID = &id
	}
	logs, _, err := usage.QueryLogs(s.Store.DB(), &u.ID, q)
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	names := s.channelNames(u.ID)
	rows := make([][]string, 0, len(logs))
	for i := range logs {
		l := &logs[i]
		channelName := ""
		if l.ChannelID != nil {
			channelName = names[*l.ChannelID]
		}
		rows = append(rows, []string{
			time.Unix(l.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			channelName,
			derefStr(l.LineURL), derefStr(l.Via),
			derefStr(l.Protocol), derefStr(l.Model), derefStr(l.UpstreamModel),
			derefStr(l.ReasoningEffort),
			strconv.Itoa(derefInt(l.StatusCode)),
			strconv.FormatInt(derefI64(l.PromptTokens), 10),
			strconv.FormatInt(derefI64(l.CompletionTokens), 10),
			strconv.FormatInt(derefI64(l.CachedTokens), 10),
			fmtCost(l.InputCost, l.OutputCost),
			derefStr(l.Error),
		})
	}
	writeCSV(c, "keyway-logs.csv",
		[]string{"时间", "渠道", "线路", "路径", "协议", "模型", "上游模型", "推理强度", "状态码",
			"输入tokens", "输出tokens", "缓存tokens", "费用USD", "错误"},
		rows)
}

func (s *Server) handleStatsExport(c *gin.Context) {
	exportStatsCSV(c, s, currentUser(c).ID)
}

func (s *Server) handleAdminStatsExport(c *gin.Context) {
	exportStatsCSV(c, s, 0)
}

func exportStatsCSV(c *gin.Context, s *Server, userID int64) {
	var id *int64
	if userID > 0 {
		id = &userID
	}
	since, until := statsRange(c)
	st, err := usage.QueryStats(s.Store.DB(), id, since, until, queryTokenID(c))
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	group := func(name string, list []usage.StatsGroup) [][]string {
		rows := make([][]string, 0, len(list))
		for _, g := range list {
			rows = append(rows, []string{name, g.Dim,
				strconv.FormatInt(g.Requests, 10),
				strconv.FormatInt(g.PromptTokens, 10),
				strconv.FormatInt(g.CompletionTokens, 10),
				fmt.Sprintf("%.4f", g.Cost),
				strconv.FormatInt(g.Errors, 10),
			})
		}
		return rows
	}
	rows := group("用户", st.ByUser)
	rows = append(rows, group("渠道", st.ByChannel)...)
	rows = append(rows, group("模型", st.ByModel)...)
	rows = append(rows, group("密钥", st.ByKey)...)
	rows = append(rows, group("推理强度", st.ByEffort)...)
	writeCSV(c, "keyway-stats.csv",
		[]string{"维度", "分组", "请求数", "输入tokens", "输出tokens", "费用USD", "错误数"},
		rows)
}

// ---------- 邀请码管理（FR-M2）----------

func (s *Server) handleAdminListInvites(c *gin.Context) {
	var invites []store.InviteCode
	s.Store.DB().Order("code").Limit(500).Find(&invites)
	out := make([]gin.H, 0, len(invites))
	for i := range invites {
		item := gin.H{"code": invites[i].Code, "createdBy": invites[i].CreatedBy}
		if invites[i].UsedBy != nil {
			item["usedBy"] = *invites[i].UsedBy
		}
		if invites[i].UsedAt != nil {
			item["usedAt"] = *invites[i].UsedAt
		}
		out = append(out, item)
	}
	s.ok(c, gin.H{"invites": out})
}

func (s *Server) handleAdminCreateInvites(c *gin.Context) {
	var req struct {
		Count int `json:"count"`
	}
	if err := c.BindJSON(&req); err != nil || req.Count < 1 || req.Count > 100 {
		req.Count = 10
	}
	admin := currentUser(c)
	out := make([]gin.H, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		code, err := generatePassword()
		if err != nil {
			s.fail(c, http.StatusInternalServerError, "生成失败")
			return
		}
		invite := store.InviteCode{Code: code, CreatedBy: &admin.ID}
		if err := s.Store.DB().Create(&invite).Error; err != nil {
			continue
		}
		out = append(out, gin.H{"code": code})
	}
	s.ok(c, gin.H{"invites": out})
}

func (s *Server) handleAdminDeleteInvite(c *gin.Context) {
	s.Store.DB().Delete(&store.InviteCode{}, c.Param("code"))
	s.ok(c, gin.H{})
}

// ---------- 辅助 ----------

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func fmtCost(in, out *float64) string {
	if in == nil {
		return ""
	}
	o := 0.0
	if out != nil {
		o = *out
	}
	return fmt.Sprintf("%.6f", *in+o)
}
