package kiro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// OriginIDE 本机 Kiro IDE 写在 /v1/responses 里的 origin。
const OriginIDE = "AI_EDITOR"

// SystemAck 系统提示后面接的确认语，和 Kiro IDE 一致。
const SystemAck = "I will follow these instructions."

// ResponsesFromKiro 把内部信封转成 Kiro 1.4.26 CreateResponse 的 body。
// model 是选中凭据下该模型的信息；声明了思考/effort 时走 reasoning 字段并按原生方式回放历史思考，否则退回提示词标签。
func ResponsesFromKiro(body, agentMode string, model *ModelInfo) (string, error) {
	var req KiroRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return "", err
	}
	st := req.ConversationState
	native := model.Native()
	if !native {
		applyThinkingPrefix(&req, &st)
	}
	out := responsesRequest{
		Model:        st.CurrentMessage.UserInputMessage.ModelID,
		Input:        responsesInput(&st, native),
		Instructions: req.Instructions,
		Tools:        responsesTools(&st),
		Stream:       true,
		Origin:       OriginIDE,
		ProfileArn:   req.ProfileArn,
		Metadata:     buildMeta(&st, agentMode),
	}
	if native {
		out.Reasoning = model.Reasoning(req.Thinking)
	}
	if out.Input == nil {
		out.Input = []any{}
	}
	b, err := MarshalNoEscape(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// applyThinkingPrefix 提示词方案：标签加在系统提示前面；没有系统提示时补一对系统消息。
func applyThinkingPrefix(req *KiroRequest, st *ConversationState) {
	if req.Thinking == nil || req.Thinking.Prefix == "" {
		return
	}
	prefix := req.Thinking.Prefix
	if req.HasSystem && len(st.History) > 0 && st.History[0].User != nil {
		u := *st.History[0].User
		if !strings.Contains(u.Content, "<thinking_mode>") && !strings.Contains(u.Content, "<max_thinking_length>") {
			if u.Content == "" {
				u.Content = prefix
			} else {
				u.Content = prefix + "\n" + u.Content
			}
		}
		st.History = append([]HistoryMessage{{User: &u}}, st.History[1:]...)
		return
	}
	model := st.CurrentMessage.UserInputMessage.ModelID
	st.History = append([]HistoryMessage{
		{User: &UserMessage{Content: prefix, ModelID: model, Origin: OriginIDE}},
		{Assistant: &AssistantMessage{Content: SystemAck}},
	}, st.History...)
}

type responsesRequest struct {
	Model        string         `json:"model"`
	Input        []any          `json:"input"`
	Instructions string         `json:"instructions,omitempty"`
	Tools        []any          `json:"tools,omitempty"`
	Stream       bool           `json:"stream"`
	Origin       string         `json:"origin"`
	ProfileArn   string         `json:"profileArn,omitempty"`
	Metadata     *responsesMeta `json:"metadata,omitempty"`
	Reasoning    *Reasoning     `json:"reasoning,omitempty"`
}

// responsesMeta 字段顺序和 IDE 写入顺序一致。
type responsesMeta struct {
	AgentMode           string `json:"kiro:agent_mode,omitempty"`
	AgentTaskType       string `json:"kiro:agent_task_type,omitempty"`
	ConversationID      string `json:"kiro:conversation_id,omitempty"`
	RootConversationID  string `json:"kiro:root_conversation_id,omitempty"`
	AgentContinuationID string `json:"kiro:agent_continuation_id,omitempty"`
}

func buildMeta(st *ConversationState, agentMode string) *responsesMeta {
	m := responsesMeta{AgentMode: agentMode, ConversationID: st.ConversationID, RootConversationID: st.ConversationID,
		AgentContinuationID: st.AgentContinuationID}
	if st.AgentTaskType == "spectask" {
		m.AgentTaskType = "spectask"
	}
	if m == (responsesMeta{}) {
		return nil
	}
	return &m
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func responsesTools(st *ConversationState) []any {
	var out []any
	for _, t := range st.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools {
		spec := t.ToolSpecification
		if spec.Name == "" {
			continue
		}
		params := spec.InputSchema.JSON
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, responsesTool{Type: "function", Name: spec.Name, Description: spec.Description, Parameters: params})
	}
	return out
}

func responsesInput(st *ConversationState, native bool) []any {
	var out []any
	for i := range st.History {
		out = append(out, messageInput(&st.History[i], native)...)
	}
	cur := HistoryMessage{User: &UserMessage{
		Content:                 st.CurrentMessage.UserInputMessage.Content,
		Images:                  st.CurrentMessage.UserInputMessage.Images,
		UserInputMessageContext: &st.CurrentMessage.UserInputMessage.UserInputMessageContext,
	}}
	return append(out, messageInput(&cur, native)...)
}

func messageInput(m *HistoryMessage, native bool) []any {
	if m.User != nil {
		return userInput(m.User)
	}
	if m.Assistant != nil {
		return assistantInput(m.Assistant, native)
	}
	return nil
}

type (
	roleText struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	roleParts struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}
	inputText struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	inputImage struct {
		Type     string `json:"type"`
		ImageURL string `json:"image_url"`
	}
	callOutput struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}
	functionCall struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	reasoningItem struct {
		Type             string `json:"type"`
		Summary          []any  `json:"summary"`
		EncryptedContent string `json:"encrypted_content"`
	}
)

func userInput(u *UserMessage) []any {
	var out []any
	if u.UserInputMessageContext != nil {
		for _, r := range u.UserInputMessageContext.ToolResults {
			out = append(out, callOutput{Type: "function_call_output", CallID: r.ToolUseID, Output: toolText(r.Content)})
		}
	}
	var images []any
	for _, img := range u.Images {
		if img.Source.Bytes == "" {
			continue
		}
		images = append(images, inputImage{Type: "input_image", ImageURL: "data:image/" + strings.ToLower(img.Format) + ";base64," + img.Source.Bytes})
	}
	if len(images) > 0 {
		var content []any
		if u.Content != "" {
			content = append(content, inputText{Type: "input_text", Text: u.Content})
		}
		return append(out, roleParts{Role: "user", Content: append(content, images...)})
	}
	if u.Content != "" {
		out = append(out, roleText{Role: "user", Content: u.Content})
	}
	return out
}

// ReasoningEnvelope encrypted_content 里的 JSON，和 IDE 回放思考时写的一样。
func ReasoningEnvelope(r *ReasoningContent) string {
	if r == nil {
		return ""
	}
	if r.Redacted != "" {
		b, _ := MarshalNoEscape(map[string]string{"redactedContent": r.Redacted})
		return string(b)
	}
	if r.Signature == "" {
		return ""
	}
	type text struct {
		Text      string `json:"text"`
		Signature string `json:"signature"`
	}
	b, _ := MarshalNoEscape(struct {
		ReasoningText text `json:"reasoningText"`
	}{text{r.Text, r.Signature}})
	return string(b)
}

// ParseReasoningEnvelope 解出 SSE 里 reasoning item 的 encrypted_content。
func ParseReasoningEnvelope(s string) *ReasoningContent {
	var v struct {
		RedactedContent string `json:"redactedContent"`
		ReasoningText   *struct {
			Text      string `json:"text"`
			Signature string `json:"signature"`
		} `json:"reasoningText"`
	}
	if json.Unmarshal([]byte(s), &v) != nil {
		return nil
	}
	if v.RedactedContent != "" {
		if _, err := base64.StdEncoding.DecodeString(v.RedactedContent); err != nil {
			return nil
		}
		return &ReasoningContent{Redacted: v.RedactedContent}
	}
	if v.ReasoningText != nil {
		return &ReasoningContent{Text: v.ReasoningText.Text, Signature: v.ReasoningText.Signature}
	}
	return nil
}

func assistantInput(a *AssistantMessage, native bool) []any {
	var out []any
	content := a.Content
	if enc := ReasoningEnvelope(a.Reasoning); native && enc != "" {
		out = append(out, reasoningItem{Type: "reasoning", Summary: []any{}, EncryptedContent: enc})
	} else if a.Reasoning != nil && a.Reasoning.Text != "" {
		// 没有签名或模型不支持原生思考：照旧把思考写回正文。
		if content != "" {
			content = "<thinking>" + a.Reasoning.Text + "</thinking>\n\n" + content
		} else {
			content = "<thinking>" + a.Reasoning.Text + "</thinking>"
		}
	}
	if content != "" {
		out = append(out, roleText{Role: "assistant", Content: content})
	}
	for _, u := range a.ToolUses {
		out = append(out, functionCall{Type: "function_call", CallID: u.ToolUseID, Name: u.Name, Arguments: argumentsText(u.Input)})
	}
	return out
}

// argumentsText 和 IDE 的 JSON.stringify(input) 一样输出紧凑 JSON。
func argumentsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

func toolText(content []map[string]any) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		t, _ := c["text"].(string)
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n")
}
