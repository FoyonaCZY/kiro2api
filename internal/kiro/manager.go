package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
)

// MaxFailuresPerCredential 连续失败阈值。
const MaxFailuresPerCredential = 3

const statsDebounce = 30 * time.Second

// DisabledReason 禁用原因。
type DisabledReason string

const (
	ReasonNone                DisabledReason = ""
	ReasonManual              DisabledReason = "Manual"
	ReasonTooManyFailures     DisabledReason = "TooManyFailures"
	ReasonTooManyRefreshFails DisabledReason = "TooManyRefreshFailures"
	ReasonQuotaExceeded       DisabledReason = "QuotaExceeded"
	ReasonInvalidRefreshToken DisabledReason = "InvalidRefreshToken"
	ReasonInvalidConfig       DisabledReason = "InvalidConfig"
)

type entry struct {
	id                  uint64
	cred                *Credentials
	failureCount        uint32
	refreshFailureCount uint32
	disabled            bool
	reason              DisabledReason
	successCount        uint64
	lastUsedAt          string
	cooldownUntil       time.Time
}

func isOpus(model string) bool { return strings.Contains(strings.ToLower(model), "opus") }

func (e *entry) supportsModel(model string) bool {
	if e.cred.IsClaudeAPI() {
		return !e.disabled
	}
	return !e.disabled && (!isOpus(model) || e.cred.SupportsOpus())
}

func (e *entry) available(model string, now time.Time) bool {
	return e.supportsModel(model) && !e.cooldownUntil.After(now)
}

// CoolingDownError 所有合适账号都在限流冷却。
type CoolingDownError struct{ RetryAfter time.Duration }

// Seconds 向上取整，至少 1 秒。
func (e *CoolingDownError) Seconds() int64 {
	s := int64(math.Ceil(e.RetryAfter.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

func (e *CoolingDownError) Error() string {
	return fmt.Sprintf("可用凭据暂时被限流，请在 %d 秒后重试", e.Seconds())
}

// CallContext 一次调用绑定的凭据。
type CallContext struct {
	ID    uint64
	Cred  *Credentials
	Token string
}

// Manager 多凭据管理和调度。
type Manager struct {
	cfg     *config.Config
	proxyMu sync.RWMutex
	proxy   *httpx.Proxy

	mu          sync.Mutex
	entries     []*entry
	maxID       uint64 // 用过的最大 id，删掉的账号也算，新账号不复用
	currentID   uint64
	groupSticky map[string]uint64
	mode        string

	refreshMu sync.Mutex

	dir   string
	store *Store

	statsMu     sync.Mutex
	lastStatsAt time.Time
	statsDirty  bool
}

// LoadStartupCredentials 库里已有数据时以库为准；空库时从 credentials.json 导入。
func LoadStartupCredentials(path string) ([]*Credentials, error) {
	store, err := OpenStore(dirOf(path))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	if store.IsEmpty() {
		list, _, err := LoadCredentialsFile(path)
		if err != nil {
			return nil, err
		}
		slog.Info("凭据数据库为空，准备从 JSON 导入", "path", path, "count", len(list))
		return list, nil
	}
	list, err := store.Load()
	if err != nil {
		return nil, err
	}
	slog.Info("已从数据库加载凭据，JSON 保留作备份", "count", len(list), "path", path)
	return list, nil
}

func dirOf(path string) string {
	d := filepath.Dir(path)
	if d == "" {
		return "."
	}
	return d
}

// NewManager credentialsPath 为空时不持久化（测试用）。
func NewManager(cfg *config.Config, creds []*Credentials, proxy *httpx.Proxy, credentialsPath string) (*Manager, error) {
	var maxID uint64
	for _, c := range creds {
		if c.ID > maxID {
			maxID = c.ID
		}
	}
	next := maxID + 1
	changed := false
	seen := map[uint64]bool{}
	var dup []uint64
	entries := make([]*entry, 0, len(creds))
	for _, c := range creds {
		c = c.Clone()
		c.Canonicalize()
		if c.ID == 0 {
			c.ID = next
			next++
			changed = true
		}
		if EnsurePersistentMachineID(c) {
			changed = true
		}
		if seen[c.ID] {
			dup = append(dup, c.ID)
		}
		seen[c.ID] = true
		e := &entry{id: c.ID, cred: c, disabled: c.Disabled}
		if c.Disabled {
			e.reason = ReasonManual
		}
		m := strings.ToLower(c.AuthMethod)
		if c.KiroAPIKey == "" && (m == "api_key" || m == "apikey") {
			slog.Warn("凭据配置了 authMethod=api_key 但缺少 kiroApiKey，已自动禁用", "credential_id", c.ID)
			e.disabled, e.reason = true, ReasonInvalidConfig
		}
		entries = append(entries, e)
	}
	if len(dup) > 0 {
		return nil, fmt.Errorf("检测到重复的凭据 ID: %v", dup)
	}
	m := &Manager{cfg: cfg, proxy: proxy, entries: entries, groupSticky: map[string]uint64{}, mode: cfg.LoadBalancingMode, maxID: next - 1}
	m.currentID = m.highestPriorityLocked(0)
	if credentialsPath != "" {
		m.dir = dirOf(credentialsPath)
		store, err := OpenStore(m.dir)
		if err != nil {
			return nil, err
		}
		m.store = store
		m.maxID = max(m.maxID, store.MaxID())
		importing := store.IsEmpty()
		if importing || changed {
			if err := m.persist(); err != nil {
				slog.Warn("写入凭据数据库失败", "err", err)
			} else if importing {
				slog.Info("凭据数据库为空，已导入，之后以数据库为准", "count", len(entries))
			}
		}
		m.loadStats()
	}
	return m, nil
}

// Config 当前配置。
func (m *Manager) Config() *config.Config { return m.cfg }

// GlobalProxy 全局代理。
func (m *Manager) GlobalProxy() *httpx.Proxy {
	m.proxyMu.RLock()
	defer m.proxyMu.RUnlock()
	return m.proxy
}

// Dir 数据目录（凭据文件所在目录）。
func (m *Manager) Dir() string { return m.dir }

// TotalCount 凭据总数。
func (m *Manager) TotalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// CredentialIDs 升序 id。
func (m *Manager) CredentialIDs() []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]uint64, 0, len(m.entries))
	for _, e := range m.entries {
		ids = append(ids, e.id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// AvailableCount 可用凭据数。
func (m *Manager) AvailableCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	n := 0
	for _, e := range m.entries {
		if e.available("", now) {
			n++
		}
	}
	return n
}

func (m *Manager) find(id uint64) *entry {
	for _, e := range m.entries {
		if e.id == id {
			return e
		}
	}
	return nil
}

// highestPriorityLocked 未禁用里 priority 最小的，exclude 非 0 时排除。
func (m *Manager) highestPriorityLocked(exclude uint64) uint64 {
	var best *entry
	for _, e := range m.entries {
		if e.disabled || (exclude != 0 && e.id == exclude) {
			continue
		}
		if best == nil || e.cred.Priority < best.cred.Priority {
			best = e
		}
	}
	if best == nil {
		return 0
	}
	return best.id
}

func (m *Manager) anyEnabledLocked() bool {
	for _, e := range m.entries {
		if !e.disabled {
			return true
		}
	}
	return false
}

// ReportRateLimited 限流冷却，不累计失败；并发请求不会缩短已有窗口。
func (m *Manager) ReportRateLimited(id uint64, delay time.Duration) {
	if delay < time.Second {
		delay = time.Second
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.find(id); e != nil {
		until := time.Now().Add(delay)
		if until.After(e.cooldownUntil) {
			e.cooldownUntil = until
		}
		slog.Warn("凭据暂时限流，进入冷却", "credential_id", id, "cooldown_seconds", int(delay.Seconds()))
	}
}

func (m *Manager) selectNextLocked(ctx context.Context, model string) *entry {
	now := time.Now()
	var best *entry
	balanced := m.mode == "balanced"
	for _, e := range m.entries {
		if !e.available(model, now) || !access.CredentialAllowed(ctx, e.id) {
			continue
		}
		if best == nil {
			best = e
			continue
		}
		if balanced {
			if e.successCount < best.successCount || (e.successCount == best.successCount && e.cred.Priority < best.cred.Priority) {
				best = e
			}
		} else if e.cred.Priority < best.cred.Priority {
			best = e
		}
	}
	return best
}

// AcquireContext 选号并保证 token 可用。刷新失败会累计，达到阈值禁用并换号。
func (m *Manager) AcquireContext(ctx context.Context, model string) (*CallContext, error) {
	total := m.TotalCount()
	maxAttempts := total * MaxFailuresPerCredential
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 0; ; {
		if attempt >= maxAttempts {
			return nil, fmt.Errorf("所有凭据均无法获取有效 Token（可用: %d/%d）", m.AvailableCount(), total)
		}
		id, cred, err := m.pick(ctx, model, total)
		if err != nil {
			return nil, err
		}
		cc, err := m.ensureToken(ctx, id, cred)
		if err == nil {
			return cc, nil
		}
		var inv *RefreshTokenInvalidError
		var hasAvailable bool
		if errors.As(err, &inv) {
			slog.Warn("凭据 refreshToken 永久失效", "credential_id", id, "err", err)
			hasAvailable = m.ReportRefreshTokenInvalid(id)
		} else {
			slog.Warn("凭据 Token 刷新失败", "credential_id", id, "err", err)
			hasAvailable = m.ReportRefreshFailure(id)
		}
		attempt++
		if !hasAvailable {
			return nil, fmt.Errorf("所有凭据均已禁用（0/%d）", total)
		}
	}
}

func (m *Manager) pick(ctx context.Context, model string, total int) (uint64, *Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	group, scoped := access.CurrentGroup(ctx)
	if m.mode != "balanced" {
		cur := m.currentID
		if scoped {
			cur = m.groupSticky[group]
		}
		if e := m.find(cur); e != nil && e.available(model, time.Now()) && access.CredentialAllowed(ctx, e.id) {
			return e.id, e.cred.Clone(), nil
		}
	}
	best := m.selectNextLocked(ctx, model)
	if best == nil {
		// 这一组全因连续失败被自动禁用时，只把这一组重置，相当于重启自愈。
		anySupported, anyAuto := false, false
		for _, e := range m.entries {
			if !access.CredentialAllowed(ctx, e.id) {
				continue
			}
			if e.supportsModel(model) {
				anySupported = true
			}
			if e.disabled && e.reason == ReasonTooManyFailures {
				anyAuto = true
			}
		}
		if !anySupported && anyAuto {
			slog.Warn("当前分组的凭据均已被自动禁用，只重置这一组")
			for _, e := range m.entries {
				if access.CredentialAllowed(ctx, e.id) && e.reason == ReasonTooManyFailures {
					e.disabled, e.reason, e.failureCount = false, ReasonNone, 0
				}
			}
			best = m.selectNextLocked(ctx, model)
		}
	}
	if best == nil {
		now := time.Now()
		var retry time.Duration
		for _, e := range m.entries {
			if e.supportsModel(model) && access.CredentialAllowed(ctx, e.id) && e.cooldownUntil.After(now) {
				if d := e.cooldownUntil.Sub(now); retry == 0 || d < retry {
					retry = d
				}
			}
		}
		if retry > 0 {
			return 0, nil, &CoolingDownError{RetryAfter: retry}
		}
		enabled := 0
		for _, e := range m.entries {
			if !e.disabled {
				enabled++
			}
		}
		if enabled == 0 {
			return 0, nil, fmt.Errorf("所有凭据均已禁用（%d/%d）", enabled, total)
		}
		return 0, nil, fmt.Errorf("没有可用且支持请求模型的凭据（已启用: %d/%d）", enabled, total)
	}
	if scoped {
		m.groupSticky[group] = best.id
	} else {
		m.currentID = best.id
	}
	return best.id, best.cred.Clone(), nil
}

func (m *Manager) snapshotCred(id uint64) (*Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil {
		return nil, fmt.Errorf("凭据 #%d 不存在", id)
	}
	return e.cred.Clone(), nil
}

func (m *Manager) ensureToken(ctx context.Context, id uint64, cred *Credentials) (*CallContext, error) {
	if cred.IsClaudeAPI() {
		if cred.ClaudeAPIKey == "" {
			return nil, errors.New("Claude API 账号缺少 API Key")
		}
		return &CallContext{ID: id, Cred: cred, Token: cred.ClaudeAPIKey}, nil
	}
	if cred.IsAPIKey() {
		if cred.KiroAPIKey == "" {
			return nil, errors.New("API Key 凭据缺少 kiroApiKey")
		}
		return &CallContext{ID: id, Cred: cred, Token: cred.KiroAPIKey}, nil
	}
	creds := cred
	if TokenExpired(cred) || TokenExpiringSoon(cred) {
		fresh, err := m.refreshIfNeeded(ctx, id, false)
		if err != nil {
			return nil, err
		}
		creds = fresh
	}
	if creds.AccessToken == "" {
		return nil, errors.New("没有可用的 accessToken")
	}
	m.mu.Lock()
	if e := m.find(id); e != nil {
		e.refreshFailureCount = 0
	}
	m.mu.Unlock()
	return &CallContext{ID: id, Cred: creds, Token: creds.AccessToken}, nil
}

// refreshIfNeeded 双重检查：拿到刷新锁后再看一次，别的请求可能已经刷过。force 时无条件刷新。
func (m *Manager) refreshIfNeeded(ctx context.Context, id uint64, force bool) (*Credentials, error) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	cur, err := m.snapshotCred(id)
	if err != nil {
		return nil, err
	}
	if !force && !TokenExpired(cur) && !TokenExpiringSoon(cur) {
		slog.Debug("Token 已被其他请求刷新，跳过刷新", "credential_id", id)
		return cur, nil
	}
	fresh, err := RefreshToken(ctx, cur, m.cfg, cur.EffectiveProxy(m.GlobalProxy()))
	if err != nil {
		return nil, err
	}
	if !force && TokenExpired(fresh) {
		return nil, errors.New("刷新后的 Token 仍然无效或已过期")
	}
	m.mu.Lock()
	if e := m.find(id); e != nil {
		e.cred = fresh.Clone()
		if force {
			e.refreshFailureCount = 0
		}
	}
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		slog.Warn("Token 刷新后持久化失败（不影响本次请求）", "err", err)
	}
	return fresh, nil
}

// persist 把全部凭据写库，disabled 同步到凭据对象。
func (m *Manager) persist() error {
	if m.store == nil {
		return nil
	}
	m.mu.Lock()
	list := make([]*Credentials, 0, len(m.entries))
	for _, e := range m.entries {
		c := e.cred.Clone()
		c.Canonicalize()
		c.Disabled = e.disabled
		list = append(list, c)
	}
	maxID := m.maxID
	m.mu.Unlock()
	if err := m.store.Save(list); err != nil {
		return err
	}
	return m.store.SetMaxID(maxID)
}

type statsEntry struct {
	SuccessCount uint64  `json:"success_count"`
	LastUsedAt   *string `json:"last_used_at"`
}

func (m *Manager) statsPath() string {
	if m.dir == "" {
		return ""
	}
	return filepath.Join(m.dir, "kiro_stats.json")
}

func (m *Manager) loadStats() {
	p := m.statsPath()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var stats map[string]statsEntry
	if err := json.Unmarshal(data, &stats); err != nil {
		slog.Warn("解析统计缓存失败，将忽略", "err", err)
		return
	}
	m.mu.Lock()
	for _, e := range m.entries {
		if s, ok := stats[strconv.FormatUint(e.id, 10)]; ok {
			e.successCount = s.SuccessCount
			if s.LastUsedAt != nil {
				e.lastUsedAt = *s.LastUsedAt
			}
		}
	}
	m.mu.Unlock()
	m.statsMu.Lock()
	m.lastStatsAt, m.statsDirty = time.Now(), false
	m.statsMu.Unlock()
	slog.Info("已从缓存加载统计数据", "count", len(stats))
}

// SaveStats 立即落盘。
func (m *Manager) SaveStats() {
	p := m.statsPath()
	if p == "" {
		return
	}
	m.mu.Lock()
	stats := make(map[string]statsEntry, len(m.entries))
	for _, e := range m.entries {
		se := statsEntry{SuccessCount: e.successCount}
		if e.lastUsedAt != "" {
			v := e.lastUsedAt
			se.LastUsedAt = &v
		}
		stats[strconv.FormatUint(e.id, 10)] = se
	}
	m.mu.Unlock()
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		slog.Warn("保存统计缓存失败", "err", err)
		return
	}
	m.statsMu.Lock()
	m.lastStatsAt, m.statsDirty = time.Now(), false
	m.statsMu.Unlock()
}

func (m *Manager) saveStatsDebounced() {
	m.statsMu.Lock()
	m.statsDirty = true
	flush := m.lastStatsAt.IsZero() || time.Since(m.lastStatsAt) >= statsDebounce
	m.statsMu.Unlock()
	if flush {
		m.SaveStats()
	}
}

// FlushStats 退出前把未落盘的统计写掉。
func (m *Manager) FlushStats() {
	m.statsMu.Lock()
	dirty := m.statsDirty
	m.statsMu.Unlock()
	if dirty {
		m.SaveStats()
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ReportSuccess 清零失败计数、累计成功。
func (m *Manager) ReportSuccess(id uint64) {
	m.mu.Lock()
	if e := m.find(id); e != nil {
		e.failureCount, e.refreshFailureCount = 0, 0
		e.successCount++
		e.lastUsedAt = nowRFC3339()
	}
	m.mu.Unlock()
	m.saveStatsDebounced()
}

// disableLocked 禁用并切到最高优先级的可用凭据，返回是否还有可用凭据。
func (m *Manager) disableLocked(e *entry, reason DisabledReason) bool {
	e.disabled, e.reason = true, reason
	if next := m.highestPriorityLocked(0); next != 0 {
		m.currentID = next
		slog.Info("已切换到凭据", "credential_id", next)
		return true
	}
	slog.Error("所有凭据均已禁用！")
	return false
}

// ReportFailure 累计失败，达到阈值禁用。返回是否还有可用凭据。
func (m *Manager) ReportFailure(id uint64) bool {
	m.mu.Lock()
	e := m.find(id)
	if e == nil || e.disabled {
		ok := m.anyEnabledLocked()
		m.mu.Unlock()
		return ok
	}
	e.failureCount++
	e.lastUsedAt = nowRFC3339()
	slog.Warn(fmt.Sprintf("凭据 #%d API 调用失败（%d/%d）", id, e.failureCount, MaxFailuresPerCredential))
	if e.failureCount >= MaxFailuresPerCredential {
		slog.Error(fmt.Sprintf("凭据 #%d 已连续失败 %d 次，已被禁用", id, e.failureCount))
		m.disableLocked(e, ReasonTooManyFailures)
	}
	ok := m.anyEnabledLocked()
	m.mu.Unlock()
	m.saveStatsDebounced()
	return ok
}

// ReportQuotaExhausted 额度用尽立即禁用。
func (m *Manager) ReportQuotaExhausted(id uint64) bool {
	m.mu.Lock()
	e := m.find(id)
	if e == nil || e.disabled {
		ok := m.anyEnabledLocked()
		m.mu.Unlock()
		return ok
	}
	e.lastUsedAt = nowRFC3339()
	e.failureCount = MaxFailuresPerCredential
	slog.Error(fmt.Sprintf("凭据 #%d 额度已用尽（MONTHLY_REQUEST_COUNT），已被禁用", id))
	ok := m.disableLocked(e, ReasonQuotaExceeded)
	m.mu.Unlock()
	m.saveStatsDebounced()
	return ok
}

// ReportRefreshFailure 刷新失败累计，达到阈值禁用。
func (m *Manager) ReportRefreshFailure(id uint64) bool {
	m.mu.Lock()
	e := m.find(id)
	if e == nil || e.disabled {
		ok := m.anyEnabledLocked()
		m.mu.Unlock()
		return ok
	}
	e.lastUsedAt = nowRFC3339()
	e.refreshFailureCount++
	slog.Warn(fmt.Sprintf("凭据 #%d Token 刷新失败（%d/%d）", id, e.refreshFailureCount, MaxFailuresPerCredential))
	ok := true
	if e.refreshFailureCount >= MaxFailuresPerCredential {
		slog.Error(fmt.Sprintf("凭据 #%d Token 已连续刷新失败 %d 次，已被禁用", id, e.refreshFailureCount))
		ok = m.disableLocked(e, ReasonTooManyRefreshFails)
	} else {
		ok = m.anyEnabledLocked()
	}
	m.mu.Unlock()
	m.saveStatsDebounced()
	return ok
}

// ReportRefreshTokenInvalid invalid_grant 立即禁用。
func (m *Manager) ReportRefreshTokenInvalid(id uint64) bool {
	m.mu.Lock()
	e := m.find(id)
	if e == nil || e.disabled {
		ok := m.anyEnabledLocked()
		m.mu.Unlock()
		return ok
	}
	e.lastUsedAt = nowRFC3339()
	slog.Error(fmt.Sprintf("凭据 #%d refreshToken 已失效 (invalid_grant)，已立即禁用", id))
	ok := m.disableLocked(e, ReasonInvalidRefreshToken)
	m.mu.Unlock()
	m.saveStatsDebounced()
	return ok
}

// SwitchToNext 换到当前以外优先级最高的凭据；分组请求同时改掉分组的粘滞账号。
func (m *Manager) SwitchToNext(ctx context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.currentID
	if g, ok := access.CurrentGroup(ctx); ok {
		cur = m.groupSticky[g]
	}
	if next := m.highestPriorityLocked(cur); next != 0 {
		m.currentID = next
		if g, ok := access.CurrentGroup(ctx); ok {
			m.groupSticky[g] = next
		}
		slog.Info(fmt.Sprintf("已切换到凭据 #%d", next))
		return true
	}
	e := m.find(cur)
	return e != nil && !e.disabled
}
