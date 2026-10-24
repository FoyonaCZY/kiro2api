package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/FoyonaCZY/kiro2api/internal/usage"
)

// localEndpoint 把 krs 指到本地假上游。
type localEndpoint struct {
	kiro.KRS
	url string
}

func (e localEndpoint) APIURL(*kiro.RequestContext) string { return e.url }

func fakeKiro(t *testing.T, gotBody *[]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Host != "runtime.eu-central-1.kiro.dev" {
			w.WriteHeader(403)
			return
		}
		*gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"<thinking>\\nplan</thinking>\\n\\n\"}\n\n"+
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n"+
			"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"t1\",\"name\":\"Read\",\"arguments\":\"\"}}\n\n"+
			"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"p\\\":\"}\n\n"+
			"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"1}\"}\n\n"+
			"data: {\"type\":\"response.function_call_arguments.done\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"kiro:context_usage_percentage\":0.5}}}\n\n")
	}))
}

func newTestServer(t *testing.T, upstream string) (*httptest.Server, *usage.Log) {
	cfg := config.Default()
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	m, err := kiro.NewManager(cfg, []*kiro.Credentials{{ID: 1, AuthMethod: "social", AccessToken: "tok", ExpiresAt: exp, Email: "a@x", ProfileArn: "arn:p"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eps := kiro.Endpoints()
	eps["krs"] = localEndpoint{url: upstream}
	ul := usage.Open("")
	acc := access.Open("")
	acc.ImportInitialKey("sk-test")
	s := &Server{APIKey: "sk-test", Provider: kiro.NewProvider(m, eps, "krs"), ExtractThinking: true, Usage: ul, Access: acc}
	mux := http.NewServeMux()
	s.Routes(mux)
	return httptest.NewServer(CORS(mux)), ul
}

const reqBody = `{"model":"claude-opus-5-5","max_tokens":100,"stream":%s,"thinking":{"type":"enabled","budget_tokens":1000},"tools":[{"name":"Read","description":"r","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`

func post(t *testing.T, url, body, key string) *http.Response {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEndToEndMessages(t *testing.T) {
	var got []byte
	up := fakeKiro(t, &got)
	defer up.Close()
	srv, ul := newTestServer(t, up.URL)
	defer srv.Close()

	// 鉴权
	if r := post(t, srv.URL+"/v1/messages", "{}", "wrong"); r.StatusCode != 401 {
		t.Fatalf("auth %d", r.StatusCode)
	}

	// 非流式
	r := post(t, srv.URL+"/v1/messages", strings.Replace(reqBody, "%s", "false", 1), "sk-test")
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var msg map[string]any
	json.Unmarshal(b, &msg)
	content := msg["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["thinking"] != "plan" || content[1].(map[string]any)["text"] != "Hello" ||
		content[2].(map[string]any)["input"].(map[string]any)["p"] != float64(1) || msg["stop_reason"] != "tool_use" {
		t.Fatalf("%s", b)
	}
	if msg["usage"].(map[string]any)["input_tokens"] != float64(5000) {
		t.Fatalf("context usage input: %v", msg["usage"])
	}
	var kr map[string]any
	json.Unmarshal(got, &kr)
	if kr["profileArn"] != "arn:p" || kr["model"] != "claude-opus-5.5" || kr["origin"] != "AI_EDITOR" || kr["stream"] != true || strings.Contains(string(got), `\u003c`) {
		t.Fatalf("outbound body %s", got)
	}

	// 流式
	r = post(t, srv.URL+"/v1/messages", strings.Replace(reqBody, "%s", "true", 1), "sk-test")
	b, _ = io.ReadAll(r.Body)
	s := string(b)
	if r.Header.Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(s, "event: message_start") ||
		!strings.Contains(s, `"thinking":"plan"`) || !strings.Contains(s, `"text":"Hello"`) ||
		!strings.Contains(s, `"partial_json":"1}"`) || !strings.Contains(s, `"stop_reason":"tool_use"`) || !strings.HasSuffix(s, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("%s", s)
	}

	// /cc 缓冲：message_start 带 contextUsage 换算的准确 input
	r = post(t, srv.URL+"/cc/v1/messages", strings.Replace(reqBody, "%s", "true", 1), "sk-test")
	b, _ = io.ReadAll(r.Body)
	s = string(b)
	if !strings.HasPrefix(s, "event: ping") || !strings.Contains(s, `"usage":{"input_tokens":5000,"output_tokens":1}`) {
		t.Fatalf("%s", s)
	}

	sum := ul.Summary()
	if sum.Requests != 3 || sum.Errors != 0 {
		t.Fatalf("%+v", sum)
	}
	list := ul.List(5)
	if list[0].Account != "a@x" || list[0].Endpoint != "krs" {
		t.Fatalf("%+v", list[0])
	}
	d := ul.Get(list[0].ID)
	if strings.Contains(d.InboundHeaders, "sk-test") || strings.Contains(d.OutboundHeaders, "tok\"") {
		t.Fatalf("secrets leaked: %s %s", d.InboundHeaders, d.OutboundHeaders)
	}

	// 不支持的模型 → 400
	r = post(t, srv.URL+"/v1/messages", `{"model":"gpt-4","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`, "sk-test")
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	// count_tokens / models
	r = post(t, srv.URL+"/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, "sk-test")
	b, _ = io.ReadAll(r.Body)
	if !strings.Contains(string(b), `"input_tokens":`) {
		t.Fatal(string(b))
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	mr, _ := http.DefaultClient.Do(req)
	b, _ = io.ReadAll(mr.Body)
	if !strings.Contains(string(b), `"claude-opus-5-5"`) || !strings.Contains(string(b), `"claude-opus-5-thinking"`) {
		t.Fatal(string(b))
	}
}
