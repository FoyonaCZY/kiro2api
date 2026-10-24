package kiro

import (
	"encoding/json"
	"strings"
)

// OriginIDE 本机 Kiro IDE 写在 /v1/responses 里的 origin。
const OriginIDE = "AI_EDITOR"

// ResponsesFromKiro 把内部的 conversationState 信封转成 Kiro 1.4.26 CreateResponse 的 body。
// agentMode 来自配置，写进 metadata 的 kiro:agent_mode。spec 任务才写 kiro:agent_task_type。
func ResponsesFromKiro(body, agentMode string) (string, error) {
	var req KiroRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return "", err
	}
	st := req.ConversationState
	model := st.CurrentMessage.UserInputMessage.ModelID
	out := responsesRequest{
		Model:        model,
		Input:        responsesInput(&st),
		Instructions: req.Instructions,
		Tools:        responsesTools(&st),
		Stream:       true,
		Origin:       OriginIDE,
		ProfileArn:   req.ProfileArn,
		Metadata:     responsesMeta(&st, agentMode),
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

type responsesRequest struct {
	Model        string            `json:"model"`
	Input        []any             `json:"input"`
	Instructions string            `json:"instructions,omitempty"`
	Tools        []any             `json:"tools,omitempty"`
	Stream       bool              `json:"stream"`
	Origin       string            `json:"origin"`
	ProfileArn   string            `json:"profileArn,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

func responsesMeta(st *ConversationState, agentMode string) map[string]string {
	m := map[string]string{}
	if agentMode != "" {
		m["kiro:agent_mode"] = agentMode
	}
	if st.AgentTaskType == "spectask" {
		m["kiro:agent_task_type"] = "spectask"
	}
	if st.ConversationID != "" {
		m["kiro:conversation_id"] = st.ConversationID
		m["kiro:root_conversation_id"] = st.ConversationID
	}
	if st.AgentContinuationID != "" {
		m["kiro:agent_continuation_id"] = st.AgentContinuationID
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func responsesTools(st *ConversationState) []any {
	tools := st.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) == 0 {
		return nil
	}
	var out []any
	for _, t := range tools {
		spec := t.ToolSpecification
		if spec.Name == "" {
			continue
		}
		params := spec.InputSchema.JSON
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        spec.Name,
			"description": spec.Description,
			"parameters":  params,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func responsesInput(st *ConversationState) []any {
	var out []any
	for i := range st.History {
		out = append(out, messageInput(&st.History[i])...)
	}
	cur := HistoryMessage{User: &UserMessage{
		Content:                 st.CurrentMessage.UserInputMessage.Content,
		Images:                  st.CurrentMessage.UserInputMessage.Images,
		UserInputMessageContext: &st.CurrentMessage.UserInputMessage.UserInputMessageContext,
	}}
	out = append(out, messageInput(&cur)...)
	return out
}

func messageInput(m *HistoryMessage) []any {
	if m.User != nil {
		return userInput(m.User)
	}
	if m.Assistant != nil {
		return assistantInput(m.Assistant)
	}
	return nil
}

func userInput(u *UserMessage) []any {
	var out []any
	if u.UserInputMessageContext != nil {
		for _, r := range u.UserInputMessageContext.ToolResults {
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": r.ToolUseID,
				"output":  toolText(r.Content),
			})
		}
	}
	var images []any
	for _, img := range u.Images {
		if img.Source.Bytes == "" {
			continue
		}
		format := strings.ToLower(img.Format)
		if format == "" {
			format = "png"
		}
		images = append(images, map[string]any{
			"type":      "input_image",
			"image_url": "data:image/" + format + ";base64," + img.Source.Bytes,
		})
	}
	text := u.Content
	if len(images) > 0 {
		var content []any
		if text != "" {
			content = append(content, map[string]any{"type": "input_text", "text": text})
		}
		content = append(content, images...)
		if len(content) > 0 {
			out = append(out, map[string]any{"role": "user", "content": content})
		}
		return out
	}
	if text != "" {
		out = append(out, map[string]any{"role": "user", "content": text})
	}
	return out
}

func assistantInput(a *AssistantMessage) []any {
	var out []any
	if a.Content != "" {
		out = append(out, map[string]any{"role": "assistant", "content": a.Content})
	}
	for _, u := range a.ToolUses {
		out = append(out, map[string]any{
			"type":      "function_call",
			"call_id":   u.ToolUseID,
			"name":      u.Name,
			"arguments": argumentsText(u.Input),
		})
	}
	return out
}

func argumentsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func toolText(content []map[string]any) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		if t, ok := c["text"].(string); ok {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}
