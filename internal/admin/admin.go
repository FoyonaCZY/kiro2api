// Package admin 管理 API：凭据、负载均衡、Social 登录、用量、接入 Key 和分组。路径与 kiro.rs 一致。
package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/anthropic"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/FoyonaCZY/kiro2api/internal/usage"
)

const balanceTTL = 5 * time.Minute

// Service 管理服务。
type Service struct {
	APIKey    string
	TM        *kiro.Manager
	Endpoints map[string]kiro.Endpoint
	Usage     *usage.Log
	Access    *access.Store

	social    *socialStore
	balMu     sync.Mutex
	balances  map[uint64]cachedBalance
	cachePath string
}

type cachedBalance struct {
	CachedAt float64 `json:"cached_at"`
	Data     Balance `json:"data"`
}

// Balance 余额。
type Balance struct {
	ID                uint64   `json:"id"`
	SubscriptionTitle *string  `json:"subscriptionTitle"`
	CurrentUsage      float64  `json:"currentUsage"`
	UsageLimit        float64  `json:"usageLimit"`
	Remaining         float64  `json:"remaining"`
	UsagePercentage   float64  `json:"usagePercentage"`
	NextResetAt       *float64 `json:"nextResetAt"`
}

// NewService 创建服务，加载余额缓存。
func NewService(apiKey string, tm *kiro.Manager, eps map[string]kiro.Endpoint, ul *usage.Log, acc *access.Store) *Service {
	s := &Service{APIKey: apiKey, TM: tm, Endpoints: eps, Usage: ul, Access: acc, social: newSocialStore(), balances: map[uint64]cachedBalance{}}
	if tm.Dir() != "" {
		s.cachePath = filepath.Join(tm.Dir(), "kiro_balance_cache.json")
		s.loadBalanceCache()
	}
	return s
}

func (s *Service) loadBalanceCache() {
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return
	}
	var m map[string]cachedBalance
	if err := json.Unmarshal(data, &m); err != nil {
		slog.Warn("解析余额缓存失败，将忽略", "err", err)
		return
	}
	now := float64(time.Now().Unix())
	for k, v := range m {
		if id, err := strconv.ParseUint(k, 10, 64); err == nil && now-v.CachedAt < balanceTTL.Seconds() {
			s.balances[id] = v
		}
	}
}

// SaveBalanceCache 持锁写，防止并发损坏。
func (s *Service) SaveBalanceCache() {
	if s.cachePath == "" {
		return
	}
	s.balMu.Lock()
	defer s.balMu.Unlock()
	m := make(map[string]cachedBalance, len(s.balances))
	for k, v := range s.balances {
		m[strconv.FormatUint(k, 10)] = v
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(s.cachePath, b, 0o600); err != nil {
		slog.Warn("保存余额缓存失败", "err", err)
	}
}

func (s *Service) labels() []usage.Label {
	var out []usage.Label
	for _, e := range s.TM.Snapshot().Entries {
		l := "#" + strconv.FormatUint(e.ID, 10)
		if e.Email != nil && strings.TrimSpace(*e.Email) != "" {
			l = strings.TrimSpace(*e.Email)
		}
		out = append(out, usage.Label{ID: e.ID, Label: l})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BackfillAccountUsage 按邮箱把升级前的请求补进账号累计。
func (s *Service) BackfillAccountUsage() int {
	emails := map[string]uint64{}
	for _, l := range s.labels() {
		if !strings.HasPrefix(l.Label, "#") {
			emails[l.Label] = l.ID
		}
	}
	return s.Usage.BackfillAccounts(emails)
}

// ---- HTTP ----

type svcErr struct {
	status int
	typ    string
	msg    string
}

func (e *svcErr) Error() string { return e.msg }

func notFound(id uint64) *svcErr {
	return &svcErr{http.StatusNotFound, "not_found", fmt.Sprintf("凭据不存在: %d", id)}
}
func invalid(msg string) *svcErr {
	return &svcErr{http.StatusBadRequest, "invalid_request", "凭据无效: " + msg}
}
func upstream(msg string) *svcErr {
	return &svcErr{http.StatusBadGateway, "api_error", "上游服务错误: " + msg}
}
func internal(msg string) *svcErr {
	return &svcErr{http.StatusInternalServerError, "internal_error", "内部错误: " + msg}
}

func writeSvcErr(w http.ResponseWriter, e *svcErr) {
	anthropic.WriteJSON(w, e.status, map[string]any{"error": map[string]any{"type": e.typ, "message": e.msg}})
}

func ok(w http.ResponseWriter, msg string) {
	anthropic.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "message": msg})
}

func plainErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		plainErr(w, http.StatusUnprocessableEntity, "请求体不是合法 JSON: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		plainErr(w, http.StatusBadRequest, "无效的 id")
		return 0, false
	}
	return id, true
}

func classifySimple(err error, id uint64) *svcErr {
	if errors.Is(err, kiro.ErrNotFound) || strings.Contains(err.Error(), "不存在") {
		return notFound(id)
	}
	return internal(err.Error())
}

func classifyBalance(err error, id uint64) *svcErr {
	msg := err.Error()
	switch {
	case errors.Is(err, kiro.ErrNotFound) || strings.Contains(msg, "不存在"):
		return notFound(id)
	case strings.Contains(msg, "API Key 凭据不支持刷新"), strings.Contains(msg, "Claude API 账号"):
		return invalid(msg)
	}
	for _, k := range []string{"凭证已过期或无效", "权限不足", "已被限流", "服务器错误", "Token 刷新失败", "暂时不可用", "connect", "connection", "timeout", "timed out", "EOF", "no such host"} {
		if strings.Contains(msg, k) {
			return upstream(msg)
		}
	}
	return internal(msg)
}

func classifyAdd(err error) *svcErr {
	msg := err.Error()
	for _, k := range []string{"缺少 refreshToken", "refreshToken 为空", "refreshToken 已被截断", "凭据已存在", "refreshToken 重复", "kiroApiKey 重复", "缺少 kiroApiKey", "kiroApiKey 为空", "凭证已过期或无效", "权限不足", "已被限流", "Claude API", "代理"} {
		if strings.Contains(msg, k) {
			return invalid(msg)
		}
	}
	for _, k := range []string{"connect", "connection", "timeout", "EOF", "no such host"} {
		if strings.Contains(msg, k) {
			return upstream(msg)
		}
	}
	return internal(msg)
}

// Routes 挂在 /api/admin 下，需要 adminApiKey。
func (s *Service) Routes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.auth(f)) }
	p := "/api/admin"
	h("GET "+p+"/credentials", s.listCredentials)
	h("POST "+p+"/credentials", s.addCredential)
	h("PUT "+p+"/credentials/{id}", s.updateCredential)
	h("DELETE "+p+"/credentials/{id}", s.deleteCredential)
	h("POST "+p+"/credentials/{id}/disabled", s.setDisabled)
	h("POST "+p+"/credentials/{id}/priority", s.setPriority)
	h("POST "+p+"/credentials/{id}/reset", s.reset)
	h("POST "+p+"/credentials/{id}/refresh", s.forceRefresh)
	h("GET "+p+"/credentials/{id}/balance", s.balance)
	h("GET "+p+"/config/load-balancing", s.getMode)
	h("PUT "+p+"/config/load-balancing", s.setMode)
	h("GET "+p+"/config/proxy", s.getProxy)
	h("PUT "+p+"/config/proxy", s.setProxy)
	h("POST "+p+"/social-login/start", s.socialStart)
	h("POST "+p+"/social-login/complete", s.socialComplete)
	h("GET "+p+"/usage/requests", func(w http.ResponseWriter, _ *http.Request) { anthropic.WriteJSON(w, 200, s.Usage.List(200)) })
	h("GET "+p+"/usage/requests/{id}", s.getRequest)
	h("GET "+p+"/usage/summary", func(w http.ResponseWriter, _ *http.Request) { anthropic.WriteJSON(w, 200, s.Usage.Summary()) })
	h("GET "+p+"/usage/prices", func(w http.ResponseWriter, _ *http.Request) { anthropic.WriteJSON(w, 200, s.Usage.Prices()) })
	h("POST "+p+"/usage/prices", s.upsertPrice)
	h("DELETE "+p+"/usage/prices/{id}", s.deletePrice)
	h("GET "+p+"/usage/accounts", func(w http.ResponseWriter, _ *http.Request) {
		anthropic.WriteJSON(w, 200, s.Usage.AccountReport(s.labels()))
	})
	h("PUT "+p+"/usage/accounts/{id}/cost", s.setAccountCost)
	h("POST "+p+"/usage/accounts/{id}/archive", s.archiveAccountUsage)
	h("GET "+p+"/access/keys", func(w http.ResponseWriter, _ *http.Request) {
		keys := s.Access.ListKeys()
		if keys == nil {
			keys = []access.Key{}
		}
		anthropic.WriteJSON(w, 200, keys)
	})
	h("POST "+p+"/access/keys", s.createKey)
	h("PUT "+p+"/access/keys/{id}", s.updateKey)
	h("DELETE "+p+"/access/keys/{id}", s.deleteKey)
	h("GET "+p+"/access/groups", s.listGroups)
	h("POST "+p+"/access/groups", s.createGroup)
	h("PUT "+p+"/access/groups/{id}", s.renameGroup)
	h("DELETE "+p+"/access/groups/{id}", s.deleteGroup)
	h("PUT "+p+"/access/groups/{id}/members", s.setMembers)
}

func (s *Service) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := anthropic.ExtractAPIKey(r)
		if !ok || subtle.ConstantTimeCompare([]byte(key), []byte(s.APIKey)) != 1 {
			writeSvcErr(w, &svcErr{http.StatusUnauthorized, "authentication_error", "Invalid or missing admin API key"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CredentialItem 列表项，字段与 kiro.rs 相同。
type CredentialItem struct {
	kiro.EntrySnapshot
	IsCurrent          bool    `json:"isCurrent"`
	Endpoint           string  `json:"endpoint"`
	ConfiguredEndpoint *string `json:"configuredEndpoint,omitempty"`
}

func (s *Service) listCredentials(w http.ResponseWriter, _ *http.Request) {
	snap := s.TM.Snapshot()
	def := s.TM.Config().DefaultEndpoint
	items := make([]CredentialItem, 0, len(snap.Entries))
	for _, e := range snap.Entries {
		it := CredentialItem{EntrySnapshot: e, IsCurrent: e.ID == snap.CurrentID}
		method := ""
		if e.AuthMethod != nil {
			method = strings.ToLower(*e.AuthMethod)
		}
		if method != "claude_api" {
			it.Endpoint = kiro.EffectiveEndpointName(e.Endpoint, def, e.APIKeyHash != nil || method == "api_key" || method == "apikey")
		}
		if e.Endpoint != "" {
			v := e.Endpoint
			it.ConfiguredEndpoint = &v
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Priority < items[j].Priority })
	anthropic.WriteJSON(w, 200, map[string]any{"total": snap.Total, "available": snap.Available, "currentId": snap.CurrentID, "credentials": items})
}

func (s *Service) setDisabled(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if !okID || !decode(w, r, &body) {
		return
	}
	cur := s.TM.Snapshot().CurrentID
	if err := s.TM.SetDisabled(id, body.Disabled); err != nil {
		writeSvcErr(w, classifySimple(err, id))
		return
	}
	if body.Disabled && id == cur {
		s.TM.SwitchToNext(context.Background())
	}
	action := "启用"
	if body.Disabled {
		action = "禁用"
	}
	ok(w, fmt.Sprintf("凭据 #%d 已%s", id, action))
}

func (s *Service) setPriority(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	var body struct {
		Priority *uint32 `json:"priority"`
	}
	if !okID || !decode(w, r, &body) {
		return
	}
	if body.Priority == nil {
		plainErr(w, http.StatusUnprocessableEntity, "缺少 priority")
		return
	}
	if err := s.TM.SetPriority(id, *body.Priority); err != nil {
		writeSvcErr(w, classifySimple(err, id))
		return
	}
	ok(w, fmt.Sprintf("凭据 #%d 优先级已设置为 %d", id, *body.Priority))
}

type updateReq struct {
	Email         *string `json:"email"`
	Priority      *uint32 `json:"priority"`
	Proxy         *string `json:"proxy"`
	ProxyURL      *string `json:"proxyUrl"`
	ProxyUsername *string `json:"proxyUsername"`
	ProxyPassword *string `json:"proxyPassword"`
	Endpoint      *string `json:"endpoint"`
	Region        *string `json:"region"`
	AuthRegion    *string `json:"authRegion"`
	APIRegion     *string `json:"apiRegion"`
	RefreshToken  *string `json:"refreshToken"`
	ClientID      *string `json:"clientId"`
	ClientSecret  *string `json:"clientSecret"`
	MachineID     *string `json:"machineId"`
	ProfileArn    *string `json:"profileArn"`
	ClaudeBaseURL *string `json:"claudeBaseUrl"`
	ClaudeAPIKey  *string `json:"claudeApiKey"`
}

func (s *Service) knownEndpoint(name string) bool { _, ok := s.Endpoints[name]; return ok }

func (s *Service) updateCredential(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	var b updateReq
	if !okID || !decode(w, r, &b) {
		return
	}
	if b.Endpoint != nil {
		if n := strings.TrimSpace(*b.Endpoint); n != "" && !s.knownEndpoint(n) {
			writeSvcErr(w, invalid(fmt.Sprintf("未知端点 \"%s\"", n)))
			return
		}
	}
	u := &kiro.Update{Email: b.Email, Priority: b.Priority, ProxyLine: b.Proxy, ProxyURL: b.ProxyURL, ProxyUsername: b.ProxyUsername,
		ProfileArn:    b.ProfileArn,
		ProxyPassword: b.ProxyPassword, Endpoint: b.Endpoint, Region: b.Region, AuthRegion: b.AuthRegion, APIRegion: b.APIRegion,
		RefreshToken: b.RefreshToken, ClientID: b.ClientID, ClientSecret: b.ClientSecret, MachineID: b.MachineID,
		ClaudeBaseURL: b.ClaudeBaseURL, ClaudeAPIKey: b.ClaudeAPIKey}
	if err := s.TM.UpdateCredential(id, u); err != nil {
		if errors.Is(err, kiro.ErrNotFound) {
			writeSvcErr(w, notFound(id))
		} else {
			writeSvcErr(w, invalid(err.Error()))
		}
		return
	}
	ok(w, fmt.Sprintf("凭据 #%d 已更新", id))
}

func (s *Service) reset(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if err := s.TM.ResetAndEnable(id); err != nil {
		writeSvcErr(w, classifySimple(err, id))
		return
	}
	ok(w, fmt.Sprintf("凭据 #%d 失败计数已重置并重新启用", id))
}

func (s *Service) isClaude(id uint64) bool {
	c, found := s.TM.CredentialsOf(id)
	return found && c.IsClaudeAPI()
}

func (s *Service) forceRefresh(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if s.isClaude(id) {
		writeSvcErr(w, invalid("Claude API 账号没有 Token 可刷新"))
		return
	}
	if err := s.TM.ForceRefresh(r.Context(), id); err != nil {
		writeSvcErr(w, classifyBalance(err, id))
		return
	}
	ok(w, fmt.Sprintf("凭据 #%d Token 已强制刷新", id))
}

func (s *Service) balance(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if s.isClaude(id) {
		writeSvcErr(w, invalid("Claude API 账号没有 Kiro 余额"))
		return
	}
	now := float64(time.Now().Unix())
	s.balMu.Lock()
	if c, hit := s.balances[id]; hit && now-c.CachedAt < balanceTTL.Seconds() {
		s.balMu.Unlock()
		anthropic.WriteJSON(w, 200, c.Data)
		return
	}
	s.balMu.Unlock()
	ul, err := s.TM.UsageLimitsFor(r.Context(), id)
	if err != nil {
		writeSvcErr(w, classifyBalance(err, id))
		return
	}
	cur, lim := ul.Totals()
	b := Balance{ID: id, CurrentUsage: cur, UsageLimit: lim, Remaining: max(lim-cur, 0), NextResetAt: ul.NextDateReset}
	if lim > 0 {
		b.UsagePercentage = min(cur/lim*100, 100)
	}
	if t := ul.SubscriptionTitle(); t != "" {
		b.SubscriptionTitle = &t
	}
	s.balMu.Lock()
	s.balances[id] = cachedBalance{CachedAt: float64(time.Now().Unix()), Data: b}
	s.balMu.Unlock()
	s.SaveBalanceCache()
	anthropic.WriteJSON(w, 200, b)
}

type addReq struct {
	RefreshToken  string `json:"refreshToken"`
	AuthMethod    string `json:"authMethod"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret"`
	Priority      uint32 `json:"priority"`
	Region        string `json:"region"`
	AuthRegion    string `json:"authRegion"`
	APIRegion     string `json:"apiRegion"`
	MachineID     string `json:"machineId"`
	ProfileArn    string `json:"profileArn,omitempty"`
	Email         string `json:"email"`
	ProxyURL      string `json:"proxyUrl"`
	ProxyUsername string `json:"proxyUsername"`
	ProxyPassword string `json:"proxyPassword"`
	KiroAPIKey    string `json:"kiroApiKey"`
	Endpoint      string `json:"endpoint"`
	ClaudeBaseURL string `json:"claudeBaseUrl"`
	ClaudeAPIKey  string `json:"claudeApiKey"`
}

func (s *Service) add(ctx context.Context, b addReq) (map[string]any, *svcErr) {
	if b.Endpoint != "" && !s.knownEndpoint(b.Endpoint) {
		names := make([]string, 0, len(s.Endpoints))
		for n := range s.Endpoints {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, invalid(fmt.Sprintf("未知端点 \"%s\"，已注册端点: %v", b.Endpoint, names))
	}
	if b.AuthMethod == "" {
		b.AuthMethod = "social"
	}
	c := &kiro.Credentials{RefreshToken: b.RefreshToken, AuthMethod: b.AuthMethod, ClientID: b.ClientID, ClientSecret: b.ClientSecret,
		Priority: b.Priority, Region: b.Region, AuthRegion: b.AuthRegion, APIRegion: b.APIRegion, MachineID: b.MachineID, ProfileArn: b.ProfileArn,
		Email: b.Email, ProxyURL: b.ProxyURL, ProxyUsername: b.ProxyUsername, ProxyPassword: b.ProxyPassword,
		KiroAPIKey: b.KiroAPIKey, Endpoint: b.Endpoint, ClaudeBaseURL: b.ClaudeBaseURL, ClaudeAPIKey: b.ClaudeAPIKey}
	id, err := s.TM.AddCredential(ctx, c)
	if err != nil {
		return nil, classifyAdd(err)
	}
	if !s.isClaude(id) {
		// 主动取订阅等级，免得 Free 账号首个请求就绕过 Opus 过滤。
		if _, err := s.TM.UsageLimitsFor(ctx, id); err != nil {
			slog.Warn("添加凭据后获取订阅等级失败（不影响凭据添加）", "err", err)
		}
	}
	resp := map[string]any{"success": true, "message": fmt.Sprintf("凭据添加成功，ID: %d", id), "credentialId": id}
	if b.Email != "" {
		resp["email"] = b.Email
	}
	return resp, nil
}

func (s *Service) addCredential(w http.ResponseWriter, r *http.Request) {
	var b addReq
	if !decode(w, r, &b) {
		return
	}
	resp, e := s.add(r.Context(), b)
	if e != nil {
		writeSvcErr(w, e)
		return
	}
	anthropic.WriteJSON(w, 200, resp)
}

func (s *Service) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if err := s.TM.DeleteCredential(id); err != nil {
		switch {
		case errors.Is(err, kiro.ErrNotFound):
			writeSvcErr(w, notFound(id))
		case strings.Contains(err.Error(), "只能删除已禁用的凭据"):
			writeSvcErr(w, invalid(err.Error()))
		default:
			writeSvcErr(w, internal(err.Error()))
		}
		return
	}
	if err := s.Access.RemoveCredential(id); err != nil {
		slog.Warn("把删除的凭据移出分组失败", "credential_id", id, "err", err)
	}
	s.balMu.Lock()
	delete(s.balances, id)
	s.balMu.Unlock()
	s.SaveBalanceCache()
	ok(w, fmt.Sprintf("凭据 #%d 已删除", id))
}

func (s *Service) getMode(w http.ResponseWriter, _ *http.Request) {
	anthropic.WriteJSON(w, 200, map[string]any{"mode": s.TM.LoadBalancingMode()})
}

func (s *Service) setMode(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Mode != "priority" && b.Mode != "balanced" {
		writeSvcErr(w, invalid("mode 必须是 'priority' 或 'balanced'"))
		return
	}
	if err := s.TM.SetLoadBalancingMode(b.Mode); err != nil {
		writeSvcErr(w, internal(err.Error()))
		return
	}
	anthropic.WriteJSON(w, 200, map[string]any{"mode": b.Mode})
}

// proxyView 全局代理，不返回密码。
func proxyView(p *httpx.Proxy) map[string]any {
	if p == nil {
		return map[string]any{"proxyUrl": "", "proxyUsername": "", "hasPassword": false}
	}
	return map[string]any{"proxyUrl": kiro.DisplayProxyURL(p.URL), "proxyUsername": p.Username, "hasPassword": p.Password != ""}
}

func (s *Service) getProxy(w http.ResponseWriter, _ *http.Request) {
	anthropic.WriteJSON(w, 200, proxyView(s.TM.GlobalProxy()))
}

func (s *Service) setProxy(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Proxy string `json:"proxy"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := s.TM.SetGlobalProxy(b.Proxy)
	if err != nil {
		if strings.HasPrefix(err.Error(), "保存全局代理失败") {
			writeSvcErr(w, internal(err.Error()))
		} else {
			writeSvcErr(w, invalid(err.Error()))
		}
		return
	}
	anthropic.WriteJSON(w, 200, proxyView(p))
}

func (s *Service) socialStart(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ProxyURL      string `json:"proxyUrl"`
		ProxyUsername string `json:"proxyUsername"`
		ProxyPassword string `json:"proxyPassword"`
	}
	if !decode(w, r, &b) {
		return
	}
	state, u, err := s.social.start(b.ProxyURL, b.ProxyUsername, b.ProxyPassword)
	if err != nil {
		writeSvcErr(w, invalid(err.Error()))
		return
	}
	anthropic.WriteJSON(w, 200, map[string]any{"state": state, "authorizationUrl": u})
}

func (s *Service) socialComplete(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if !decode(w, r, &b) {
		return
	}
	cb, err := parseCallback(b.CallbackURL)
	if err != nil {
		writeSvcErr(w, invalid(err.Error()))
		return
	}
	p, err := s.social.take(cb.state)
	if err != nil {
		writeSvcErr(w, invalid(err.Error()))
		return
	}
	tok, err := exchangeSocialCode(r.Context(), s.TM.Config(), p, cb)
	if err != nil {
		s.social.restore(cb.state, p)
		writeSvcErr(w, upstream(err.Error()))
		return
	}
	slog.Info("Social 登录换码成功", "login_option", cb.loginOption, "has_profile", tok.ProfileArn != "", "expires_in", tok.ExpiresIn)
	resp, e := s.add(r.Context(), addReq{RefreshToken: tok.RefreshToken, AuthMethod: "social", MachineID: p.machineID,
		ProxyURL: p.proxyURL, ProxyUsername: p.proxyUsername, ProxyPassword: p.proxyPassword})
	if e != nil {
		writeSvcErr(w, e)
		return
	}
	resp["message"] = fmt.Sprintf("已通过 %s 登录并添加凭据，ID: %v", cb.loginOption, resp["credentialId"])
	anthropic.WriteJSON(w, 200, resp)
}

func (s *Service) getRequest(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	d := s.Usage.Get(id)
	if d == nil {
		plainErr(w, http.StatusNotFound, "请求不存在")
		return
	}
	anthropic.WriteJSON(w, 200, d)
}

func (s *Service) upsertPrice(w http.ResponseWriter, r *http.Request) {
	var p usage.ModelPrice
	if !decode(w, r, &p) {
		return
	}
	out, err := s.Usage.UpsertPrice(p)
	if err != nil {
		plainErr(w, http.StatusBadRequest, err.Error())
		return
	}
	anthropic.WriteJSON(w, 200, out)
}

func (s *Service) deletePrice(w http.ResponseWriter, r *http.Request) {
	if err := s.Usage.DeletePrice(r.PathValue("id")); err != nil {
		plainErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) setAccountCost(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	var b struct {
		CostCNY *float64 `json:"costCny"`
	}
	if !okID || !decode(w, r, &b) {
		return
	}
	if err := s.Usage.SetAccountCost(id, b.CostCNY); err != nil {
		plainErr(w, http.StatusBadRequest, err.Error())
		return
	}
	anthropic.WriteJSON(w, 200, s.Usage.AccountReport(s.labels()))
}

// archiveAccountUsage 把账号现有的用量历史挪到一个新的已删除编号下，账号从零开始统计。
func (s *Service) archiveAccountUsage(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	spare, err := s.TM.NewSpareID()
	if err != nil {
		writeSvcErr(w, internal(err.Error()))
		return
	}
	if err := s.Usage.MoveAccount(id, spare); err != nil {
		writeSvcErr(w, internal(err.Error()))
		return
	}
	slog.Info("已归档账号用量历史", "credential_id", id, "archived_as", spare)
	anthropic.WriteJSON(w, 200, map[string]any{"archivedAs": spare, "report": s.Usage.AccountReport(s.labels())})
}

func (s *Service) createKey(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Secret  string `json:"secret"`
		GroupID string `json:"groupId"`
	}
	if !decode(w, r, &b) {
		return
	}
	k, err := s.Access.CreateKey(b.Name, b.Secret, b.GroupID)
	if err != nil {
		plainErr(w, http.StatusBadRequest, err.Error())
		return
	}
	anthropic.WriteJSON(w, 200, k)
}

func pathInt(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		plainErr(w, http.StatusBadRequest, "无效的 id")
		return 0, false
	}
	return id, true
}

func noContentOr(w http.ResponseWriter, err error) {
	if err != nil {
		plainErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) updateKey(w http.ResponseWriter, r *http.Request) {
	id, okID := pathInt(w, r)
	var b struct {
		Name     *string `json:"name"`
		GroupID  *string `json:"groupId"`
		Disabled *bool   `json:"disabled"`
	}
	if !okID || !decode(w, r, &b) {
		return
	}
	noContentOr(w, s.Access.UpdateKey(id, b.Name, b.GroupID, b.Disabled))
}

func (s *Service) deleteKey(w http.ResponseWriter, r *http.Request) {
	if id, okID := pathInt(w, r); okID {
		noContentOr(w, s.Access.DeleteKey(id))
	}
}

func (s *Service) listGroups(w http.ResponseWriter, _ *http.Request) {
	groups := s.Access.ListGroups()
	if groups == nil {
		groups = []access.Group{}
	}
	assigned := s.Access.AssignedCredentials()
	for i := range groups {
		if !groups[i].IsDefault {
			continue
		}
		have := map[uint64]bool{}
		for _, id := range groups[i].Members {
			have[id] = true
		}
		for _, id := range s.TM.CredentialIDs() {
			if !assigned[id] && !have[id] {
				groups[i].Members = append(groups[i].Members, id)
				have[id] = true
			}
		}
		sort.Slice(groups[i].Members, func(a, b int) bool { return groups[i].Members[a] < groups[i].Members[b] })
	}
	anthropic.WriteJSON(w, 200, groups)
}

func (s *Service) createGroup(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	g, err := s.Access.CreateGroup(b.Name)
	if err != nil {
		plainErr(w, http.StatusBadRequest, err.Error())
		return
	}
	anthropic.WriteJSON(w, 200, g)
}

func (s *Service) renameGroup(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if decode(w, r, &b) {
		noContentOr(w, s.Access.RenameGroup(r.PathValue("id"), b.Name))
	}
}

func (s *Service) deleteGroup(w http.ResponseWriter, r *http.Request) {
	noContentOr(w, s.Access.DeleteGroup(r.PathValue("id")))
}

func (s *Service) setMembers(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CredentialIDs []uint64 `json:"credentialIds"`
	}
	if decode(w, r, &b) {
		noContentOr(w, s.Access.SetMembers(r.PathValue("id"), b.CredentialIDs, s.TM.CredentialIDs()))
	}
}
