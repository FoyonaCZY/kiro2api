package kiro

import (
	"strings"
	"testing"

	"github.com/FoyonaCZY/kiro2api/internal/config"
)

func TestStoreRoundTrip(t *testing.T) {
	s, err := OpenMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsEmpty() {
		t.Fatal("new store should be empty")
	}
	a := &Credentials{ID: 5, Email: "b@x", ProxyURL: "socks5://gate.example:1000", ProxyUsername: "u", ProxyPassword: "p-5", RefreshToken: "rt-5"}
	b := &Credentials{ID: 2, Email: "a@x"}
	if err := s.Save([]*Credentials{a, b}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 5 || got[1].ID != 2 || got[0].ProxyPassword != "p-5" || got[0].RefreshToken != "rt-5" {
		t.Fatalf("unexpected %+v", got)
	}
	if err := s.Save([]*Credentials{{Email: "x"}}); err == nil {
		t.Fatal("missing id must fail")
	}
	got, _ = s.Load()
	if len(got) != 2 {
		t.Fatal("failed save must not change table")
	}
}

func TestParseProxyLine(t *testing.T) {
	cases := map[string]ParsedProxy{
		"":                           {},
		"direct":                     {URL: "direct"},
		"socks5://gate.example:1000": {URL: "socks5://gate.example:1000"},
		"socks5://user:p%3Aass@gate.example:1000": {URL: "socks5://gate.example:1000", Username: "user", Password: "p%3Aass"},
		"socks5://gate.example:1000:user-a:sec:":  {URL: "socks5://gate.example:1000", Username: "user-a", Password: "sec"},
		"gate.example:1000:u:p":                   {URL: "socks5://gate.example:1000", Username: "u", Password: "p"},
	}
	for in, want := range cases {
		got, err := ParseProxyLine(in)
		if err != nil || got != want {
			t.Errorf("%q => %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"socks5://gate.example:0", "ftp://h:1", "h"} {
		if _, err := ParseProxyLine(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestMachineID(t *testing.T) {
	n, ok := NormalizeMachineID("2582956e-cc88-4669-b546-07adbffcb894")
	if !ok || n != "2582956ecc884669b54607adbffcb8942582956ecc884669b54607adbffcb894" {
		t.Fatal(n)
	}
	if _, ok := NormalizeMachineID(strings.Repeat("g", 64)); ok {
		t.Fatal("non hex accepted")
	}
	if sha256Hex("test") != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatal("sha")
	}
	cfg := config.Default()
	cfg.MachineID = strings.Repeat("a", 64)
	c := &Credentials{MachineID: strings.Repeat("b", 64)}
	if MachineIDFor(c, cfg) != strings.Repeat("b", 64) {
		t.Fatal("credential id must win")
	}
	x := &Credentials{ID: 1 << 60}
	if MachineIDFor(x, config.Default()) != MachineIDFor(x, config.Default()) {
		t.Fatal("fallback must be stable")
	}
	ua := SDKUserAgent(config.Default(), strings.Repeat("ab", 32), "codewhispererstreaming")
	if !strings.Contains(ua, "os/win32#10.0.26200") || !strings.Contains(ua, "KiroIDE 1.4.26 ") || strings.Contains(ua, "x64") {
		t.Fatal(ua)
	}
	rt := RuntimeUserAgent(config.Default(), strings.Repeat("ab", 32))
	if !strings.Contains(rt, "aws-sdk-js/1.0.0") || !strings.Contains(rt, "api/kiroruntime#1.0.0") || !strings.Contains(rt, "md/nodejs#24.21.0") || !strings.Contains(rt, "KiroIDE-1.4.26-") {
		t.Fatal(rt)
	}
	ctx := &RequestContext{Cred: &Credentials{APIRegion: "eu-central-1"}, Config: config.Default()}
	if got := (IDE{}).APIURL(ctx); got != "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse" {
		t.Fatal(got)
	}
	if got := (KRS{}).MCPURL(ctx); got != "https://runtime.eu-central-1.kiro.dev/mcp" {
		t.Fatal(got)
	}
}

func TestClaudeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.anthropic.com":             "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/":         "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/messages": "https://api.anthropic.com/v1/messages",
	} {
		got, err := ClaudeMessagesURL(in)
		if err != nil || got != want {
			t.Errorf("%s => %s %v", in, got, err)
		}
	}
	if _, err := ClaudeMessagesURL("https://u:p@x"); err == nil {
		t.Fatal("userinfo accepted")
	}
}
