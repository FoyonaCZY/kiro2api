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
