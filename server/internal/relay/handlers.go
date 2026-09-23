package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/breaker"
	"keyway/internal/convert"
	"keyway/internal/httpx"
	"keyway/internal/probe"
	"keyway/internal/routing"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// ---------- OpenAI 入站 ----------

// HandleOpenAIChat POST /v1/chat/completions
func (s *Server) HandleOpenAIChat(c *gin.Context) {
	body := readBody(c, s.Cfg.BodyLimitMB)
	if body == nil {
		return
	}
	var req convert.OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		respondOpenAIError(c, http.StatusBadRequest, "请求体不是合法的 OpenAI JSON："+err.Error())
		return
	}
	s.relay(c, "openai", req.Model, body, &req)
}

// HandleOpenAIPassthrough POST /v1/completions、/v1/embeddings（仅 openai 型渠道）
func (s *Server) HandleOpenAIPassthrough(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := readBody(c, s.Cfg.BodyLimitMB)
		if body == nil {
			return
		}
		var probe struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &probe)
		if probe.Model == "" {
			respondOpenAIError(c, http.StatusBadRequest, "缺少 model 字段")
			return
		}
		token, user := ctxTokenUser(c)
		matched, defaults, err := s.Routing.Resolve(user.ID, probe.Model, token.RouteFilter(), deref(token.ModelScope))
		if err != nil {
			respondOpenAIError(c, http.StatusInternalServerError, err.Error())
			return
		}
		candidates := append(matched, defaults...)
		// 协议不兼容的渠道必然不会尝试，先剔除再算熔断视图
		// （否则"唯一可用渠道熔断 + 其余渠道不兼容"会被误判为存在可用候选）
		viable := make([]*routing.ResolvedChannel, 0, len(candidates))
		for _, rc := range candidates {
			if rc.Channel.ForwardMode == "convert" && rc.Channel.Type != "openai" {
				continue
			}
			viable = append(viable, rc)
		}
		view := s.breakerView(viable, probe.Model)
		now := time.Now().Unix()
		reqStart := time.Now()
		recorded := map[int64]bool{} // 熔断失败只按渠道记一次（matched+defaults 可能重复命中）
		for _, rc := range viable {
			if view != nil && view.Open(rc.Channel.ID) && !view.Due(rc.Channel.ID, now) {
				continue // 熔断冷却中：跳过该渠道
			}
			upstreamModel := mapModel(rc.Channel, probe.Model)
			m := map[string]any{}
			json.Unmarshal(body, &m)
			m["model"] = upstreamModel
			sendBody, _ := json.Marshal(m)
			tried := false
			segErr := "" // 段落失败原因（熔断 last_error 用，取末次失败摘要）
			for _, a := range s.planFor(rc, probe.Model, view) {
				if a.trial && !s.Breaker.ClaimHalfOpen(rc.Channel.ID, probe.Model) {
					continue // 半开试探已被并发请求认领
				}
				tried = true
				a.protocol = "openai"
				a.request = c.Request
				resp, _, err := s.sendUpstream(a, "POST", httpx.UpstreamEndpoint(a.lineURL, path), sendBody, false)
				if err != nil {
					segErr = err.Error()
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, segErr, reqStart)
					continue
				}
				if isHTMLResponse(resp) {
					// 上游 2xx 却返回 HTML（SPA 回退）：端点不存在，换组合
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					segErr = "上游返回 HTML（端点不存在）"
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, segErr, reqStart)
					continue
				}
				if resp.StatusCode >= 500 {
					errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					segErr = upstreamStatusError(resp.StatusCode, errBody)
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, segErr, reqStart)
					continue
				}
				s.recordUpstreamOutcome(rc.Channel.ID, probe.Model, resp.StatusCode)
				recorded[rc.Channel.ID] = true
				passthroughResponse(c, resp)
				s.submitLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, convert.Usage{}, 0, 0)
				return
			}
			if tried && !recorded[rc.Channel.ID] {
				if segErr == "" {
					segErr = "全部组合尝试失败"
				}
				s.Breaker.RecordFailure(rc.Channel.ID, probe.Model, segErr)
				recorded[rc.Channel.ID] = true
			}
		}
		respondOpenAIError(c, http.StatusBadGateway, "无可用上游（completions/embeddings 仅支持 openai 型渠道）")
	}
}

// HandleOpenAIResponses POST /v1/responses（OpenAI Responses API，Codex 默认协议）
// 仅 openai 型渠道：模型名映射后透传到上游 /responses；流式逐块回写。
// 失败切换与 chat 管线同策略：网络错误/5xx 换线路组合，401/402/403/429 换 key
// （429/402 冷却）。
func (s *Server) HandleOpenAIResponses(c *gin.Context) {
	body := readBody(c, s.Cfg.BodyLimitMB)
	if body == nil {
		return
	}
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		respondOpenAIError(c, http.StatusBadRequest, "请求体不是合法的 OpenAI JSON："+err.Error())
		return
	}
	if probe.Model == "" {
		respondOpenAIError(c, http.StatusBadRequest, "缺少 model 字段")
		return
	}
	token, user := ctxTokenUser(c)
	matched, defaults, err := s.Routing.Resolve(user.ID, probe.Model, token.RouteFilter(), deref(token.ModelScope))
	if err != nil {
		respondOpenAIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if len(matched) == 0 && len(defaults) == 0 {
		respondOpenAIError(c, http.StatusNotFound,
			fmt.Sprintf("model %s 未命中任何渠道，请到控制台配置或设置默认渠道", probe.Model))
		return
	}
	reqStart := time.Now()
	c.Set(ctxEffortKey, reasoningEffortOf("openai", body))
	// 协议不兼容的渠道必然不会尝试，先剔除再算熔断视图（同 completions 管线）
	var viable []*routing.ResolvedChannel
	for _, rc := range append(matched, defaults...) {
		if rc.Channel.ForwardMode == "convert" && rc.Channel.Type != "openai" {
			continue
		}
		viable = append(viable, rc)
	}
	view := s.breakerView(viable, probe.Model)
	now := time.Now().Unix()
	var (
		lastStatus int
		lastBody   []byte
		lastErr    string
	)
	recorded := map[int64]bool{} // 熔断失败只按渠道记一次（matched+defaults 可能重复命中）
	for _, rc := range viable {
		if view != nil && view.Open(rc.Channel.ID) && !view.Due(rc.Channel.ID, now) {
			continue // 熔断冷却中：跳过该渠道
		}
		upstreamModel := mapModel(rc.Channel, probe.Model)
		m := map[string]any{}
		if e := json.Unmarshal(body, &m); e != nil {
			respondOpenAIError(c, http.StatusBadRequest, "非法 JSON: "+e.Error())
			return
		}
		m["model"] = upstreamModel
		sendBody, _ := json.Marshal(m)
		tried := false
		for _, a := range s.planFor(rc, probe.Model, view) {
			if a.trial && !s.Breaker.ClaimHalfOpen(rc.Channel.ID, probe.Model) {
				continue // 半开试探已被并发请求认领
			}
			tried = true
			a.protocol = "openai"
			a.request = c.Request
			resp, _, err := s.sendUpstream(a, "POST", httpx.UpstreamEndpoint(a.lineURL, "/responses"), sendBody, probe.Stream)
			if err != nil {
				lastErr = err.Error()
				s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, err.Error())
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, err.Error(), reqStart)
				continue
			}
			if isHTMLResponse(resp) {
				// 上游 2xx 却返回 HTML（SPA 回退）：该线路无 /responses 端点，换组合
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				lastErr = "上游 /responses 返回 HTML（端点不存在）"
				s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, lastErr)
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, lastErr, reqStart)
				continue
			}
			if keyLevelStatus(resp.StatusCode) {
				if cool := s.keyErrorCooldown(resp); cool > 0 {
					s.Routing.UpdateKeyCooldown(a.key.ID, cool)
				}
				s.Routing.MarkKeyError(a.key.ID, fmt.Sprintf("上游 %d", resp.StatusCode))
				lastStatus = resp.StatusCode
				lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				lastErr = upstreamStatusError(resp.StatusCode, lastBody)
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, lastErr, reqStart)
				continue
			}
			if resp.StatusCode >= 500 {
				lastStatus = resp.StatusCode
				lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				lastErr = upstreamStatusError(resp.StatusCode, lastBody)
				s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, fmt.Sprintf("上游 %d", resp.StatusCode))
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, lastErr, reqStart)
				continue
			}
			// 流式：先窥探首帧再提交（FR-SG2，与 chat 管线同策略；oct-yescode 案例）
			if probe.Stream {
				u, ttft, total, fate, summary, committed := s.streamResponse(c, a, "openai", resp, reqStart, 0, dialectResponses)
				if !committed {
					lastErr = summary
					s.Routing.MarkChannelStatus(rc.Channel.ID, false, summary)
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, summary, reqStart)
					continue
				}
				recorded[rc.Channel.ID] = true // 结局记账已完成，渠道段落不再重复计
				mark := ""
				if fate == fateErrFrame || fate == fateAborted {
					mark = summary
				}
				s.Routing.MarkChannelStatus(rc.Channel.ID, mark == "", mark)
				s.recordStreamOutcome(rc.Channel.ID, probe.Model, fate, summary)
				s.submitLogWithError(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, u, ttft, total, mark)
				return
			}
			s.recordUpstreamOutcome(rc.Channel.ID, probe.Model, resp.StatusCode)
			recorded[rc.Channel.ID] = true
			s.Routing.MarkChannelStatus(rc.Channel.ID, resp.StatusCode < 400, "")
			u, ttft, total := s.bodyResponse(c, a, "openai", resp, reqStart, 0)
			s.submitLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, u, ttft, total)
			return
		}
		if tried && !recorded[rc.Channel.ID] {
			s.Breaker.RecordFailure(rc.Channel.ID, probe.Model, lastErr)
			recorded[rc.Channel.ID] = true
		}
	}
	// 全部组合耗尽：每次失败尝试已逐条落日志，这里透传最后一次上游错误
	if lastStatus > 0 {
		respondRawOrConverted(c, "openai", lastStatus, lastBody, false)
		return
	}
	msg := "全部上游不可达（responses 仅支持 openai 型渠道）"
	if lastErr != "" {
		msg += "：" + lastErr
	}
	respondOpenAIError(c, http.StatusBadGateway, msg)
}

// passthroughResponse 原样回写上游响应
func passthroughResponse(c *gin.Context, resp *http.Response) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

// isHTMLResponse 上游 2xx 却返回 text/html：多数为网关型上游的 SPA 回退
// （未知路径返回前端页面），说明请求的 API 端点在该线路不存在，应换组合重试
func isHTMLResponse(resp *http.Response) bool {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	return strings.Contains(resp.Header.Get("Content-Type"), "text/html")
}

// ---------- Anthropic 入站 ----------

// HandleAnthropicMessages POST /v1/messages
func (s *Server) HandleAnthropicMessages(c *gin.Context) {
	body := readBody(c, s.Cfg.BodyLimitMB)
	if body == nil {
		return
	}
	var req convert.AnthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		respondAnthropicError(c, http.StatusBadRequest, "请求体不是合法的 Anthropic JSON："+err.Error())
		return
	}
	s.relay(c, "anthropic", req.Model, body, &req)
}

// HandleAnthropicCountTokens POST /v1/messages/count_tokens（本地估算）
func (s *Server) HandleAnthropicCountTokens(c *gin.Context) {
	if _, _, ok := s.tokenUserOr401(c); !ok {
		return
	}
	body := readBody(c, s.Cfg.BodyLimitMB)
	if body == nil {
		return
	}
	var req convert.AnthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		respondAnthropicError(c, http.StatusBadRequest, "非法 JSON："+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": convert.EstimateTokens(&req)})
}

// HandleModels GET /v1/models（OpenAI / Anthropic 双格式）
func (s *Server) HandleModels(c *gin.Context) {
	token, user := ctxTokenUser(c)
	models, err := s.Routing.AllEnabledModels(user.ID, token.RouteFilter())
	if err != nil {
		respondOpenAIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if token.ModelScope != nil && *token.ModelScope != "" {
		models = filterByScope(models, *token.ModelScope)
	}
	if c.GetHeader("anthropic-version") != "" || strings.Contains(c.GetHeader("Accept"), "anthropic") {
		type anModel struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			DisplayName string `json:"display_name"`
		}
		data := make([]anModel, 0, len(models))
		for _, m := range models {
			data = append(data, anModel{ID: m, Type: "model", DisplayName: m})
		}
		c.JSON(http.StatusOK, gin.H{
			"data": data, "has_more": false,
			"first_id": firstOr(models, ""), "last_id": lastOr(models, ""),
		})
		return
	}
	type oiModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]oiModel, 0, len(models))
	for _, m := range models {
		data = append(data, oiModel{ID: m, Object: "model", Created: 1700000000, OwnedBy: "keyway"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ---------- 核心管线 ----------

func (s *Server) relay(c *gin.Context, inbound, model string, rawBody []byte, _ any) {
	token, user := ctxTokenUser(c)

	matched, defaults, err := s.Routing.Resolve(user.ID, model, token.RouteFilter(), deref(token.ModelScope))
	if err != nil {
		respondProtocolError(c, inbound, http.StatusInternalServerError, err.Error())
		return
	}
	if len(matched) == 0 && len(defaults) == 0 {
		respondProtocolError(c, inbound, http.StatusNotFound,
			fmt.Sprintf("model %s 未命中任何渠道，请到控制台配置或设置默认渠道", model))
		return
	}

	view := s.breakerView(append(matched, defaults...), model)
	attempts := s.plan(matched, defaults, model, view)
	if len(attempts) == 0 && view != nil {
		// 过滤后无任何尝试（熔断渠道 + 其余渠道无密钥等）→ 旁路重试，可用性优先
		attempts = s.plan(matched, defaults, model, nil)
	}
	reqStart := time.Now()
	c.Set(ctxEffortKey, reasoningEffortOf(inbound, rawBody))
	var (
		lastStatus     int
		lastBody       []byte
		lastErr        string
		lastCrossProto bool // 最后一次失败尝试是否跨协议（决定错误体是否需要转换）
	)
	// 熔断记录：按渠道分段，段落内全部尝试耗尽 → 记一次「渠道×模型」失败（FR-B1）
	var (
		segCh    int64
		segTried bool
		segErr   string
	)
	closeSeg := func() {
		if segCh != 0 && segTried {
			s.Breaker.RecordFailure(segCh, model, segErr)
		}
	}
	for _, a := range attempts {
		if a.rc.Channel.ID != segCh {
			closeSeg()
			segCh, segTried, segErr = a.rc.Channel.ID, false, ""
		}
		if a.trial && !s.Breaker.ClaimHalfOpen(a.rc.Channel.ID, model) {
			continue // 半开试探已被并发请求认领，跳过该渠道
		}
		segTried = true
		a.protocol = inbound
		if a.rc.Channel.ForwardMode == "convert" {
			a.protocol = a.rc.Channel.Type
		}
		a.request = c.Request
		upstreamModel := mapModel(a.rc.Channel, model)
		sendBody, targetURL, dropped, cross, err := s.buildUpstreamRequest(inbound, a.protocol, rawBody, a, upstreamModel)
		if err != nil {
			respondProtocolError(c, inbound, http.StatusBadRequest, err.Error())
			return
		}

		stream := s.requestWantsStream(inbound, rawBody)
		resp, _, err := s.sendUpstream(a, "POST", targetURL, sendBody, stream)
		if err != nil {
			lastErr = err.Error()
			lastCrossProto = cross
			segErr = err.Error()
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, err.Error())
			s.submitFailureLog(c, a, inbound, model, upstreamModel, http.StatusBadGateway, err.Error(), reqStart)
			continue // 网络错误 → 下一组合
		}

		// 上游 2xx 却返回 HTML（SPA 回退）：端点不存在，换组合
		if isHTMLResponse(resp) {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = "上游返回 HTML（端点不存在）"
			lastCrossProto = cross
			segErr = lastErr
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, lastErr)
			s.submitFailureLog(c, a, inbound, model, upstreamModel, http.StatusBadGateway, lastErr, reqStart)
			continue
		}

		// 密钥级错误（鉴权/限流/计费限额）→ 换 key；429/402 设置冷却
		if keyLevelStatus(resp.StatusCode) {
			if cool := s.keyErrorCooldown(resp); cool > 0 {
				s.Routing.UpdateKeyCooldown(a.key.ID, cool)
			}
			s.Routing.MarkKeyError(a.key.ID, fmt.Sprintf("上游 %d", resp.StatusCode))
			lastStatus = resp.StatusCode
			lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			lastCrossProto = cross
			segErr = upstreamStatusError(resp.StatusCode, lastBody)
			resp.Body.Close()
			s.submitFailureLog(c, a, inbound, model, upstreamModel, resp.StatusCode, upstreamStatusError(resp.StatusCode, lastBody), reqStart)
			continue
		}
		// 5xx → 换组合
		if resp.StatusCode >= 500 {
			lastStatus = resp.StatusCode
			lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			lastCrossProto = cross
			resp.Body.Close()
			segErr = upstreamStatusError(resp.StatusCode, lastBody)
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, fmt.Sprintf("上游 %d", resp.StatusCode))
			s.submitFailureLog(c, a, inbound, model, upstreamModel, resp.StatusCode, upstreamStatusError(resp.StatusCode, lastBody), reqStart)
			continue
		}

		// 流式：先窥探首帧再提交（FR-SG2）——首帧错误/超时/零数据终止按上游
		// 失败换下一组合，未向客户端写出任何字节（无损切换）
		if stream {
			if len(dropped) > 0 {
				c.Header("X-Keyway-Dropped", strings.Join(dropped, ","))
			}
			dialect := dialectChat
			if a.protocol == "anthropic" {
				dialect = dialectAnthropic
			}
			promptEst := estimateRequestTokens(inbound, rawBody)
			u, ttft, total, fate, summary, committed := s.streamResponse(c, a, inbound, resp, reqStart, promptEst, dialect)
			if !committed {
				segErr = summary
				lastErr = summary
				s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, summary)
				s.submitFailureLog(c, a, inbound, model, upstreamModel, http.StatusBadGateway, summary, reqStart)
				continue
			}
			// 提交后按流结局记账（FR-SG1）：错误/异常终止计熔断失败并落错误摘要
			mark := ""
			if fate == fateErrFrame || fate == fateAborted {
				mark = summary
			}
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, mark == "", mark)
			s.recordStreamOutcome(a.rc.Channel.ID, model, fate, summary)
			s.submitLogWithError(c, a, inbound, model, upstreamModel, resp.StatusCode, u, ttft, total, mark)
			return
		}

		// 非流式成功（或其他 4xx 透传）：按最终状态维护熔断（2xx/3xx 关闭；404 计为
		// 模型维度失败，其余 4xx 是客户端侧问题不计）
		s.recordUpstreamOutcome(a.rc.Channel.ID, model, resp.StatusCode)
		s.Routing.MarkChannelStatus(a.rc.Channel.ID, resp.StatusCode < 400, "")
		if len(dropped) > 0 {
			c.Header("X-Keyway-Dropped", strings.Join(dropped, ","))
		}
		promptEst := estimateRequestTokens(inbound, rawBody)
		u, ttft, total := s.bodyResponse(c, a, inbound, resp, reqStart, promptEst)
		s.submitLog(c, a, inbound, model, upstreamModel, resp.StatusCode, u, ttft, total)
		return
	}
	closeSeg()

	// 全部组合耗尽：每次失败尝试已逐条落日志，这里透传最后一次上游错误
	if lastStatus > 0 {
		respondRawOrConverted(c, inbound, lastStatus, lastBody, lastCrossProto)
		return
	}
	msg := "全部上游不可达"
	if lastErr != "" {
		msg += "：" + lastErr
	}
	respondProtocolError(c, inbound, http.StatusBadGateway, msg)
}

// buildUpstreamRequest 按入站协议与渠道类型组装上游请求体
func (s *Server) buildUpstreamRequest(inbound, channelType string, rawBody []byte, a attempt, upstreamModel string) (sendBody []byte, targetURL string, dropped []string, crossConvert bool, err error) {
	if inbound == "openai" {
		if channelType == "openai" {
			m := map[string]any{}
			if e := json.Unmarshal(rawBody, &m); e != nil {
				return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
			}
			m["model"] = upstreamModel
			ensureIncludeUsage(m)
			sendBody, _ = json.Marshal(m)
			return sendBody, httpx.UpstreamEndpoint(a.lineURL, "/chat/completions"), nil, false, nil
		}
		var req convert.OpenAIChatRequest
		if e := json.Unmarshal(rawBody, &req); e != nil {
			return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
		}
		if req.N > 1 {
			return nil, "", nil, false, convert.ErrNNotSupported
		}
		an, dr, e := convert.OpenAIChatToAnthropic(&req)
		if e != nil {
			return nil, "", nil, false, e
		}
		an.Model = upstreamModel
		sendBody, _ = json.Marshal(an)
		return sendBody, httpx.UpstreamEndpoint(a.lineURL, "/messages"), dr, true, nil
	}
	// anthropic 入站
	if channelType == "anthropic" {
		m := map[string]any{}
		if e := json.Unmarshal(rawBody, &m); e != nil {
			return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
		}
		m["model"] = upstreamModel
		sendBody, _ = json.Marshal(m)
		return sendBody, httpx.UpstreamEndpoint(a.lineURL, "/messages"), nil, false, nil
	}
	var req convert.AnthropicMessagesRequest
	if e := json.Unmarshal(rawBody, &req); e != nil {
		return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
	}
	oi, dr := convert.AnthropicToOpenAIChat(&req)
	oi.Model = upstreamModel
	sendBody, _ = json.Marshal(oi)
	return sendBody, httpx.UpstreamEndpoint(a.lineURL, "/chat/completions"), dr, true, nil
}

// ensureIncludeUsage 流式请求且未显式拒绝 usage 回传时注入 stream_options.include_usage，
// 让上游在末帧回带 usage（统计兜底，DESIGN §7.3）；客户端显式传 false 时尊重原值
func ensureIncludeUsage(m map[string]any) {
	stream, _ := m["stream"].(bool)
	if !stream {
		return
	}
	if so, ok := m["stream_options"].(map[string]any); ok {
		if inc, ok := so["include_usage"].(bool); ok && !inc {
			return
		}
		so["include_usage"] = true
		return
	}
	m["stream_options"] = map[string]any{"include_usage": true}
}

// sendUpstream 发送上游请求（含鉴权头、代理客户端、超时）
func (s *Server) sendUpstream(a attempt, method, url string, sendBody []byte, stream bool) (*http.Response, string, error) {
	keyPlain, err := routing.DecodeKeyValue(s.Secret, a.key)
	if err != nil {
		return nil, "", err
	}
	ctx := context.Background()
	if a.request != nil {
		ctx = a.request.Context()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(sendBody))
	if err != nil {
		return nil, "", err
	}
	if a.request != nil {
		copyForwardHeaders(req.Header, a.request.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	protocol := a.protocol
	if protocol == "" {
		protocol = a.rc.Channel.Type
	}
	if protocol == "anthropic" {
		req.Header.Set("x-api-key", keyPlain)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	} else {
		req.Header.Del("anthropic-version")
		req.Header.Del("anthropic-beta")
		req.Header.Set("Authorization", "Bearer "+keyPlain)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	client, err := s.getClient(a.proxyURL)
	if err != nil {
		return nil, "", err
	}
	if a.proxyID != 0 && a.rc != nil && a.rc.Channel != nil {
		// 公共代理流量按请求体与响应体累计；请求失败时也计入已发出的请求体。
		s.PM.Record(a.rc.Channel.UserID, a.proxyID, int64(len(sendBody)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("上游连接失败（%s %s）: %w", a.via, shortURL(a.lineURL), err)
	}
	return resp, keyPlain, nil
}

// copyForwardHeaders 保留透明转发所需的普通请求头，认证头由网关重新写入。
func copyForwardHeaders(dst, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" || lower == "host" ||
			lower == "content-length" || lower == "connection" || lower == "accept-encoding" {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// streamResponse 流式回写：跨协议走转换器，同协议逐行透传（flush）；公共代理统计出站字节。
// v1.5.54 重构（FR-SG1/SG2）：先**窥探首帧**再向客户端提交——首帧是错误事件、
// 零数据终止或首帧超时（复用空闲超时配置）→ 不提交任何字节，返回
// committed=false 由调用方继续尝试下一组合（可无损切换）；提交后透传全程
// 由 streamTracker 追踪结局，正常结束/流内错误/异常终止分别回报调用方记账。
// 保活与止损（对齐 new-api 的 SSE 处理经验）：
//   - ping：每 15s 向客户端写一行 SSE 注释（": ping"），防中间代理掐断长空闲连接
//   - 空闲超时：上游连续 IdleStreamTimeoutSec 无数据 → 关闭上游 body 唤醒读循环并告知客户端
//   - 客户端断开：立即关闭上游 body 止损（不等 transport 传播）
//   - usage 兜底：上游未回传 usage 时按入站请求与累计输出文本本地估算
//
// 返回 usage、首字节耗时、总耗时、流结局（fate）与失败摘要、是否已向客户端提交
func (s *Server) streamResponse(c *gin.Context, a attempt, inbound string, resp *http.Response, reqStart time.Time, promptEst int, dialect streamDialect) (convert.Usage, int64, int64, streamFate, string, bool) {
	channelType := a.protocol
	if channelType == "" {
		channelType = inbound
	}
	idle := time.Duration(s.Cfg.IdleStreamTimeoutSec) * time.Second

	// 阶段一：首帧窥探（FR-SG2）——未提交任何字节前判定，失败可无损切换
	pk := peekFirstData(resp, dialect, idle, c.Request.Context())
	if !pk.committed {
		return convert.Usage{}, 0, 0, pk.fate, pk.summary, false
	}

	// 阶段二：提交响应头，进入透传
	tracker := newStreamTracker(dialect)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	c.Writer.WriteHeader(resp.StatusCode)

	var conv interface {
		Feed([]byte) ([]byte, bool, error)
		Finish() ([]byte, error)
		Usage() convert.Usage
	}
	if inbound == "openai" && channelType == "anthropic" {
		conv = convert.NewAnthropicToOpenAIStream()
	} else if inbound == "anthropic" && channelType == "openai" {
		conv = convert.NewOpenAIToAnthropicStream()
	}

	// 写互斥：读循环与保活协程都可能写客户端
	var writeMu sync.Mutex
	writeOut := func(b []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		written, err := c.Writer.Write(b)
		if written > 0 {
			c.Writer.Flush()
		}
		_ = err
	}

	// 保活/止损协程（引用局部变量而非 gin.Context：其对象池会被后续请求复用）
	reqCtx := c.Request.Context()
	done := make(chan struct{})
	watchDone := make(chan struct{})
	var lastUpstream atomic.Int64
	lastUpstream.Store(time.Now().UnixMilli())
	var clientGone atomic.Bool
	go func() {
		defer close(watchDone)
		// 检查粒度 500ms：空闲超时（可低至秒级）与 ping（15s）共用同一循环
		const pingEvery = 15 * time.Second
		tick := 500 * time.Millisecond
		if idle > 0 && idle/4 < tick {
			tick = idle / 4
		}
		lastPing := time.Now()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-reqCtx.Done():
				// 客户端断开：立即关闭上游止损
				clientGone.Store(true)
				resp.Body.Close()
				return
			case <-t.C:
				if idle > 0 && time.Since(time.UnixMilli(lastUpstream.Load())) > idle {
					writeOut([]byte(": keyway: upstream idle timeout\n\n"))
					resp.Body.Close()
					return
				}
				if time.Since(lastPing) >= pingEvery {
					lastPing = time.Now()
					writeOut([]byte(": ping\n\n"))
				}
			}
		}
	}()
	// 收尾顺序：先通知协程退出并等其完全退出，再返回（gin.Context/Writer 会被
	// 下一个请求复用，避免协程残留访问）
	defer func() {
		close(done)
		<-watchDone
	}()

	buf := make([]byte, 32*1024)
	var lineBuf []byte
	var u convert.Usage
	var tally streamTally
	var totalBytes int64
	ttft := int64(0)
	markTTFT := func() {
		if ttft == 0 {
			ttft = time.Since(reqStart).Milliseconds()
		}
	}
	// 行处理：窥探缓冲与后续读取统一喂入（含结局追踪与 usage 嗅探）
	processChunk := func(chunk []byte) {
		if len(chunk) == 0 {
			return
		}
		lastUpstream.Store(time.Now().UnixMilli())
		totalBytes += int64(len(chunk))
		lineBuf = append(lineBuf, chunk...)
		for {
			idx := bytes.IndexByte(lineBuf, '\n')
			if idx < 0 {
				break
			}
			line := lineBuf[:idx+1]
			lineBuf = lineBuf[idx+1:]
			tracker.feedLine(line)
			s.writeStreamLine(conv, line, &u, markTTFT, writeOut)
			tally.feed(line)
		}
	}
	processChunk(pk.buf)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			processChunk(buf[:n])
		}
		if readErr != nil {
			if len(lineBuf) > 0 {
				tracker.feedTail(lineBuf)
				s.writeStreamLine(conv, lineBuf, &u, markTTFT, writeOut)
				tally.feed(lineBuf)
			}
			if conv != nil {
				if out, err := conv.Finish(); err == nil && len(out) > 0 {
					writeOut(out)
				}
				u = conv.Usage()
			}
			break
		}
	}
	resp.Body.Close()
	writeMu.Lock()
	c.Writer.Flush()
	writeMu.Unlock()
	if a.proxyID != 0 {
		if _, user := ctxTokenUser(c); user != nil {
			s.PM.Record(user.ID, a.proxyID, totalBytes)
		}
	}
	// usage 兜底：上游未回传的字段用本地估算补齐（不覆盖真实值）
	if u.PromptTokens == 0 && promptEst > 0 {
		u.PromptTokens = promptEst
	}
	if u.CompletionTokens == 0 {
		if est := tally.tokens(); est > 0 {
			u.CompletionTokens = est
		}
	}
	if clientGone.Load() {
		tracker.markClientGone()
	}
	fate, summary := tracker.ended()
	total := time.Since(reqStart).Milliseconds()
	return u, ttft, total, fate, summary, true
}

func (s *Server) writeStreamLine(conv interface {
	Feed([]byte) ([]byte, bool, error)
	Finish() ([]byte, error)
	Usage() convert.Usage
}, line []byte, u *convert.Usage, markTTFT func(), writeOut func([]byte)) {
	if conv != nil {
		out, _, err := conv.Feed(line)
		if err == nil && len(out) > 0 {
			markTTFT()
			writeOut(out)
		}
		return
	}
	// 同协议透传，并嗅探 usage
	markTTFT()
	*u = sniffUsage(*u, line)
	writeOut(line)
}

// bodyResponse 非流式回写：跨协议转换响应体；公共代理统计出站字节。
// 上游未回传 usage 时按入站请求与响应文本本地估算兜底。
// 返回 usage、首字节耗时（自请求开始到开始写出）、总耗时
func (s *Server) bodyResponse(c *gin.Context, a attempt, inbound string, resp *http.Response, reqStart time.Time, promptEst int) (convert.Usage, int64, int64) {
	channelType := a.protocol
	if channelType == "" {
		channelType = inbound
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	if a.proxyID != 0 {
		if _, user := ctxTokenUser(c); user != nil {
			s.PM.Record(user.ID, a.proxyID, int64(len(body)))
		}
	}
	ttft := time.Since(reqStart).Milliseconds()

	estimate := func(u convert.Usage) convert.Usage {
		if u.PromptTokens == 0 && promptEst > 0 {
			u.PromptTokens = promptEst
		}
		if u.CompletionTokens == 0 {
			if est := estimateResponseTokens(body); est > 0 {
				u.CompletionTokens = est
			}
		}
		return u
	}

	if inbound == "openai" && channelType == "anthropic" {
		var an convert.AnthropicMessagesResponse
		if err := json.Unmarshal(body, &an); err == nil {
			u := estimate(convert.NormalizeAnthropicUsage(&an.Usage))
			oi := convert.AnthropicToOpenAIResponse(&an)
			oi.Model = modelFromUpstream(body)
			c.Data(resp.StatusCode, "application/json", mustJSON(oi))
			return u, ttft, time.Since(reqStart).Milliseconds()
		}
	} else if inbound == "anthropic" && channelType == "openai" {
		var oi convert.OpenAIChatResponse
		if err := json.Unmarshal(body, &oi); err == nil {
			u := estimate(convert.NormalizeOpenAIUsage(oi.Usage))
			an := convert.OpenAIToAnthropicResponse(&oi)
			c.Data(resp.StatusCode, "application/json", mustJSON(an))
			return u, ttft, time.Since(reqStart).Milliseconds()
		}
	}
	// 同协议或转换失败：原样透传
	u := estimate(sniffUsage(convert.Usage{}, body))
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	return u, ttft, time.Since(reqStart).Milliseconds()
}

// ---------- 辅助 ----------

func (s *Server) tokenUserOr401(c *gin.Context) (*store.Token, *store.User, bool) {
	raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if raw == "" {
		raw = c.GetHeader("x-api-key")
	}
	tok, user, err := s.Auth.AuthenticateGatewayToken(raw)
	if err != nil {
		respondOpenAIError(c, http.StatusUnauthorized, err.Error())
		return nil, nil, false
	}
	return tok, user, true
}

func (s *Server) planFor(rc *routing.ResolvedChannel, model string, view breaker.View) []attempt {
	return s.plan([]*routing.ResolvedChannel{rc}, nil, model, view)
}

// recordUpstreamOutcome 以请求的最终上游结果维护熔断（FR-B1/FR-B4）：
// 2xx/3xx → 关闭熔断（流量切回）；404 与密钥级错误（401/402/403/429，仅出现在
// 透传型管线——chat/responses 管线中这些码会继续换 key、由段落耗尽统一记录）
// → 计一次失败；404 多为上游「模型/端点不存在」（如 new_api model_not_found、
// 网关 provider 不命中），正是模型维度熔断要捕获的形态；其余 4xx
// （400/413/422 等）是客户端侧问题，不计
func (s *Server) recordUpstreamOutcome(channelID int64, model string, statusCode int) {
	if s.Breaker == nil || model == "" {
		return
	}
	switch {
	case statusCode < 400:
		s.Breaker.RecordSuccess(channelID, model)
	case statusCode == http.StatusNotFound:
		s.Breaker.RecordFailure(channelID, model, "上游 404（模型或端点不存在）")
	case keyLevelStatus(statusCode):
		s.Breaker.RecordFailure(channelID, model, fmt.Sprintf("上游 %d", statusCode))
	}
}

// recordStreamOutcome 流式结局的熔断记账（FR-SG1，v1.5.54）：正常结束 →
// 关闭熔断（流量切回）；流内错误事件/异常终止 → 计一次「渠道×模型」失败
// （keyway 此前对流式一律按 HTTP 200 记成功，200+SSE 错误事件会清零熔断计数，
// oct-yescode 案例中限额渠道因此长期不被熔断）；客户端断开不计（不归上游）
func (s *Server) recordStreamOutcome(channelID int64, model string, fate streamFate, summary string) {
	if s.Breaker == nil || model == "" {
		return
	}
	switch fate {
	case fateOK:
		s.Breaker.RecordSuccess(channelID, model)
	case fateErrFrame, fateAborted:
		if summary == "" {
			summary = "流异常终止"
		}
		s.Breaker.RecordFailure(channelID, model, summary)
	}
}

// submitLog 异步记录日志（含费用快照与耗时）
func (s *Server) submitLog(c *gin.Context, a attempt, inbound, model, upstreamModel string, statusCode int, u convert.Usage, ttftMs, totalMs int64) {
	s.submitLogWithError(c, a, inbound, model, upstreamModel, statusCode, u, ttftMs, totalMs, "")
}

// upstreamStatusError 失败尝试的日志/错误摘要：状态码 + 上游错误体 message
// （与 probe 探测摘要同格式，复用同一解析）
func upstreamStatusError(status int, body []byte) string {
	if summary := probe.UpstreamErrorSummary(body); summary != "" {
		return fmt.Sprintf("上游 %d：%s", status, summary)
	}
	return fmt.Sprintf("上游 %d", status)
}

func (s *Server) submitFailureLog(c *gin.Context, a attempt, inbound, model, upstreamModel string, statusCode int, message string, reqStart time.Time) {
	s.submitLogWithError(c, a, inbound, model, upstreamModel, statusCode, convert.Usage{}, 0, time.Since(reqStart).Milliseconds(), message)
}

func (s *Server) submitLogWithError(c *gin.Context, a attempt, inbound, model, upstreamModel string, statusCode int, u convert.Usage, ttftMs, totalMs int64, message string) {
	token, user := ctxTokenUser(c)
	if a.rc.Channel == nil {
		return
	}
	now := time.Now().Unix()
	multiplier := a.rc.Channel.PriceMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	ic, oc := usage.ComputeCost(s.Store.DB(), model, upstreamModel,
		a.rc.Channel.PricingMode, multiplier, a.rc.Channel.CNYRatio, u)
	l := &store.Log{
		CreatedAt:        now,
		UserID:           user.ID,
		TokenID:          &token.ID,
		ChannelID:        &a.rc.Channel.ID,
		TemplateSourceID: a.rc.Channel.CopiedFromTemplateID,
		LineURL:          &a.lineURL,
		Via:              &a.via,
		KeyID:            &a.key.ID,
		Protocol:         &inbound,
		Model:            &model,
		UpstreamModel:    &upstreamModel,
		StatusCode:       &statusCode,
		TotalMs:          &totalMs,
		TtftMs:           &ttftMs,
		PromptTokens:     int64p(u.PromptTokens),
		CompletionTokens: int64p(u.CompletionTokens),
		CachedTokens:     int64p(u.CachedTokens),
		CacheWriteTokens: int64p(u.CacheWriteTokens),
		InputCost:        ic,
		OutputCost:       oc,
	}
	if message == "" && statusCode >= 400 {
		message = fmt.Sprintf("上游返回 %d", statusCode)
	}
	if message != "" {
		if len(message) > 512 {
			message = message[:512]
		}
		l.Error = &message
	}
	if v, ok := c.Get(ctxEffortKey); ok {
		if effort, ok := v.(string); ok && effort != "" {
			l.ReasoningEffort = &effort
		}
	}
	s.Logs.Submit(l)
}

func (s *Server) requestWantsStream(inbound string, rawBody []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(rawBody, &probe)
	return probe.Stream
}

// ctxEffortKey 请求解析出的推理强度暂存于 gin.Context（submitLogWithError 统一取用）
const ctxEffortKey = "kw_effort"

// reasoningEffortOf 解析入站请求的推理强度（未开启返回空串）：
//   - openai（chat / responses）：顶层 reasoning_effort，或 reasoning.effort
//     （Responses API 嵌套结构），取原值（minimal/low/medium/high/…）
//   - anthropic：thinking.budget_tokens → 归一为 thinking:N
//
// 先做子串预检（O(n) 无分配扫描）再解析：正文可能恰好含这些词，误命中只是
// 多付一次解析，不损正确性；绝大多数未开启推理的请求免掉整包 JSON 解析
func reasoningEffortOf(inbound string, rawBody []byte) string {
	if inbound == "anthropic" {
		if !bytes.Contains(rawBody, []byte("thinking")) {
			return ""
		}
		var probe struct {
			Thinking *struct {
				BudgetTokens int64 `json:"budget_tokens"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(rawBody, &probe); err == nil &&
			probe.Thinking != nil && probe.Thinking.BudgetTokens > 0 {
			return "thinking:" + strconv.FormatInt(probe.Thinking.BudgetTokens, 10)
		}
		return ""
	}
	if !bytes.Contains(rawBody, []byte("reasoning")) {
		return ""
	}
	var probe struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(rawBody, &probe); err != nil {
		return ""
	}
	if probe.ReasoningEffort != "" {
		return probe.ReasoningEffort
	}
	if probe.Reasoning != nil && probe.Reasoning.Effort != "" {
		return probe.Reasoning.Effort
	}
	return ""
}

func readBody(c *gin.Context, limitMB int) []byte {
	if limitMB <= 0 {
		limitMB = 50
	}
	limit := int64(limitMB) << 20
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
	if err != nil {
		respondOpenAIError(c, http.StatusBadRequest, "读取请求体失败")
		return nil
	}
	if int64(len(body)) > limit {
		respondOpenAIError(c, http.StatusRequestEntityTooLarge, "请求体超过大小限制")
		return nil
	}
	return body
}

func mapModel(ch *store.Channel, model string) string {
	return routing.ApplyModelMapping(ch, model)
}

// keyLevelStatus 是否密钥级错误：401/403 鉴权失败、429 限流、402 计费限额
// 耗尽（中转/聚合型网关以 402 表达余额或团队消费上限，如 OpenRouter
// insufficient credits、Team weekly spending limit；官方 API 的配额耗尽走 429）。
// 这类错误重试同一把 key 不会成功，应换 key 继续尝试
func keyLevelStatus(code int) bool {
	return code == 401 || code == 402 || code == 403 || code == 429
}

// keyErrorCooldown 密钥级错误的冷却秒数：429 按 Retry-After 头（缺省
// KEYWAY_KEY_COOLDOWN_S）；402 计费限额耗尽为长冷却 KEYWAY_QUOTA_COOLDOWN_S
// （限额多为天/周级，短期内重试同一把 key 必然失败；≤0 回落 3600）；
// 401/403 不冷却（换 key 即可）
func (s *Server) keyErrorCooldown(resp *http.Response) int64 {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return int64(parseRetryAfter(resp.Header.Get("Retry-After")))
	case http.StatusPaymentRequired:
		cool := s.Cfg.QuotaCooldownSec
		if cool <= 0 {
			cool = 3600
		}
		return int64(cool)
	}
	return 0
}

func parseRetryAfter(v string) int {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return 60
}

func respondOpenAIError(c *gin.Context, status int, msg string) {
	c.JSON(status, convert.OpenAIErrorBody{Error: convert.OpenAIErrorDetail{
		Message: msg, Type: "keyway_error",
	}})
}

func respondAnthropicError(c *gin.Context, status int, msg string) {
	c.JSON(status, convert.AnthropicErrorBody{
		Type:  "error",
		Error: convert.AnthropicErrorDetail{Type: "api_error", Message: msg},
	})
}

func respondProtocolError(c *gin.Context, inbound string, status int, msg string) {
	if inbound == "anthropic" {
		respondAnthropicError(c, status, msg)
		return
	}
	respondOpenAIError(c, status, msg)
}

// respondRawOrConverted 上游错误透传（跨协议失败时才转换错误结构，同协议保持原文）
func respondRawOrConverted(c *gin.Context, inbound string, status int, body []byte, crossProto bool) {
	if len(body) == 0 {
		respondProtocolError(c, inbound, status, "上游错误（无响应体）")
		return
	}
	if crossProto {
		if inbound == "anthropic" {
			c.Data(status, "application/json", convert.OpenAIErrorToAnthropic(body))
			return
		}
		c.Data(status, "application/json", convert.AnthropicErrorToOpenAI(body))
		return
	}
	c.Data(status, "application/json", body)
}

// sniffUsage 从透传字节中嗅探 usage（尽力而为；兼容 SSE 行前缀）。
// 兼容结构：chat completions 顶层 usage、Responses response.completed 的
// response.usage、anthropic 非流式顶层 usage 与流式 message_start 的
// message.usage / message_delta 的顶层 usage（v1.5.66——此前 anthropic 形状
// 不识别，透传流量全部落到本地估算兜底，缓存读/写丢失导致费用按全价高估）。
// anthropic 流式分片按字段级非零合并（message_start 带 input+缓存，message_delta 带 output）
func sniffUsage(u convert.Usage, data []byte) convert.Usage {
	payload := data
	if s := strings.TrimSpace(string(data)); strings.HasPrefix(s, "data:") {
		payload = []byte(strings.TrimSpace(strings.TrimPrefix(s, "data:")))
	}
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return u
	}
	var probe struct {
		Response *struct {
			Usage *responsesUsage `json:"usage"`
		} `json:"response"`
		Message *struct {
			Usage *anthropicSniffUsage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return u
	}
	if probe.Response != nil && probe.Response.Usage != nil {
		return probe.Response.Usage.normalized()
	}
	if probe.Message != nil && probe.Message.Usage != nil {
		return mergeUsage(u, probe.Message.Usage.usage())
	}
	// 顶层 usage：anthropic 形状（非流式 / message_delta）优先按其字段解析，
	// 命中任一非零字段即合并；否则回退 OpenAI 形状
	var an struct {
		Usage *anthropicSniffUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &an); err == nil && an.Usage != nil {
		au := an.Usage.usage()
		if au.PromptTokens > 0 || au.CompletionTokens > 0 || au.CachedTokens > 0 || au.CacheWriteTokens > 0 {
			return mergeUsage(u, au)
		}
	}
	var openai struct {
		Usage *convert.OpenAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &openai); err == nil && openai.Usage != nil {
		return convert.NormalizeOpenAIUsage(openai.Usage)
	}
	return u
}

// anthropicSniffUsage anthropic usage 的嗅探形状（归一口径同 NormalizeAnthropicUsage：
// 总输入 = input + cache_read + cache_creation）
type anthropicSniffUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (a *anthropicSniffUsage) usage() convert.Usage {
	return convert.NormalizeAnthropicUsage(&convert.AnthropicUsage{
		InputTokens:              a.InputTokens,
		OutputTokens:             a.OutputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens,
	})
}

// mergeUsage 字段级非零合并（anthropic 流式 usage 分片到帧：message_start 带
// input+缓存，message_delta 带 output）
func mergeUsage(base, extra convert.Usage) convert.Usage {
	if extra.PromptTokens > 0 {
		base.PromptTokens = extra.PromptTokens
	}
	if extra.CompletionTokens > 0 {
		base.CompletionTokens = extra.CompletionTokens
	}
	if extra.CachedTokens > 0 {
		base.CachedTokens = extra.CachedTokens
	}
	if extra.CacheWriteTokens > 0 {
		base.CacheWriteTokens = extra.CacheWriteTokens
	}
	return base
}

// responsesUsage Responses API 的 usage 结构（input/output 命名）
type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (r *responsesUsage) normalized() convert.Usage {
	cached := 0
	if r.InputTokensDetails != nil {
		cached = r.InputTokensDetails.CachedTokens
	}
	return convert.Usage{
		PromptTokens:     r.InputTokens,
		CompletionTokens: r.OutputTokens,
		CachedTokens:     cached,
	}
}

func ctxTokenUser(c *gin.Context) (*store.Token, *store.User) {
	t, _ := c.Get("token")
	u, _ := c.Get("user")
	tok, _ := t.(*store.Token)
	user, _ := u.(*store.User)
	return tok, user
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int64p(v int) *int64 {
	n := int64(v)
	return &n
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func modelFromUpstream(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &probe)
	return probe.Model
}

func shortURL(u string) string {
	if len(u) > 48 {
		return u[:48] + "…"
	}
	return u
}

func filterByScope(models []string, scope string) []string {
	var out []string
	for _, m := range models {
		if matchScopeStr(scope, m) {
			out = append(out, m)
		}
	}
	return out
}

func matchScopeStr(scope, model string) bool {
	if strings.HasSuffix(scope, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(scope, "*"))
	}
	return scope == model
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}

func lastOr(list []string, def string) string {
	if len(list) > 0 {
		return list[len(list)-1]
	}
	return def
}
