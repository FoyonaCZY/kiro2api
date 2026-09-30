package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/google/uuid"
)

const (
	writeToolSuffix   = "- IMPORTANT: If the content to write exceeds 150 lines, you MUST only write the first 50 lines using this tool, then use `Edit` tool to append the remaining content in chunks of no more than 50 lines each. If needed, leave a unique placeholder to help append content. Do NOT attempt to write all content at once."
	editToolSuffix    = "- IMPORTANT: If the `new_string` content exceeds 50 lines, you MUST split it into multiple Edit calls, each replacing no more than 50 lines at a time. If used to append content, leave a unique placeholder to help append content. On the final chunk, do NOT include the placeholder."
	systemChunkPolicy = "When the Write or Edit tool has content size limits, always comply silently. Never suggest bypassing these limits via alternative tools. Never ask the user whether to switch approaches. Complete all chunked operations without commentary."
	toolNameMaxLen    = 63
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

// ConversionResult 转换结果。ToolNameMap 为短名 → 原名。
type ConversionResult struct {
	State       kiro.ConversationState
	ToolNameMap map[string]string
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

func placeholderTool(name string) kiro.KiroTool {
	return kiro.KiroTool{ToolSpecification: kiro.ToolSpecification{
		Name:        name,
		Description: "Tool used in conversation history",
		InputSchema: kiro.InputSchema{JSON: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":true,"properties":{},"required":[],"type":"object"}`)},
	}}
}

func shortenToolName(name string) string {
	sum := sha256.Sum256([]byte(name))
	h := hex.EncodeToString(sum[:])[:8]
	prefixMax := toolNameMaxLen - 1 - 8
	prefix := name
	if utf8.RuneCountInString(name) > prefixMax {
		prefix = string([]rune(name)[:prefixMax])
	}
	return prefix + "_" + h
}

func mapToolName(name string, m map[string]string) string {
	if len(name) <= toolNameMaxLen {
		return name
	}
	s := shortenToolName(name)
	m[s] = name
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func convertTools(tools []Tool, m map[string]string) []kiro.KiroTool {
	out := make([]kiro.KiroTool, 0, len(tools))
	for _, t := range tools {
		desc := t.Description
		switch t.Name {
		case "Write":
			desc += "\n" + writeToolSuffix
		case "Edit":
			desc += "\n" + editToolSuffix
		}
		out = append(out, kiro.KiroTool{ToolSpecification: kiro.ToolSpecification{
			Name:        mapToolName(t.Name, m),
			Description: truncateRunes(desc, 10000),
			InputSchema: kiro.InputSchema{JSON: normalizeSchema(t.InputSchema)},
		}})
	}
	return out
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

func userHistory(content, model string) kiro.HistoryMessage {
	return kiro.HistoryMessage{User: &kiro.UserMessage{Content: content, ModelID: model, Origin: "AI_EDITOR"}}
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
	u := &kiro.UserMessage{Content: strings.Join(parts, "\n"), ModelID: model, Origin: "AI_EDITOR", Images: images}
	if len(results) > 0 {
		u.UserInputMessageContext = &kiro.UserInputMessageContext{ToolResults: results}
	}
	return kiro.HistoryMessage{User: u}
}

func convertAssistant(m Message, names map[string]string) *kiro.AssistantMessage {
	var thinking, text strings.Builder
	var uses []kiro.ToolUseEntry
	s, isStr, blocks := ParseBlocks(m.Content)
	if isStr {
		text.WriteString(s)
	}
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			thinking.WriteString(str(b.Thinking))
		case "text":
			text.WriteString(str(b.Text))
		case "tool_use":
			if b.ID != nil && b.Name != nil {
				in := b.Input
				if len(in) == 0 || string(in) == "null" {
					in = json.RawMessage(`{}`)
				}
				uses = append(uses, kiro.ToolUseEntry{ToolUseID: *b.ID, Name: mapToolName(*b.Name, names), Input: in})
			}
		}
	}
	content := text.String()
	switch {
	case thinking.Len() > 0 && content != "":
		content = "<thinking>" + thinking.String() + "</thinking>\n\n" + content
	case thinking.Len() > 0:
		content = "<thinking>" + thinking.String() + "</thinking>"
	case content == "" && len(uses) > 0:
		content = " " // Kiro 不接受空 content
	}
	return &kiro.AssistantMessage{Content: content, ToolUses: uses}
}

func mergeAssistant(msgs []Message, names map[string]string) kiro.HistoryMessage {
	if len(msgs) == 1 {
		return kiro.HistoryMessage{Assistant: convertAssistant(msgs[0], names)}
	}
	var parts []string
	var uses []kiro.ToolUseEntry
	for _, m := range msgs {
		a := convertAssistant(m, names)
		if strings.TrimSpace(a.Content) != "" {
			parts = append(parts, a.Content)
		}
		uses = append(uses, a.ToolUses...)
	}
	content := strings.Join(parts, "\n\n")
	if len(parts) == 0 && len(uses) > 0 {
		content = " "
	}
	return kiro.HistoryMessage{Assistant: &kiro.AssistantMessage{Content: content, ToolUses: uses}}
}

func buildHistory(req *MessagesRequest, msgs []Message, model string, names map[string]string) []kiro.HistoryMessage {
	var history []kiro.HistoryMessage
	prefix := thinkingPrefix(req)
	if len(req.System) > 0 {
		texts := make([]string, len(req.System))
		for i, s := range req.System {
			texts[i] = s.Text
		}
		sys := strings.Join(texts, "\n")
		if sys != "" {
			sys = sys + "\n" + systemChunkPolicy
			if prefix != "" && !hasThinkingTags(sys) {
				sys = prefix + "\n" + sys
			}
			history = append(history, userHistory(sys, model), assistantHistory("I will follow these instructions."))
		}
	} else if prefix != "" {
		history = append(history, userHistory(prefix, model), assistantHistory("I will follow these instructions."))
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
		history = append(history, mergeUser(ub, model), assistantHistory("OK"))
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
	for id := range unpaired {
		slog.Warn("检测到孤立的 tool_use，将从历史中移除", "tool_use_id", id)
	}
	return out, unpaired
}

func removeOrphanedToolUses(history []kiro.HistoryMessage, orphaned map[string]bool) {
	if len(orphaned) == 0 {
		return
	}
	for _, h := range history {
		if h.Assistant == nil || len(h.Assistant.ToolUses) == 0 {
			continue
		}
		kept := h.Assistant.ToolUses[:0]
		for _, u := range h.Assistant.ToolUses {
			if !orphaned[u.ToolUseID] {
				kept = append(kept, u)
			}
		}
		if len(kept) == 0 {
			kept = nil
		}
		h.Assistant.ToolUses = kept
	}
}

// KiroBody 请求根；profileArn 由 provider 按实际凭据注入。
func KiroBody(r *ConversionResult) kiro.KiroRequest {
	return kiro.KiroRequest{ConversationState: r.State}
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
	names := map[string]string{}
	tools := convertTools(req.Tools, names)
	history := buildHistory(req, msgs, model, names)
	results, orphaned := validateToolPairing(history, results)
	removeOrphanedToolUses(history, orphaned)

	existing := map[string]bool{}
	for _, t := range tools {
		existing[strings.ToLower(t.ToolSpecification.Name)] = true
	}
	for _, h := range history {
		if h.Assistant == nil {
			continue
		}
		for _, u := range h.Assistant.ToolUses {
			if !existing[strings.ToLower(u.Name)] {
				existing[strings.ToLower(u.Name)] = true
				tools = append(tools, placeholderTool(u.Name))
			}
		}
	}
	if len(names) > 0 {
		slog.Info(fmt.Sprintf("工具名称映射: %d 个超长名称已缩短", len(names)))
	}
	return &ConversionResult{
		State: kiro.ConversationState{
			AgentContinuationID: uuid.NewString(),
			AgentTaskType:       "vibe",
			ChatTriggerType:     "MANUAL",
			CurrentMessage: kiro.CurrentMessage{UserInputMessage: kiro.UserInputMessage{
				UserInputMessageContext: kiro.UserInputMessageContext{ToolResults: results, Tools: tools},
				Content:                 text,
				ModelID:                 model,
				Images:                  images,
				Origin:                  "AI_EDITOR",
			}},
			ConversationID: convID,
			History:        history,
		},
		ToolNameMap: names,
	}, nil
}
