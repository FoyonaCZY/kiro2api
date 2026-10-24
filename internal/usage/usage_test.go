package usage

import (
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }

func sample(model string, status int, cred uint64) NewRequest {
	return NewRequest{CredentialID: cred, Model: model, Status: status, InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 2_000_000, CacheWrite5mTokens: 400_000, Account: "a@x"}
}

func TestChargeAndSummary(t *testing.T) {
	l := Open("")
	p, err := l.UpsertPrice(ModelPrice{Name: "Opus", Aliases: []string{" Claude-Opus-5-5 ", "claude-opus-5-5"}, InputPerM: 5, OutputPerM: 25})
	if err != nil || len(p.Aliases) != 1 {
		t.Fatal(err, p)
	}
	if _, err := l.UpsertPrice(ModelPrice{Name: "dup", Aliases: []string{"claude-opus-5-5"}, InputPerM: 1}); err == nil {
		t.Fatal("alias conflict")
	}
	c := Charge(l.Prices(), "CLAUDE-OPUS-5-5", Tokens{Input: 1_000_000, Output: 100_000, CacheRead: 2_000_000, CacheWrite5m: 400_000})
	// 5 + 2.5 + 2*0.5 + 0.4*6.25
	if c == nil || *c != 5+2.5+1+2.5 {
		t.Fatalf("%v", c)
	}
	l.Record(sample("claude-opus-5-5", 200, 1))
	l.Record(sample("claude-opus-5-5", 502, 1))
	l.Record(sample("unknown", 200, 0))
	s := l.Summary()
	if s.Requests != 3 || s.Errors != 1 || s.UnpricedRequests != 1 || s.InputTokens != 2_000_000 {
		t.Fatalf("%+v", s)
	}
	if s.CostUSD != 11 {
		t.Fatalf("failed requests must not be billed: %v", s.CostUSD)
	}
	list := l.List(10)
	if len(list) != 3 || list[1].Status != 502 || list[1].CostUSD == nil || *list[1].CostUSD != 0 {
		t.Fatalf("%+v", list)
	}
	if d := l.Get(list[0].ID); d == nil || d.Model != "unknown" {
		t.Fatal("get")
	}
}

func TestAccountReportRepricesAndKeepsRemoved(t *testing.T) {
	l := Open("")
	p, _ := l.UpsertPrice(ModelPrice{Name: "S", Aliases: []string{"m"}, InputPerM: 1, OutputPerM: 10})
	l.Record(NewRequest{CredentialID: 1, Model: "m", Status: 200, InputTokens: 1_000_000, OutputTokens: 100_000})
	l.Record(NewRequest{CredentialID: 1, Model: "m", Status: 502, InputTokens: 9_000_000})
	l.Record(NewRequest{CredentialID: 9, Model: "m", Status: 200, InputTokens: 1_000_000})
	if err := l.SetAccountCost(1, f(4)); err != nil {
		t.Fatal(err)
	}
	r := l.AccountReport([]Label{{1, "a@x"}, {2, "b@x"}})
	if len(r.Accounts) != 3 || r.Accounts[0].UsageUSD != 2 || *r.Accounts[0].CostRatio != 2 || r.Accounts[0].Errors != 1 {
		t.Fatalf("%+v", r.Accounts)
	}
	if !r.Accounts[2].Removed || r.Accounts[2].Label != "#9" {
		t.Fatal("removed credential")
	}
	p.InputPerM = 2
	l.UpsertPrice(p)
	r = l.AccountReport([]Label{{1, "a@x"}})
	if r.Accounts[0].UsageUSD != 3 || *r.CostRatio != 4.0/3 {
		t.Fatalf("reprice: %+v", r)
	}
	if err := l.SetAccountCost(1, f(-1)); err == nil {
		t.Fatal("negative")
	}
	l.SetAccountCost(1, nil)
	if l.AccountReport(nil).CostedAccounts != 0 {
		t.Fatal("clear")
	}
}

func TestCreditsRecorded(t *testing.T) {
	l := Open("")
	l.Record(NewRequest{CredentialID: 1, Model: "claude-opus-5.5", Status: 200, Credits: f(0.25)})
	// 失败请求上游照样扣了 credits，照记。
	l.Record(NewRequest{CredentialID: 1, Model: "claude-opus-5.5", Status: 502, Credits: f(0.5)})
	l.Record(NewRequest{CredentialID: 2, Model: "claude-sonnet-5", Status: 200})
	l.Record(NewRequest{CredentialID: 2, Model: "claude-sonnet-5", Status: 200, Credits: f(-1)})
	list := l.List(10)
	if list[0].Credits != nil || list[1].Credits != nil || list[2].Credits == nil || *list[2].Credits != 0.5 {
		t.Fatalf("%+v", list)
	}
	if d := l.Get(list[3].ID); d == nil || d.Credits == nil || *d.Credits != 0.25 {
		t.Fatalf("%+v", d)
	}
	s := l.Summary()
	if s.Credits != 0.75 || s.ByModel[0].Credits+s.ByModel[1].Credits != 0.75 {
		t.Fatalf("%+v", s)
	}
	r := l.AccountReport([]Label{{1, "a"}, {2, "b"}})
	if r.Credits != 0.75 || r.Accounts[0].Credits != 0.75 || r.Accounts[1].Credits != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCreditsMigratesOldSchema(t *testing.T) {
	path := t.TempDir() + "/usage.sqlite"
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE requests (id INTEGER PRIMARY KEY, time TEXT NOT NULL, model TEXT NOT NULL, stream INTEGER NOT NULL, status INTEGER NOT NULL, duration_ms INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, error TEXT)`,
		`CREATE TABLE model_totals (model TEXT PRIMARY KEY, requests INTEGER NOT NULL, errors INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL)`,
		`CREATE TABLE account_totals (credential_id INTEGER NOT NULL, model TEXT NOT NULL, requests INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0, cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (credential_id, model))`,
		`INSERT INTO requests(time, model, stream, status, duration_ms, input_tokens, output_tokens) VALUES ('t', 'm', 0, 200, 1, 1, 1)`,
		`INSERT INTO model_totals VALUES ('m', 1, 0, 1, 1)`,
		`INSERT INTO account_totals(credential_id, model, requests) VALUES (1, 'm', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	l := Open(path)
	defer l.db.Close()
	l.Record(NewRequest{CredentialID: 1, Model: "m", Status: 200, Credits: f(0.5)})
	list := l.List(10)
	if len(list) != 2 || list[1].Credits != nil || list[0].Credits == nil {
		t.Fatalf("%+v", list)
	}
	if s := l.Summary(); s.Requests != 2 || s.Credits != 0.5 {
		t.Fatalf("%+v", s)
	}
	if r := l.AccountReport([]Label{{1, "a"}}); r.Accounts[0].Requests != 2 || r.Accounts[0].Credits != 0.5 {
		t.Fatalf("%+v", r)
	}
}

func TestBackfillAndPrune(t *testing.T) {
	l := openWithRetain("", 3)
	l.UpsertPrice(ModelPrice{Name: "S", Aliases: []string{"m"}, InputPerM: 1})
	l.Record(NewRequest{Model: "m", Status: 200, InputTokens: 1_000_000, Account: "a@x"})
	l.Record(NewRequest{Model: "m", Status: 200, InputTokens: 1_000_000, Account: "#7"})
	if n := l.BackfillAccounts(map[string]uint64{"a@x": 3}); n != 2 {
		t.Fatalf("backfill %d", n)
	}
	if n := l.BackfillAccounts(map[string]uint64{"a@x": 3}); n != 0 {
		t.Fatal("backfill must be idempotent")
	}
	for i := 0; i < 5; i++ {
		l.Record(NewRequest{CredentialID: 3, Model: "m", Status: 200, InputTokens: 1_000_000})
	}
	if len(l.List(100)) != 3 {
		t.Fatal("prune")
	}
	r := l.AccountReport([]Label{{3, "a"}, {7, "b"}})
	if r.Accounts[0].UsageUSD != 6 || r.Accounts[1].UsageUSD != 1 {
		t.Fatalf("totals survive prune: %+v", r.Accounts)
	}
}

func TestClipAndProfile(t *testing.T) {
	if got := Clip("你好世界", 4); got != "你\n…[truncated 9 bytes]" {
		t.Fatal(got)
	}
	body := `{"conversationState":{"history":[{"userInputMessage":{"content":"` + strings.Repeat("x", 3000) + `"}}],"currentMessage":{"userInputMessage":{"content":"hi"}}}}`
	p := PayloadProfile(body)
	if !strings.Contains(p, "history=1") || !strings.Contains(p, "largest conversationState.history[0].userInputMessage.content 3000") || strings.Contains(p, "xxxx") {
		t.Fatal(p)
	}
	if PayloadProfile("nope") != "bytes=4 json=invalid" {
		t.Fatal("invalid")
	}
}

func TestPayloadProfileResponses(t *testing.T) {
	body := `{"model":"m","input":[{"role":"user","content":"` + strings.Repeat("y", 2500) + `"},{"type":"function_call","call_id":"t","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"t","output":"ok"},{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAIAAAA="}]}],"tools":[{"type":"function","name":"Read"}]}`
	p := PayloadProfile(body)
	if !strings.Contains(p, "input=4 messages=2 images=1") || !strings.Contains(p, "tools=1 function_calls=1 function_outputs=1") ||
		!strings.Contains(p, "i0 user content=2500") || strings.Contains(p, "yyyy") {
		t.Fatal(p)
	}
}
