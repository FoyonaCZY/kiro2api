package kiro

import (
	"bytes"
	"encoding/json"
)

// MarshalNoEscape 不把 <>& 转成 \u003c 之类，与 serde_json 输出一致。
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// 以下是 anthropic 包交给 provider 的内部信封，provider 选定凭据后再转成 /v1/responses。

// KiroRequest 请求根。
type KiroRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
	Instructions      string            `json:"instructions,omitempty"`
	// CallPosition 本轮用户输入后第几次调用模型（0 起），决定 x-kiro-attempt 的预算。
	CallPosition int `json:"callPosition,omitempty"`
	// HasSystem 历史第一对消息是系统提示和确认语。
	HasSystem bool `json:"hasSystem,omitempty"`
	// Thinking 客户端要求的思考方式；模型支持原生 reasoning 时用 reasoning 字段，否则退回提示词标签。
	Thinking *ThinkingRequest `json:"thinking,omitempty"`
}

// ThinkingRequest 思考配置。Prefix 是退回提示词方案时要加的标签。
type ThinkingRequest struct {
	Enabled bool   `json:"enabled,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
}

// ConversationState 对话状态。
type ConversationState struct {
	AgentContinuationID string           `json:"agentContinuationId,omitempty"`
	AgentTaskType       string           `json:"agentTaskType,omitempty"`
	ChatTriggerType     string           `json:"chatTriggerType,omitempty"`
	CurrentMessage      CurrentMessage   `json:"currentMessage"`
	ConversationID      string           `json:"conversationId"`
	History             []HistoryMessage `json:"history,omitempty"`
}

// CurrentMessage 当前消息容器。
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage 当前用户输入。
type UserInputMessage struct {
	UserInputMessageContext UserInputMessageContext `json:"userInputMessageContext"`
	Content                 string                  `json:"content"`
	ModelID                 string                  `json:"modelId"`
	Images                  []KiroImage             `json:"images,omitempty"`
	Origin                  string                  `json:"origin,omitempty"`
}

// UserInputMessageContext 工具定义和工具结果。
type UserInputMessageContext struct {
	ToolResults []ToolResult `json:"toolResults,omitempty"`
	Tools       []KiroTool   `json:"tools,omitempty"`
}

// IsEmpty 没有工具和结果。
func (c *UserInputMessageContext) IsEmpty() bool { return len(c.ToolResults) == 0 && len(c.Tools) == 0 }

// KiroImage 图片。
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

// KiroImageSource base64 数据。
type KiroImageSource struct {
	Bytes string `json:"bytes"`
}

// HistoryMessage 历史消息：用户或助手二选一。
type HistoryMessage struct {
	User      *UserMessage      `json:"-"`
	Assistant *AssistantMessage `json:"-"`
}

// MarshalJSON untagged 输出。
func (m HistoryMessage) MarshalJSON() ([]byte, error) {
	if m.User != nil {
		return MarshalNoEscape(struct {
			UserInputMessage *UserMessage `json:"userInputMessage"`
		}{m.User})
	}
	return MarshalNoEscape(struct {
		AssistantResponseMessage *AssistantMessage `json:"assistantResponseMessage"`
	}{m.Assistant})
}

// UnmarshalJSON untagged 输入。
func (m *HistoryMessage) UnmarshalJSON(b []byte) error {
	var raw struct {
		UserInputMessage         *UserMessage      `json:"userInputMessage"`
		AssistantResponseMessage *AssistantMessage `json:"assistantResponseMessage"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.User, m.Assistant = raw.UserInputMessage, raw.AssistantResponseMessage
	return nil
}

// UserMessage 历史里的用户消息。
type UserMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// AssistantMessage 历史里的助手消息。
type AssistantMessage struct {
	Content   string            `json:"content"`
	ToolUses  []ToolUseEntry    `json:"toolUses,omitempty"`
	Reasoning *ReasoningContent `json:"reasoning,omitempty"`
}

// ReasoningContent 上一轮的思考内容。带签名（或 redacted）的才能按原生 reasoning 回放。
type ReasoningContent struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"redacted,omitempty"`
}

// KiroTool 工具定义。
type KiroTool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification 工具规范。
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema JSON Schema 包装。
type InputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// ToolResult 工具执行结果。
type ToolResult struct {
	ToolUseID string           `json:"toolUseId"`
	Content   []map[string]any `json:"content"`
	Status    string           `json:"status,omitempty"`
	IsError   bool             `json:"isError,omitempty"`
}

// ToolUseEntry 历史里的工具调用。
type ToolUseEntry struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}
