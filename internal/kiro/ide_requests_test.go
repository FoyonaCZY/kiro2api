package kiro

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
)

// 下面的期望值都是用 Kiro 1.4.26 自己的客户端代码对着本地抓包服务器发请求得到的原文。
const (
	capMID     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	capProfile = "arn:aws:codewhisperer:us-east-1:000000000000:profile/TESTPROFILE0"
)

func capContext(authMethod string) *RequestContext {
	return &RequestContext{Cred: &Credentials{ProfileArn: capProfile, AuthMethod: authMethod}, Token: "TOKEN", MachineID: capMID, Config: config.Default()}
}

// headersWithout 去掉每次都会变的 amz-sdk-invocation-id。
func headersWithout(r *httpx.IDERequest) [][2]string {
	var out [][2]string
	for _, h := range r.Header {
		if h[0] != "amz-sdk-invocation-id" {
			out = append(out, h)
		}
	}
	return out
}

func assertHeaders(t *testing.T, name string, r *httpx.IDERequest, want [][2]string) {
	t.Helper()
	if got := headersWithout(r); !reflect.DeepEqual(got, want) {
		var g, w strings.Builder
		for _, h := range got {
			g.WriteString(h[0] + ": " + h[1] + "\n")
		}
		for _, h := range want {
			w.WriteString(h[0] + ": " + h[1] + "\n")
		}
		t.Fatalf("%s 头不一致\n--- got\n%s--- want\n%s", name, g.String(), w.String())
	}
}

func TestResponsesRequestMatchesIDE(t *testing.T) {
	body := []byte(`{}`)
	r := ResponsesRequest(capContext("social"), body, 1, 3)
	if r.Method != "POST" || r.URL != "https://runtime.us-east-1.kiro.dev/v1/responses" {
		t.Fatal(r.Method, r.URL)
	}
	assertHeaders(t, "CreateResponse", r, [][2]string{
		{"Content-Type", "application/json"},
		{"Authorization", "Bearer TOKEN"},
		{"x-amzn-kiro-origin", "AI_EDITOR"},
		{"x-amzn-codewhisperer-optout", "true"},
		{"x-amzn-kiro-profile", capProfile},
		{"User-Agent", "KiroIDE 1.4.26 " + capMID + " KAS/0.66.56 acp-client/kiro-ide"},
		{"x-kiro-attempt", "1;max=3"},
		{"Content-Length", "2"},
		{"Host", "runtime.us-east-1.kiro.dev"},
		{"Connection", "keep-alive"},
	})
	idc := ResponsesRequest(capContext("idc"), body, 2, 4)
	if idc.Get("TokenType") != "SSO_OIDC" || idc.Get("x-kiro-attempt") != "2;max=4" {
		t.Fatal(idc.Header)
	}
}

func TestMCPRequestMatchesIDE(t *testing.T) {
	r, err := MCPRequest(capContext("social"), []byte(`{"id":"tooluse_abc","method":"tools/call","jsonrpc":"2.0","params":{"name":"web_search","arguments":{"query":"q"}}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := `{"id":"tooluse_abc","method":"tools/call","profileArn":"` + capProfile + `","jsonrpc":"2.0","params":{"name":"web_search","arguments":{"query":"q"}}}`
	if string(r.Body) != wantBody || r.URL != "https://runtime.us-east-1.kiro.dev/" {
		t.Fatalf("%s %s", r.URL, r.Body)
	}
	custom := "KiroIDE-1.4.26-" + capMID + "-KAS/0.66.56 acp-client/kiro-ide"
	assertHeaders(t, "InvokeMCP", r, [][2]string{
		{"content-type", "application/x-amz-json-1.0"},
		{"x-amz-target", "KiroRuntimeService.InvokeMCP"},
		{"content-length", "196"},
		{"x-amz-user-agent", "aws-sdk-js/1.0.0 " + custom},
		{"user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/win32#10.0.26200 lang/js md/nodejs#24.21.0 api/kiroruntime#1.0.0 m/N,E " + custom},
		{"host", "runtime.us-east-1.kiro.dev"},
		{"amz-sdk-request", "attempt=1; max=3"},
		{"Authorization", "Bearer TOKEN"},
		{"Connection", "keep-alive"},
	})
	idc, _ := MCPRequest(capContext("idc"), []byte(`{"id":"x","method":"tools/list","jsonrpc":"2.0"}`), 1)
	if idc.Header[3] != [2]string{"TokenType", "SSO_OIDC"} {
		t.Fatalf("TokenType 位置不对: %v", idc.Header)
	}
}

func TestListModelsRequestMatchesIDE(t *testing.T) {
	r := ListModelsRequest(capContext("social"), "")
	if string(r.Body) != `{"origin":"AI_EDITOR","profileArn":"`+capProfile+`"}` || r.URL != "https://management.us-east-1.kiro.dev/" {
		t.Fatalf("%s %s", r.URL, r.Body)
	}
	custom := "KiroIDE-1.4.26-" + capMID + "-KAS/0.66.56 acp-client/kiro-ide"
	assertHeaders(t, "ListAvailableModels", r, [][2]string{
		{"content-type", "application/x-amz-json-1.0"},
		{"x-amz-target", "KiroControlPlaneBearerService.ListAvailableModels"},
		{"content-length", "103"},
		{"x-amz-user-agent", "aws-sdk-js/1.0.0 " + custom},
		{"user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/win32#10.0.26200 lang/js md/nodejs#24.21.0 api/kirocontrolplanebearer#1.0.0 m/N,E " + custom},
		{"host", "management.us-east-1.kiro.dev"},
		{"amz-sdk-request", "attempt=1; max=3"},
		{"Authorization", "Bearer TOKEN"},
		{"Connection", "keep-alive"},
	})
}

func TestUsageLimitsRequestMatchesIDE(t *testing.T) {
	r := UsageLimitsRequest(capContext("social"))
	wantURL := "https://management.us-east-1.kiro.dev/getUsageLimits?origin=AI_EDITOR&profileArn=arn%3Aaws%3Acodewhisperer%3Aus-east-1%3A000000000000%3Aprofile%2FTESTPROFILE0&resourceType=AGENTIC_REQUEST"
	if r.Method != "GET" || r.URL != wantURL || len(r.Body) != 0 {
		t.Fatal(r.Method, r.URL)
	}
	custom := "KiroIDE-1.4.26-" + capMID
	assertHeaders(t, "GetUsageLimits", r, [][2]string{
		{"x-amz-user-agent", "aws-sdk-js/1.0.0 " + custom},
		{"user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/win32#10.0.26200 lang/js md/nodejs#24.21.0 api/codewhispererruntime#1.0.0 m/N,E " + custom},
		{"host", "management.us-east-1.kiro.dev"},
		{"amz-sdk-request", "attempt=1; max=3"},
		{"Authorization", "Bearer TOKEN"},
		{"Connection", "keep-alive"},
	})
	// 扩展侧额度客户端对 IdC 不发 TokenType，对 API Key 发。
	if UsageLimitsRequest(capContext("idc")).Get("TokenType") != "" || UsageLimitsRequest(capContext("api_key")).Header[0] != [2]string{"TokenType", "API_KEY"} {
		t.Fatal("usage TokenType")
	}
}

func TestAuthRequestsMatchIDE(t *testing.T) {
	cfg := config.Default()
	body := []byte(`{"clientId":"cid","clientSecret":"csecret","grantType":"refresh_token","refreshToken":"rtok"}`)
	r := IdCTokenRequest(cfg, "us-east-1", body)
	if r.URL != "https://oidc.us-east-1.amazonaws.com/token" {
		t.Fatal(r.URL)
	}
	assertHeaders(t, "SSO OIDC CreateToken", r, [][2]string{
		{"content-type", "application/json"},
		{"content-length", "93"},
		{"x-amz-user-agent", "aws-sdk-js/3.980.0 KiroIDE"},
		{"user-agent", "aws-sdk-js/3.980.0 ua/2.1 os/win32#10.0.26200 lang/js md/nodejs#24.21.0 api/sso-oidc#3.980.0 m/N,E KiroIDE"},
		{"host", "oidc.us-east-1.amazonaws.com"},
		{"amz-sdk-request", "attempt=1; max=4"},
		{"Connection", "keep-alive"},
	})
	s := SocialAuthRequest(cfg, capMID, "/refreshToken", []byte(`{"refreshToken":"rtok"}`))
	if s.URL != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
		t.Fatal(s.URL)
	}
	assertHeaders(t, "Social refresh", s, [][2]string{
		{"Accept", "application/json, text/plain, */*"},
		{"Content-Type", "application/json"},
		{"User-Agent", "KiroIDE-1.4.26-" + capMID},
		{"Content-Length", "23"},
		{"Accept-Encoding", "gzip, compress, deflate, br"},
		{"Host", "prod.us-east-1.auth.desktop.kiro.dev"},
		{"Connection", "keep-alive"},
	})
}

func TestResponsesBodyMatchesIDE(t *testing.T) {
	req := KiroRequest{
		ProfileArn: capProfile,
		Thinking:   &ThinkingRequest{Enabled: true, Effort: "high", Prefix: "<thinking_mode>adaptive</thinking_mode>"},
		ConversationState: ConversationState{
			ConversationID:      "11111111-1111-1111-1111-111111111111",
			AgentContinuationID: "22222222-2222-2222-2222-222222222222",
			AgentTaskType:       "vibe",
			CurrentMessage: CurrentMessage{UserInputMessage: UserInputMessage{
				Content: "hi", ModelID: "claude-sonnet-4.5", Origin: OriginIDE,
			}},
		},
	}
	raw, _ := MarshalNoEscape(req)
	model := &ModelInfo{ThinkingToggleable: true, EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}, DefaultEffort: "high"}
	out, err := ResponsesFromKiro(string(raw), "vibe", model)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-sonnet-4.5","input":[{"role":"user","content":"hi"}],"stream":true,"origin":"AI_EDITOR","profileArn":"` + capProfile + `",` +
		`"metadata":{"kiro:agent_mode":"vibe","kiro:conversation_id":"11111111-1111-1111-1111-111111111111","kiro:root_conversation_id":"11111111-1111-1111-1111-111111111111","kiro:agent_continuation_id":"22222222-2222-2222-2222-222222222222"},` +
		`"reasoning":{"type":"adaptive","effort":"high"}}`
	if out != want {
		t.Fatalf("\n got %s\nwant %s", out, want)
	}
	// 模型不支持原生思考时退回标签：补一对系统消息，不发 reasoning。
	out, _ = ResponsesFromKiro(string(raw), "vibe", nil)
	if strings.Contains(out, `"reasoning"`) || !strings.HasPrefix(out, `{"model":"claude-sonnet-4.5","input":[{"role":"user","content":"<thinking_mode>adaptive</thinking_mode>"},{"role":"assistant","content":"I will follow these instructions."},`) {
		t.Fatal(out)
	}
}

func TestReasoningReplay(t *testing.T) {
	req := KiroRequest{ConversationState: ConversationState{
		CurrentMessage: CurrentMessage{UserInputMessage: UserInputMessage{Content: "next", ModelID: "m"}},
		History: []HistoryMessage{
			{User: &UserMessage{Content: "q"}},
			{Assistant: &AssistantMessage{Content: "a", Reasoning: &ReasoningContent{Text: "think", Signature: "sig"},
				ToolUses: []ToolUseEntry{{ToolUseID: "t1", Name: "Read", Input: json.RawMessage("{ \"p\" : 1 }")}}}},
		},
	}}
	raw, _ := MarshalNoEscape(req)
	native, _ := ResponsesFromKiro(string(raw), "vibe", &ModelInfo{ThinkingToggleable: true})
	if !strings.Contains(native, `{"type":"reasoning","summary":[],"encrypted_content":"{\"reasoningText\":{\"text\":\"think\",\"signature\":\"sig\"}}"},{"role":"assistant","content":"a"},{"type":"function_call","call_id":"t1","name":"Read","arguments":"{\"p\":1}"}`) {
		t.Fatal(native)
	}
	tags, _ := ResponsesFromKiro(string(raw), "vibe", nil)
	if !strings.Contains(tags, `{"role":"assistant","content":"<thinking>think</thinking>\n\na"}`) || strings.Contains(tags, `"reasoning"`) {
		t.Fatal(tags)
	}
}

func TestModelReasoningRules(t *testing.T) {
	m := &ModelInfo{ThinkingToggleable: true, EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}, DefaultEffort: "high"}
	if r := m.Reasoning(&ThinkingRequest{Enabled: true, Effort: "max"}); *r != (Reasoning{"adaptive", "max"}) {
		t.Fatal(r)
	}
	// 关掉思考时 xhigh/max 不可用，退回默认 high。
	if r := m.Reasoning(&ThinkingRequest{Effort: "max"}); *r != (Reasoning{"disabled", "high"}) {
		t.Fatal(r)
	}
	if r := m.Reasoning(nil); *r != (Reasoning{"disabled", "high"}) {
		t.Fatal(r)
	}
	noDefault := &ModelInfo{EffortLevels: []string{"low", "high"}}
	if r := noDefault.Reasoning(nil); *r != (Reasoning{"", "low"}) {
		t.Fatal(r)
	}
	if (&ModelInfo{}).Native() || (*ModelInfo)(nil).Reasoning(nil) != nil {
		t.Fatal("no schema")
	}
	var raw rawModel
	_ = json.Unmarshal([]byte(`{"modelId":"x","additionalModelRequestFieldsSchema":{"properties":{"thinking":{"properties":{"type":{"enum":["disabled","adaptive"],"default":"adaptive"}}},"output_config":{"properties":{"effort":{"enum":["low","high"],"default":"high"}}}}}}`), &raw)
	if p := parseModel(raw); !p.ThinkingToggleable || !p.DefaultThinkingEnabled || p.DefaultEffort != "high" || len(p.EffortLevels) != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestRegionFromProfileArn(t *testing.T) {
	cfg := config.Default()
	cfg.Region = "eu-central-1"
	c := &Credentials{ProfileArn: capProfile}
	if RuntimeHost(c, cfg) != "runtime.us-east-1.kiro.dev" || ManagementHost(c, cfg) != "management.us-east-1.kiro.dev" {
		t.Fatal(RuntimeHost(c, cfg))
	}
	c.APIRegion = "eu-central-1"
	if RuntimeHost(c, cfg) != "runtime.eu-central-1.kiro.dev" {
		t.Fatal("apiRegion override")
	}
	if (&Credentials{}).EffectiveAPIRegion(cfg) != "eu-central-1" || (&Credentials{}).EffectiveAPIRegion(config.Default()) != "us-east-1" {
		t.Fatal("fallback")
	}
}

func decodeAll(t *testing.T, lines ...string) []*Event {
	t.Helper()
	var d SSEDecoder
	if err := d.Feed([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	var out []*Event
	for {
		ev, err := d.NextEvent()
		if err != nil {
			t.Fatal(err)
		}
		if ev == nil {
			return out
		}
		out = append(out, ev)
	}
}

func TestSSEMatchesIDEEdgeCases(t *testing.T) {
	// 拒答已经流过 delta，done 不再重复。
	evs := decodeAll(t,
		`data: {"type":"response.refusal.delta","delta":"no"}`,
		`data: {"type":"response.refusal.done","refusal":"no"}`)
	if len(evs) != 1 || evs[0].Content != "no" {
		t.Fatalf("%+v", evs)
	}
	// 截断：给出 MAX_TOKENS，未完成的工具调用不补 stop。
	evs = decodeAll(t,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"t1","name":"Write","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"a\":"}`,
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"kiro:credits":{"value":0.5}}}}`)
	var stop string
	var credits float64
	for _, e := range evs {
		if e.Kind == EvToolUse && e.ToolStop {
			t.Fatal("incomplete tool call must not be stopped")
		}
		if e.Kind == EvStop {
			stop = e.StopReason
		}
		if e.Kind == EvMetering {
			credits = e.Credits
		}
	}
	if stop != "MAX_TOKENS" || credits != 0.5 {
		t.Fatalf("%q %v", stop, credits)
	}
	// 原生思考的签名和流中途错误。
	evs = decodeAll(t,
		`data: {"type":"response.reasoning_text.delta","delta":"hm"}`,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"{\"reasoningText\":{\"text\":\"hm\",\"signature\":\"SIG\"}}"}}`,
		`data: {"type":"response.failed","response":{"error":{"type":"too_many_requests","code":"rate_limit_exceeded","message":"slow"}}}`)
	if len(evs) != 3 || evs[1].Kind != EvReasoningSignature || evs[1].Signature != "SIG" || evs[2].Kind != EvError || evs[2].Code != "rate_limit_exceeded" || evs[2].ErrorType != "too_many_requests" {
		t.Fatalf("%+v %+v", evs[1], evs[len(evs)-1])
	}
}
