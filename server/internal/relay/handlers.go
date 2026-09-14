package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"keyway/internal/convert"
	"keyway/internal/routing"
	"keyway/internal/store"
	"keyway/internal/usage"
)

// ---------- OpenAI 入站 ----------

// HandleOpenAIChat POST /v1/chat/completions
func (s *Server) HandleOpenAIChat(c *gin.Context) {
	body := readBody(c, s.Cfg.BodyLimitMB)
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
		var probe struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &probe)
		if probe.Model == "" {
			respondOpenAIError(c, http.StatusBadRequest, "缺少 model 字段")
			return
		}
		token, user := ctxTokenUser(c)
		matched, defaults, err := s.Routing.Resolve(user.ID, probe.Model, token.ChannelID, deref(token.ModelScope))
		if err != nil {
			respondOpenAIError(c, http.StatusInternalServerError, err.Error())
			return
		}
		candidates := append(matched, defaults...)
		for _, rc := range candidates {
			if rc.Channel.Type != "openai" {
				continue
			}
			upstreamModel := mapModel(rc.Channel, probe.Model)
			m := map[string]any{}
			json.Unmarshal(body, &m)
			m["model"] = upstreamModel
			sendBody, _ := json.Marshal(m)
			for _, a := range s.planFor(rc) {
				resp, _, err := s.sendUpstream(a, "POST", strings.TrimSuffix(a.lineURL, "/")+path, sendBody, false)
				if err != nil {
					continue
				}
				if resp.StatusCode >= 500 {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					continue
				}
				passthroughResponse(c, resp)
				s.submitLog(c, a, "openai", probe.Model, upstreamModel, resp.StatusCode, convert.Usage{})
				return
			}
		}
		respondOpenAIError(c, http.StatusBadGateway, "无可用上游（completions/embeddings 仅支持 openai 型渠道）")
	}
}

// passthroughResponse 原样回写上游响应
func passthroughResponse(c *gin.Context, resp *http.Response) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

// ---------- Anthropic 入站 ----------

// HandleAnthropicMessages POST /v1/messages
func (s *Server) HandleAnthropicMessages(c *gin.Context) {
	body := readBody(c, s.Cfg.BodyLimitMB)
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
	models, err := s.Routing.AllEnabledModels(user.ID)
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

	matched, defaults, err := s.Routing.Resolve(user.ID, model, token.ChannelID, deref(token.ModelScope))
	if err != nil {
		respondProtocolError(c, inbound, http.StatusInternalServerError, err.Error())
		return
	}
	if len(matched) == 0 && len(defaults) == 0 {
		respondProtocolError(c, inbound, http.StatusNotFound,
			fmt.Sprintf("model %s 未命中任何渠道，请到控制台配置或设置默认渠道", model))
		return
	}

	attempts := s.plan(matched, defaults)
	var (
		lastStatus int
		lastBody   []byte
		lastErr    string
	)
	for _, a := range attempts {
		upstreamModel := mapModel(a.rc.Channel, model)
		sendBody, targetURL, dropped, _, err := s.buildUpstreamRequest(inbound, a.rc.Channel.Type, rawBody, a, upstreamModel)
		if err != nil {
			respondProtocolError(c, inbound, http.StatusBadRequest, err.Error())
			return
		}

		stream := s.requestWantsStream(inbound, rawBody)
		resp, _, err := s.sendUpstream(a, "POST", targetURL, sendBody, stream)
		if err != nil {
			lastErr = err.Error()
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, err.Error())
			continue // 网络错误 → 下一组合
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
			resp.Body.Close()
			continue
		}
		// 5xx → 换组合
		if resp.StatusCode >= 500 {
			lastStatus = resp.StatusCode
			lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			s.Routing.MarkChannelStatus(a.rc.Channel.ID, false, fmt.Sprintf("上游 %d", resp.StatusCode))
			continue
		}

		// 成功（或其他 4xx 透传）
		s.Routing.MarkChannelStatus(a.rc.Channel.ID, resp.StatusCode < 400, "")
		if len(dropped) > 0 {
			c.Header("X-Keyway-Dropped", strings.Join(dropped, ","))
		}
		var u convert.Usage
		if stream {
			u = s.streamResponse(c, a, inbound, resp)
		} else {
			u = s.bodyResponse(c, a, inbound, resp)
		}
		s.submitLog(c, a, inbound, model, upstreamModel, resp.StatusCode, u)
		return
	}

	// 全部组合耗尽
	if lastStatus > 0 {
		respondRawOrConverted(c, inbound, lastStatus, lastBody)
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
			sendBody, _ = json.Marshal(m)
			return sendBody, strings.TrimSuffix(a.lineURL, "/") + "/chat/completions", nil, false, nil
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
		return sendBody, strings.TrimSuffix(a.lineURL, "/") + "/v1/messages", dr, true, nil
	}
	// anthropic 入站
	if channelType == "anthropic" {
		m := map[string]any{}
		if e := json.Unmarshal(rawBody, &m); e != nil {
			return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
		}
		m["model"] = upstreamModel
		sendBody, _ = json.Marshal(m)
		return sendBody, strings.TrimSuffix(a.lineURL, "/") + "/v1/messages", nil, false, nil
	}
	var req convert.AnthropicMessagesRequest
	if e := json.Unmarshal(rawBody, &req); e != nil {
		return nil, "", nil, false, fmt.Errorf("非法 JSON: %w", e)
	}
	oi, dr := convert.AnthropicToOpenAIChat(&req)
	oi.Model = upstreamModel
	sendBody, _ = json.Marshal(oi)
	return sendBody, strings.TrimSuffix(a.lineURL, "/") + "/chat/completions", dr, true, nil
}

// sendUpstream 发送上游请求（含鉴权头、代理客户端、超时）
func (s *Server) sendUpstream(a attempt, method, url string, sendBody []byte, stream bool) (*http.Response, string, error) {
	keyPlain, err := routing.DecodeKeyValue(s.Secret, a.key)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(sendBody))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.rc.Channel.Type == "anthropic" {
		req.Header.Set("x-api-key", keyPlain)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+keyPlain)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	client, err := s.getClient(a.proxyURL)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("上游连接失败（%s %s）: %w", a.via, shortURL(a.lineURL), err)
	}
	return resp, keyPlain, nil
}

// streamResponse 流式回写：跨协议走转换器，同协议逐行透传（flush）；公共代理统计出站字节
func (s *Server) streamResponse(c *gin.Context, a attempt, inbound string, resp *http.Response) convert.Usage {
	channelType := a.rc.Channel.Type
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher := c.Writer

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

	buf := make([]byte, 32*1024)
	var lineBuf []byte
	var u convert.Usage
	var totalBytes int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			lineBuf = append(lineBuf, buf[:n]...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx+1]
				lineBuf = lineBuf[idx+1:]
				s.writeStreamLine(c, flusher, conv, line, &u)
			}
		}
		if readErr != nil {
			if len(lineBuf) > 0 {
				s.writeStreamLine(c, flusher, conv, lineBuf, &u)
			}
			if conv != nil {
				if out, err := conv.Finish(); err == nil && len(out) > 0 {
					flusher.Write(out)
				}
				u = conv.Usage()
			}
			break
		}
	}
	resp.Body.Close()
	c.Writer.Flush()
	if a.proxyID != 0 {
		if _, user := ctxTokenUser(c); user != nil {
			s.PM.Record(user.ID, a.proxyID, totalBytes)
		}
	}
	return u
}

func (s *Server) writeStreamLine(c *gin.Context, w io.Writer, conv interface {
	Feed([]byte) ([]byte, bool, error)
	Finish() ([]byte, error)
	Usage() convert.Usage
}, line []byte, u *convert.Usage) {
	if conv != nil {
		out, _, err := conv.Feed(line)
		if err == nil && len(out) > 0 {
			w.Write(out)
			c.Writer.Flush()
		}
		return
	}
	// 同协议透传，并嗅探 usage
	*u = sniffUsage(*u, line)
	w.Write(line)
	c.Writer.Flush()
}

// bodyResponse 非流式回写：跨协议转换响应体；公共代理统计出站字节
func (s *Server) bodyResponse(c *gin.Context, a attempt, inbound string, resp *http.Response) convert.Usage {
	channelType := a.rc.Channel.Type
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	if a.proxyID != 0 {
		if _, user := ctxTokenUser(c); user != nil {
			s.PM.Record(user.ID, a.proxyID, int64(len(body)))
		}
	}

	if inbound == "openai" && channelType == "anthropic" {
		var an convert.AnthropicMessagesResponse
		if err := json.Unmarshal(body, &an); err == nil {
			u := convert.NormalizeAnthropicUsage(&an.Usage)
			oi := convert.AnthropicToOpenAIResponse(&an)
			oi.Model = modelFromUpstream(body)
			c.Data(resp.StatusCode, "application/json", mustJSON(oi))
			return u
		}
	} else if inbound == "anthropic" && channelType == "openai" {
		var oi convert.OpenAIChatResponse
		if err := json.Unmarshal(body, &oi); err == nil {
			u := convert.NormalizeOpenAIUsage(oi.Usage)
			an := convert.OpenAIToAnthropicResponse(&oi)
			c.Data(resp.StatusCode, "application/json", mustJSON(an))
			return u
		}
	}
	// 同协议或转换失败：原样透传
	u := sniffUsage(convert.Usage{}, body)
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	return u
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

func (s *Server) planFor(rc *routing.ResolvedChannel) []attempt {
	return s.plan([]*routing.ResolvedChannel{rc}, nil)
}

// submitLog 异步记录日志（含费用快照）
func (s *Server) submitLog(c *gin.Context, a attempt, inbound, model, upstreamModel string, statusCode int, u convert.Usage) {
	token, user := ctxTokenUser(c)
	if a.rc.Channel == nil {
		return
	}
	now := time.Now().Unix()
	multiplier := a.rc.Channel.PriceMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	ic, oc := usage.ComputeCost(s.Store.DB(), model, upstreamModel, multiplier, u)
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
		TotalMs:          nil,
		PromptTokens:     int64p(u.PromptTokens),
		CompletionTokens: int64p(u.CompletionTokens),
		CachedTokens:     int64p(u.CachedTokens),
		CacheWriteTokens: int64p(u.CacheWriteTokens),
		InputCost:        ic,
		OutputCost:       oc,
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
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, int64(limitMB)<<20))
	if err != nil {
		respondOpenAIError(c, http.StatusBadRequest, "读取请求体失败")
		return nil
	}
	return body
}

func mapModel(ch *store.Channel, model string) string {
	var mapping map[string]string
	json.Unmarshal([]byte(ch.ModelMappingJSON), &mapping)
	if to, ok := mapping[model]; ok && to != "" {
		return to
	}
	return model
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

// respondRawOrConverted 上游错误透传（跨协议时转换错误结构）
func respondRawOrConverted(c *gin.Context, inbound string, status int, body []byte) {
	if len(body) == 0 {
		respondProtocolError(c, inbound, status, "上游错误（无响应体）")
		return
	}
	if inbound == "anthropic" {
		c.Data(status, "application/json", convert.OpenAIErrorToAnthropic(body))
		return
	}
	c.Data(status, "application/json", convert.AnthropicErrorToOpenAI(body))
}

// sniffUsage 从透传字节中嗅探 usage（尽力而为；兼容 SSE 行前缀）
func sniffUsage(u convert.Usage, data []byte) convert.Usage {
	payload := data
	if s := strings.TrimSpace(string(data)); strings.HasPrefix(s, "data:") {
		payload = []byte(strings.TrimSpace(strings.TrimPrefix(s, "data:")))
	}
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return u
	}
	var probe struct {
		Usage *convert.OpenAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &probe); err == nil && probe.Usage != nil {
		return convert.NormalizeOpenAIUsage(probe.Usage)
	}
	return u
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
