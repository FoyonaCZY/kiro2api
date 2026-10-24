package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-4-20250514":            "claude-sonnet-4",
		"claude-opus-5-5":                     "claude-opus-5.5",
		"claude-opus-5.5":                     "claude-opus-5.5",
		"claude-opus-5-5-20260922-thinking":   "claude-opus-5.5",
		"claude-sonnet-5-5-thinking":          "claude-sonnet-5.5",
		"CLAUDE-OPUS-5-5":                     "claude-opus-5.5",
		"claude-opus-5-10":                    "claude-opus-5.10",
		"claude-sonnet-4-5-20250929-thinking": "claude-sonnet-4.5",
		"claude-opus-4-6-thinking":            "claude-opus-4.6",
		"claude-haiku-4-5-20251001-thinking":  "claude-haiku-4.5",
		"claude-sonnet-5":                     "claude-sonnet-5",
	} {
		if got := MapModel(in); got != want {
			t.Errorf("%s => %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"gpt-4", "claude-3-5-sonnet-20241022", "fake-opus-5", "claude-opus-5-bogus", "claude-opus-5..5", "claude-opus-", "claude-opus-5-5-5"} {
		if got := MapModel(bad); got != "" {
			t.Errorf("%s => %q want empty", bad, got)
		}
	}
	if ContextWindowSize("claude-opus-5-5") != 1_000_000 || ContextWindowSize("claude-sonnet-4-5") != 200_000 {
		t.Fatal("window")
	}
}

func TestExtractSessionID(t *testing.T) {
	id := "0b4445e1-f5be-49e1-87ce-62bbc28ad705"
	if ExtractSessionID("user_x_account__session_"+id) != id {
		t.Fatal("string form")
	}
	if ExtractSessionID(`{"device_id":"d","session_id":"`+id+`"}`) != id {
		t.Fatal("json form")
	}
	if ExtractSessionID(`{"session_id":"nope"}`) != "" || ExtractSessionID("user_x") != "" || ExtractSessionID("session_short") != "" {
		t.Fatal("invalid")
	}
}

func mustReq(t *testing.T, s string) *MessagesRequest {
	t.Helper()
	var r MessagesRequest
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestConvertRequestHistoryAndTools(t *testing.T) {
	long := strings.Repeat("mcp__very_long_server_name__", 4) + "tool"
	req := mustReq(t, `{
		"model":"claude-opus-5-5","max_tokens":100,
		"system":[{"type":"text","text":"be nice"}],
		"thinking":{"type":"enabled","budget_tokens":99999},
		"metadata":{"user_id":"u_session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},
		"tools":[{"name":"Write","description":"w","input_schema":{"type":"object","required":null}},{"name":"`+long+`","description":"x","input_schema":{}}],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"reading"},{"type":"tool_use","id":"t1","name":"Read","input":{"p":1}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"`+long+`","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"}]},{"type":"tool_result","tool_use_id":"zz","content":"orphan"},{"type":"text","text":"next"}]},
			{"role":"assistant","content":"prefill"}
		]}`)
	res, err := ConvertRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	st := res.State
	if st.ConversationID != "0b4445e1-f5be-49e1-87ce-62bbc28ad705" || st.ChatTriggerType != "MANUAL" || st.AgentTaskType != "vibe" {
		t.Fatalf("%+v", st)
	}
	cur := st.CurrentMessage.UserInputMessage
	// t1 有结果；t2 没有结果，照 IDE 补一条失败结果；孤立的 zz 丢掉。
	rs := cur.UserInputMessageContext.ToolResults
	if cur.ModelID != "claude-opus-5.5" || cur.Content != "next" || len(rs) != 2 || rs[0].ToolUseID != "t1" ||
		rs[1].ToolUseID != "t2" || rs[1].Status != "error" || rs[1].Content[0]["text"] != toolFailedText {
		t.Fatalf("current %+v", cur)
	}
	if st.AgentContinuationID == st.ConversationID || len(st.AgentContinuationID) != 36 || res.CallPosition != 0 {
		t.Fatalf("identity %s %d", st.AgentContinuationID, res.CallPosition)
	}
	// 系统提示在历史里，不进 instructions。free 账号会拒绝 instructions。
	if res.Instructions != "" || len(st.History) != 4 {
		t.Fatalf("history len %d instructions %q", len(st.History), res.Instructions)
	}
	// 思考标签不再写进系统提示，由 provider 按模型决定用 reasoning 字段还是标签。
	sys := st.History[0].User.Content
	if !strings.HasPrefix(sys, "be nice\n") || !res.HasSystem || st.History[1].Assistant.Content != "I will follow these instructions." {
		t.Fatal(sys)
	}
	if res.Thinking == nil || !res.Thinking.Enabled || res.Thinking.Prefix != "<thinking_mode>enabled</thinking_mode><max_thinking_length>24576</max_thinking_length>" {
		t.Fatalf("%+v", res.Thinking)
	}
	asst := st.History[3].Assistant
	if asst.Content != "reading" || asst.Reasoning == nil || asst.Reasoning.Text != "hmm" {
		t.Fatalf("%+v", asst)
	}
	if len(asst.ToolUses) != 2 || asst.ToolUses[0].ToolUseID != "t1" || asst.ToolUses[1].ToolUseID != "t2" {
		t.Fatalf("%+v", asst.ToolUses)
	}
	if len(res.ToolNameMap) != 1 {
		t.Fatal("long tool name must be mapped")
	}
	names := map[string]bool{}
	for _, tl := range cur.UserInputMessageContext.Tools {
		names[tl.ToolSpecification.Name] = true
		if len(tl.ToolSpecification.Name) > 64 {
			t.Fatal("tool name too long")
		}
		if tl.ToolSpecification.Name == "Write" {
			if !strings.Contains(tl.ToolSpecification.Description, "150 lines") || !strings.Contains(string(tl.ToolSpecification.InputSchema.JSON), `"required":[]`) {
				t.Fatal("write tool")
			}
		}
	}
	// 请求本身带了工具时不再给历史工具补占位定义。
	if names["Read"] {
		t.Fatal("unexpected placeholder for Read")
	}
	b, _ := Marshal(KiroBody(res))
	if strings.Contains(string(b), `\u003c`) {
		t.Fatal("html escaped")
	}
}

func TestConvertErrors(t *testing.T) {
	if _, err := ConvertRequest(mustReq(t, `{"model":"gpt-4","messages":[{"role":"user","content":"x"}]}`)); err == nil {
		t.Fatal("unsupported")
	}
	if _, err := ConvertRequest(mustReq(t, `{"model":"claude-opus-5","messages":[]}`)); err == nil {
		t.Fatal("empty")
	}
	res, err := ConvertRequest(mustReq(t, `{"model":"claude-opus-5","system":"sys","messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	h := res.State.History
	if res.Instructions != "" || len(h) != 4 || h[2].User.Content != "a" || h[3].Assistant.Content != alternationFiller {
		t.Fatalf("%+v", h)
	}
}

func TestToolNamesFollowKiro(t *testing.T) {
	n := newToolNamer()
	long := strings.Repeat("a", 70)
	if n.name("Read") != "Read" || n.name("mcp.server/tool") != "mcp_server_tool" || n.name("mcp_server_tool") != "mcp_server_tool_1" {
		t.Fatalf("%v", n.fwd)
	}
	if got := n.name(long); got != strings.Repeat("a", 64) {
		t.Fatal(got)
	}
	if got := n.name(long + "b"); got != strings.Repeat("a", 62)+"_1" {
		t.Fatal(got)
	}
	if n.rev["mcp_server_tool"] != "mcp.server/tool" || n.rev["mcp_server_tool_1"] != "mcp_server_tool" || n.name("") != "tool" {
		t.Fatalf("%v", n.rev)
	}
	if toolDescription(" ", "Read") != "Read" || toolDescription("", " ") != "tool" {
		t.Fatal("description fallback")
	}
}

func TestCallPositionAndContinuation(t *testing.T) {
	body := `{"model":"claude-opus-5","metadata":{"user_id":"u_session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},"tools":[{"name":"Read","description":"r","input_schema":{}}],"messages":[
		{"role":"user","content":"do it"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"y"}]}]}`
	a, err := ConvertRequest(mustReq(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if a.CallPosition != 2 {
		t.Fatal(a.CallPosition)
	}
	// 同一轮里 continuation id 不变，新一轮换新的。
	first, _ := ConvertRequest(mustReq(t, `{"model":"claude-opus-5","metadata":{"user_id":"u_session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},"messages":[{"role":"user","content":"do it"}]}`))
	next, _ := ConvertRequest(mustReq(t, strings.Replace(body, `"content":"y"}]}]}`, `"content":"y"},{"type":"text","text":"again"}]}]}`, 1)))
	if first.State.AgentContinuationID != a.State.AgentContinuationID || next.State.AgentContinuationID == a.State.AgentContinuationID || next.CallPosition != 0 {
		t.Fatal(first.State.AgentContinuationID, a.State.AgentContinuationID, next.State.AgentContinuationID, next.CallPosition)
	}
}
