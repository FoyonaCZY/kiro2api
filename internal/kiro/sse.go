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
	case "response.output_text.delta", "response.refusal.delta":
		if w.Delta != "" {
			d.push(&Event{Kind: EvAssistant, Content: w.Delta})
		}
	case "response.refusal.done":
		if w.Refusal != "" {
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
		d.finish(w.Response)
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

func (d *SSEDecoder) finish(raw json.RawMessage) {
	for _, id := range d.held {
		c := d.open[id]
		if !c.stopped {
			c.stopped = true
			d.open[id] = c
			d.push(&Event{Kind: EvToolUse, ToolUseID: id, ToolStop: true})
		}
	}
	d.held = nil
	for id, c := range d.open {
		if !c.stopped {
			c.stopped = true
			d.open[id] = c
			d.push(&Event{Kind: EvToolUse, ToolUseID: id, ToolStop: true})
		}
	}
	var resp struct {
		Usage struct {
			Pct float64 `json:"kiro:context_usage_percentage"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &resp) == nil && resp.Usage.Pct != 0 {
		d.push(&Event{Kind: EvContextUsage, ContextUsage: resp.Usage.Pct})
	}
}

func (d *SSEDecoder) fail(raw json.RawMessage) {
	var e struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	code := e.Code
	if code == "" {
		code = e.Type
	}
	if code == "" {
		code = "error"
	}
	d.push(&Event{Kind: EvError, Code: code, Message: e.Message})
}

func (d *SSEDecoder) push(ev *Event) { d.pending = append(d.pending, ev) }

type callItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status"`
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
