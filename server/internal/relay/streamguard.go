// Package relay —— 流式失败检测与结局追踪（v1.5.54，FR-SG）
//
// 背景：中转/聚合型网关（oct-yescode 案例）在流式请求下不回 HTTP 错误码，而是
// 200 + SSE 错误事件（如 Codex Responses 协议的 type:"error" / upstream_error
// stream_read_error），状态码层失败切换被整体绕过，且 200 被记为成功清零熔断计数。
//
// 设计原则（DESIGN §5.6）：
//   - 协议知识只依赖少量「锚点」：错误帧形态（结构化判定：JSON 带 error 对象 /
//     type=="error"）与终止标记（chat 的 [DONE]、anthropic 的 message_stop、
//     responses 的 response.completed/failed/incomplete/cancelled）。锚点属于
//     客户端契约面，是协议中最稳定的部分；传输层事实（EOF/读错误/超时/客户端
//     断开）零协议依赖
//   - 记账宽松化（lenient）：只在「明确看到错误帧」或「未见终止标记的异常
//     终止」时计失败；未知的终止事件形态最多导致一次误记，连续失败阈值可吸收
//   - 检测只读不写数据路径：透传循环逐行喂入 tracker，不影响写出
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// streamDialect 上游流式方言（决定终止标记与错误帧判定）
type streamDialect string

const (
	dialectChat      streamDialect = "chat"      // openai 型渠道 /chat/completions 上游
	dialectResponses streamDialect = "responses" // openai 型渠道 /responses 上游
	dialectAnthropic streamDialect = "anthropic" // anthropic 型渠道 /messages 上游
)

// sseFrameKind 单个 SSE 数据帧的分类
type sseFrameKind int

const (
	frameData     sseFrameKind = iota // 普通数据帧
	frameTerminal                     // 终止标记（流正常收尾）
	frameError                        // 错误事件（流内失败）
)

// classifyDataLine 分类一行 SSE 数据（含或不含 "data:" 前缀均可）。
// 解析尽力而为：非法 JSON 一律按普通数据帧处理（不影响透传）
func classifyDataLine(dialect streamDialect, line []byte) sseFrameKind {
	payload := trimSSEDataPrefix(line)
	if len(payload) == 0 {
		return frameData
	}
	if dialect == dialectChat && bytes.Equal(payload, []byte("[DONE]")) {
		return frameTerminal
	}
	var probe struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return frameData
	}
	switch dialect {
	case dialectAnthropic:
		switch probe.Type {
		case "message_stop":
			return frameTerminal
		case "error":
			return frameError
		}
	case dialectResponses:
		switch probe.Type {
		case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
			return frameTerminal
		case "error":
			return frameError
		}
	default: // chat：无 type 字段，错误以顶层 error 对象表达（兼容网关惯例）
		if probe.Error != nil && len(probe.Error) > 2 { // 非空对象/字符串
			return frameError
		}
	}
	return frameData
}

// trimSSEDataPrefix 去掉 "data:" 前缀并去首尾空白；非 data 行或空载荷返回 nil
func trimSSEDataPrefix(line []byte) []byte {
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "data:") {
		return nil
	}
	payload := []byte(strings.TrimSpace(strings.TrimPrefix(s, "data:")))
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// errorFrameSummary 错误帧摘要（日志/熔断 last_error 用，截断 200 字符）。
// 兼容三种错误形态：顶层 error 对象（chat/Responses）、type=="error"（Responses/
// anthropic）、response.failed 事件的 response.error 嵌套（Responses API）
func errorFrameSummary(dialect streamDialect, line []byte) string {
	payload := trimSSEDataPrefix(line)
	if len(payload) == 0 {
		return "流内错误事件"
	}
	var probe struct {
		Type  string `json:"type"`
		Error *struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Response *struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	msg := "流内错误事件"
	if err := json.Unmarshal(payload, &probe); err == nil {
		switch {
		case probe.Response != nil && probe.Response.Error != nil: // response.failed 嵌套
			e := probe.Response.Error
			msg = fmt.Sprintf("流内错误事件 type=%s code=%s", probe.Type, e.Code)
			if e.Message != "" {
				msg += "：" + e.Message
			}
		case probe.Error != nil:
			msg = fmt.Sprintf("流内错误事件 type=%s", probe.Error.Type)
			if probe.Error.Code != nil {
				msg += fmt.Sprintf(" code=%v", probe.Error.Code)
			}
			if probe.Error.Message != "" {
				msg += "：" + probe.Error.Message
			}
		case probe.Type == "error":
			msg = "流内错误事件（无错误详情）"
		}
	}
	return truncateStr(msg, 200)
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// streamFate 流结局（一次流式回写的最终判定）
type streamFate int

const (
	fateOK         streamFate = iota // 正常：见到终止标记
	fateErrFrame                     // 流内错误事件（可能发生于任意位置）
	fateAborted                      // 异常终止：未见终止标记即结束（EOF/读错误/空闲超时）
	fateClientGone                   // 客户端主动断开（不归上游，不计失败）
)

// streamTracker 透传过程中的流结局追踪器：逐行喂入，结束时给判定
type streamTracker struct {
	dialect     streamDialect
	sawTerminal bool
	sawError    bool
	errSummary  string
	clientGone  bool
}

func newStreamTracker(dialect streamDialect) *streamTracker {
	return &streamTracker{dialect: dialect}
}

// feedLine 喂入一行（透传前调用；含 data: 前缀的原始行）
func (t *streamTracker) feedLine(line []byte) {
	switch classifyDataLine(t.dialect, line) {
	case frameTerminal:
		t.sawTerminal = true
	case frameError:
		if !t.sawError { // 首个错误事件的摘要最有信息量
			t.sawError = true
			t.errSummary = errorFrameSummary(t.dialect, line)
		}
	}
}

// feedTail 收尾时喂入残行（无换行结尾的最后一段；非 data 行忽略）
func (t *streamTracker) feedTail(tail []byte) {
	if len(trimSSEDataPrefix(tail)) > 0 {
		t.feedLine(tail)
	}
}

// markClientGone 标记客户端断开
func (t *streamTracker) markClientGone() { t.clientGone = true }

// ended 结束时的判定：错误帧 > 未见终止的异常终止 > 正常。
// 错误事件即使出现在终止标记之后也按失败计——错误后置同样是流内失败
func (t *streamTracker) ended() (streamFate, string) {
	switch {
	case t.clientGone:
		return fateClientGone, "客户端断开"
	case t.sawError:
		return fateErrFrame, t.errSummary
	case t.sawTerminal:
		return fateOK, ""
	default:
		return fateAborted, "流异常终止（未见终止标记）"
	}
}

// failed 是否应按失败记账（客户端断开除外）
func (t *streamTracker) failed() bool {
	fate, _ := t.ended()
	return fate == fateErrFrame || fate == fateAborted
}

// ---------- 首帧窥探（FR-SG2：提交前判定，可无损切换） ----------

// peekResult 首帧窥探结论；committed=false 时未向客户端写出任何字节，
// 调用方可按上游失败继续尝试下一组合（换 key/线路/渠道），
// fate 给出失败分类（fateErrFrame / fateAborted / fateClientGone）
type peekResult struct {
	committed bool
	fate      streamFate
	summary   string // 失败原因摘要（committed=false 时非空）
	buf       []byte // 窥探期间读到的全部原始字节（committed 时需先写出）
}

// peekFirstData 在向客户端写出任何字节前读取上游，直到出现首个数据帧、
// 错误帧或流结束。超时（timeout > 0 时）通过关闭上游 body 解除阻塞读，
// 同样视为未提交失败。ctx 取消（客户端断开）时立即放弃。
// 非数据行（SSE 注释 / event 行）不触发提交，继续等待。
func peekFirstData(resp *http.Response, dialect streamDialect, timeout time.Duration, ctx context.Context) peekResult {
	var timedOut atomic.Bool
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			resp.Body.Close()
		})
		defer t.Stop()
	}

	var raw, lineBuf []byte
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return peekResult{fate: fateClientGone, summary: "客户端断开", buf: raw}
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			lineBuf = append(lineBuf, buf[:n]...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := append([]byte(nil), lineBuf[:idx+1]...)
				lineBuf = lineBuf[idx+1:]
				if payload := trimSSEDataPrefix(line); payload != nil {
					kind := classifyDataLine(dialect, line)
					if kind == frameError {
						return peekResult{fate: fateErrFrame, summary: errorFrameSummary(dialect, line), buf: raw}
					}
					// 首个数据帧为正常内容或终止标记（空响应）→ 提交
					return peekResult{committed: true, buf: raw}
				}
				// 注释/event 行：继续等首个数据帧
			}
		}
		if err != nil {
			if timedOut.Load() {
				return peekResult{fate: fateAborted, summary: "首帧超时（上游无数据）", buf: raw}
			}
			if ctx.Err() != nil {
				return peekResult{fate: fateClientGone, summary: "客户端断开", buf: raw}
			}
			if err == io.EOF && len(raw) > 0 {
				// 读到字节但流在完整数据帧前结束（无换行的残段不算数据帧）
				if trimSSEDataPrefix(lineBuf) != nil &&
					classifyDataLine(dialect, lineBuf) == frameError {
					return peekResult{fate: fateErrFrame, summary: errorFrameSummary(dialect, lineBuf), buf: raw}
				}
			}
			return peekResult{fate: fateAborted, summary: "上游流在首帧前结束：" + err.Error(), buf: raw}
		}
	}
}
