package kiro

import (
	"bytes"
	"encoding/json"
	"strings"
)

// SSEDecoder 解析 /v1/responses 的 SSE，产出和旧事件流相同的 Event。
type SSEDecoder struct {
	buf      []byte
	pending  []*Event
	open     map[string]toolCall
	current  string
	held     []string
	errCount int
	stopped  bool
	refusal  bool
}

type toolCall struct {
	sawDelta bool
	stopped  bool
}

// Feed 追加一块响应体。
func (d *SSEDecoder) Feed(b []byte) error {
	if d.stopped {
		return ErrTooManyErrors
	}
	if len(d.buf)+len(b) > maxBufferSize {
		d.stopped = true
		return ErrTooManyErrors
	}
	d.buf = append(d.buf, b...)
	for {
		i := bytes.IndexByte(d.buf, '\n')
		if i < 0 {
			return nil
		}
		line := string(d.buf[:i])
		d.buf = d.buf[i+1:]
		line = strings.TrimRight(line, "\r")
		d.consume(line)
	}
}

// NextEvent 取下一个已解析事件。nil, nil 表示当前缓冲里没有完整事件。
func (d *SSEDecoder) NextEvent() (*Event, error) {
	if d.stopped && len(d.pending) == 0 {
		return nil, ErrTooManyErrors
	}
	if len(d.pending) == 0 {
		return nil, nil
	}
	ev := d.pending[0]
	d.pending = d.pending[1:]
	return ev, nil
}

func (d *SSEDecoder) consume(line string) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(line[len("data:"):])
	if data == "" || data == "[DONE]" {
		return
	}
	var w struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		Refusal  string          `json:"refusal"`
		Item     json.RawMessage `json:"item"`
		Response json.RawMessage `json:"response"`
		Error    json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return
	}
	switch w.Type {
	case "response.output_text.delta":
		if w.Delta != "" {
			d.push(&Event{Kind: EvAssistant, Content: w.Delta})
		}
	case "response.refusal.delta":
		d.refusal = true
		if w.Delta != "" {
			d.push(&Event{Kind: EvAssistant, Content: w.Delta})
		}
	case "response.refusal.done":
		// 和 IDE 一样：已经流过 delta 就不再重复输出完整拒答。
		if w.Refusal != "" && !d.refusal {
			d.push(&Event{Kind: EvAssistant, Content: w.Refusal})
		}
	case "response.reasoning_text.delta", "response.reasoning.delta":
		if w.Delta != "" {
			d.push(&Event{Kind: EvReasoning, Text: w.Delta})
		}
	case "response.output_item.added":
		d.onItemAdded(w.Item)
	case "response.function_call_arguments.delta":
		if w.Delta != "" && d.current != "" {
			c := d.open[d.current]
			c.sawDelta = true
			d.open[d.current] = c
			d.push(&Event{Kind: EvToolUse, ToolUseID: d.current, ToolInput: w.Delta})
		}
	case "response.function_call_arguments.done":
		if d.current != "" {
			if c, ok := d.open[d.current]; ok && !c.stopped {
				c.stopped = true
				d.open[d.current] = c
				d.push(&Event{Kind: EvToolUse, ToolUseID: d.current, ToolStop: true})
			}
			d.current = ""
		}
	case "response.output_item.done":
		d.onItemDone(w.Item)
	case "response.completed", "response.incomplete":
		d.finish(w.Type, w.Response)
	case "error":
		d.fail(w.Error)
	case "response.failed":
		var resp struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(w.Response, &resp)
		d.fail(resp.Error)
	}
}

func (d *SSEDecoder) onItemAdded(raw json.RawMessage) {
	item := parseCall(raw)
	if item.Type != "function_call" {
		return
	}
	id := item.id()
	if d.open == nil {
		d.open = map[string]toolCall{}
	}
	args := item.args()
	d.open[id] = toolCall{sawDelta: args != ""}
	d.current = id
	d.push(&Event{Kind: EvToolUse, ToolUseID: id, ToolName: item.Name, ToolInput: args})
}

func (d *SSEDecoder) onItemDone(raw json.RawMessage) {
	item := parseCall(raw)
	if item.Type == "reasoning" {
		if r := ParseReasoningEnvelope(item.EncryptedContent); r != nil && (r.Signature != "" || r.Redacted != "") {
			d.push(&Event{Kind: EvReasoningSignature, Signature: r.Signature, Redacted: r.Redacted})
		}
		return
	}
	if item.Type != "function_call" {
		return
	}
	id := item.id()
	c, ok := d.open[id]
	if !ok || c.stopped {
		return
	}
	if !c.sawDelta && item.args() != "" {
		d.push(&Event{Kind: EvToolUse, ToolUseID: id, ToolName: item.Name, ToolInput: item.args()})
	}
	if item.Status == "incomplete" {
		c.stopped = true
		d.open[id] = c
		return
	}
	d.held = append(d.held, id)
}

// incompleteStop incomplete_details.reason → 结束原因，与 IDE 的映射相同。
var incompleteStop = map[string]string{
	"max_output_tokens":                     "MAX_TOKENS",
	"content_filter":                        "CONTENT_FILTERED",
	"compaction_required_for_model_handoff": "COMPACTION_REQUIRED_FOR_MODEL_HANDOFF",
}

func (d *SSEDecoder) finish(typ string, raw json.RawMessage) {
	var resp struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage struct {
			Pct     float64 `json:"kiro:context_usage_percentage"`
			Credits *struct {
				Value float64 `json:"value"`
			} `json:"kiro:credits"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &resp)
	incomplete := typ == "response.incomplete" || resp.Status == "incomplete"
	if !incomplete {
		for _, id := range d.held {
			d.stopTool(id)
		}
		for id := range d.open {
			d.stopTool(id)
		}
	}
	// 截断时 IDE 不给未完成的工具调用补 stop，这些调用作废。
	d.held = nil
	d.open = map[string]toolCall{}
	d.current = ""
	if incomplete {
		reason := ""
		if resp.IncompleteDetails != nil {
			reason = resp.IncompleteDetails.Reason
		}
		stop := incompleteStop[reason]
		if stop == "" {
			stop = "END_TURN"
		}
		d.push(&Event{Kind: EvStop, StopReason: stop})
	}
	if resp.Usage.Pct != 0 {
		d.push(&Event{Kind: EvContextUsage, ContextUsage: resp.Usage.Pct})
	}
	if resp.Usage.Credits != nil {
		d.push(&Event{Kind: EvMetering, Credits: resp.Usage.Credits.Value})
	}
}

func (d *SSEDecoder) stopTool(id string) {
	if c, ok := d.open[id]; ok && !c.stopped {
		c.stopped = true
		d.open[id] = c
		d.push(&Event{Kind: EvToolUse, ToolUseID: id, ToolStop: true})
	}
}

func (d *SSEDecoder) fail(raw json.RawMessage) {
	var e struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	d.held = nil
	d.open = map[string]toolCall{}
	d.current = ""
	d.push(&Event{Kind: EvError, Code: e.Code, ErrorType: e.Type, Message: e.Message})
}

func (d *SSEDecoder) push(ev *Event) { d.pending = append(d.pending, ev) }

type callItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status"`
	// EncryptedContent reasoning item 的签名信封。
	EncryptedContent string `json:"encrypted_content"`
}

func (c callItem) id() string {
	if c.CallID != "" {
		return c.CallID
	}
	return c.ID
}

func (c callItem) args() string {
	if len(c.Arguments) == 0 || string(c.Arguments) == "null" {
		return ""
	}
	if c.Arguments[0] == '"' {
		var s string
		if json.Unmarshal(c.Arguments, &s) == nil {
			return s
		}
	}
	return string(c.Arguments)
}

func parseCall(raw json.RawMessage) callItem {
	var c callItem
	_ = json.Unmarshal(raw, &c)
	return c
}
