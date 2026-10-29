package kiro

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/config"
)

func apiKeyCred(id uint64, prio uint32) *Credentials {
	return &Credentials{ID: id, Priority: prio, AuthMethod: "api_key", KiroAPIKey: fmt.Sprintf("ksk_test_%d", id)}
}

func coolingManager(t *testing.T, mode string) *Manager {
	cfg := config.Default()
	cfg.LoadBalancingMode = mode
	m, err := NewManager(cfg, []*Credentials{apiKeyCred(1, 1), apiKeyCred(2, 2)}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustID(t *testing.T, m *Manager, ctx context.Context, model string) uint64 {
	t.Helper()
	cc, err := m.AcquireContext(ctx, model)
	if err != nil {
		t.Fatal(err)
	}
	return cc.ID
}

func TestCooldownFailoverAndExpiry(t *testing.T) {
	for _, mode := range []string{"priority", "balanced"} {
		m := coolingManager(t, mode)
		ctx := context.Background()
		if mustID(t, m, ctx, "") != 1 {
			t.Fatal(mode)
		}
		m.ReportRateLimited(1, 30*time.Second)
		if mustID(t, m, ctx, "") != 2 {
			t.Fatal(mode)
		}
		s := m.Snapshot()
		if s.Available != 1 || s.Entries[0].Disabled || s.Entries[0].FailureCount != 0 || s.Entries[0].CooldownRemainingSeconds <= 0 {
			t.Fatalf("%s %+v", mode, s)
		}
		m.ReportRateLimited(2, 60*time.Second)
		_, err := m.AcquireContext(ctx, "")
		var cd *CoolingDownError
		if !errors.As(err, &cd) || cd.Seconds() < 1 || cd.Seconds() > 30 {
			t.Fatalf("%s %v", mode, err)
		}
		m.mu.Lock()
		m.entries[0].cooldownUntil = time.Now().Add(-time.Second)
		m.mu.Unlock()
		if mustID(t, m, ctx, "") != 1 {
			t.Fatal(mode)
		}
	}
}

func TestPriorityRespectsModelPermission(t *testing.T) {
	m := coolingManager(t, "priority")
	m.entries[0].cred.SubscriptionTitle = "KIRO FREE"
	ctx := context.Background()
	if mustID(t, m, ctx, "claude-sonnet-4.5") != 1 || mustID(t, m, ctx, "claude-opus-5.5") != 2 {
		t.Fatal("opus must skip free account")
	}
	m.ReportRateLimited(2, time.Minute)
	var cd *CoolingDownError
	if _, err := m.AcquireContext(ctx, "claude-opus-5.5"); !errors.As(err, &cd) {
		t.Fatalf("free account must not bypass opus cooldown: %v", err)
	}
}

func TestCooldownNotShortenedAndNoReenable(t *testing.T) {
	m := coolingManager(t, "priority")
	m.ReportRateLimited(1, time.Minute)
	deadline := m.entries[0].cooldownUntil
	m.ReportRateLimited(1, time.Second)
	m.ReportSuccess(1)
	if !m.entries[0].cooldownUntil.Equal(deadline) {
		t.Fatal("cooldown shortened")
	}
	m2 := coolingManager(t, "priority")
	m2.entries[0].disabled, m2.entries[0].reason = true, ReasonTooManyFailures
	m2.ReportRateLimited(2, 30*time.Second)
	var cd *CoolingDownError
	if _, err := m2.AcquireContext(context.Background(), ""); !errors.As(err, &cd) {
		t.Fatalf("want cooldown, got %v", err)
	}
	if !m2.Snapshot().Entries[0].Disabled {
		t.Fatal("cooldown must not heal disabled")
	}
}

func TestReportFailureDisablesAndHeals(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	m, err := NewManager(config.Default(), []*Credentials{{AccessToken: "t1", ExpiresAt: exp}, {AccessToken: "t2", ExpiresAt: exp}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxFailuresPerCredential; i++ {
		m.ReportFailure(1)
		m.ReportFailure(2)
	}
	if m.AvailableCount() != 0 {
		t.Fatal("all should be disabled")
	}
	cc, err := m.AcquireContext(context.Background(), "")
	if err != nil || (cc.Token != "t1" && cc.Token != "t2") || m.AvailableCount() != 2 {
		t.Fatalf("heal failed: %v", err)
	}
}

func TestQuotaAndRefreshDisabledNotHealed(t *testing.T) {
	m := coolingManager(t, "priority")
	m.ReportQuotaExhausted(1)
	for i := 0; i < MaxFailuresPerCredential; i++ {
		m.ReportRefreshFailure(2)
	}
	if _, err := m.AcquireContext(context.Background(), ""); err == nil {
		t.Fatal("quota/refresh disabled must not be auto-healed")
	}
}

func TestBalancedSkipsBadRefresh(t *testing.T) {
	cfg := config.Default()
	cfg.LoadBalancingMode = "balanced"
	bad := &Credentials{Priority: 0, RefreshToken: "short"}
	good := &Credentials{Priority: 1, AccessToken: "good-token", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	m, _ := NewManager(cfg, []*Credentials{bad, good}, nil, "")
	cc, err := m.AcquireContext(context.Background(), "")
	if err != nil || cc.ID != 2 || cc.Token != "good-token" {
		t.Fatalf("%+v %v", cc, err)
	}
}

func TestBalancedUsesLastMinute(t *testing.T) {
	cfg := config.Default()
	cfg.LoadBalancingMode = "balanced"
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	m, err := NewManager(cfg, []*Credentials{
		{AccessToken: "a", ExpiresAt: exp, Priority: 1},
		{AccessToken: "b", ExpiresAt: exp, Priority: 2},
	}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.AcquireContext(context.Background(), "")
	if err != nil || first.ID != 1 {
		t.Fatalf("tie goes to higher priority: %+v %v", first, err)
	}
	second, err := m.AcquireContext(context.Background(), "")
	if err != nil || second.ID != 2 {
		t.Fatalf("busier account must yield: %+v %v", second, err)
	}
	third, err := m.AcquireContext(context.Background(), "")
	if err != nil || third.ID != 1 {
		t.Fatalf("equal minute count goes to priority: %+v %v", third, err)
	}
	m.mu.Lock()
	m.find(1).recentRequests = []time.Time{time.Now().Add(-2 * time.Minute)}
	m.find(2).recentRequests = []time.Time{time.Now()}
	m.mu.Unlock()
	fourth, err := m.AcquireContext(context.Background(), "")
	if err != nil || fourth.ID != 1 {
		t.Fatalf("requests older than a minute must not count, got %+v %v", fourth, err)
	}
}

func TestInvalidConfigAndDuplicates(t *testing.T) {
	m, _ := NewManager(config.Default(), []*Credentials{{AuthMethod: "api_key"}}, nil, "")
	s := m.Snapshot()
	if !s.Entries[0].Disabled || s.Entries[0].DisabledReason != "InvalidConfig" {
		t.Fatalf("%+v", s.Entries[0])
	}
	if err := m.ResetAndEnable(1); err == nil {
		t.Fatal("invalid config reset must fail")
	}
	if _, err := NewManager(config.Default(), []*Credentials{{ID: 1}, {ID: 1}}, nil, ""); err == nil {
		t.Fatal("duplicate ids")
	}
}

func TestGroupPoolsStickSeparately(t *testing.T) {
	m, err := NewManager(config.Default(), []*Credentials{apiKeyCred(1, 1), apiKeyCred(2, 2), apiKeyCred(0, 0)}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	global := m.Snapshot().CurrentID
	scope := func(g string, ids ...uint64) context.Context {
		allowed := map[uint64]bool{}
		for _, id := range ids {
			allowed[id] = true
		}
		return access.WithScope(context.Background(), &access.Scope{GroupID: g, Allowed: allowed})
	}
	if mustID(t, m, scope("alpha", 1), "") != 1 || mustID(t, m, scope("beta", 2), "") != 2 {
		t.Fatal("groups")
	}
	if m.Snapshot().CurrentID != global {
		t.Fatal("group pick must not move global current")
	}
	if mustID(t, m, scope("default", 3), "") != 3 {
		t.Fatal("default group")
	}
	for i := 0; i < 3; i++ {
		m.ReportFailure(1)
		m.ReportFailure(2)
	}
	if mustID(t, m, scope("alpha", 1), "") != 1 {
		t.Fatal("alpha heal")
	}
	s := m.Snapshot()
	if s.Entries[0].Disabled || !s.Entries[1].Disabled {
		t.Fatal("heal must only touch own group")
	}
}

func TestSwitchToNextMovesGroupSticky(t *testing.T) {
	m, _ := NewManager(config.Default(), []*Credentials{apiKeyCred(1, 0), apiKeyCred(2, 1)}, nil, "")
	ctx := access.WithScope(context.Background(), &access.Scope{GroupID: "g", Allowed: map[uint64]bool{1: true, 2: true}})
	if mustID(t, m, ctx, "") != 1 {
		t.Fatal()
	}
	m.SwitchToNext(ctx)
	if mustID(t, m, ctx, "") != 2 {
		t.Fatal("switch must move group sticky")
	}
}

func TestUpdatesGoToSQLiteAndLeaveJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	original := "{\"authMethod\":\"social\"}\n"
	os.WriteFile(path, []byte(original), 0o600)
	m, err := NewManager(config.Default(), []*Credentials{{AuthMethod: "social", RefreshToken: "rt"}}, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	email, prio, line := "a@example.com", uint32(3), "socks5://gate.example:1000:user-a:secret-a"
	if err := m.UpdateCredential(1, &Update{Email: &email, Priority: &prio, ProxyLine: &line}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.store.Load()
	if got[0].Email != email || got[0].Priority != 3 || got[0].ProxyURL != "socks5://gate.example:1000" || got[0].ProxyUsername != "user-a" || got[0].ProxyPassword != "secret-a" {
		t.Fatalf("%+v", got[0])
	}
	u2, user2 := "socks5://gate.example:2000", "user-b"
	m.UpdateCredential(1, &Update{ProxyURL: &u2, ProxyUsername: &user2})
	got, _ = m.store.Load()
	if got[0].ProxyURL != u2 || got[0].ProxyUsername != user2 || got[0].ProxyPassword != "secret-a" {
		t.Fatalf("password must be kept: %+v", got[0])
	}
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatal("json changed")
	}
	os.WriteFile(path, []byte(`{"authMethod":"social","email":"json-only@example.com"}`), 0o600)
	re, err := LoadStartupCredentials(path)
	if err != nil || re[0].Email != email {
		t.Fatalf("db must win: %v %+v", err, re)
	}
	m.store.Close()
}

func TestSetLoadBalancingModePersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"loadBalancingMode":"priority"}`), 0o600)
	cfg, _ := config.Load(p)
	m, _ := NewManager(cfg, []*Credentials{{}}, nil, "")
	if err := m.SetLoadBalancingMode("balanced"); err != nil {
		t.Fatal(err)
	}
	c2, _ := config.Load(p)
	if c2.LoadBalancingMode != "balanced" || m.LoadBalancingMode() != "balanced" {
		t.Fatal("persist")
	}
}

func TestSetGlobalProxyPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"loadBalancingMode":"priority"}`), 0o600)
	cfg, _ := config.Load(p)
	m, _ := NewManager(cfg, []*Credentials{{}, {ProxyURL: ProxyDirect}}, nil, "")
	if _, err := m.SetGlobalProxy("socks5://1.2.3.4:1080:alice:pw"); err != nil {
		t.Fatal(err)
	}
	c2, _ := config.Load(p)
	if c2.ProxyURL != "socks5://1.2.3.4:1080" || c2.ProxyUsername != "alice" || c2.ProxyPassword != "pw" || c2.LoadBalancingMode != "priority" {
		t.Fatalf("persist: %+v", c2)
	}
	if px := m.entries[0].cred.EffectiveProxy(m.GlobalProxy()); px == nil || px.Password != "pw" {
		t.Fatalf("account without proxy must follow global: %+v", px)
	}
	if px := m.entries[1].cred.EffectiveProxy(m.GlobalProxy()); px != nil {
		t.Fatal("direct account must stay direct")
	}
	// 只带用户名时沿用原密码
	if px, err := m.SetGlobalProxy("socks5://alice@5.6.7.8:1080"); err != nil || px.URL != "socks5://5.6.7.8:1080" || px.Password != "pw" {
		t.Fatalf("keep password: %v %+v", err, px)
	}
	if _, err := m.SetGlobalProxy("socks5://bob@5.6.7.8:1080"); err == nil {
		t.Fatal("new user without password must fail")
	}
	if _, err := m.SetGlobalProxy("bad"); err == nil {
		t.Fatal("invalid line must fail")
	}
	if px, err := m.SetGlobalProxy(""); err != nil || px != nil || m.GlobalProxy() != nil {
		t.Fatal("clear")
	}
	c3, _ := config.Load(p)
	if c3.ProxyURL != "" || c3.ProxyPassword != "" {
		t.Fatalf("clear persist: %+v", c3)
	}
}

func TestAddCredential(t *testing.T) {
	m, _ := NewManager(config.Default(), nil, nil, "")
	id, err := m.AddCredential(context.Background(), &Credentials{AuthMethod: "api_key", KiroAPIKey: "ksk_abc"})
	if err != nil || id != 1 {
		t.Fatal(err)
	}
	if _, err := m.AddCredential(context.Background(), &Credentials{AuthMethod: "api_key", KiroAPIKey: "ksk_abc"}); err == nil {
		t.Fatal("dup")
	}
	if _, err := m.AddCredential(context.Background(), &Credentials{AuthMethod: "api_key"}); err == nil {
		t.Fatal("missing key")
	}
	id, err = m.AddCredential(context.Background(), &Credentials{AuthMethod: "Claude-API", ClaudeBaseURL: "https://api.example.com/", ClaudeAPIKey: "sk"})
	if err != nil || id != 2 {
		t.Fatal(err)
	}
	c, _ := m.CredentialsOf(2)
	if c.AuthMethod != "claude_api" || c.ClaudeBaseURL != "https://api.example.com" || c.MachineID == "" {
		t.Fatalf("%+v", c)
	}
	if err := m.ForceRefresh(context.Background(), 2); err == nil {
		t.Fatal("claude refresh must be refused")
	}
	if _, err := m.UsageLimitsFor(context.Background(), 2); err == nil {
		t.Fatal("claude balance must be refused")
	}
	if err := m.DeleteCredential(2); err == nil {
		t.Fatal("must disable first")
	}
	m.SetDisabled(2, true)
	if err := m.DeleteCredential(2); err != nil || m.TotalCount() != 1 {
		t.Fatal(err)
	}
}

func TestTokenExpiry(t *testing.T) {
	c := &Credentials{}
	if !TokenExpired(c) || TokenExpiringSoon(c) {
		t.Fatal("missing expiry")
	}
	c.ExpiresAt = time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339)
	if !TokenExpired(c) {
		t.Fatal("within 5 min")
	}
	c.ExpiresAt = time.Now().Add(8 * time.Minute).UTC().Format(time.RFC3339)
	if TokenExpired(c) || !TokenExpiringSoon(c) {
		t.Fatal("8 min")
	}
	c.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if TokenExpired(c) || TokenExpiringSoon(c) {
		t.Fatal("1h")
	}
}

func TestDeletedIDsAreNotReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	m, err := NewManager(config.Default(), []*Credentials{{ID: 3, AuthMethod: "api_key", KiroAPIKey: "ksk_a"}, {ID: 4, AuthMethod: "api_key", KiroAPIKey: "ksk_b"}}, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	m.SetDisabled(4, true)
	if err := m.DeleteCredential(4); err != nil {
		t.Fatal(err)
	}
	if id, err := m.AddCredential(context.Background(), &Credentials{AuthMethod: "api_key", KiroAPIKey: "ksk_c"}); err != nil || id != 5 {
		t.Fatalf("deleted id reused: %d %v", id, err)
	}
	m.SetDisabled(5, true)
	m.DeleteCredential(5)
	m.store.Close()

	// 重启后仍记得用过的编号
	creds, err := LoadStartupCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(config.Default(), creds, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.store.Close()
	if err := m.ReserveIDs(7); err != nil {
		t.Fatal(err)
	}
	if spare, _ := m.NewSpareID(); spare != 8 {
		t.Fatalf("spare %d", spare)
	}
	if id, _ := m.AddCredential(context.Background(), &Credentials{AuthMethod: "api_key", KiroAPIKey: "ksk_d"}); id != 9 {
		t.Fatalf("after restart got %d", id)
	}
}
