package kiro

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/config"
)

// EntrySnapshot 管理端展示用，不含任何密钥。JSON 字段与 kiro.rs 一致。
type EntrySnapshot struct {
	CooldownRemainingSeconds int64   `json:"cooldownRemainingSeconds"`
	ID                       uint64  `json:"id"`
	Priority                 uint32  `json:"priority"`
	Disabled                 bool    `json:"disabled"`
	FailureCount             uint32  `json:"failureCount"`
	AuthMethod               *string `json:"authMethod"`
	HasProfileArn            bool    `json:"hasProfileArn"`
	ExpiresAt                *string `json:"expiresAt"`
	RefreshTokenHash         *string `json:"refreshTokenHash"`
	APIKeyHash               *string `json:"apiKeyHash"`
	MaskedAPIKey             *string `json:"maskedApiKey"`
	Email                    *string `json:"email"`
	SuccessCount             uint64  `json:"successCount"`
	LastUsedAt               *string `json:"lastUsedAt"`
	HasProxy                 bool    `json:"hasProxy"`
	ProxyURL                 string  `json:"proxyUrl,omitempty"`
	RefreshFailureCount      uint32  `json:"refreshFailureCount"`
	DisabledReason           string  `json:"disabledReason,omitempty"`
	Endpoint                 string  `json:"endpoint,omitempty"`
	ProxyUsername            string  `json:"proxyUsername,omitempty"`
	HasProxyPassword         bool    `json:"hasProxyPassword"`
	Region                   string  `json:"region,omitempty"`
	AuthRegion               string  `json:"authRegion,omitempty"`
	APIRegion                string  `json:"apiRegion,omitempty"`
	ClientID                 string  `json:"clientId,omitempty"`
	HasClientSecret          bool    `json:"hasClientSecret"`
	HasRefreshToken          bool    `json:"hasRefreshToken"`
	MachineID                string  `json:"machineId,omitempty"`
	ClaudeBaseURL            string  `json:"claudeBaseUrl,omitempty"`
	HasClaudeAPIKey          bool    `json:"hasClaudeApiKey"`
	SubscriptionTitle        string  `json:"-"`
}

// Snapshot 管理器状态。
type Snapshot struct {
	Entries   []EntrySnapshot `json:"entries"`
	CurrentID uint64          `json:"currentId"`
	Total     int             `json:"total"`
	Available int             `json:"available"`
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DisplayProxyURL 去掉 URL 里的 userinfo。
func DisplayProxyURL(u string) string {
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			return scheme + "://" + rest[i+1:]
		}
	}
	return u
}

// Snapshot 快照。
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := Snapshot{CurrentID: m.currentID, Total: len(m.entries), Entries: make([]EntrySnapshot, 0, len(m.entries))}
	for _, e := range m.entries {
		c := e.cred
		if e.available("", now) {
			out.Available++
		}
		s := EntrySnapshot{
			ID: e.id, Priority: c.Priority, Disabled: e.disabled, FailureCount: e.failureCount,
			HasProfileArn: c.ProfileArn != "", Email: strp(c.Email), SuccessCount: e.successCount,
			LastUsedAt: strp(e.lastUsedAt), HasProxy: c.ProxyURL != "", ProxyURL: DisplayProxyURL(c.ProxyURL),
			RefreshFailureCount: e.refreshFailureCount, DisabledReason: string(e.reason), Endpoint: c.Endpoint,
			ProxyUsername: c.ProxyUsername, HasProxyPassword: c.ProxyPassword != "", Region: c.Region,
			AuthRegion: c.AuthRegion, APIRegion: c.APIRegion, ClientID: c.ClientID, HasClientSecret: c.ClientSecret != "",
			HasRefreshToken: c.RefreshToken != "", MachineID: c.MachineID, ClaudeBaseURL: c.ClaudeBaseURL,
			HasClaudeAPIKey: c.ClaudeAPIKey != "", SubscriptionTitle: c.SubscriptionTitle,
		}
		if e.cooldownUntil.After(now) {
			s.CooldownRemainingSeconds = int64(math.Ceil(e.cooldownUntil.Sub(now).Seconds()))
			if s.CooldownRemainingSeconds < 1 {
				s.CooldownRemainingSeconds = 1
			}
		}
		if c.IsAPIKey() {
			s.AuthMethod = strp("api_key")
			if c.KiroAPIKey != "" {
				s.APIKeyHash = strp(sha256HexStr(c.KiroAPIKey))
				s.MaskedAPIKey = strp(maskAPIKey(c.KiroAPIKey))
			}
		} else {
			s.AuthMethod = strp(canonicalAuthMethod(c.AuthMethod))
			s.ExpiresAt = strp(c.ExpiresAt)
			if c.RefreshToken != "" {
				s.RefreshTokenHash = strp(sha256HexStr(c.RefreshToken))
			}
		}
		out.Entries = append(out.Entries, s)
	}
	return out
}

// CredentialsOf 按 id 取凭据副本（含密钥，仅内部使用）。
func (m *Manager) CredentialsOf(id uint64) (*Credentials, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.find(id); e != nil {
		return e.cred.Clone(), true
	}
	return nil, false
}

// ErrNotFound 凭据不存在。
var ErrNotFound = errors.New("凭据不存在")

func notFound(id uint64) error { return fmt.Errorf("%w: %d", ErrNotFound, id) }

// SetDisabled 启用时清零失败计数。
func (m *Manager) SetDisabled(id uint64, disabled bool) error {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return notFound(id)
	}
	e.disabled = disabled
	if disabled {
		e.reason = ReasonManual
	} else {
		e.failureCount, e.refreshFailureCount, e.reason = 0, 0, ReasonNone
	}
	m.mu.Unlock()
	return m.persist()
}

// SetPriority 改完立即按新优先级重选当前凭据。
func (m *Manager) SetPriority(id uint64, priority uint32) error {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return notFound(id)
	}
	e.cred.Priority = priority
	if best := m.highestPriorityLocked(0); best != 0 {
		m.currentID = best
	}
	m.mu.Unlock()
	return m.persist()
}

// ResetAndEnable 配置无效的凭据不能直接重置。
func (m *Manager) ResetAndEnable(id uint64) error {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return notFound(id)
	}
	if e.reason == ReasonInvalidConfig {
		m.mu.Unlock()
		return fmt.Errorf("凭据 #%d 因配置无效被禁用，请修正配置后重启服务", id)
	}
	e.failureCount, e.refreshFailureCount, e.disabled, e.reason = 0, 0, false, ReasonNone
	m.mu.Unlock()
	return m.persist()
}

// Update 管理端的修改。nil 表示不改。名称、区域、端点、代理地址传空表示清空；
// 密码、Refresh Token、Client Secret、Claude Key 传空表示保留。
type Update struct {
	Email         *string
	Priority      *uint32
	ProxyLine     *string
	ProxyURL      *string
	ProxyUsername *string
	ProxyPassword *string
	Endpoint      *string
	Region        *string
	AuthRegion    *string
	APIRegion     *string
	RefreshToken  *string
	ClientID      *string
	ClientSecret  *string
	MachineID     *string
	ClaudeBaseURL *string
	ClaudeAPIKey  *string
}

func nonBlank(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	v := strings.TrimSpace(*p)
	return v, v != ""
}

func setTrim(dst *string, p *string) {
	if p != nil {
		*dst = strings.TrimSpace(*p)
	}
}

// ApplyUpdate 就地修改凭据。
func ApplyUpdate(c *Credentials, u *Update) error {
	setTrim(&c.Email, u.Email)
	if u.Priority != nil {
		c.Priority = *u.Priority
	}
	if err := applyProxyUpdate(c, u); err != nil {
		return err
	}
	setTrim(&c.Endpoint, u.Endpoint)
	setTrim(&c.Region, u.Region)
	setTrim(&c.AuthRegion, u.AuthRegion)
	setTrim(&c.APIRegion, u.APIRegion)
	setTrim(&c.ClientID, u.ClientID)
	if v, ok := nonBlank(u.ClientSecret); ok {
		c.ClientSecret = v
	}
	if v, ok := nonBlank(u.MachineID); ok {
		n, ok := NormalizeMachineID(v)
		if !ok {
			return errors.New("Machine ID 需要是 64 位十六进制或 UUID")
		}
		c.MachineID = n
	}
	if v, ok := nonBlank(u.ClaudeBaseURL); ok {
		if _, err := ClaudeMessagesURL(v); err != nil {
			return err
		}
		c.ClaudeBaseURL = strings.TrimRight(v, "/")
	}
	if v, ok := nonBlank(u.ClaudeAPIKey); ok {
		c.ClaudeAPIKey = v
	}
	if v, ok := nonBlank(u.RefreshToken); ok {
		if c.IsAPIKey() {
			return errors.New("API Key 凭据没有 Refresh Token")
		}
		c.RefreshToken, c.AccessToken, c.ExpiresAt = v, "", ""
	}
	return nil
}

func applyProxyUpdate(c *Credentials, u *Update) error {
	if u.ProxyLine != nil {
		p, err := ParseProxyLine(*u.ProxyLine)
		if err != nil {
			return err
		}
		keep := p.Password == "" && p.URL != "" && !strings.EqualFold(p.URL, ProxyDirect)
		old := c.ProxyPassword
		c.ProxyURL, c.ProxyUsername, c.ProxyPassword = p.URL, p.Username, p.Password
		if keep {
			c.ProxyPassword = old
		}
		return nil
	}
	pw, hasPw := nonBlank(u.ProxyPassword)
	if u.ProxyURL == nil && u.ProxyUsername == nil && !hasPw {
		return nil
	}
	if u.ProxyURL != nil {
		p, err := ParseProxyLine(*u.ProxyURL)
		if err != nil {
			return err
		}
		if p.URL == "" || strings.EqualFold(p.URL, ProxyDirect) {
			c.ProxyURL, c.ProxyUsername, c.ProxyPassword = p.URL, "", ""
			return nil
		}
		if p.Username != "" || p.Password != "" {
			old := c.ProxyPassword
			c.ProxyURL, c.ProxyUsername, c.ProxyPassword = p.URL, p.Username, p.Password
			if p.Password == "" {
				c.ProxyPassword = old
			}
			return nil
		}
		c.ProxyURL = p.URL
	}
	if u.ProxyUsername != nil {
		c.ProxyUsername = strings.TrimSpace(*u.ProxyUsername)
	}
	if hasPw {
		c.ProxyPassword = pw
	}
	return nil
}

// UpdateCredential 修改后立即写库，下一次请求就用新配置。
func (m *Manager) UpdateCredential(id uint64, u *Update) error {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return notFound(id)
	}
	next := e.cred.Clone()
	if err := ApplyUpdate(next, u); err != nil {
		m.mu.Unlock()
		return err
	}
	e.cred = next
	m.mu.Unlock()
	return m.persist()
}

// UsageLimitsFor 查询额度，顺带更新订阅等级。
func (m *Manager) UsageLimitsFor(ctx context.Context, id uint64) (*UsageLimits, error) {
	cred, ok := m.CredentialsOf(id)
	if !ok {
		return nil, notFound(id)
	}
	if cred.IsClaudeAPI() {
		return nil, errors.New("Claude API 账号没有 Kiro 余额")
	}
	var token string
	if cred.IsAPIKey() {
		if cred.KiroAPIKey == "" {
			return nil, errors.New("API Key 凭据缺少 kiroApiKey")
		}
		token = cred.KiroAPIKey
	} else {
		if TokenExpired(cred) || TokenExpiringSoon(cred) {
			fresh, err := m.refreshIfNeeded(ctx, id, false)
			if err != nil {
				return nil, err
			}
			cred = fresh
		}
		if cred.AccessToken == "" {
			return nil, errors.New("凭据无 access_token")
		}
		token = cred.AccessToken
	}
	cred, _ = m.CredentialsOf(id)
	ul, err := FetchUsageLimits(ctx, cred, m.cfg, token, cred.EffectiveProxy(m.proxy))
	if err != nil {
		return nil, err
	}
	if title := ul.SubscriptionTitle(); title != "" {
		m.mu.Lock()
		changed := false
		if e := m.find(id); e != nil && e.cred.SubscriptionTitle != title {
			slog.Info("凭据订阅等级已更新", "credential_id", id, "old", e.cred.SubscriptionTitle, "new", title)
			e.cred.SubscriptionTitle = title
			changed = true
		}
		m.mu.Unlock()
		if changed {
			if err := m.persist(); err != nil {
				slog.Warn("订阅等级更新后持久化失败", "err", err)
			}
		}
	}
	return ul, nil
}

// AddCredential 校验、查重、OAuth 刷新验证后入库，返回新 id。
func (m *Manager) AddCredential(ctx context.Context, nc *Credentials) (uint64, error) {
	nc = nc.Clone()
	switch strings.ToLower(nc.AuthMethod) {
	case "claude_api", "claude-api", "messages":
		nc.AuthMethod = "claude_api"
	default:
		nc.Canonicalize()
	}
	switch {
	case nc.IsClaudeAPI():
		if nc.ClaudeAPIKey == "" {
			return 0, errors.New("Claude API Key 为空")
		}
		if _, err := nc.ClaudeMessagesURL(); err != nil {
			return 0, err
		}
		nc.ClaudeBaseURL = strings.TrimRight(strings.TrimSpace(nc.ClaudeBaseURL), "/")
	case nc.IsAPIKey():
		if nc.KiroAPIKey == "" {
			if strings.EqualFold(nc.AuthMethod, "api_key") {
				return 0, errors.New("缺少 kiroApiKey")
			}
			return 0, errors.New("kiroApiKey 为空")
		}
	default:
		if err := ValidateRefreshToken(nc); err != nil {
			return 0, err
		}
	}
	m.mu.Lock()
	for _, e := range m.entries {
		switch {
		case nc.IsClaudeAPI() && e.cred.ClaudeAPIKey != "" && e.cred.ClaudeAPIKey == nc.ClaudeAPIKey && e.cred.ClaudeBaseURL == nc.ClaudeBaseURL:
			m.mu.Unlock()
			return 0, errors.New("凭据已存在（Claude API Key 重复）")
		case !nc.IsClaudeAPI() && nc.IsAPIKey() && e.cred.KiroAPIKey != "" && e.cred.KiroAPIKey == nc.KiroAPIKey:
			m.mu.Unlock()
			return 0, errors.New("凭据已存在（kiroApiKey 重复）")
		case !nc.IsClaudeAPI() && !nc.IsAPIKey() && e.cred.RefreshToken != "" && e.cred.RefreshToken == nc.RefreshToken:
			m.mu.Unlock()
			return 0, errors.New("凭据已存在（refreshToken 重复）")
		}
	}
	m.mu.Unlock()

	validated := nc
	if !nc.IsClaudeAPI() && !nc.IsAPIKey() {
		v, err := RefreshToken(ctx, nc, m.cfg, nc.EffectiveProxy(m.proxy))
		if err != nil {
			return 0, err
		}
		validated = v
	}
	EnsurePersistentMachineID(validated)
	validated.Disabled = false

	m.mu.Lock()
	var maxID uint64
	for _, e := range m.entries {
		if e.id > maxID {
			maxID = e.id
		}
	}
	validated.ID = maxID + 1
	m.entries = append(m.entries, &entry{id: validated.ID, cred: validated})
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		return 0, err
	}
	slog.Info("成功添加凭据", "credential_id", validated.ID)
	return validated.ID, nil
}

// DeleteCredential 只能删已禁用的凭据。
func (m *Manager) DeleteCredential(id uint64) error {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return notFound(id)
	}
	if !e.disabled {
		m.mu.Unlock()
		return fmt.Errorf("只能删除已禁用的凭据（请先禁用凭据 #%d）", id)
	}
	kept := m.entries[:0]
	for _, x := range m.entries {
		if x.id != id {
			kept = append(kept, x)
		}
	}
	m.entries = kept
	for g, v := range m.groupSticky {
		if v == id {
			delete(m.groupSticky, g)
		}
	}
	if m.currentID == id || len(m.entries) == 0 {
		m.currentID = m.highestPriorityLocked(0)
	}
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		return err
	}
	m.SaveStats()
	slog.Info("已删除凭据", "credential_id", id)
	return nil
}

// ForceRefresh 无条件刷新 token。
func (m *Manager) ForceRefresh(ctx context.Context, id uint64) error {
	cred, ok := m.CredentialsOf(id)
	if !ok {
		return notFound(id)
	}
	if cred.IsClaudeAPI() {
		return errors.New("Claude API 账号没有 Token 可刷新")
	}
	_, err := m.refreshIfNeeded(ctx, id, true)
	return err
}

// LoadBalancingMode 当前模式。
func (m *Manager) LoadBalancingMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// SetLoadBalancingMode 写回 config.json，失败时回滚。
func (m *Manager) SetLoadBalancingMode(mode string) error {
	if mode != "priority" && mode != "balanced" {
		return fmt.Errorf("无效的负载均衡模式: %s", mode)
	}
	m.mu.Lock()
	prev := m.mode
	if prev == mode {
		m.mu.Unlock()
		return nil
	}
	m.mode = mode
	m.mu.Unlock()
	if p := m.cfg.Path(); p != "" {
		c, err := config.Load(p)
		if err == nil {
			c.LoadBalancingMode = mode
			err = c.Save()
		}
		if err != nil {
			m.mu.Lock()
			m.mode = prev
			m.mu.Unlock()
			return fmt.Errorf("持久化负载均衡模式失败: %w", err)
		}
	}
	m.cfg.LoadBalancingMode = mode
	slog.Info("负载均衡模式已设置", "mode", mode)
	return nil
}
