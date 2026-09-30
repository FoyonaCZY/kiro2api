// Package anthropic 实现 Anthropic Messages 兼容接口：请求转换、流式/非流式响应、计费和透传。
package anthropic

import (
	"bytes"
	"encoding/json"
)

const maxBudgetTokens = 24576

// ErrorResponse Anthropic 错误体。
type ErrorResponse struct {
	Type  string      `json:"type,omitempty"`
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情。
type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// NewError 构造错误体。
func NewError(typ, msg string) ErrorResponse {
	return ErrorResponse{Error: ErrorDetail{Type: typ, Message: msg}}
}

// Thinking 思考配置。budget_tokens 缺省 20000，上限 24576。
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// UnmarshalJSON 处理默认值和上限。
func (t *Thinking) UnmarshalJSON(b []byte) error {
	var raw struct {
		Type         string `json:"type"`
		BudgetTokens *int   `json:"budget_tokens"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	t.Type = raw.Type
	t.BudgetTokens = 20000
	if raw.BudgetTokens != nil {
		t.BudgetTokens = min(*raw.BudgetTokens, maxBudgetTokens)
	}
	return nil
}

// Enabled enabled 或 adaptive。
func (t *Thinking) Enabled() bool { return t != nil && (t.Type == "enabled" || t.Type == "adaptive") }

// OutputConfig effort 缺省 high。
type OutputConfig struct {
	Effort string `json:"effort"`
}

// Metadata Claude Code 的 metadata。
type Metadata struct {
	UserID *string `json:"user_id"`
}

// SystemList system 字段，支持字符串或 [{text}] 数组。
type SystemList []SystemMessage

// SystemMessage 系统消息。
type SystemMessage struct {
	Text string `json:"text"`
}

// UnmarshalJSON 字符串或数组；空数组视为没有。
func (s *SystemList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = SystemList{{Text: str}}
		return nil
	}
	var arr []SystemMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	if len(arr) == 0 {
		*s = nil
		return nil
	}
	*s = arr
	return nil
}

// Message 对话消息；content 是字符串或内容块数组。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Tool 普通工具或 web_search 工具。
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	MaxUses     *int            `json:"max_uses,omitempty"`
}

// MessagesRequest /v1/messages 请求体。
type MessagesRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	Messages     []Message       `json:"messages"`
	Stream       bool            `json:"stream"`
	System       SystemList      `json:"system"`
	Tools        []Tool          `json:"tools"`
	ToolChoice   json.RawMessage `json:"tool_choice"`
	Thinking     *Thinking       `json:"thinking"`
	OutputConfig *OutputConfig   `json:"output_config"`
	Metadata     *Metadata       `json:"metadata"`
}

// ContentBlock 内容块。
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	Thinking  *string         `json:"thinking,omitempty"`
	ToolUseID *string         `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Name      *string         `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ID        *string         `json:"id,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
	Source    *ImageSource    `json:"source,omitempty"`
}

// ImageSource base64 图片。
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// CountTokensRequest count_tokens 请求。
type CountTokensRequest struct {
	Model    string     `json:"model"`
	Messages []Message  `json:"messages"`
	System   SystemList `json:"system,omitempty"`
	Tools    []Tool     `json:"tools,omitempty"`
}

// Marshal 不转义 <>&，与 serde_json 输出一致，也避免放大正文。
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
