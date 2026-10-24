// Package kiro 包含 Kiro 凭据、端点、事件流解析和多账号调度。
package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
)

// ProxyDirect 显式直连。
const ProxyDirect = "direct"

// Credentials 与 kiro.rs 的 KiroCredentials JSON 一致，accounts.sqlite 里整条存这个结构。
type Credentials struct {
	ID                uint64 `json:"id,omitempty"`
	AccessToken       string `json:"accessToken,omitempty"`
	RefreshToken      string `json:"refreshToken,omitempty"`
	ProfileArn        string `json:"profileArn,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	AuthMethod        string `json:"authMethod,omitempty"`
	ClientID          string `json:"clientId,omitempty"`
	ClientSecret      string `json:"clientSecret,omitempty"`
	Priority          uint32 `json:"priority,omitempty"`
	Region            string `json:"region,omitempty"`
	AuthRegion        string `json:"authRegion,omitempty"`
	APIRegion         string `json:"apiRegion,omitempty"`
	MachineID         string `json:"machineId,omitempty"`
	Email             string `json:"email,omitempty"`
	SubscriptionTitle string `json:"subscriptionTitle,omitempty"`
	ProxyURL          string `json:"proxyUrl,omitempty"`
	ProxyUsername     string `json:"proxyUsername,omitempty"`
	ProxyPassword     string `json:"proxyPassword,omitempty"`
	Disabled          bool   `json:"disabled"`
	KiroAPIKey        string `json:"kiroApiKey,omitempty"`
	ClaudeBaseURL     string `json:"claudeBaseUrl,omitempty"`
	ClaudeAPIKey      string `json:"claudeApiKey,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
}

// Clone 值拷贝。
func (c *Credentials) Clone() *Credentials {
	cp := *c
	return &cp
}

func canonicalAuthMethod(v string) string {
	switch strings.ToLower(v) {
	case "builder-id", "iam":
		return "idc"
	case "api_key", "apikey":
		return "api_key"
	}
	return v
}

// Canonicalize 统一 authMethod 写法。
func (c *Credentials) Canonicalize() {
	if c.AuthMethod != "" {
		c.AuthMethod = canonicalAuthMethod(c.AuthMethod)
	}
}

// EffectiveAuthRegion 凭据.authRegion > 凭据.region > 全局。
func (c *Credentials) EffectiveAuthRegion(cfg *config.Config) string {
	if c.AuthRegion != "" {
		return c.AuthRegion
	}
	if c.Region != "" {
		return c.Region
	}
	return cfg.EffectiveAuthRegion()
}

// EffectiveAPIRegion 对齐 Kiro IDE：凭据.apiRegion > profileArn 里的区域 > 全局 apiRegion > 凭据.region > 全局 region。
// profileArn 所在区域以外的 runtime / management 会直接回 400。
func (c *Credentials) EffectiveAPIRegion(cfg *config.Config) string {
	if c.APIRegion != "" {
		return c.APIRegion
	}
	if r := ProfileArnRegion(c.ProfileArn); r != "" {
		return r
	}
	if cfg.APIRegion != "" {
		return cfg.APIRegion
	}
	if c.Region != "" {
		return c.Region
	}
	return cfg.Region
}

// EffectiveProxy 凭据代理 > 全局代理；direct 表示直连。
func (c *Credentials) EffectiveProxy(global *httpx.Proxy) *httpx.Proxy {
	switch {
	case strings.EqualFold(c.ProxyURL, ProxyDirect):
		return nil
	case c.ProxyURL != "":
		p := &httpx.Proxy{URL: c.ProxyURL}
		if c.ProxyUsername != "" && c.ProxyPassword != "" {
			p.Username, p.Password = c.ProxyUsername, c.ProxyPassword
		}
		return p
	}
	return global
}

// SupportsOpus 订阅等级带 FREE 的不支持 Opus；未知时放行。
func (c *Credentials) SupportsOpus() bool {
	if c.SubscriptionTitle == "" {
		return true
	}
	return !strings.Contains(strings.ToUpper(c.SubscriptionTitle), "FREE")
}

// IsAPIKey Kiro API Key 凭据。
func (c *Credentials) IsAPIKey() bool {
	m := strings.ToLower(c.AuthMethod)
	return c.KiroAPIKey != "" || m == "api_key" || m == "apikey"
}

// IsClaudeAPI Claude Messages 透传账号。
func (c *Credentials) IsClaudeAPI() bool {
	switch strings.ToLower(c.AuthMethod) {
	case "claude_api", "claude-api", "messages":
		return true
	}
	return false
}

// ClaudeMessagesURL {base}/v1/messages。
func (c *Credentials) ClaudeMessagesURL() (string, error) {
	base := strings.TrimSpace(c.ClaudeBaseURL)
	if base == "" {
		return "", errors.New("Claude API 账号缺少地址")
	}
	return ClaudeMessagesURL(base)
}

// ClaudeMessagesURL 校验并拼接 Messages 地址。
func ClaudeMessagesURL(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return "", errors.New("Claude API 地址要以 http:// 或 https:// 开头")
	}
	if strings.Contains(base, "@") {
		return "", errors.New("Claude API 地址不要带账号密码，密钥单独填写")
	}
	if root, ok := strings.CutSuffix(base, "/v1/messages"); ok {
		return root + "/v1/messages", nil
	}
	if root, ok := strings.CutSuffix(base, "/v1"); ok {
		return root + "/v1/messages", nil
	}
	return base + "/v1/messages", nil
}

// UpstreamTokenType Kiro Agent 发给 runtime / management 的 TokenType；Social 不发送。
func (c *Credentials) UpstreamTokenType() string {
	if c.IsAPIKey() {
		return "API_KEY"
	}
	switch strings.ToLower(c.AuthMethod) {
	case "idc", "builder-id", "iam":
		return "SSO_OIDC"
	case "machine_token":
		return "KIRO_MACHINE_TOKEN"
	case "external_idp":
		return "EXTERNAL_IDP"
	}
	return ""
}

// LoadCredentialsFile 读取 credentials.json，支持单对象和数组。返回是否为数组格式。
func LoadCredentialsFile(path string) ([]*Credentials, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, true, nil
	}
	if strings.HasPrefix(text, "[") {
		var list []*Credentials
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, true, err
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Priority < list[j].Priority })
		for _, c := range list {
			c.Canonicalize()
		}
		return list, true, nil
	}
	var one Credentials
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		return nil, false, err
	}
	one.Canonicalize()
	return []*Credentials{&one}, false, nil
}

// ParsedProxy 管理端代理输入的解析结果。URLSet=false 表示回退全局。
type ParsedProxy struct {
	URL      string
	Username string
	Password string
}

// ParseProxyLine 支持空、direct、scheme://host:port、scheme://user:pass@host:port、
// scheme://host:port:user:pass 和 host:port:user:pass（按 socks5）。
func ParseProxyLine(raw string) (ParsedProxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") {
		return ParsedProxy{}, nil
	}
	if strings.EqualFold(raw, ProxyDirect) {
		return ParsedProxy{URL: ProxyDirect}, nil
	}
	if scheme, body, ok := strings.Cut(raw, "://"); ok {
		scheme = strings.ToLower(scheme)
		if err := checkProxyScheme(scheme); err != nil {
			return ParsedProxy{}, err
		}
		if i := strings.LastIndex(body, "@"); i >= 0 {
			hp, err := normalizeHostport(body[i+1:])
			if err != nil {
				return ParsedProxy{}, err
			}
			user, pass, _ := strings.Cut(body[:i], ":")
			return ParsedProxy{URL: scheme + "://" + hp, Username: user, Password: pass}, nil
		}
		return parseHostportParts(scheme, body)
	}
	return parseHostportParts("socks5", raw)
}

func checkProxyScheme(s string) error {
	switch s {
	case "socks5", "socks5h", "http", "https":
		return nil
	}
	return errors.New("代理协议只支持 socks5、socks5h、http、https")
}

func parseHostportParts(scheme, body string) (ParsedProxy, error) {
	parts := strings.Split(body, ":")
	if len(parts) == 2 {
		hp, err := normalizeHostport(body)
		if err != nil {
			return ParsedProxy{}, err
		}
		return ParsedProxy{URL: scheme + "://" + hp}, nil
	}
	if len(parts) >= 4 {
		hp, err := normalizeHostport(parts[0] + ":" + parts[1])
		if err != nil {
			return ParsedProxy{}, err
		}
		user := strings.TrimSpace(parts[2])
		pass := strings.TrimSpace(parts[3])
		if user == "" || pass == "" {
			return ParsedProxy{}, errors.New("代理账号或密码为空")
		}
		return ParsedProxy{URL: scheme + "://" + hp, Username: user, Password: pass}, nil
	}
	return ParsedProxy{}, errors.New("代理地址无效。可用 socks5://user:pass@host:port，或 socks5://host:port:user:pass")
}

func normalizeHostport(hp string) (string, error) {
	host, port, ok := strings.Cut(hp, ":")
	if !ok {
		return "", errors.New("代理缺少端口")
	}
	if host == "" || strings.ContainsAny(host, "/ @") {
		return "", errors.New("代理主机无效")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", errors.New("代理端口无效")
	}
	return fmt.Sprintf("%s:%d", host, n), nil
}
