package kiro

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
)

// AWS Event Stream 帧：total(4) | headerLen(4) | preludeCRC(4) | headers | payload | msgCRC(4)。
const (
	preludeSize     = 12
	minMessageSize  = preludeSize + 4
	maxMessageSize  = 16 * 1024 * 1024
	maxBufferSize   = 16 * 1024 * 1024
	maxDecodeErrors = 5
)

type parseErrKind int

const (
	errPrelude parseErrKind = iota // 帧边界可能错位，逐字节扫描
	errData                        // 边界对但数据坏，整帧跳过
	errOther
)

// ParseError 帧解析错误。
type ParseError struct {
	kind parseErrKind
	msg  string
}

func (e *ParseError) Error() string { return e.msg }

// ErrTooManyErrors 连续错误过多，解码器停止。
var ErrTooManyErrors = errors.New("解码器连续错误过多已停止")

// Frame 一帧消息。
type Frame struct {
	Headers map[string]any
	Payload []byte
}

func (f *Frame) header(name string) string {
	s, _ := f.Headers[name].(string)
	return s
}

func parseHeaders(data []byte) (map[string]any, error) {
	h := map[string]any{}
	off := 0
	need := func(n int) error {
		if off+n > len(data) {
			return &ParseError{errOther, fmt.Sprintf("数据不完整: 需要 %d 字节", n)}
		}
		return nil
	}
	for off < len(data) {
		nameLen := int(data[off])
		off++
		if nameLen == 0 {
			return nil, &ParseError{errData, "头部名称长度不能为 0"}
		}
		if err := need(nameLen); err != nil {
			return nil, err
		}
		name := string(data[off : off+nameLen])
		off += nameLen
		if err := need(1); err != nil {
			return nil, err
		}
		typ := data[off]
		off++
		var v any
		switch typ {
		case 0:
			v = true
		case 1:
			v = false
		case 2:
			if err := need(1); err != nil {
				return nil, err
			}
			v = int8(data[off])
			off++
		case 3:
			if err := need(2); err != nil {
				return nil, err
			}
			v = int16(binary.BigEndian.Uint16(data[off:]))
			off += 2
		case 4:
			if err := need(4); err != nil {
				return nil, err
			}
			v = int32(binary.BigEndian.Uint32(data[off:]))
			off += 4
		case 5, 8:
			if err := need(8); err != nil {
				return nil, err
			}
			v = int64(binary.BigEndian.Uint64(data[off:]))
			off += 8
		case 6, 7:
			if err := need(2); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(data[off:]))
			if err := need(2 + n); err != nil {
				return nil, err
			}
			b := data[off+2 : off+2+n]
			if typ == 7 {
				v = string(b)
			} else {
				v = append([]byte(nil), b...)
			}
			off += 2 + n
		case 9:
			if err := need(16); err != nil {
				return nil, err
			}
			v = append([]byte(nil), data[off:off+16]...)
			off += 16
		default:
			return nil, &ParseError{errOther, fmt.Sprintf("无效的头部值类型: %d", typ)}
		}
		h[name] = v
	}
	return h, nil
}

// parseFrame 返回 (帧, 消费字节)；数据不够时帧为 nil。
func parseFrame(buf []byte) (*Frame, int, error) {
	if len(buf) < preludeSize {
		return nil, 0, nil
	}
	total := binary.BigEndian.Uint32(buf[0:4])
	hlen := binary.BigEndian.Uint32(buf[4:8])
	pcrc := binary.BigEndian.Uint32(buf[8:12])
	if total < minMessageSize {
		return nil, 0, &ParseError{errPrelude, fmt.Sprintf("消息过小: %d", total)}
	}
	if total > maxMessageSize {
		return nil, 0, &ParseError{errPrelude, fmt.Sprintf("消息过大: %d", total)}
	}
	t := int(total)
	if len(buf) < t {
		return nil, 0, nil
	}
	if got := crc32.ChecksumIEEE(buf[:8]); got != pcrc {
		return nil, 0, &ParseError{errPrelude, "Prelude CRC 不匹配"}
	}
	if got := crc32.ChecksumIEEE(buf[:t-4]); got != binary.BigEndian.Uint32(buf[t-4:t]) {
		return nil, 0, &ParseError{errData, "Message CRC 不匹配"}
	}
	hend := preludeSize + int(hlen)
	if hend > t-4 {
		return nil, 0, &ParseError{errData, "头部长度超出消息边界"}
	}
	h, err := parseHeaders(buf[preludeSize:hend])
	if err != nil {
		return nil, 0, err
	}
	return &Frame{Headers: h, Payload: append([]byte(nil), buf[hend:t-4]...)}, t, nil
}

// Decoder 流式解码，带容错恢复。
type Decoder struct {
	buf        []byte
	errCount   int
	stopped    bool
	recovering bool
	skipped    int
}

// Feed 追加数据。
func (d *Decoder) Feed(b []byte) error {
	if len(d.buf)+len(b) > maxBufferSize {
		return fmt.Errorf("缓冲区溢出: %d > %d", len(d.buf)+len(b), maxBufferSize)
	}
	d.buf = append(d.buf, b...)
	d.recovering = false
	return nil
}

// Next 取下一帧；nil, nil 表示需要更多数据。出错后本轮停止，下次 Feed 再继续。
func (d *Decoder) Next() (*Frame, error) {
	if d.stopped {
		return nil, ErrTooManyErrors
	}
	if d.recovering || len(d.buf) == 0 {
		return nil, nil
	}
	f, n, err := parseFrame(d.buf)
	if err == nil {
		if f == nil {
			return nil, nil
		}
		d.buf = d.buf[n:]
		d.errCount = 0
		return f, nil
	}
	d.errCount++
	if d.errCount >= maxDecodeErrors {
		d.stopped = true
		slog.Error("解码器停止", "errors", d.errCount, "last", err)
		return nil, ErrTooManyErrors
	}
	d.recover(err)
	d.recovering = true
	return nil, err
}

func (d *Decoder) recover(err error) {
	if len(d.buf) == 0 {
		return
	}
	var pe *ParseError
	if errors.As(err, &pe) && pe.kind == errData && len(d.buf) >= preludeSize {
		if t := int(binary.BigEndian.Uint32(d.buf[:4])); t >= 16 && t <= len(d.buf) {
			slog.Warn("Data 错误恢复: 跳过损坏帧", "bytes", t)
			d.buf = d.buf[t:]
			d.skipped += t
			return
		}
	}
	d.buf = d.buf[1:]
	d.skipped++
	slog.Warn("事件流错误恢复: 跳过 1 字节", "skipped_total", d.skipped)
}

// Recovering 当前是否等待新数据来恢复。
func (d *Decoder) Recovering() bool { return d.recovering }

// EventKind 事件种类。
type EventKind int

const (
	EvUnknown EventKind = iota
	EvAssistant
	EvToolUse
	EvReasoning
	EvMetering
	EvContextUsage
	EvError
	EvException
)

// Event 解析后的事件。
type Event struct {
	Kind         EventKind
	Content      string  // assistantResponseEvent.content
	Text         string  // reasoningContentEvent.text
	ToolName     string  // toolUseEvent
	ToolUseID    string  //
	ToolInput    string  //
	ToolStop     bool    //
	ContextUsage float64 // contextUsagePercentage
	Code         string  // error code / exception type
	Message      string  // 错误正文
}

// EventFromFrame 按 :message-type / :event-type 分发。
func EventFromFrame(f *Frame) (*Event, error) {
	mt := f.header(":message-type")
	if mt == "" {
		mt = "event"
	}
	switch mt {
	case "error":
		code := f.header(":error-code")
		if code == "" {
			code = "UnknownError"
		}
		return &Event{Kind: EvError, Code: code, Message: string(f.Payload)}, nil
	case "exception":
		t := f.header(":exception-type")
		if t == "" {
			t = "UnknownException"
		}
		return &Event{Kind: EvException, Code: t, Message: string(f.Payload)}, nil
	case "event":
	default:
		return nil, fmt.Errorf("无效的消息类型: %s", mt)
	}
	switch f.header(":event-type") {
	case "assistantResponseEvent":
		var p struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return &Event{Kind: EvAssistant, Content: p.Content}, nil
	case "toolUseEvent":
		var p struct {
			Name      string `json:"name"`
			ToolUseID string `json:"toolUseId"`
			Input     string `json:"input"`
			Stop      bool   `json:"stop"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return &Event{Kind: EvToolUse, ToolName: p.Name, ToolUseID: p.ToolUseID, ToolInput: p.Input, ToolStop: p.Stop}, nil
	case "reasoningContentEvent":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return &Event{Kind: EvReasoning, Text: p.Text}, nil
	case "meteringEvent":
		return &Event{Kind: EvMetering}, nil
	case "contextUsageEvent":
		var p struct {
			Pct float64 `json:"contextUsagePercentage"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		return &Event{Kind: EvContextUsage, ContextUsage: p.Pct}, nil
	}
	return &Event{Kind: EvUnknown}, nil
}

// EncodeFrame 测试和本地模拟用：按协议拼一帧（只写 string 头）。
func EncodeFrame(headers [][2]string, payload []byte) []byte {
	var hb []byte
	for _, kv := range headers {
		hb = append(hb, byte(len(kv[0])))
		hb = append(hb, kv[0]...)
		hb = append(hb, 7)
		hb = binary.BigEndian.AppendUint16(hb, uint16(len(kv[1])))
		hb = append(hb, kv[1]...)
	}
	total := preludeSize + len(hb) + len(payload) + 4
	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(hb)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:8]))
	out = append(out, hb...)
	out = append(out, payload...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out))
}

// EncodeEvent 拼一个 event 帧。
func EncodeEvent(eventType string, payload any) []byte {
	b, _ := json.Marshal(payload)
	return EncodeFrame([][2]string{{":message-type", "event"}, {":event-type", eventType}, {":content-type", "application/json"}}, b)
}
