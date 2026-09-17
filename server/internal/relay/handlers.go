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
		reqStart := time.Now()
		recorded := map[int64]bool{} // 熔断失败只按渠道记一次（matched+defaults 可能重复命中）
		for _, rc := range viable {
			if view != nil && view.Open(rc.Channel.ID) {
				continue // 熔断中：跳过该渠道
			}
			upstreamModel := mapModel(rc.Channel, probe.Model)
			m := map[string]any{}
			json.Unmarshal(body, &m)
			m["model"] = upstreamModel
			sendBody, _ := json.Marshal(m)
			tried := false
			for _, a := range s.planFor(rc, probe.Model, view) {
				tried = true
				a.protocol = "openai"
				a.request = c.Request
				resp, _, err := s.sendUpstream(a, "POST", httpx.UpstreamEndpoint(a.lineURL, path), sendBody, false)
				if err != nil {
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, err.Error(), reqStart)
					continue
				}
				if isHTMLResponse(resp) {
					// 上游 2xx 却返回 HTML（SPA 回退）：端点不存在，换组合
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					lastErr := "上游返回 HTML（端点不存在）"
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, http.StatusBadGateway, lastErr, reqStart)
					continue
				}
				if resp.StatusCode >= 500 {
					errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, upstreamStatusError(resp.StatusCode, errBody), reqStart)
					continue
				}
				s.recordUpstreamOutcome(rc.Channel.ID, probe.Model, resp.StatusCode)
				recorded[rc.Channel.ID] = true
				passthroughResponse(c, resp)
				s.submitLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, convert.Usage{}, 0, 0)
				return
			}
			if tried && !recorded[rc.Channel.ID] {
				s.Breaker.RecordFailure(rc.Channel.ID, probe.Model, "全部组合尝试失败")
				recorded[rc.Channel.ID] = true
			}
		}
		respondOpenAIError(c, http.StatusBadGateway, "无可用上游（completions/embeddings 仅支持 openai 型渠道）")
	}
}

// HandleOpenAIResponses POST /v1/responses（OpenAI Responses API，Codex 默认协议）
// 仅 openai 型渠道：模型名映射后透传到上游 /responses；流式逐块回写。
// 失败切换与 chat 管线同策略：网络错误/5xx 换线路组合，401/403/429 换 key（429 冷却）。
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
	// 协议不兼容的渠道必然不会尝试，先剔除再算熔断视图（同 completions 管线）
	var viable []*routing.ResolvedChannel
	for _, rc := range append(matched, defaults...) {
		if rc.Channel.ForwardMode == "convert" && rc.Channel.Type != "openai" {
			continue
		}
		viable = append(viable, rc)
	}
	view := s.breakerView(viable, probe.Model)
	var (
		lastStatus int
		lastBody   []byte
		lastErr    string
	)
	recorded := map[int64]bool{} // 熔断失败只按渠道记一次（matched+defaults 可能重复命中）
	for _, rc := range viable {
		if view != nil && view.Open(rc.Channel.ID) {
			continue // 熔断中：跳过该渠道
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
			if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 {
				if resp.StatusCode == 429 {
					cool := parseRetryAfter(resp.Header.Get("Retry-After"))
					s.Routing.UpdateKeyCooldown(a.key.ID, int64(cool))
				}
				s.Routing.MarkKeyError(a.key.ID, fmt.Sprintf("上游 %d", resp.StatusCode))
				lastStatus = resp.StatusCode
				lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, upstreamStatusError(resp.StatusCode, lastBody), reqStart)
				continue
			}
			if resp.StatusCode >= 500 {
				lastStatus = resp.StatusCode
				lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, fmt.Sprintf("上游 %d", resp.StatusCode))
				s.submitFailureLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, upstreamStatusError(resp.StatusCode, lastBody), reqStart)
				continue
			}
			s.recordUpstreamOutcome(rc.Channel.ID, probe.Model, resp.StatusCode)
			recorded[rc.Channel.ID] = true
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, resp.StatusCode < 400, "")
			var u convert.Usage
			ttft, total := int64(0), int64(0)
			if probe.Stream {
				u, ttft, total = s.streamResponse(c, a, "openai", resp, reqStart, 0)
			} else {
				u, ttft, total = s.bodyResponse(c, a, "openai", resp, reqStart, 0)
			}
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

		// 鉴权/限流 → 换 key；429 设置冷却
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 {
			if resp.StatusCode == 429 {
				cool := parseRetryAfter(resp.Header.Get("Retry-After"))
				s.Routing.UpdateKeyCooldown(a.key.ID, int64(cool))
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

		// 成功（或其他 4xx 透传）：按最终状态维护熔断（2xx/3xx 关闭；404 计为
		// 模型维度失败，其余 4xx 是客户端侧问题不计）
		s.recordUpstreamOutcome(a.rc.Channel.ID, model, resp.StatusCode)
		s.Routing.MarkChannelStatus(a.rc.Channel.ID, resp.StatusCode < 400, "")
		if len(dropped) > 0 {
			c.Header("X-Keyway-Dropped", strings.Join(dropped, ","))
		}
		var u convert.Usage
		ttft, total := int64(0), int64(0)
		promptEst := estimateRequestTokens(inbound, rawBody)
		if stream {
			u, ttft, total = s.streamResponse(c, a, inbound, resp, reqStart, promptEst)
		} else {
			u, ttft, total = s.bodyResponse(c, a, inbound, resp, reqStart, promptEst)
		}
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
// 保活与止损（对齐 new-api 的 SSE 处理经验）：
//   - ping：每 15s 向客户端写一行 SSE 注释（": ping"），防中间代理掐断长空闲连接
//   - 空闲超时：上游连续 IdleStreamTimeoutSec 无数据 → 关闭上游 body 唤醒读循环并告知客户端
//   - 客户端断开：立即关闭上游 body 止损（不等 transport 传播）
//   - usage 兜底：上游未回传 usage 时按入站请求与累计输出文本本地估算
//
// 返回 usage、首字节耗时（自请求开始到首个写出块）、总耗时
func (s *Server) streamResponse(c *gin.Context, a attempt, inbound string, resp *http.Response, reqStart time.Time, promptEst int) (convert.Usage, int64, int64) {
	channelType := a.protocol
	if channelType == "" {
		channelType = inbound
	}
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
	idle := time.Duration(s.Cfg.IdleStreamTimeoutSec) * time.Second
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
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			lastUpstream.Store(time.Now().UnixMilli())
			totalBytes += int64(n)
			markTTFT()
			lineBuf = append(lineBuf, buf[:n]...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx+1]
				lineBuf = lineBuf[idx+1:]
				s.writeStreamLine(conv, line, &u, markTTFT, writeOut)
				tally.feed(line)
			}
		}
		if readErr != nil {
			if len(lineBuf) > 0 {
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
	total := time.Since(reqStart).Milliseconds()
	return u, ttft, total
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
// 2xx/3xx → 关闭熔断（流量切回）；404 与鉴权/限流类（401/403/429，仅出现在
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
	case statusCode == 401 || statusCode == 403 || statusCode == 429:
		s.Breaker.RecordFailure(channelID, model, fmt.Sprintf("上游 %d", statusCode))
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
	s.Logs.Submit(l)
}

func (s *Server) requestWantsStream(inbound string, rawBody []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(rawBody, &probe)
	return probe.Stream
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
// 兼容两种结构：chat completions 的顶层 usage，与 Responses API
// response.completed 事件的 response.usage 嵌套结构
func sniffUsage(u convert.Usage, data []byte) convert.Usage {
	payload := data
	if s := strings.TrimSpace(string(data)); strings.HasPrefix(s, "data:") {
		payload = []byte(strings.TrimSpace(strings.TrimPrefix(s, "data:")))
	}
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return u
	}
	var probe struct {
		Usage    *convert.OpenAIUsage `json:"usage"`
		Response *struct {
			Usage *responsesUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return u
	}
	if probe.Usage != nil {
		return convert.NormalizeOpenAIUsage(probe.Usage)
	}
	if probe.Response != nil && probe.Response.Usage != nil {
		return probe.Response.Usage.normalized()
	}
	return u
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
