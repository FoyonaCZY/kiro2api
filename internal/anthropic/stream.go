package anthropic

import (
	"log/slog"
	"sort"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/google/uuid"
)

// SSEEvent 一个 SSE 事件。
type SSEEvent struct {
	Event string
	Data  map[string]any
}

// String event: x\ndata: {...}\n\n
func (e SSEEvent) String() string {
	b, _ := Marshal(e.Data)
	return "event: " + e.Event + "\ndata: " + string(b) + "\n\n"
}

// PingSSE 25 秒一次的保活事件，格式与 kiro.rs 相同。
const PingSSE = "event: ping\ndata: {\"type\": \"ping\"}\n\n"

type blockState struct {
	typ     string
	started bool
	stopped bool
}

// sseState 保证事件序列符合 Claude API：message_start 一次、块先 start 再 delta 再 stop、
// message_delta 一次且在所有块关闭之后、message_stop 最后。
type sseState struct {
	messageStarted bool
	deltaSent      bool
	blocks         map[int]*blockState
	ended          bool
	nextIndex      int
	stopReason     string
	hasToolUse     bool
}

func newSSEState() *sseState { return &sseState{blocks: map[int]*blockState{}} }

func (s *sseState) sortedIndexes() []int {
	idx := make([]int, 0, len(s.blocks))
	for i := range s.blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	return idx
}

func (s *sseState) isOpen(i int, typ string) bool {
	b := s.blocks[i]
	return b != nil && b.started && !b.stopped && b.typ == typ
}

func (s *sseState) next() int { i := s.nextIndex; s.nextIndex++; return i }

func (s *sseState) hasNonThinking() bool {
	for _, b := range s.blocks {
		if b.typ != "thinking" {
			return true
		}
	}
	return false
}

func (s *sseState) finalStopReason() string {
	switch {
	case s.stopReason != "":
		return s.stopReason
	case s.hasToolUse:
		return "tool_use"
	}
	return "end_turn"
}

func stopEvent(i int) SSEEvent {
	return SSEEvent{"content_block_stop", map[string]any{"type": "content_block_stop", "index": i}}
}

func (s *sseState) messageStart(data map[string]any) []SSEEvent {
	if s.messageStarted {
		return nil
	}
	s.messageStarted = true
	return []SSEEvent{{"message_start", data}}
}

func (s *sseState) blockStart(i int, typ string, data map[string]any) []SSEEvent {
	var out []SSEEvent
	if typ == "tool_use" {
		s.hasToolUse = true
		for _, j := range s.sortedIndexes() {
			if b := s.blocks[j]; b.typ == "text" && b.started && !b.stopped {
				out = append(out, stopEvent(j))
				b.stopped = true
			}
		}
	}
	if b, ok := s.blocks[i]; ok {
		if b.started {
			return out
		}
		b.started = true
	} else {
		s.blocks[i] = &blockState{typ: typ, started: true}
	}
	return append(out, SSEEvent{"content_block_start", data})
}

func (s *sseState) blockDelta(i int, data map[string]any) []SSEEvent {
	b := s.blocks[i]
	if b == nil || !b.started || b.stopped {
		slog.Warn("忽略状态异常内容块的 delta", "index", i)
		return nil
	}
	return []SSEEvent{{"content_block_delta", data}}
}

func (s *sseState) blockStop(i int) []SSEEvent {
	b := s.blocks[i]
	if b == nil || b.stopped {
		return nil
	}
	b.stopped = true
	return []SSEEvent{stopEvent(i)}
}

func (s *sseState) final(usage map[string]any) []SSEEvent {
	var out []SSEEvent
	for _, i := range s.sortedIndexes() {
		if b := s.blocks[i]; b.started && !b.stopped {
			out = append(out, stopEvent(i))
			b.stopped = true
		}
	}
	if !s.deltaSent {
		s.deltaSent = true
		out = append(out, SSEEvent{"message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": s.finalStopReason(), "stop_sequence": nil},
			"usage": usage,
		}})
	}
	if !s.ended {
		s.ended = true
		out = append(out, SSEEvent{"message_stop", map[string]any{"type": "message_stop"}})
	}
	return out
}

// UsageReport 流结束时交给用量记录的内容。Error 非空表示断流，按失败入库。
type UsageReport struct {
	InputTokens  int
	OutputTokens int
	StopReason   string
	ResponseBody string
	Error        string
	ClientGone   bool
	Status       int
	// Credits 上游 usage 报的 kiro:credits，nil 表示没报。
	Credits *float64
}

// StreamContext Kiro 事件 → Anthropic SSE。
type StreamContext struct {
	st                *sseState
	model             string
	messageID         string
	inputTokens       int
	contextInput      int
	hasContextInput   bool
	outputTokens      int
	toolIndexes       map[string]int
	toolNames         map[string]string
	thinkingEnabled   bool
	buf               string
	inThinking        bool
	thinkingExtracted bool
	thinkingIndex     int
	hasThinkingIndex  bool
	textIndex         int
	hasTextIndex      bool
	stripLeadingNL    bool
	reasoningOpen     bool
	answerText        strings.Builder
	thinkingText      strings.Builder
	onUsage           func(UsageReport)
	cache             *CacheSplit
	signature         string
	upstreamErr       *StreamError
	credits           *float64
}

// StreamError 上游在流中途报的错，已映射成 Anthropic 错误。
type StreamError struct {
	Status  int
	Type    string
	Message string
}

// MapStreamError 按 Kiro IDE 的规则把 /v1/responses 的错误 code/type 归类，再换成 Anthropic 错误类型。
func MapStreamError(code, typ, msg string) *StreamError {
	t := strings.ToLower(typ)
	c := strings.ToUpper(strings.TrimPrefix(code, "kiro:"))
	e := &StreamError{Status: 500, Type: "api_error", Message: msg}
	switch {
	case code == "context_length_exceeded":
		e.Status, e.Type = 400, "invalid_request_error"
		if e.Message == "" {
			e.Message = "Input is too long. Reduce the size of your messages."
		}
	case code == "model_not_found" || t == "not_found":
		e.Status, e.Type = 404, "not_found_error"
	case t == "too_many_requests" || code == "rate_limit_exceeded" || c == "THROTTLING":
		e.Status, e.Type = 429, "rate_limit_error"
	case t == "insufficient_quota" || code == "credit_balance_exhausted" || strings.Contains(c, "MONTHLY_REQUEST_COUNT"):
		e.Status, e.Type = 402, "billing_error"
	case code == "access_denied" || code == "kiro:model_requires_user_opt_in":
		e.Status, e.Type = 403, "permission_error"
	case t == "invalid_request" || strings.HasPrefix(code, "invalid_") || strings.HasPrefix(code, "unsupported_") || strings.HasPrefix(code, "missing_"):
		e.Status, e.Type = 400, "invalid_request_error"
	case code == "server_is_overloaded":
		e.Status, e.Type = 529, "overloaded_error"
	}
	if e.Message == "" {
		e.Message = "Upstream error: " + strings.TrimSpace(code+" "+typ)
	}
	return e
}

// stopReasonFor 上游结束原因 → Anthropic stop_reason。
func stopReasonFor(r string) string {
	switch r {
	case "MAX_TOKENS":
		return "max_tokens"
	case "CONTENT_FILTERED":
		return "refusal"
	case "COMPACTION_REQUIRED_FOR_MODEL_HANDOFF":
		return "model_context_window_exceeded"
	}
	return ""
}

// NewStreamContext 创建上下文。
func NewStreamContext(model string, inputTokens int, thinking bool, toolNames map[string]string) *StreamContext {
	return &StreamContext{
		st:              newSSEState(),
		model:           model,
		messageID:       "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		inputTokens:     inputTokens,
		toolIndexes:     map[string]int{},
		toolNames:       toolNames,
		thinkingEnabled: thinking,
	}
}

// SetOnUsage 用量回调，只调用一次。
func (c *StreamContext) SetOnUsage(f func(UsageReport)) { c.onUsage = f }

// SetCache 模拟缓存拆分。
func (c *StreamContext) SetCache(s *CacheSplit) { c.cache = s }

func (c *StreamContext) usage(in, out int) map[string]any { return UsageJSON(c.cache, in, out) }

func (c *StreamContext) finalInput() int {
	if c.hasContextInput {
		return c.contextInput
	}
	return c.inputTokens
}

func (c *StreamContext) assembled() string {
	var b strings.Builder
	if c.thinkingText.Len() > 0 {
		b.WriteString("thinking:\n")
		b.WriteString(c.thinkingText.String())
		b.WriteString("\n\n")
	}
	b.WriteString(c.answerText.String())
	return b.String()
}

func (c *StreamContext) messageStartData() map[string]any {
	return map[string]any{"type": "message_start", "message": map[string]any{
		"id": c.messageID, "type": "message", "role": "assistant", "content": []any{},
		"model": c.model, "stop_reason": nil, "stop_sequence": nil,
		"usage": c.usage(c.inputTokens, 1),
	}}
}

// InitialEvents message_start；未开 thinking 时同时打开文本块 0。
func (c *StreamContext) InitialEvents() []SSEEvent {
	out := c.st.messageStart(c.messageStartData())
	if c.thinkingEnabled {
		return out
	}
	i := c.st.next()
	c.textIndex, c.hasTextIndex = i, true
	return append(out, c.st.blockStart(i, "text", textStart(i))...)
}

func textStart(i int) map[string]any {
	return map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}}
}

func thinkingStart(i int) map[string]any {
	return map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "thinking", "thinking": ""}}
}

// Process 处理一个 Kiro 事件。
func (c *StreamContext) Process(ev *kiro.Event) []SSEEvent {
	switch ev.Kind {
	case kiro.EvAssistant:
		return c.assistant(ev.Content)
	case kiro.EvReasoning:
		return c.reasoning(ev.Text)
	case kiro.EvToolUse:
		return c.toolUse(ev)
	case kiro.EvContextUsage:
		c.contextInput = int(ev.ContextUsage * float64(ContextWindowSize(c.model)) / 100)
		c.hasContextInput = true
		if ev.ContextUsage >= 100 {
			c.st.stopReason = "model_context_window_exceeded"
		}
	case kiro.EvReasoningSignature:
		return c.reasoningSignature(ev)
	case kiro.EvStop:
		if r := stopReasonFor(ev.StopReason); r != "" {
			c.st.stopReason = r
		}
	case kiro.EvMetering:
		v := ev.Credits
		c.credits = &v
	case kiro.EvError:
		slog.Error("上游流中途报错", "code", ev.Code, "type", ev.ErrorType, "message", ev.Message)
		if c.upstreamErr == nil {
			c.upstreamErr = MapStreamError(ev.Code, ev.ErrorType, ev.Message)
		}
	}
	return nil
}

// UpstreamError 流中途收到的上游错误；非空时调用方应发 error 事件并结束。
func (c *StreamContext) UpstreamError() *StreamError { return c.upstreamErr }

// reasoningSignature 原生思考的签名跟在思考块末尾；redacted 思考单独成块。
func (c *StreamContext) reasoningSignature(ev *kiro.Event) []SSEEvent {
	if !c.thinkingEnabled {
		return nil
	}
	if ev.Redacted != "" {
		out := c.closeReasoning()
		i := c.st.next()
		out = append(out, c.st.blockStart(i, "redacted_thinking", map[string]any{
			"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "redacted_thinking", "data": ev.Redacted},
		})...)
		return append(out, c.st.blockStop(i)...)
	}
	c.signature = ev.Signature
	return nil
}

func (c *StreamContext) assistant(content string) []SSEEvent {
	if content == "" {
		return nil
	}
	c.outputTokens += estimateTokens(content)
	out := c.closeReasoning()
	if c.thinkingEnabled {
		return append(out, c.withThinking(content)...)
	}
	return append(out, c.textDelta(content)...)
}

func (c *StreamContext) reasoning(text string) []SSEEvent {
	if text == "" {
		return nil
	}
	c.outputTokens += estimateTokens(text)
	if !c.thinkingEnabled {
		return nil
	}
	if c.inThinking {
		if c.hasThinkingIndex {
			return []SSEEvent{c.thinkingDelta(c.thinkingIndex, text)}
		}
		return nil
	}
	var out []SSEEvent
	if !c.reasoningOpen {
		if c.hasTextIndex && c.st.isOpen(c.textIndex, "text") {
			out = append(out, c.st.blockStop(c.textIndex)...)
		}
		i := c.st.next()
		c.thinkingIndex, c.hasThinkingIndex = i, true
		c.reasoningOpen = true
		out = append(out, c.st.blockStart(i, "thinking", thinkingStart(i))...)
	}
	return append(out, c.thinkingDelta(c.thinkingIndex, text))
}

func (c *StreamContext) closeThinkingBlock() []SSEEvent {
	if !c.hasThinkingIndex {
		return nil
	}
	out := []SSEEvent{c.thinkingDelta(c.thinkingIndex, "")}
	if c.signature != "" {
		out = append(out, SSEEvent{"content_block_delta", map[string]any{
			"type": "content_block_delta", "index": c.thinkingIndex,
			"delta": map[string]any{"type": "signature_delta", "signature": c.signature},
		}})
		c.signature = ""
	}
	return append(out, c.st.blockStop(c.thinkingIndex)...)
}

func (c *StreamContext) closeReasoning() []SSEEvent {
	if !c.reasoningOpen {
		return nil
	}
	c.reasoningOpen = false
	c.thinkingExtracted = true
	return c.closeThinkingBlock()
}

func (c *StreamContext) withThinking(content string) []SSEEvent {
	var out []SSEEvent
	c.buf += content
	for {
		switch {
		case !c.inThinking && !c.thinkingExtracted:
			if start := findRealThinkingStart(c.buf); start >= 0 {
				if before := c.buf[:start]; strings.TrimSpace(before) != "" {
					out = append(out, c.textDelta(before)...)
				}
				c.inThinking, c.stripLeadingNL = true, true
				c.buf = c.buf[start+len(openTag):]
				i := c.st.next()
				c.thinkingIndex, c.hasThinkingIndex = i, true
				out = append(out, c.st.blockStart(i, "thinking", thinkingStart(i))...)
				continue
			}
			// 保留可能是半截 <thinking> 的尾巴；只有空白时继续等，免得 text 块跑到 thinking 前面。
			safe := charBoundary(c.buf, max(len(c.buf)-len(openTag), 0))
			if safe > 0 {
				if s := c.buf[:safe]; strings.TrimSpace(s) != "" {
					out = append(out, c.textDelta(s)...)
					c.buf = c.buf[safe:]
				}
			}
			return out
		case c.inThinking:
			if c.stripLeadingNL {
				if strings.HasPrefix(c.buf, "\n") {
					c.buf = c.buf[1:]
					c.stripLeadingNL = false
				} else if c.buf != "" {
					c.stripLeadingNL = false
				}
			}
			if end := findRealThinkingEnd(c.buf); end >= 0 {
				if s := c.buf[:end]; s != "" && c.hasThinkingIndex {
					out = append(out, c.thinkingDelta(c.thinkingIndex, s))
				}
				c.inThinking, c.thinkingExtracted = false, true
				out = append(out, c.closeThinkingBlock()...)
				c.buf = c.buf[end+len(closeTag)+2:]
				continue
			}
			// 保留区覆盖 "</thinking>\n\n" 全长，免得标签前几个字符被当成思考内容发出。
			safe := charBoundary(c.buf, max(len(c.buf)-len(closeTag)-2, 0))
			if safe > 0 {
				if s := c.buf[:safe]; s != "" && c.hasThinkingIndex {
					out = append(out, c.thinkingDelta(c.thinkingIndex, s))
				}
				c.buf = c.buf[safe:]
			}
			return out
		default:
			if c.buf != "" {
				rest := c.buf
				c.buf = ""
				out = append(out, c.textDelta(rest)...)
			}
			return out
		}
	}
}

// textDelta 文本块被 tool_use 自动关闭后会重建新文本块，避免吞字。
func (c *StreamContext) textDelta(text string) []SSEEvent {
	var out []SSEEvent
	c.answerText.WriteString(text)
	if c.hasTextIndex && !c.st.isOpen(c.textIndex, "text") {
		c.hasTextIndex = false
	}
	if !c.hasTextIndex {
		i := c.st.next()
		c.textIndex, c.hasTextIndex = i, true
		out = append(out, c.st.blockStart(i, "text", textStart(i))...)
	}
	return append(out, c.st.blockDelta(c.textIndex, map[string]any{
		"type": "content_block_delta", "index": c.textIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})...)
}

func (c *StreamContext) thinkingDelta(i int, text string) SSEEvent {
	c.thinkingText.WriteString(text)
	return SSEEvent{"content_block_delta", map[string]any{
		"type": "content_block_delta", "index": i,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	}}
}

// flushTrailingThinkingEnd 处理 </thinking> 后面没有 \n\n 的边界情况（紧跟 tool_use 或流结束）。
func (c *StreamContext) flushTrailingThinkingEnd() ([]SSEEvent, bool) {
	end := findRealThinkingEndAtBufferEnd(c.buf)
	if end < 0 {
		return nil, false
	}
	var out []SSEEvent
	if s := c.buf[:end]; s != "" && c.hasThinkingIndex {
		out = append(out, c.thinkingDelta(c.thinkingIndex, s))
	}
	c.inThinking, c.thinkingExtracted = false, true
	out = append(out, c.closeThinkingBlock()...)
	rest := strings.TrimLeft(c.buf[end+len(closeTag):], " \t\r\n")
	c.buf = ""
	if rest != "" {
		out = append(out, c.textDelta(rest)...)
	}
	return out, true
}

func (c *StreamContext) toolUse(ev *kiro.Event) []SSEEvent {
	c.st.hasToolUse = true
	out := c.closeReasoning()
	if c.thinkingEnabled && c.inThinking {
		ev2, _ := c.flushTrailingThinkingEnd()
		out = append(out, ev2...)
	}
	// 为探测 <thinking> 暂存的尾部文本，在 tool_use 前当普通文本发出。
	if c.thinkingEnabled && !c.inThinking && !c.thinkingExtracted && c.buf != "" {
		b := c.buf
		c.buf = ""
		out = append(out, c.textDelta(b)...)
	}
	idx, ok := c.toolIndexes[ev.ToolUseID]
	if !ok {
		idx = c.st.next()
		c.toolIndexes[ev.ToolUseID] = idx
	}
	name := ev.ToolName
	if orig, ok := c.toolNames[name]; ok {
		name = orig
	}
	out = append(out, c.st.blockStart(idx, "tool_use", map[string]any{
		"type": "content_block_start", "index": idx,
		"content_block": map[string]any{"type": "tool_use", "id": ev.ToolUseID, "name": name, "input": map[string]any{}},
	})...)
	if ev.ToolInput != "" {
		c.outputTokens += (len(ev.ToolInput) + 3) / 4
		out = append(out, c.st.blockDelta(idx, map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.ToolInput},
		})...)
	}
	if ev.ToolStop {
		out = append(out, c.st.blockStop(idx)...)
	}
	return out
}

// FinalEvents 流正常结束。
func (c *StreamContext) FinalEvents() []SSEEvent {
	out := c.closeReasoning()
	if c.thinkingEnabled && c.buf != "" {
		if c.inThinking {
			if ev, ok := c.flushTrailingThinkingEnd(); ok {
				out = append(out, ev...)
			} else {
				if c.hasThinkingIndex {
					out = append(out, c.thinkingDelta(c.thinkingIndex, c.buf))
				}
				out = append(out, c.closeThinkingBlock()...)
			}
		} else {
			out = append(out, c.textDelta(c.buf)...)
		}
		c.buf = ""
	}
	// 只有 thinking 没有正文：模型把预算用在思考上，补一个空格文本块并标 max_tokens。
	if c.thinkingEnabled && c.hasThinkingIndex && !c.st.hasNonThinking() {
		c.st.stopReason = "max_tokens"
		out = append(out, c.textDelta(" ")...)
	}
	in, outTok := c.finalInput(), c.outputTokens
	if f := c.onUsage; f != nil {
		c.onUsage = nil
		f(UsageReport{InputTokens: in, OutputTokens: outTok, StopReason: c.st.finalStopReason(), ResponseBody: c.assembled(), Credits: c.credits})
	}
	return append(out, c.st.final(c.usage(in, outTok))...)
}

// Fail 上游断流：只发 error 事件，不发 message_delta/stop，用量按失败入库。
func (c *StreamContext) Fail(reason string) []SSEEvent {
	return c.FailWith(&StreamError{Status: 502, Type: "api_error", Message: "Upstream stream was interrupted before the response finished. Please retry."}, "上游流中断: "+reason)
}

// FailWith 发出指定的 Anthropic 错误事件。
func (c *StreamContext) FailWith(e *StreamError, logText string) []SSEEvent {
	if f := c.onUsage; f != nil {
		c.onUsage = nil
		f(UsageReport{InputTokens: c.finalInput(), OutputTokens: c.outputTokens, ResponseBody: c.assembled(), Error: logText, Status: e.Status, Credits: c.credits})
	}
	return []SSEEvent{{"error", map[string]any{"type": "error", "error": map[string]any{"type": e.Type, "message": e.Message}}}}
}

// Cancel 客户端先断开：按失败入库，不计费，也不再写任何事件。
func (c *StreamContext) Cancel() {
	if f := c.onUsage; f != nil {
		c.onUsage = nil
		f(UsageReport{InputTokens: c.finalInput(), OutputTokens: c.outputTokens, ResponseBody: c.assembled(), Error: "客户端断开连接", ClientGone: true, Credits: c.credits})
	}
}

// BufferedStreamContext /cc 用：缓冲到流结束，再用 contextUsageEvent 的准确值更正 message_start。
type BufferedStreamContext struct {
	inner     *StreamContext
	buf       []SSEEvent
	estimated int
	started   bool
}

// NewBufferedStreamContext 创建缓冲上下文。
func NewBufferedStreamContext(model string, estimated int, thinking bool, toolNames map[string]string) *BufferedStreamContext {
	return &BufferedStreamContext{inner: NewStreamContext(model, estimated, thinking, toolNames), estimated: estimated}
}

// Inner 内部上下文（设置回调和缓存）。
func (b *BufferedStreamContext) Inner() *StreamContext { return b.inner }

func (b *BufferedStreamContext) ensureStart() {
	if !b.started {
		b.buf = append(b.buf, b.inner.InitialEvents()...)
		b.started = true
	}
}

// Process 处理并缓冲。
func (b *BufferedStreamContext) Process(ev *kiro.Event) {
	b.ensureStart()
	b.buf = append(b.buf, b.inner.Process(ev)...)
}

// Fail 丢掉半截回复，只发 error。
func (b *BufferedStreamContext) Fail(reason string) []SSEEvent {
	b.buf = nil
	return b.inner.Fail(reason)
}

// FailWith 丢掉半截回复，发指定错误。
func (b *BufferedStreamContext) FailWith(e *StreamError, logText string) []SSEEvent {
	b.buf = nil
	return b.inner.FailWith(e, logText)
}

// Finish 返回全部事件，message_start 的 usage 已更正。
func (b *BufferedStreamContext) Finish() []SSEEvent {
	b.ensureStart()
	b.buf = append(b.buf, b.inner.FinalEvents()...)
	in := b.estimated
	if b.inner.hasContextInput {
		in = b.inner.contextInput
	}
	corrected := b.inner.usage(in, 1)
	for _, e := range b.buf {
		if e.Event == "message_start" {
			if m, ok := e.Data["message"].(map[string]any); ok {
				m["usage"] = corrected
			}
		}
	}
	out := b.buf
	b.buf = nil
	return out
}
