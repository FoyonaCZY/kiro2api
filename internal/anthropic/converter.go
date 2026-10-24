package anthropic

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/google/uuid"
)

const (
	writeToolSuffix   = "- IMPORTANT: If the content to write exceeds 150 lines, you MUST only write the first 50 lines using this tool, then use `Edit` tool to append the remaining content in chunks of no more than 50 lines each. If needed, leave a unique placeholder to help append content. Do NOT attempt to write all content at once."
	editToolSuffix    = "- IMPORTANT: If the `new_string` content exceeds 50 lines, you MUST split it into multiple Edit calls, each replacing no more than 50 lines at a time. If used to append content, leave a unique placeholder to help append content. On the final chunk, do NOT include the placeholder."
	systemChunkPolicy = "When the Write or Edit tool has content size limits, always comply silently. Never suggest bypassing these limits via alternative tools. Never ask the user whether to switch approaches. Complete all chunked operations without commentary."
	toolNameMaxLen    = 64
)

// MapModel claude-{sonnet|opus|haiku}-X[-Y] → claude-family-X.Y；去掉 -thinking 和日期后缀。未知模型返回空。
func MapModel(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	name = strings.TrimSuffix(name, "-thinking")
	if i := strings.LastIndex(name, "-"); i >= 0 {
		d := name[i+1:]
		if len(d) == 8 && strings.HasPrefix(d, "20") && allDigits(d) {
			name = name[:i]
		}
	}
	name = strings.TrimSuffix(name, "-thinking")
	rest, ok := strings.CutPrefix(name, "claude-")
	if !ok {
		return ""
	}
	family, version, ok := strings.Cut(rest, "-")
	if !ok || (family != "sonnet" && family != "opus" && family != "haiku") {
		return ""
	}
	parts := strings.FieldsFunc(version, func(r rune) bool { return r == '-' || r == '.' })
	// FieldsFunc 会吞掉空段，这里用原始分隔数校验。
	seps := strings.Count(version, "-") + strings.Count(version, ".")
	if len(parts) < 1 || len(parts) > 2 || seps != len(parts)-1 {
		return ""
	}
	for _, p := range parts {
		if !allDigits(p) {
			return ""
		}
	}
	return "claude-" + family + "-" + strings.Join(parts, ".")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ContextWindowSize 这些模型在 Kiro 上是 1M，其余 200K。
func ContextWindowSize(model string) int {
	switch MapModel(model) {
	case "claude-sonnet-5.5", "claude-opus-5.5", "claude-sonnet-5", "claude-opus-5",
		"claude-sonnet-4.6", "claude-opus-4.6", "claude-opus-4.7", "claude-opus-4.8":
		return 1_000_000
	}
	return 200_000
}

// ConversionError 转换失败。
type ConversionError struct {
	Unsupported string
	Empty       bool
}

func (e *ConversionError) Error() string {
	if e.Empty {
		return "消息列表为空"
	}
	return "模型不支持: " + e.Unsupported
}

// ConversionResult 转换结果。ToolNameMap 为改写后的工具名 → 原名。
type ConversionResult struct {
	State        kiro.ConversationState
	Instructions string
	ToolNameMap  map[string]string
	CallPosition int
	HasSystem    bool
	Thinking     *kiro.ThinkingRequest
}

func normalizeSchema(raw json.RawMessage) json.RawMessage {
	var obj map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil || obj == nil {
		obj = map[string]any{}
	}
	if s, ok := obj["type"].(string); !ok || s == "" {
		obj["type"] = "object"
	}
	if _, ok := obj["properties"].(map[string]any); !ok {
		obj["properties"] = map[string]any{}
	}
	req := []any{}
	if arr, ok := obj["required"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				req = append(req, s)
			}
		}
	}
	obj["required"] = req
	switch obj["additionalProperties"].(type) {
	case bool, map[string]any:
	default:
		obj["additionalProperties"] = true
	}
	b, _ := Marshal(obj)
	return b
}

func isValidUUID(s string) bool { return len(s) == 36 && strings.Count(s, "-") == 4 }

// ExtractSessionID 支持 JSON {"session_id":...} 和 "..._session_<uuid>" 两种格式。
func ExtractSessionID(userID string) string {
	var v struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(userID), &v) == nil && isValidUUID(v.SessionID) {
		return v.SessionID
	}
	if i := strings.Index(userID, "session_"); i >= 0 {
		part := userID[i+8:]
		if len(part) >= 36 && isValidUUID(part[:36]) {
			return part[:36]
		}
	}
	return ""
}

func imageFormat(mt string) string {
	switch mt {
	case "image/jpeg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return ""
}

func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, it := range arr {
			if t, ok := it["text"].(string); ok {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		b, _ := Marshal(v)
		return string(b)
	}
	return string(raw)
}

// ParseBlocks 字符串内容当作一个 text 块；解析失败的块跳过。
func ParseBlocks(content json.RawMessage) (text string, isString bool, blocks []ContentBlock) {
	if json.Unmarshal(content, &text) == nil {
		return text, true, nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(content, &arr) != nil {
		return "", false, nil
	}
	for _, it := range arr {
		var b ContentBlock
		if json.Unmarshal(it, &b) == nil && b.Type != "" {
			blocks = append(blocks, b)
		}
	}
	return "", false, blocks
}

func processContent(content json.RawMessage) (string, []kiro.KiroImage, []kiro.ToolResult) {
	s, isStr, blocks := ParseBlocks(content)
	if isStr {
		return s, nil, nil
	}
	var texts []string
	var images []kiro.KiroImage
	var results []kiro.ToolResult
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != nil {
				texts = append(texts, *b.Text)
			}
		case "image":
			if b.Source != nil {
				if f := imageFormat(b.Source.MediaType); f != "" {
					images = append(images, kiro.KiroImage{Format: f, Source: kiro.KiroImageSource{Bytes: b.Source.Data}})
				}
			}
		case "tool_result":
			if b.ToolUseID != nil {
				isErr := b.IsError != nil && *b.IsError
				status := "success"
				if isErr {
					status = "error"
				}
				results = append(results, kiro.ToolResult{
					ToolUseID: *b.ToolUseID,
					Content:   []map[string]any{{"text": toolResultText(b.Content)}},
					Status:    status,
					IsError:   isErr,
				})
			}
		}
	}
	return strings.Join(texts, "\n"), images, results
}

func thinkingPrefix(req *MessagesRequest) string {
	if req.Thinking == nil {
		return ""
	}
	switch req.Thinking.Type {
	case "enabled":
		return fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", req.Thinking.BudgetTokens)
	case "adaptive":
		effort := "high"
		if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
			effort = req.OutputConfig.Effort
		}
		return "<thinking_mode>adaptive</thinking_mode><thinking_effort>" + effort + "</thinking_effort>"
	}
	return ""
}

func hasThinkingTags(s string) bool {
	return strings.Contains(s, "<thinking_mode>") || strings.Contains(s, "<max_thinking_length>")
}

func placeholderTool(name string) kiro.KiroTool {
	return kiro.KiroTool{ToolSpecification: kiro.ToolSpecification{
		Name:        name,
		Description: "Tool used in conversation history",
		InputSchema: kiro.InputSchema{JSON: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":true,"properties":{},"required":[],"type":"object"}`)},
	}}
}

// toolNamer 照 Kiro IDE 处理工具名：只允许 [A-Za-z0-9_-] 且不超过 64 字符，其他字符换成 _，
// 截断后撞名加 _1、_2。rev 记录改过的名字，回包时换回原名。
type toolNamer struct {
	used map[string]bool
	fwd  map[string]string
	rev  map[string]string
}

func newToolNamer() *toolNamer {
	return &toolNamer{used: map[string]bool{}, fwd: map[string]string{}, rev: map[string]string{}}
}

func validToolName(s string) bool {
	if s == "" || len(s) > toolNameMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func sanitizeToolName(name string) string {
	if validToolName(name) {
		return name
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			b.WriteRune(r)
		} else {
			// JS 的 replace 按 UTF-16 码元替换，BMP 外的字符算两个。
			b.WriteString(strings.Repeat("_", len(utf16.Encode([]rune{r}))))
		}
	}
	s := b.String()
	if len(s) > toolNameMaxLen {
		s = s[:toolNameMaxLen]
	}
	if s == "" {
		return "tool"
	}
	return s
}

func (n *toolNamer) name(orig string) string {
	if s, ok := n.fwd[orig]; ok {
		return s
	}
	s := sanitizeToolName(orig)
	if n.used[s] {
		base := s
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			s = base[:min(len(base), toolNameMaxLen-len(suffix))] + suffix
			if !n.used[s] {
				break
			}
		}
	}
	n.used[s] = true
	n.fwd[orig] = s
	if s != orig {
		n.rev[s] = orig
	}
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// toolDescription 空描述用工具名，再空用 tool，与 IDE 一致。
func toolDescription(desc, name string) string {
	switch {
	case strings.TrimSpace(desc) != "":
		return desc
	case strings.TrimSpace(name) != "":
		return name
	}
	return "tool"
}

func convertTools(tools []Tool, names *toolNamer) []kiro.KiroTool {
	out := make([]kiro.KiroTool, 0, len(tools))
	for _, t := range tools {
		desc := toolDescription(t.Description, t.Name)
		switch t.Name {
		case "Write":
			desc += "\n" + writeToolSuffix
		case "Edit":
			desc += "\n" + editToolSuffix
		}
		out = append(out, kiro.KiroTool{ToolSpecification: kiro.ToolSpecification{
			Name:        names.name(t.Name),
			Description: truncateRunes(desc, 10000),
			InputSchema: kiro.InputSchema{JSON: normalizeSchema(t.InputSchema)},
		}})
	}
	return out
}

// alternationFiller 两条用户消息之间没有助手回复时，Kiro IDE 插入的占位回复。
const alternationFiller = "(No assistant response was recorded for the preceding turn. This placeholder was inserted automatically to preserve message alternation — it is not a reply the assistant produced.)"

// toolFailedText 工具调用没有结果时 IDE 补的错误结果。
const toolFailedText = "Tool execution failed"

func userHistory(content, model string) kiro.HistoryMessage {
	return kiro.HistoryMessage{User: &kiro.UserMessage{Content: content, ModelID: model, Origin: kiro.OriginIDE}}
}

func assistantHistory(content string) kiro.HistoryMessage {
	return kiro.HistoryMessage{Assistant: &kiro.AssistantMessage{Content: content}}
}

func mergeUser(msgs []Message, model string) kiro.HistoryMessage {
	var parts []string
	var images []kiro.KiroImage
	var results []kiro.ToolResult
	for _, m := range msgs {
		t, im, tr := processContent(m.Content)
		if t != "" {
			parts = append(parts, t)
		}
		images = append(images, im...)
		results = append(results, tr...)
	}
	u := &kiro.UserMessage{Content: strings.Join(parts, "\n"), ModelID: model, Origin: kiro.OriginIDE, Images: images}
	if len(results) > 0 {
		u.UserInputMessageContext = &kiro.UserInputMessageContext{ToolResults: results}
	}
	return kiro.HistoryMessage{User: u}
}

func convertAssistant(m Message, names *toolNamer) *kiro.AssistantMessage {
	var thinking, text strings.Builder
	var signature, redacted string
	var uses []kiro.ToolUseEntry
	s, isStr, blocks := ParseBlocks(m.Content)
	if isStr {
		text.WriteString(s)
	}
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			thinking.WriteString(str(b.Thinking))
			if signature == "" {
				signature = str(b.Signature)
			}
		case "redacted_thinking":
			if redacted == "" {
				redacted = str(b.Data)
			}
		case "text":
			text.WriteString(str(b.Text))
		case "tool_use":
			if b.ID != nil && b.Name != nil {
				in := b.Input
				if len(in) == 0 || string(in) == "null" {
					in = json.RawMessage(`{}`)
				}
				uses = append(uses, kiro.ToolUseEntry{ToolUseID: *b.ID, Name: names.name(*b.Name), Input: in})
			}
		}
	}
	a := &kiro.AssistantMessage{Content: text.String(), ToolUses: uses}
	switch {
	case thinking.Len() > 0:
		a.Reasoning = &kiro.ReasoningContent{Text: thinking.String(), Signature: signature}
	case redacted != "":
		a.Reasoning = &kiro.ReasoningContent{Redacted: redacted}
	}
	return a
}

func mergeAssistant(msgs []Message, names *toolNamer) kiro.HistoryMessage {
	if len(msgs) == 1 {
		return kiro.HistoryMessage{Assistant: convertAssistant(msgs[0], names)}
	}
	var parts, thoughts []string
	var uses []kiro.ToolUseEntry
	var reasoning *kiro.ReasoningContent
	for _, m := range msgs {
		a := convertAssistant(m, names)
		if strings.TrimSpace(a.Content) != "" {
			parts = append(parts, a.Content)
		}
		if a.Reasoning != nil {
			if reasoning == nil {
				reasoning = a.Reasoning
			}
			if a.Reasoning.Text != "" {
				thoughts = append(thoughts, a.Reasoning.Text)
			}
		}
		uses = append(uses, a.ToolUses...)
	}
	// 多段思考合并后签名对不上，只保留文本。
	if len(thoughts) > 1 {
		reasoning = &kiro.ReasoningContent{Text: strings.Join(thoughts, "\n\n")}
	}
	return kiro.HistoryMessage{Assistant: &kiro.AssistantMessage{Content: strings.Join(parts, "\n\n"), ToolUses: uses, Reasoning: reasoning}}
}

// systemPrompt 系统提示正文（不含思考标签，标签由 provider 按模型决定加不加）。
func systemPrompt(req *MessagesRequest) string {
	if len(req.System) == 0 {
		return ""
	}
	texts := make([]string, len(req.System))
	for i, s := range req.System {
		texts[i] = s.Text
	}
	sys := strings.Join(texts, "\n")
	if sys != "" {
		sys += "\n" + systemChunkPolicy
	}
	return sys
}

func thinkingRequest(req *MessagesRequest) *kiro.ThinkingRequest {
	t := &kiro.ThinkingRequest{Enabled: req.Thinking.Enabled(), Prefix: thinkingPrefix(req)}
	if req.OutputConfig != nil {
		t.Effort = req.OutputConfig.Effort
	}
	if !t.Enabled && t.Effort == "" && t.Prefix == "" {
		return nil
	}
	return t
}

func buildHistory(sys string, msgs []Message, model string, names *toolNamer) []kiro.HistoryMessage {
	var history []kiro.HistoryMessage
	if sys != "" {
		// 免费账号的 /v1/responses 拒绝 instructions（systemPrompt 未开通）。系统提示放进对话，和客户端实验关闭时一样。
		history = append(history, userHistory(sys, model), assistantHistory(kiro.SystemAck))
	}
	var ub, ab []Message
	for _, m := range msgs[:len(msgs)-1] {
		switch m.Role {
		case "user":
			if len(ab) > 0 {
				history = append(history, mergeAssistant(ab, names))
				ab = nil
			}
			ub = append(ub, m)
		case "assistant":
			if len(ub) > 0 {
				history = append(history, mergeUser(ub, model))
				ub = nil
			}
			ab = append(ab, m)
		}
	}
	if len(ab) > 0 {
		history = append(history, mergeAssistant(ab, names))
	}
	if len(ub) > 0 {
		history = append(history, mergeUser(ub, model), assistantHistory(alternationFiller))
	}
	return history
}

// validateToolPairing 过滤孤立/重复的 tool_result，返回还没配对的 tool_use id。
func validateToolPairing(history []kiro.HistoryMessage, results []kiro.ToolResult) ([]kiro.ToolResult, map[string]bool) {
	all := map[string]bool{}
	paired := map[string]bool{}
	for _, h := range history {
		if h.Assistant != nil {
			for _, u := range h.Assistant.ToolUses {
				all[u.ToolUseID] = true
			}
		}
		if h.User != nil && h.User.UserInputMessageContext != nil {
			for _, r := range h.User.UserInputMessageContext.ToolResults {
				paired[r.ToolUseID] = true
			}
		}
	}
	unpaired := map[string]bool{}
	for id := range all {
		if !paired[id] {
			unpaired[id] = true
		}
	}
	var out []kiro.ToolResult
	for _, r := range results {
		switch {
		case unpaired[r.ToolUseID]:
			out = append(out, r)
			delete(unpaired, r.ToolUseID)
		case all[r.ToolUseID]:
			slog.Warn("跳过重复的 tool_result：该 tool_use 已在历史中配对", "tool_use_id", r.ToolUseID)
		default:
			slog.Warn("跳过孤立的 tool_result：找不到对应的 tool_use", "tool_use_id", r.ToolUseID)
		}
	}
	return out, unpaired
}

func failedResult(id string) kiro.ToolResult {
	return kiro.ToolResult{ToolUseID: id, Content: []map[string]any{{"text": toolFailedText}}, Status: "error", IsError: true}
}

// fillMissingToolResults 没拿到结果的工具调用照 IDE 补一条失败结果：下一条用户消息已带结果就并进去，
// 否则在它前面插一条只有失败结果的用户消息，再接占位回复保持交替。
func fillMissingToolResults(history []kiro.HistoryMessage, cur *kiro.UserInputMessage, missing map[string]bool) []kiro.HistoryMessage {
	if len(missing) == 0 {
		return history
	}
	out := make([]kiro.HistoryMessage, 0, len(history)+2)
	for i, h := range history {
		out = append(out, h)
		if h.Assistant == nil {
			continue
		}
		var ids []string
		for _, u := range h.Assistant.ToolUses {
			if missing[u.ToolUseID] {
				ids = append(ids, u.ToolUseID)
				slog.Warn("tool_use 没有对应结果，补一条失败结果", "tool_use_id", u.ToolUseID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		var nextCtx *kiro.UserInputMessageContext
		if i+1 < len(history) {
			if u := history[i+1].User; u != nil && u.UserInputMessageContext != nil && len(u.UserInputMessageContext.ToolResults) > 0 {
				nextCtx = u.UserInputMessageContext
			}
		} else if len(cur.UserInputMessageContext.ToolResults) > 0 {
			nextCtx = &cur.UserInputMessageContext
		}
		if nextCtx != nil {
			for _, id := range ids {
				nextCtx.ToolResults = append(nextCtx.ToolResults, failedResult(id))
			}
			continue
		}
		var results []kiro.ToolResult
		for _, id := range ids {
			results = append(results, failedResult(id))
		}
		out = append(out, kiro.HistoryMessage{User: &kiro.UserMessage{Origin: kiro.OriginIDE,
			UserInputMessageContext: &kiro.UserInputMessageContext{ToolResults: results}}}, assistantHistory(alternationFiller))
	}
	return out
}

// turnInfo 最后一条有正文（非纯工具结果）的用户消息是第几轮，以及此后已经调用过几次模型。
func turnInfo(msgs []Message) (turn, callPosition int) {
	last := -1
	for i, m := range msgs {
		if m.Role != "user" {
			continue
		}
		text, images, _ := processContent(m.Content)
		if strings.TrimSpace(text) != "" || len(images) > 0 {
			turn++
			last = i
		}
	}
	for _, m := range msgs[last+1:] {
		if m.Role == "assistant" {
			callPosition++
		}
	}
	return turn, callPosition
}

// continuationID IDE 每次执行（一轮用户输入及其工具循环）生成一个随机 UUID。
// 这里按会话和轮次派生，同一轮里的多次调用保持不变。
func continuationID(conversationID string, turn int) string {
	sum := sha256.Sum256([]byte(conversationID + "/continuation/" + strconv.Itoa(turn)))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return u.String()
}

// KiroBody 请求根；profileArn 由 provider 按实际凭据注入。
func KiroBody(r *ConversionResult) kiro.KiroRequest {
	return kiro.KiroRequest{ConversationState: r.State, Instructions: r.Instructions,
		CallPosition: r.CallPosition, HasSystem: r.HasSystem, Thinking: r.Thinking}
}

// ConvertRequest Anthropic → Kiro。
func ConvertRequest(req *MessagesRequest) (*ConversionResult, error) {
	model := MapModel(req.Model)
	if model == "" {
		return nil, &ConversionError{Unsupported: req.Model}
	}
	if len(req.Messages) == 0 {
		return nil, &ConversionError{Empty: true}
	}
	msgs := req.Messages
	if msgs[len(msgs)-1].Role != "user" {
		// Claude 4.x 已弃用 assistant prefill，Kiro 也不支持，截到最后一条 user。
		last := -1
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				last = i
				break
			}
		}
		if last < 0 {
			return nil, &ConversionError{Empty: true}
		}
		slog.Info("检测到末尾 assistant 消息（prefill），静默丢弃")
		msgs = msgs[:last+1]
	}
	convID := ""
	if req.Metadata != nil && req.Metadata.UserID != nil {
		convID = ExtractSessionID(*req.Metadata.UserID)
	}
	if convID == "" {
		convID = uuid.NewString()
	}
	text, images, results := processContent(msgs[len(msgs)-1].Content)
	names := newToolNamer()
	tools := convertTools(req.Tools, names)
	sys := systemPrompt(req)
	history := buildHistory(sys, msgs, model, names)
	results, missing := validateToolPairing(history, results)
	cur := kiro.UserInputMessage{
		UserInputMessageContext: kiro.UserInputMessageContext{ToolResults: results},
		Content:                 text,
		ModelID:                 model,
		Images:                  images,
		Origin:                  kiro.OriginIDE,
	}
	history = fillMissingToolResults(history, &cur, missing)

	// 带工具调用的历史必须有 toolConfig；请求本身没有工具时才给历史里的工具补占位定义。
	if len(tools) == 0 {
		seen := map[string]bool{}
		for _, h := range history {
			if h.Assistant == nil {
				continue
			}
			for _, u := range h.Assistant.ToolUses {
				if !seen[u.Name] {
					seen[u.Name] = true
					tools = append(tools, placeholderTool(u.Name))
				}
			}
		}
	}
	cur.UserInputMessageContext.Tools = tools
	if len(names.rev) > 0 {
		slog.Info(fmt.Sprintf("工具名称映射: %d 个名称按 Kiro 规则改写", len(names.rev)))
	}
	limitHistoryImages(history, len(images))
	turn, callPosition := turnInfo(msgs)
	return &ConversionResult{
		State: kiro.ConversationState{
			AgentContinuationID: continuationID(convID, turn),
			AgentTaskType:       "vibe",
			ChatTriggerType:     "MANUAL",
			CurrentMessage:      kiro.CurrentMessage{UserInputMessage: cur},
			ConversationID:      convID,
			History:             history,
		},
		ToolNameMap:  names.rev,
		CallPosition: callPosition,
		HasSystem:    sys != "",
		Thinking:     thinkingRequest(req),
	}, nil
}
