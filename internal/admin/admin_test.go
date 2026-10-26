package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/FoyonaCZY/kiro2api/internal/usage"
)

func setup(t *testing.T) *httptest.Server {
	m, err := kiro.NewManager(config.Default(), []*kiro.Credentials{
		{ID: 1, AuthMethod: "api_key", KiroAPIKey: "ksk_0123456789abcdef_secret", Email: "a@x"},
		{ID: 2, AuthMethod: "claude_api", ClaudeBaseURL: "https://c.example", ClaudeAPIKey: "sk-claude-secret", Priority: 1},
	}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s := NewService("adm", m, kiro.Endpoints(), usage.Open(""), access.Open(""))
	mux := http.NewServeMux()
	s.Routes(mux)
	return httptest.NewServer(mux)
}

func do(t *testing.T, srv *httptest.Server, method, path, key, body string) (int, string) {
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("x-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAdminCredentials(t *testing.T) {
	srv := setup(t)
	defer srv.Close()
	if st, _ := do(t, srv, "GET", "/api/admin/credentials", "wrong", ""); st != 401 {
		t.Fatal(st)
	}
	st, body := do(t, srv, "PUT", "/api/admin/credentials/1", "adm", `{"email":"renamed","proxy":"socks5://gate.example:1080:user-a:pw-secret","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/TESTPROFILE"}`)
	if st != 200 {
		t.Fatal(st, body)
	}
	_, body = do(t, srv, "GET", "/api/admin/credentials", "adm", "")
	for _, secret := range []string{"ksk_0123456789abcdef_secret", "sk-claude-secret", "pw-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	var resp struct {
		Total       int              `json:"total"`
		CurrentID   uint64           `json:"currentId"`
		Credentials []map[string]any `json:"credentials"`
	}
	json.Unmarshal([]byte(body), &resp)
	c1, c2 := resp.Credentials[0], resp.Credentials[1]
	for _, k := range []string{"id", "priority", "disabled", "failureCount", "isCurrent", "authMethod", "maskedApiKey", "endpoint", "hasProxyPassword", "proxyUrl", "successCount", "cooldownRemainingSeconds"} {
		if _, ok := c1[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	if c1["email"] != "renamed" || c1["proxyUrl"] != "socks5://gate.example:1080" || c1["proxyUsername"] != "user-a" || c1["hasProxyPassword"] != true || c1["endpoint"] != "ide" || c1["maskedApiKey"] != "ksk_...cret" || c1["hasProfileArn"] != true {
		t.Fatalf("%v", c1)
	}
	if c2["authMethod"] != "claude_api" || c2["endpoint"] != "" || c2["claudeBaseUrl"] != "https://c.example" || c2["hasClaudeApiKey"] != true {
		t.Fatalf("%v", c2)
	}
	if st, body := do(t, srv, "GET", "/api/admin/credentials/2/balance", "adm", ""); st != 400 || !strings.Contains(body, "Claude API") {
		t.Fatal(st, body)
	}
	if st, _ := do(t, srv, "DELETE", "/api/admin/credentials/1", "adm", ""); st != 400 {
		t.Fatal("must disable first")
	}
	if st, _ := do(t, srv, "GET", "/api/admin/credentials/9/balance", "adm", ""); st != 404 {
		t.Fatal(st)
	}
	if st, body := do(t, srv, "PUT", "/api/admin/credentials/1", "adm", `{"endpoint":"nope"}`); st != 400 || !strings.Contains(body, "未知端点") {
		t.Fatal(st, body)
	}
}

func TestAdminAccessAndUsage(t *testing.T) {
	srv := setup(t)
	defer srv.Close()
	st, body := do(t, srv, "POST", "/api/admin/access/groups", "adm", `{"name":"spare"}`)
	var g map[string]any
	json.Unmarshal([]byte(body), &g)
	if st != 200 || g["id"] == nil {
		t.Fatal(st, body)
	}
	if st, _ := do(t, srv, "PUT", "/api/admin/access/groups/"+g["id"].(string)+"/members", "adm", `{"credentialIds":[2]}`); st != 204 {
		t.Fatal(st)
	}
	_, body = do(t, srv, "GET", "/api/admin/access/groups", "adm", "")
	var groups []map[string]any
	json.Unmarshal([]byte(body), &groups)
	if groups[0]["isDefault"] != true || len(groups[0]["members"].([]any)) != 1 || groups[0]["members"].([]any)[0] != float64(1) {
		t.Fatalf("default group should list unassigned credential 1: %s", body)
	}
	if st, body := do(t, srv, "POST", "/api/admin/usage/prices", "adm", `{"name":"S","aliases":["m"],"inputPerM":1,"outputPerM":2}`); st != 200 || !strings.Contains(body, `"cacheReadPerM":null`) {
		t.Fatal(st, body)
	}
	if st, body := do(t, srv, "PUT", "/api/admin/usage/accounts/1/cost", "adm", `{"costCny":10}`); st != 200 || !strings.Contains(body, `"costCny":10`) {
		t.Fatal(st, body)
	}
	if st, body := do(t, srv, "GET", "/api/admin/usage/requests/99", "adm", ""); st != 404 || body != "请求不存在" {
		t.Fatal(st, body)
	}
	if st, body := do(t, srv, "PUT", "/api/admin/config/load-balancing", "adm", `{"mode":"x"}`); st != 400 {
		t.Fatal(st, body)
	}
}

func TestParseCallback(t *testing.T) {
	p, err := parseCallback("http://localhost:3128/oauth/callback?code=abc&state=s1&login_option=GitHub")
	if err != nil || p.code != "abc" || p.loginOption != "github" || p.exchangeRedirect() != "http://localhost:3128/oauth/callback?login_option=github" {
		t.Fatal(err, p)
	}
	if _, err := parseCallback("https://example.com/oauth/callback?code=a&state=b&login_option=github"); err == nil || !strings.Contains(err.Error(), "localhost") {
		t.Fatal(err)
	}
	u := portalURL("state-1", "challenge")
	if !strings.HasPrefix(u, "https://app.kiro.dev/signin?") || !strings.Contains(u, "redirect_uri=http%3A%2F%2Flocalhost%3A3128") || !strings.Contains(u, "code_challenge_method=S256") {
		t.Fatal(u)
	}
}

func TestDeleteRemovesGroupMembership(t *testing.T) {
	m, _ := kiro.NewManager(config.Default(), []*kiro.Credentials{{ID: 1, AuthMethod: "api_key", KiroAPIKey: "ksk_1"}, {ID: 2, AuthMethod: "api_key", KiroAPIKey: "ksk_2"}}, nil, "")
	acc := access.Open("")
	g, _ := acc.CreateGroup("power")
	acc.SetMembers(g.ID, []uint64{2}, []uint64{1, 2})
	s := NewService("adm", m, kiro.Endpoints(), usage.Open(""), acc)
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	do(t, srv, "POST", "/api/admin/credentials/2/disabled", "adm", `{"disabled":true}`)
	if code, body := do(t, srv, "DELETE", "/api/admin/credentials/2", "adm", ""); code != 200 {
		t.Fatal(code, body)
	}
	if acc.MaxMemberID() != 0 {
		t.Fatal("deleted credential must leave groups")
	}
}
