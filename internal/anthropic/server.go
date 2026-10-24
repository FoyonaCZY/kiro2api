package anthropic

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/FoyonaCZY/kiro2api/internal/redact"
	"github.com/FoyonaCZY/kiro2api/internal/usage"
	"github.com/google/uuid"
)

const (
	maxBodySize  = 50 * 1024 * 1024
	pingInterval = 25 * time.Second
)

// Server Anthropic 兼容接口。
type Server struct {
	APIKey          string
	Provider        *kiro.Provider
	ExtractThinking bool
	Usage           *usage.Log
	Access          *access.Store
}

// Routes /v1 和 /cc/v1。
func (s *Server) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/models", s.auth(http.HandlerFunc(s.models)))
	mux.Handle("POST /v1/messages", s.auth(s.messages(false)))
	mux.Handle("POST /v1/messages/count_tokens", s.auth(http.HandlerFunc(s.countTokens)))
	mux.Handle("POST /cc/v1/messages", s.auth(s.messages(true)))
	mux.Handle("POST /cc/v1/messages/count_tokens", s.auth(http.HandlerFunc(s.countTokens)))
}

// CORS 允许任意来源（与 kiro.rs 相同，这是公开 API 服务）。
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "*")
			if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			} else {
				h.Set("Access-Control-Allow-Headers", "*")
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteJSON 不转义 HTML。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":{"type":"internal_error","message":"序列化失败"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	WriteJSON(w, status, NewError(typ, msg))
}

// ExtractAPIKey x-api-key 优先，其次 Authorization: Bearer。
func ExtractAPIKey(r *http.Request) (string, bool) {
	if v := r.Header.Get("x-api-key"); v != "" {
		return v, true
	}
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return v, true
	}
	return "", false
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := ExtractAPIKey(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_error", "Invalid API key")
			return
		}
		if s.Access != nil {
			found := s.Access.Authenticate(key)
			if found == nil {
				writeError(w, http.StatusUnauthorized, "authentication_error", "Invalid API key")
				return
			}
			var ids []uint64
			if s.Provider != nil {
				ids = s.Provider.Manager().CredentialIDs()
			}
			scope := &access.Scope{GroupID: found.GroupID, Allowed: s.Access.AllowedCredentials(found.GroupID, ids), KeyID: found.ID, HasKey: true}
			next.ServeHTTP(w, r.WithContext(access.WithScope(r.Context(), scope)))
			return
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "authentication_error", "Invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type modelInfo struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	MaxTokens   int    `json:"max_tokens"`
}

var modelList = func() []modelInfo {
	rows := []struct {
		id, name string
		created  int64
		max      int
		thinking bool
	}{
		{"claude-opus-5-5", "Claude Opus 5.5", 1790035200, 128000, false},
		{"claude-sonnet-5-5", "Claude Sonnet 5.5", 1790553600, 128000, false},
		{"claude-opus-5", "Claude Opus 5", 1782777600, 128000, true},
		{"claude-sonnet-5", "Claude Sonnet 5", 1782777600, 128000, true},
		{"claude-opus-4-8", "Claude Opus 4.8", 1779897600, 128000, true},
		{"claude-opus-4-7", "Claude Opus 4.7", 1776276000, 64000, true},
		{"claude-opus-4-6", "Claude Opus 4.6", 1770163200, 64000, true},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6", 1771286400, 64000, true},
		{"claude-opus-4-5-20251101", "Claude Opus 4.5", 1763942400, 64000, true},
		{"claude-sonnet-4-5-20250929", "Claude Sonnet 4.5", 1759104000, 64000, true},
		{"claude-haiku-4-5-20251001", "Claude Haiku 4.5", 1760486400, 64000, true},
	}
	var out []modelInfo
	for _, r := range rows {
		out = append(out, modelInfo{r.id, "model", r.created, "anthropic", r.name, "chat", r.max})
		if r.thinking {
			out = append(out, modelInfo{r.id + "-thinking", "model", r.created, "anthropic", r.name + " (Thinking)", "chat", r.max})
		}
	}
	return out
}()

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": modelList})
}

func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	var req CountTokensRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON: "+err.Error())
		return
	}
	n := CountAllTokens(r.Context(), req.Model, req.System, req.Messages, req.Tools)
	WriteJSON(w, http.StatusOK, map[string]any{"input_tokens": max(n, 1)})
}

// overrideThinkingFromModel 模型名带 thinking 时覆写 thinking 配置。
func overrideThinkingFromModel(p *MessagesRequest) {
	m := strings.ToLower(p.Model)
	if !strings.Contains(m, "thinking") {
		return
	}
	adaptive := (strings.Contains(m, "opus") && (strings.Contains(m, "4-6") || strings.Contains(m, "4.6"))) ||
		strings.Contains(m, "sonnet-5") || strings.Contains(m, "opus-5")
	typ := "enabled"
	if adaptive {
		typ = "adaptive"
		p.OutputConfig = &OutputConfig{Effort: "high"}
	}
	slog.Info("模型名包含 thinking 后缀，覆写 thinking 配置", "model", p.Model, "thinking_type", typ)
	p.Thinking = &Thinking{Type: typ, BudgetTokens: 20000}
}

type logCtx struct {
	inboundHeaders string
	inboundBody    string
	outboundBody   string
}

func encodeInbound(h http.Header) string {
	pairs := make([][2]string, 0, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			pairs = append(pairs, [2]string{strings.ToLower(k), vs[len(vs)-1]})
		}
	}
	return redact.EncodeHeaders(pairs)
}

type call struct {
	s        *Server
	model    string
	stream   bool
	started  time.Time
	input    int
	thinking bool
	names    map[string]string
	kiroBody []byte
	forward  *kiro.ClaudeForward
	profile  *CacheProfile
	log      logCtx
}

func (c *call) record(status int, in, out int, errText, stop string, t *kiro.Target, response string, cache *CacheSplit) {
	if c.s.Usage == nil {
		return
	}
	split := UsageForLog(cache, in)
	r := usage.NewRequest{
		Model: c.model, Stream: c.stream, Status: status, DurationMs: uint64(time.Since(c.started).Milliseconds()),
		InputTokens: int64(split.InputTokens), OutputTokens: int64(out), CacheReadTokens: int64(split.CacheRead),
		CacheWrite5mTokens: int64(split.CacheWrite5m), CacheWrite1hTokens: int64(split.CacheWrite1h),
		Error: errText, RequestBytes: uint64(len(c.log.outboundBody)), StopReason: stop,
		InboundHeaders: c.log.inboundHeaders, InboundBody: c.log.inboundBody, OutboundBody: c.log.outboundBody, ResponseBody: response,
	}
	if t != nil {
		r.CredentialID, r.Account, r.Endpoint, r.OutboundHeaders = t.CredentialID, t.Account, t.Endpoint, t.OutboundHeaders
	}
	c.s.Usage.Record(r)
}

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (c *call) recordError(err error) {
	msg := firstRunes(err.Error(), 180)
	status := http.StatusBadGateway
	if strings.Contains(msg, "CONTENT_LENGTH_EXCEEDS_THRESHOLD") || strings.Contains(msg, "Input is too long") {
		status = http.StatusBadRequest
	}
	var ue *kiro.UpstreamError
	var t *kiro.Target
	if errors.As(err, &ue) {
		t = ue.Target
	}
	c.record(status, c.input, 0, msg, "", t, "", nil)
}

// MapProviderError 限流 429（带 Retry-After）；上下文满和输入过长 400；其余 502。
func MapProviderError(w http.ResponseWriter, err error) {
	var cd *kiro.CoolingDownError
	if errors.As(err, &cd) {
		w.Header().Set("Retry-After", strconv.FormatInt(cd.Seconds(), 10))
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", cd.Error())
		return
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "CONTENT_LENGTH_EXCEEDS_THRESHOLD"):
		slog.Warn("上游拒绝请求：上下文窗口已满（不应重试）", "err", msg)
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Context window is full. Reduce conversation history, system prompt, or tools.")
	case strings.Contains(msg, "Input is too long"):
		slog.Warn("上游拒绝请求：输入过长（不应重试）", "err", msg)
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Input is too long. Reduce the size of your messages.")
	default:
		slog.Error("Kiro API 调用失败", "err", msg)
		writeError(w, http.StatusBadGateway, "api_error", "上游 API 调用失败: "+msg)
	}
}

func sseHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

func writeEvents(w http.ResponseWriter, evs []SSEEvent) {
	for _, e := range evs {
		_, _ = io.WriteString(w, e.String())
	}
	flush(w)
}

func flush(w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
}

func (s *Server) messages(cc bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "读取请求体失败: "+err.Error())
			return
		}
		var p MessagesRequest
		if err := json.Unmarshal(body, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON: "+err.Error())
			return
		}
		path := "/v1/messages"
		if cc {
			path = "/cc/v1/messages"
		}
		slog.Info("Received POST "+path+" request", "model", p.Model, "max_tokens", p.MaxTokens, "stream", p.Stream, "message_count", len(p.Messages))
		if s.Provider == nil {
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Kiro API provider not configured")
			return
		}
		overrideThinkingFromModel(&p)
		ctx := r.Context()
		hasClaude := s.Provider.HasClaudeAPI()

		if HasWebSearchTool(&p) && !hasClaude {
			slog.Info("检测到 WebSearch 工具，路由到 WebSearch 处理")
			in := int(CountAllTokens(ctx, p.Model, p.System, p.Messages, p.Tools))
			evs, err := RunWebSearch(ctx, s.Provider, &p, in)
			if errors.Is(err, errEmptyQuery) {
				writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
				return
			}
			if err != nil {
				MapProviderError(w, err)
				return
			}
			sseHeaders(w)
			writeEvents(w, evs)
			return
		}

		c := &call{s: s, model: p.Model, stream: p.Stream, started: time.Now(),
			forward: &kiro.ClaudeForward{Body: body, AnthropicVersion: r.Header.Get("anthropic-version"), AnthropicBeta: r.Header.Get("anthropic-beta")},
			log:     logCtx{inboundHeaders: encodeInbound(r.Header), inboundBody: string(body)}}

		res, err := ConvertRequest(&p)
		if err != nil {
			if hasClaude {
				slog.Info("请求不能转成 Kiro，交给 Claude API 账号透传")
				s.dispatch(w, r, c, cc)
				return
			}
			slog.Warn("请求转换失败", "err", err)
			writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		kb, err := Marshal(KiroBody(res))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "序列化请求失败: "+err.Error())
			return
		}
		c.kiroBody = kb
		c.log.outboundBody = string(kb)
		c.input = int(CountAllTokens(ctx, p.Model, p.System, p.Messages, p.Tools))
		c.thinking = p.Thinking.Enabled()
		c.names = res.ToolNameMap
		c.profile = Profile(body)
		if !p.Stream {
			c.thinking = s.ExtractThinking && c.thinking
		}
		s.dispatch(w, r, c, cc)
	})
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, c *call, cc bool) {
	switch {
	case !c.stream:
		s.nonStream(w, r, c)
	case cc:
		s.streamResp(w, r, c, true)
	default:
		s.streamResp(w, r, c, false)
	}
}

func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, c *call, up *kiro.UpstreamResponse) {
	defer up.Resp.Body.Close()
	status := up.Resp.StatusCode
	errText := ""
	if status/100 != 2 {
		errText = fmt.Sprintf("Claude API %d", status)
	}
	c.record(status, 0, 0, errText, "", &up.Target, "", nil)
	if ct := up.Resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(status)
	buf := make([]byte, 32*1024)
	for {
		n, err := up.Resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flush(w)
		}
		if err != nil || r.Context().Err() != nil {
			return
		}
	}
}

type chunk struct {
	data []byte
	err  error
}

func readChunks(body io.Reader, done <-chan struct{}) <-chan chunk {
	ch := make(chan chunk)
	go func() {
		defer close(ch)
		for {
			buf := make([]byte, 32*1024)
			n, err := body.Read(buf)
			if n > 0 {
				select {
				case ch <- chunk{data: buf[:n]}:
				case <-done:
					return
				}
			}
			if err != nil {
				select {
				case ch <- chunk{err: err}:
				case <-done:
				}
				return
			}
		}
	}()
	return ch
}

// eventSource 上游事件。/v1/responses 是 SSE。
type eventSource interface {
	Feed([]byte) error
	NextEvent() (*kiro.Event, error)
}

// drain 取出所有完整事件；连续错误过多时停止。
func drain(d eventSource, fn func(*kiro.Event)) {
	for {
		ev, err := d.NextEvent()
		if err != nil {
			if errors.Is(err, kiro.ErrTooManyErrors) {
				return
			}
			slog.Warn("解码事件失败", "err", err)
			return
		}
		if ev == nil {
			return
		}
		fn(ev)
	}
}

func (s *Server) streamResp(w http.ResponseWriter, r *http.Request, c *call, buffered bool) {
	ctx := r.Context()
	up, err := s.Provider.CallMessages(ctx, c.kiroBody, c.forward, true)
	if err != nil {
		c.recordError(err)
		MapProviderError(w, err)
		return
	}
	if up.Passthrough {
		s.passthrough(w, r, c, up)
		return
	}
	defer up.Resp.Body.Close()

	cache := Observe(ctx, c.profile)
	target := up.Target
	onUsage := func(rep UsageReport) {
		status := http.StatusOK
		switch {
		case rep.ClientGone:
			status = 499
		case rep.Error != "":
			status = http.StatusBadGateway
		}
		c.record(status, rep.InputTokens, rep.OutputTokens, rep.Error, rep.StopReason, &target, rep.ResponseBody, cache)
	}
	var sc *StreamContext
	var bc *BufferedStreamContext
	if buffered {
		bc = NewBufferedStreamContext(c.model, c.input, c.thinking, c.names)
		sc = bc.Inner()
	} else {
		sc = NewStreamContext(c.model, c.input, c.thinking, c.names)
	}
	sc.SetCache(cache)
	sc.SetOnUsage(onUsage)

	sseHeaders(w)
	if !buffered {
		writeEvents(w, sc.InitialEvents())
	}
	_, _ = io.WriteString(w, PingSSE)
	flush(w)

	done := make(chan struct{})
	defer close(done)
	chunks := readChunks(up.Resp.Body, done)
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	var dec kiro.SSEDecoder
	for {
		select {
		case <-ctx.Done():
			// 客户端断开：记 499，不计费。
			sc.Cancel()
			return
		case <-ticker.C:
			_, _ = io.WriteString(w, PingSSE)
			flush(w)
		case ch, ok := <-chunks:
			if !ok {
				return
			}
			if ch.err != nil {
				if errors.Is(ch.err, io.EOF) {
					if buffered {
						writeEvents(w, bc.Finish())
					} else {
						writeEvents(w, sc.FinalEvents())
					}
				} else {
					slog.Error("读取响应流失败", "err", ch.err)
					if buffered {
						writeEvents(w, bc.Fail(ch.err.Error()))
					} else {
						writeEvents(w, sc.Fail(ch.err.Error()))
					}
				}
				return
			}
			if err := dec.Feed(ch.data); err != nil {
				slog.Warn("缓冲区溢出", "err", err)
			}
			var evs []SSEEvent
			drain(&dec, func(ev *kiro.Event) {
				if buffered {
					bc.Process(ev)
				} else {
					evs = append(evs, sc.Process(ev)...)
				}
			})
			if len(evs) > 0 {
				writeEvents(w, evs)
			}
		}
	}
}

func (s *Server) nonStream(w http.ResponseWriter, r *http.Request, c *call) {
	ctx := r.Context()
	up, err := s.Provider.CallMessages(ctx, c.kiroBody, c.forward, false)
	if err != nil {
		c.recordError(err)
		MapProviderError(w, err)
		return
	}
	if up.Passthrough {
		s.passthrough(w, r, c, up)
		return
	}
	defer up.Resp.Body.Close()
	cache := Observe(ctx, c.profile)
	raw, err := io.ReadAll(up.Resp.Body)
	if err != nil {
		c.recordError(err)
		slog.Error("读取响应体失败", "err", err)
		writeError(w, http.StatusBadGateway, "api_error", "读取响应失败: "+err.Error())
		return
	}
	var dec kiro.SSEDecoder
	if err := dec.Feed(raw); err != nil {
		slog.Warn("缓冲区溢出", "err", err)
	}
	var text, reasoning strings.Builder
	var toolUses []map[string]any
	hasTool := false
	stop := "end_turn"
	ctxInput, hasCtxInput := 0, false
	buffers := map[string]*strings.Builder{}
	drain(&dec, func(ev *kiro.Event) {
		switch ev.Kind {
		case kiro.EvAssistant:
			text.WriteString(ev.Content)
		case kiro.EvReasoning:
			reasoning.WriteString(ev.Text)
		case kiro.EvToolUse:
			hasTool = true
			b := buffers[ev.ToolUseID]
			if b == nil {
				b = &strings.Builder{}
				buffers[ev.ToolUseID] = b
			}
			b.WriteString(ev.ToolInput)
			if ev.ToolStop {
				var input any = map[string]any{}
				if b.Len() > 0 {
					var v any
					if err := json.Unmarshal([]byte(b.String()), &v); err != nil {
						slog.Warn("工具输入 JSON 解析失败", "err", err, "tool_use_id", ev.ToolUseID)
					} else {
						input = v
					}
				}
				name := ev.ToolName
				if orig, ok := c.names[name]; ok {
					name = orig
				}
				toolUses = append(toolUses, map[string]any{"type": "tool_use", "id": ev.ToolUseID, "name": name, "input": input})
			}
		case kiro.EvContextUsage:
			ctxInput, hasCtxInput = int(ev.ContextUsage*float64(ContextWindowSize(c.model))/100), true
			if ev.ContextUsage >= 100 {
				stop = "model_context_window_exceeded"
			}
		case kiro.EvException:
			if ev.Code == "ContentLengthExceededException" {
				stop = "max_tokens"
			}
		}
	})
	if hasTool && stop == "end_turn" {
		stop = "tool_use"
	}
	content := []map[string]any{}
	if c.thinking {
		th, ok, rest := reasoning.String(), reasoning.Len() > 0, text.String()
		if !ok {
			th, ok, rest = ExtractThinking(text.String())
		}
		if ok {
			content = append(content, map[string]any{"type": "thinking", "thinking": th})
		}
		if rest != "" {
			content = append(content, map[string]any{"type": "text", "text": rest})
		}
	} else if text.Len() > 0 {
		content = append(content, map[string]any{"type": "text", "text": text.String()})
	}
	content = append(content, toolUses...)
	out := EstimateOutputTokens(content)
	in := c.input
	if hasCtxInput {
		in = ctxInput
	}
	resp := map[string]any{
		"id": "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""), "type": "message", "role": "assistant",
		"content": content, "model": c.model, "stop_reason": stop, "stop_sequence": nil,
		"usage": UsageJSON(cache, in, out),
	}
	b, _ := Marshal(resp)
	c.record(http.StatusOK, in, out, "", stop, &up.Target, string(b), cache)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
