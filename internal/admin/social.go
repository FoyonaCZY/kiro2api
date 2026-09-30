package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/google/uuid"
)

// Social（GitHub / Google）登录，对齐 Kiro IDE：生成 app.kiro.dev/signin 链接，
// 浏览器回调到 http://localhost:3128，把回调 URL 贴回来后用同一条 SOCKS5 换 refresh token。
const (
	authPortal     = "https://app.kiro.dev"
	tokenEndpoint  = "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/token"
	redirectOrigin = "http://localhost:3128"
	loginTTL       = 10 * time.Minute
)

type pendingLogin struct {
	codeVerifier  string
	proxyURL      string
	proxyUsername string
	proxyPassword string
	machineID     string
	created       time.Time
}

type socialStore struct {
	mu      sync.Mutex
	pending map[string]pendingLogin
}

func newSocialStore() *socialStore { return &socialStore{pending: map[string]pendingLogin{}} }

func (s *socialStore) purge() {
	for k, p := range s.pending {
		if time.Since(p.created) >= loginTTL {
			delete(s.pending, k)
		}
	}
}

func validateSocksProxy(u string) error {
	l := strings.ToLower(strings.TrimSpace(u))
	if !strings.HasPrefix(l, "socks5://") && !strings.HasPrefix(l, "socks5h://") {
		return errors.New("代理必须是 socks5:// 或 socks5h://")
	}
	if _, err := url.Parse(strings.TrimSpace(u)); err != nil {
		return errors.New("代理 URL 无法解析")
	}
	return nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *socialStore) start(proxyURL, user, pass string) (string, string, error) {
	if err := validateSocksProxy(proxyURL); err != nil {
		return "", "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier := b64url(raw)
	sum := sha256.Sum256([]byte(verifier))
	state := uuid.NewString()
	s.mu.Lock()
	s.pending[state] = pendingLogin{codeVerifier: verifier, proxyURL: proxyURL, proxyUsername: user, proxyPassword: pass, machineID: kiro.RandomMachineID(), created: time.Now()}
	s.purge()
	s.mu.Unlock()
	return state, portalURL(state, b64url(sum[:])), nil
}

func (s *socialStore) take(state string) (pendingLogin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	p, ok := s.pending[state]
	if !ok {
		return p, errors.New("登录会话不存在或已过期，请重新生成授权链接")
	}
	delete(s.pending, state)
	return p, nil
}

func (s *socialStore) restore(state string, p pendingLogin) {
	s.mu.Lock()
	s.pending[state] = p
	s.mu.Unlock()
}

func encodeQuery(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func portalURL(state, challenge string) string {
	return authPortal + "/signin?state=" + encodeQuery(state) + "&code_challenge=" + encodeQuery(challenge) +
		"&code_challenge_method=S256&redirect_uri=" + encodeQuery(redirectOrigin) + "&redirect_from=KiroIDE"
}

type parsedCallback struct {
	code, state, loginOption, path string
	port                           string
}

func parseCallback(raw string) (*parsedCallback, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return nil, errors.New("回调不是合法 URL")
	}
	if u.Scheme != "http" || u.Hostname() != "localhost" {
		return nil, errors.New("回调必须是 http://localhost 开头")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if port != "3128" {
		return nil, errors.New("回调端口必须是 3128")
	}
	if u.Path != "/oauth/callback" && u.Path != "/signin/callback" {
		return nil, errors.New("回调路径必须是 /oauth/callback 或 /signin/callback")
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		d := q.Get("error_description")
		if d == "" {
			d = e
		}
		return nil, fmt.Errorf("授权被拒绝: %s", d)
	}
	p := &parsedCallback{code: q.Get("code"), state: q.Get("state"), loginOption: strings.ToLower(q.Get("login_option")), path: u.Path, port: port}
	if p.code == "" {
		return nil, errors.New("回调里没有 code")
	}
	if p.state == "" {
		return nil, errors.New("回调里没有 state")
	}
	if p.loginOption != "github" && p.loginOption != "google" {
		return nil, errors.New("只支持 GitHub 或 Google 登录回调")
	}
	return p, nil
}

func (p *parsedCallback) exchangeRedirect() string {
	return "http://localhost:" + p.port + p.path + "?login_option=" + p.loginOption
}

type socialToken struct {
	AccessToken  string
	RefreshToken string
	ProfileArn   string
	ExpiresIn    int64
}

func (t *socialToken) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	get := func(keys ...string) json.RawMessage {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				return v
			}
		}
		return nil
	}
	_ = json.Unmarshal(get("accessToken", "access_token"), &t.AccessToken)
	_ = json.Unmarshal(get("refreshToken", "refresh_token"), &t.RefreshToken)
	_ = json.Unmarshal(get("profileArn", "profile_arn"), &t.ProfileArn)
	_ = json.Unmarshal(get("expiresIn", "expires_in"), &t.ExpiresIn)
	if t.RefreshToken == "" {
		return errors.New("响应里没有 refreshToken")
	}
	return nil
}

func exchangeSocialCode(ctx context.Context, cfg *config.Config, p pendingLogin, cb *parsedCallback) (*socialToken, error) {
	px := &httpx.Proxy{URL: p.proxyURL}
	if p.proxyUsername != "" && p.proxyPassword != "" {
		px.Username, px.Password = p.proxyUsername, p.proxyPassword
	}
	cl, err := httpx.New(px, 60*time.Second)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"code": cb.code, "code_verifier": p.codeVerifier, "redirect_uri": cb.exchangeRedirect()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", kiro.SocialRefreshUserAgent(cfg, p.machineID))
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		text := strings.TrimSpace(string(raw))
		if len(text) > 180 {
			text = text[:180] + "…"
		}
		return nil, fmt.Errorf("换取 token 失败: HTTP %d %s", resp.StatusCode, text)
	}
	var t socialToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
