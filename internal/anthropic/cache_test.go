package anthropic

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func longSystem() string { return strings.Repeat("cache ", 3000) }

func body(v any) []byte { b, _ := json.Marshal(v); return b }

func turnOne(system any) []byte {
	return body(map[string]any{"model": "claude-opus-5-5", "system": system, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
}

func turnTwo(system any) []byte {
	return body(map[string]any{"model": "claude-opus-5-5", "system": system, "messages": []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": "hello"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "next question"}}},
	}})
}

func TestCacheFirstCreationThenRead(t *testing.T) {
	tr := newCacheTracker()
	now := time.Now()
	s := tr.observe("k", profileFromBody(turnOne(longSystem())), now)
	if s.Read != 0 || s.Creation < 0.99 || s.OneHour {
		t.Fatalf("%+v", s)
	}
	s = tr.observe("k", profileFromBody(turnTwo(longSystem())), now.Add(30*time.Second))
	if s.Read < 0.95 || s.Creation <= 0 || s.Creation > 0.05 || math.Abs(s.Read+s.Creation-1) > 1e-9 {
		t.Fatalf("%+v", s)
	}
	p := profileFromBody(turnOne(longSystem()))
	tr2 := newCacheTracker()
	tr2.observe("k", p, now)
	if s := tr2.observe("k", p, now.Add(6*time.Minute)); s.Read != 0 {
		t.Fatal("5m entries must expire")
	}
	tr3 := newCacheTracker()
	tr3.observe("k", p, now)
	tr3.observe("k", p, now.Add(4*time.Minute))
	if s := tr3.observe("k", p, now.Add(8*time.Minute)); s.Read < 0.99 {
		t.Fatal("hit must refresh ttl")
	}
	if s := tr3.observe("key:2", p, now); s.Read != 0 {
		t.Fatal("namespaces isolated")
	}
}

func TestCacheOneHourAndMarks(t *testing.T) {
	sys := []any{map[string]any{"type": "text", "text": longSystem(), "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}}}
	tr := newCacheTracker()
	now := time.Now()
	if s := tr.observe("k", profileFromBody(turnOne(sys)), now); !s.OneHour {
		t.Fatal("1h")
	}
	if s := tr.observe("k", profileFromBody(turnTwo(sys)), now.Add(30*time.Minute)); s.Read < 0.95 {
		t.Fatalf("%+v", s)
	}
	marked := []any{map[string]any{"type": "text", "text": longSystem(), "cache_control": map[string]any{"type": "ephemeral"}}}
	plain := []any{map[string]any{"type": "text", "text": longSystem()}}
	tr2 := newCacheTracker()
	tr2.observe("k", profileFromBody(turnOne(marked)), now)
	if s := tr2.observe("k", profileFromBody(turnOne(plain)), now.Add(5*time.Second)); s.Read < 0.99 {
		t.Fatalf("cache_control must not change fingerprint %+v", s)
	}
	a := strings.ReplaceAll(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"text","text":"PAD"}]}]}`, "PAD", longSystem())
	b := strings.ReplaceAll(`{"messages":[{"content":[{"text":"PAD","type":"text"}],"role":"user"}],"model":"claude-opus-5-5"}`, "PAD", longSystem())
	tr3 := newCacheTracker()
	tr3.observe("k", profileFromBody([]byte(a)), now)
	if s := tr3.observe("k", profileFromBody([]byte(b)), now); s.Read < 0.99 {
		t.Fatal("key order")
	}
	opus := turnOne(longSystem())
	sonnet := []byte(strings.Replace(string(opus), "claude-opus-5-5", "claude-sonnet-4-6", 1))
	tr4 := newCacheTracker()
	tr4.observe("k", profileFromBody(opus), now)
	if s := tr4.observe("k", profileFromBody(sonnet), now); s.Read != 0 {
		t.Fatal("model isolation")
	}
}

func TestCacheThresholdAndApply(t *testing.T) {
	if profileFromBody(body(map[string]any{"model": "claude-opus-5-5", "system": "short", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})) != nil {
		t.Fatal("short")
	}
	medium := strings.Repeat("cache ", 1000)
	if profileFromBody(body(map[string]any{"model": "claude-sonnet-4-6", "system": medium, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})) == nil {
		t.Fatal("sonnet 1024")
	}
	if profileFromBody(body(map[string]any{"model": "claude-opus-5-5", "system": medium, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})) != nil {
		t.Fatal("opus 4096")
	}
	s := CacheSplit{Read: 0.8, Creation: 0.15}
	if u := s.Apply(10000, 1, 1); u != (CacheUsage{InputTokens: 500, CacheRead: 8000, CacheWrite5m: 1500}) {
		t.Fatalf("%+v", u)
	}
	if u := s.Apply(10000, 0.5, 0); u.CacheRead != 4000 || u.Creation() != 0 || u.InputTokens != 6000 {
		t.Fatalf("%+v", u)
	}
	s.OneHour = true
	if u := s.Apply(10000, 1, 1); u.CacheWrite5m != 0 || u.CacheWrite1h != 1500 {
		t.Fatal("1h split")
	}
	InitCache(configDisabled())
	if j := UsageJSON(&s, 100, 7); len(j) != 2 {
		t.Fatalf("disabled shape %v", j)
	}
}
