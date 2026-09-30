package kiro

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/config"
)

// 上游 5xx 的 Claude 账号在同一请求里只试一次、不被禁用，并切到下一个账号。
func TestClaudeFailoverDoesNotDisable(t *testing.T) {
	var badHits, goodHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.WriteHeader(504)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&goodHits, 1)
		if r.Header.Get("x-api-key") != "k2" || r.Header.Get("anthropic-version") != "2023-06-01" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		w.Write(b)
	}))
	defer good.Close()
	creds := []*Credentials{
		{ID: 1, Priority: 0, AuthMethod: "claude_api", ClaudeBaseURL: bad.URL, ClaudeAPIKey: "k1"},
		{ID: 2, Priority: 1, AuthMethod: "claude_api", ClaudeBaseURL: good.URL, ClaudeAPIKey: "k2"},
	}
	m, err := NewManager(config.Default(), creds, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProvider(m, Endpoints(), "krs")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	up, err := p.CallMessages(ctx, nil, &ClaudeForward{Body: []byte(`{"model":"claude-opus-5-5","messages":[]}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(up.Resp.Body)
	up.Resp.Body.Close()
	if !up.Passthrough || up.Target.CredentialID != 2 || string(body) != `{"model":"claude-opus-5-5","messages":[]}` {
		t.Fatalf("%+v %s", up.Target, body)
	}
	if badHits != 1 {
		t.Fatalf("bad upstream hit %d times, want 1", badHits)
	}
	s := m.Snapshot()
	if s.Entries[0].Disabled || s.Entries[0].FailureCount != 0 {
		t.Fatalf("5xx must not count toward disable: %+v", s.Entries[0])
	}

	// 只剩坏账号时一次就返回错误，不打三遍。
	m2, _ := NewManager(config.Default(), creds[:1], nil, "")
	atomic.StoreInt32(&badHits, 0)
	if _, err := NewProvider(m2, Endpoints(), "krs").CallMessages(ctx, nil, &ClaudeForward{Body: []byte(`{}`)}, false); err == nil {
		t.Fatal("want error")
	}
	if badHits != 1 {
		t.Fatalf("hit %d times", badHits)
	}
}

func TestRateLimitDelay(t *testing.T) {
	h := http.Header{}
	h.Set("x-amzn-kiro-ratelimit-retry-after", "2500")
	if RateLimitDelay(h, time.Minute) != 2500*time.Millisecond {
		t.Fatal("kiro header")
	}
	h = http.Header{}
	h.Set("Retry-After", "7")
	if RateLimitDelay(h, time.Minute) != 7*time.Second {
		t.Fatal("retry-after")
	}
	if RateLimitDelay(http.Header{}, 0) != time.Second {
		t.Fatal("clamp")
	}
}

func TestEndpointNames(t *testing.T) {
	if EffectiveEndpointName("", "ide", false) != "krs" || EffectiveEndpointName("ide", "krs", false) != "ide" ||
		EffectiveEndpointName("", "krs", true) != "ide" || EffectiveEndpointName("krs", "krs", true) != "ide" {
		t.Fatal("endpoint names")
	}
	if got := InjectProfileArn(`{"conversationState":{"conversationId":"c1"},"profileArn":"old"}`, "new"); got != `{"conversationState":{"conversationId":"c1"},"profileArn":"new"}` {
		t.Fatal(got)
	}
	if InjectProfileArn("not-json", "a") != "not-json" {
		t.Fatal("invalid json")
	}
}
