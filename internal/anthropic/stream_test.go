package anthropic

import (
	"strings"
	"testing"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
)

func configDisabled() config.CacheEmulation { return config.CacheEmulation{} }

func answer(s string) *kiro.Event      { return &kiro.Event{Kind: kiro.EvAssistant, Content: s} }
func reasoningEv(s string) *kiro.Event { return &kiro.Event{Kind: kiro.EvReasoning, Text: s} }
func toolEv(id, name, in string, stop bool) *kiro.Event {
	return &kiro.Event{Kind: kiro.EvToolUse, ToolUseID: id, ToolName: name, ToolInput: in, ToolStop: stop}
}

func deltas(evs []SSEEvent, typ, field string) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Event != "content_block_delta" {
			continue
		}
		d := e.Data["delta"].(map[string]any)
		if d["type"] == typ {
			b.WriteString(d[field].(string))
		}
	}
	return b.String()
}

func pos(evs []SSEEvent, f func(SSEEvent) bool) int {
	for i, e := range evs {
		if f(e) {
			return i
		}
	}
	return -1
}

func blockType(e SSEEvent) string {
	if cb, ok := e.Data["content_block"].(map[string]any); ok {
		return cb["type"].(string)
	}
	return ""
}

func run(c *StreamContext, chunks ...string) []SSEEvent {
	all := c.InitialEvents()
	for _, ch := range chunks {
		all = append(all, c.Process(answer(ch))...)
	}
	return append(all, c.FinalEvents()...)
}

func TestThinkingTagSearch(t *testing.T) {
	cases := []struct {
		f    func(string) int
		in   string
		want int
	}{
		{findRealThinkingStart, "<thinking>", 0},
		{findRealThinkingStart, "prefix<thinking>", 6},
		{findRealThinkingStart, "`<thinking>`", -1},
		{findRealThinkingStart, "about `<thinking>` tag<thinking>content", 22},
		{findRealThinkingStart, "about \"<thinking>\" and '<thinking>' then<thinking>", 40},
		{findRealThinkingEnd, "</thinking>\n\n", 0},
		{findRealThinkingEnd, "some text</thinking>\n\nmore text", 9},
		{findRealThinkingEnd, "</thinking>", -1},
		{findRealThinkingEnd, "</thinking>\n", -1},
		{findRealThinkingEnd, "</thinking> more", -1},
		{findRealThinkingEnd, "`</thinking>`\n\n", -1},
		{findRealThinkingEnd, "about '</thinking>' tag</thinking>\n\n", 23},
		{findRealThinkingEnd, "`</thinking>` and \"</thinking>\" and '</thinking>' done</thinking>\n\n", 54},
	}
	for _, c := range cases {
		if got := c.f(c.in); got != c.want {
			t.Errorf("%q => %d want %d", c.in, got, c.want)
		}
	}
}

func TestThinkingSplitAcrossChunks(t *testing.T) {
	for _, chunks := range [][]string{
		{"\n", "\n", "<thin", "king>", "\n", "hello", "</thi", "nking>", "\n", "\n", "world"},
		{"\n\n", "<thinking>", "\n", "hello</thinking>\n\nworld"},
		{"<thinking>\nhello</thinking>", "\n\nworld"},
		{"<thinking>\nhello</thinking>\n", "\n", "world"},
	} {
		evs := run(NewStreamContext("m", 1, true, nil), chunks...)
		if th, tx := deltas(evs, "thinking_delta", "thinking"), deltas(evs, "text_delta", "text"); th != "hello" || tx != "world" {
			t.Errorf("%q => thinking %q text %q", chunks, th, tx)
		}
		// thinking 块必须在 text 块前面
		if pos(evs, func(e SSEEvent) bool { return blockType(e) == "thinking" }) > pos(evs, func(e SSEEvent) bool { return blockType(e) == "text" }) {
			t.Errorf("%q thinking block must come first", chunks)
		}
	}
}

func TestThinkingOnlySetsMaxTokens(t *testing.T) {
	evs := run(NewStreamContext("m", 1, true, nil), "<thinking>\nabc</thinking>")
	md := evs[pos(evs, func(e SSEEvent) bool { return e.Event == "message_delta" })]
	if md.Data["delta"].(map[string]any)["stop_reason"] != "max_tokens" || deltas(evs, "text_delta", "text") != " " {
		t.Fatalf("%v", evs)
	}
	for _, e := range evs {
		if e.Event == "content_block_delta" && e.Data["delta"].(map[string]any)["thinking"] == "</thinking>" {
			t.Fatal("end tag leaked")
		}
	}
}

func TestToolUseAfterThinkingAndTextRestart(t *testing.T) {
	c := NewStreamContext("m", 1, true, map[string]string{"short_abc": "mcp__long_name"})
	all := c.InitialEvents()
	all = append(all, c.Process(answer("<thinking>abc</thinking>"))...)
	all = append(all, c.Process(toolEv("t1", "short_abc", `{"a":1}`, true))...)
	all = append(all, c.Process(answer("after"))...)
	all = append(all, c.FinalEvents()...)
	thinkStop := pos(all, func(e SSEEvent) bool { return e.Event == "content_block_stop" && e.Data["index"] == 0 })
	toolStart := pos(all, func(e SSEEvent) bool { return blockType(e) == "tool_use" })
	if thinkStop < 0 || toolStart < 0 || thinkStop > toolStart {
		t.Fatalf("thinking must stop before tool starts: %v", all)
	}
	if all[toolStart].Data["content_block"].(map[string]any)["name"] != "mcp__long_name" {
		t.Fatal("tool name not restored")
	}
	if deltas(all, "text_delta", "text") != "after" || deltas(all, "input_json_delta", "partial_json") != `{"a":1}` {
		t.Fatal("text after tool")
	}
	md := all[pos(all, func(e SSEEvent) bool { return e.Event == "message_delta" })]
	if md.Data["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatal("stop reason")
	}

	// 暂存的短前缀在 tool_use 前发出：text_delta → text_stop → tool_start
	c2 := NewStreamContext("m", 1, true, nil)
	c2.InitialEvents()
	if e := c2.Process(answer("有修")); len(e) != 0 {
		t.Fatal("should buffer")
	}
	c2.Process(answer("改："))
	evs := c2.Process(toolEv("t1", "Write", "{}", false))
	pd := pos(evs, func(e SSEEvent) bool { return e.Event == "content_block_delta" })
	ps := pos(evs, func(e SSEEvent) bool { return e.Event == "content_block_stop" })
	pt := pos(evs, func(e SSEEvent) bool { return blockType(e) == "tool_use" })
	if deltas(evs, "text_delta", "text") != "有修改：" || !(pd < ps && ps < pt) {
		t.Fatalf("%v", evs)
	}
}

func TestReasoningEvents(t *testing.T) {
	c := NewStreamContext("m", 1, true, nil)
	all := c.InitialEvents()
	all = append(all, c.Process(reasoningEv("Let "))...)
	all = append(all, c.Process(reasoningEv("me"))...)
	all = append(all, c.Process(answer("Hi"))...)
	all = append(all, c.FinalEvents()...)
	if deltas(all, "thinking_delta", "thinking") != "Let me" || deltas(all, "text_delta", "text") != "Hi" {
		t.Fatal(all)
	}
	starts := 0
	for _, e := range all {
		if blockType(e) == "thinking" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatal("reasoning must be one block")
	}
	c2 := NewStreamContext("m", 1, false, nil)
	evs := run(c2)
	_ = evs
	c3 := NewStreamContext("m", 1, false, nil)
	all = c3.InitialEvents()
	all = append(all, c3.Process(reasoningEv("secret"))...)
	all = append(all, c3.Process(answer("ok"))...)
	all = append(all, c3.FinalEvents()...)
	if deltas(all, "thinking_delta", "thinking") != "" || deltas(all, "text_delta", "text") != "ok" {
		t.Fatal("reasoning must be dropped when thinking disabled")
	}
}

func TestUsageReportsAndFailure(t *testing.T) {
	var reports []UsageReport
	c := NewStreamContext("claude-opus-5-5", 50, false, nil)
	c.SetOnUsage(func(r UsageReport) { reports = append(reports, r) })
	c.InitialEvents()
	c.Process(answer("hello"))
	c.Process(&kiro.Event{Kind: kiro.EvContextUsage, ContextUsage: 1})
	evs := c.Fail("eof")
	c.FinalEvents()
	if len(reports) != 1 || reports[0].Error == "" || reports[0].InputTokens != 10000 || evs[0].Event != "error" {
		t.Fatalf("%+v %v", reports, evs)
	}
	for _, e := range evs {
		if e.Event == "message_stop" {
			t.Fatal("failed stream must not send message_stop")
		}
	}
	b := NewBufferedStreamContext("claude-opus-5-5", 50, false, nil)
	b.Process(answer("partial"))
	if out := b.Fail("x"); len(out) != 1 || out[0].Event != "error" {
		t.Fatal("buffered fail drops partial reply")
	}
	b2 := NewBufferedStreamContext("claude-opus-5-5", 50, false, nil)
	b2.Process(answer("hello"))
	b2.Process(&kiro.Event{Kind: kiro.EvContextUsage, ContextUsage: 2})
	out := b2.Finish()
	ms := out[0].Data["message"].(map[string]any)["usage"].(map[string]any)
	if out[0].Event != "message_start" || ms["input_tokens"] != 20000 {
		t.Fatalf("%v", out[0])
	}
}

func TestExtractThinkingComplete(t *testing.T) {
	th, ok, rest := ExtractThinking("<thinking>\nplan</thinking>\n\nanswer")
	if !ok || th != "plan" || rest != "answer" {
		t.Fatalf("%q %v %q", th, ok, rest)
	}
	if _, ok, rest := ExtractThinking("no tags"); ok || rest != "no tags" {
		t.Fatal("no tags")
	}
	th, ok, rest = ExtractThinking("<thinking>p</thinking>")
	if !ok || th != "p" || rest != "" {
		t.Fatal("end at buffer end")
	}
}

func TestSSEFormat(t *testing.T) {
	s := SSEEvent{"message_stop", map[string]any{"type": "message_stop"}}.String()
	if s != "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n" {
		t.Fatal(s)
	}
}

func TestNativeReasoningSignatureAndStops(t *testing.T) {
	c := NewStreamContext("claude-opus-5-5", 10, true, nil)
	evs := c.InitialEvents()
	evs = append(evs, c.Process(reasoningEv("plan"))...)
	evs = append(evs, c.Process(&kiro.Event{Kind: kiro.EvReasoningSignature, Signature: "SIG"})...)
	evs = append(evs, c.Process(answer("done"))...)
	evs = append(evs, c.Process(&kiro.Event{Kind: kiro.EvStop, StopReason: "MAX_TOKENS"})...)
	evs = append(evs, c.FinalEvents()...)
	sig := pos(evs, func(e SSEEvent) bool {
		d, _ := e.Data["delta"].(map[string]any)
		return d["type"] == "signature_delta" && d["signature"] == "SIG"
	})
	text := pos(evs, func(e SSEEvent) bool { return deltas([]SSEEvent{e}, "text_delta", "text") == "done" })
	if sig < 0 || text < sig || !strings.Contains(evs[len(evs)-2].String(), `"stop_reason":"max_tokens"`) {
		t.Fatalf("%v", evs)
	}
	// redacted 思考单独成块。
	r := NewStreamContext("m", 1, true, nil)
	out := r.Process(&kiro.Event{Kind: kiro.EvReasoningSignature, Redacted: "QUJD"})
	if len(out) != 2 || !strings.Contains(out[0].String(), `{"data":"QUJD","type":"redacted_thinking"}`) {
		t.Fatalf("%v", out)
	}
}

func TestUpstreamStreamErrorMapping(t *testing.T) {
	c := NewStreamContext("m", 1, false, nil)
	var rep UsageReport
	c.SetOnUsage(func(r UsageReport) { rep = r })
	c.Process(&kiro.Event{Kind: kiro.EvError, Code: "rate_limit_exceeded", ErrorType: "too_many_requests", Message: "slow down"})
	e := c.UpstreamError()
	if e == nil || e.Status != 429 || e.Type != "rate_limit_error" {
		t.Fatalf("%+v", e)
	}
	out := c.FailWith(e, "x")
	if len(out) != 1 || out[0].Event != "error" || !strings.Contains(out[0].String(), `{"message":"slow down","type":"rate_limit_error"}`) || rep.Status != 429 {
		t.Fatalf("%v %+v", out, rep)
	}
	for code, want := range map[string]string{"context_length_exceeded": "invalid_request_error", "server_is_overloaded": "overloaded_error", "credit_balance_exhausted": "billing_error", "weird": "api_error"} {
		if got := MapStreamError(code, "", "m").Type; got != want {
			t.Errorf("%s => %s", code, got)
		}
	}
}
