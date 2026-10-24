package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/FoyonaCZY/kiro2api/internal/redact"
)

const (
	maxRetriesPerCredential = 3
	maxTotalRetries         = 9
	upstreamTimeout         = 720 * time.Second
)

// Target 一次上游调用选中的凭据；出站头里的令牌已打码。
type Target struct {
	CredentialID    uint64
	Account         string
	Endpoint        string
	OutboundHeaders string
	OutboundBody    string
}

// UpstreamResponse 成功的上游响应。Passthrough 表示上游已是 Anthropic 格式，原样返回。
type UpstreamResponse struct {
	Resp        *http.Response
	Target      Target
	Passthrough bool
}

// ClaudeForward 选中 Claude 账号时转发的原始请求。
type ClaudeForward struct {
	Body             []byte
	AnthropicVersion string
	AnthropicBeta    string
}

// UpstreamError 带上游目标的失败，方便记账。
type UpstreamError struct {
	Message string
	Target  *Target
}

func (e *UpstreamError) Error() string { return e.Message }

func tagUpstream(err error, t *Target) error {
	var cd *CoolingDownError
	var ue *UpstreamError
	if t == nil || errors.As(err, &cd) || errors.As(err, &ue) {
		return err
	}
	tt := *t
	return &UpstreamError{Message: err.Error(), Target: &tt}
}

// Provider 与 Kiro / Claude 上游通信，负责重试和故障转移。
type Provider struct {
	tm              *Manager
	pool            *httpx.Pool
	endpoints       map[string]Endpoint
	defaultEndpoint string
	models          *modelCache
}

// NewProvider 创建 provider。
func NewProvider(tm *Manager, endpoints map[string]Endpoint, defaultEndpoint string) *Provider {
	return &Provider{tm: tm, pool: httpx.NewPool(upstreamTimeout), endpoints: endpoints, defaultEndpoint: defaultEndpoint, models: newModelCache()}
}

// Manager 调度器。
func (p *Provider) Manager() *Manager { return p.tm }

func (p *Provider) client(c *Credentials) (*http.Client, error) {
	return p.pool.Get(c.EffectiveProxy(p.tm.GlobalProxy()))
}

func (p *Provider) endpointFor(c *Credentials) (Endpoint, error) {
	name := EffectiveEndpointName(c.Endpoint, p.defaultEndpoint, c.IsAPIKey())
	if e, ok := p.endpoints[name]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("未知端点: %s", name)
}

func accountName(c *Credentials, id uint64) string {
	if s := strings.TrimSpace(c.Email); s != "" {
		return s
	}
	return "#" + strconv.FormatUint(id, 10)
}

// HasClaudeAPI 池里有 Claude 账号。
func (p *Provider) HasClaudeAPI() bool {
	for _, e := range p.tm.Snapshot().Entries {
		if e.AuthMethod != nil && strings.EqualFold(*e.AuthMethod, "claude_api") {
			return true
		}
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	backoff := int64(200) << attempt
	if backoff > 2000 {
		backoff = 2000
	}
	jmax := backoff / 4
	if jmax < 1 {
		jmax = 1
	}
	return time.Duration(backoff+rand.Int64N(jmax+1)) * time.Millisecond
}

// RateLimitDelay x-amzn-kiro-ratelimit-retry-after(ms) > Retry-After(秒或日期) > fallback，夹在 1s~24h。
func RateLimitDelay(h http.Header, fallback time.Duration) time.Duration {
	d := fallback
	if ms, err := strconv.ParseUint(strings.TrimSpace(h.Get("x-amzn-kiro-ratelimit-retry-after")), 10, 64); err == nil {
		d = time.Duration(ms) * time.Millisecond
	} else if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if s, err := strconv.ParseUint(v, 10, 64); err == nil {
			d = time.Duration(s) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			d = max(time.Until(t), 0)
		}
	}
	return min(max(d, time.Second), 24*time.Hour)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ExtractModel conversationState.currentMessage.userInputMessage.modelId。
func ExtractModel(body []byte) string {
	m, _ := envelopeInfo(body)
	return m
}

// envelopeInfo 模型名和本轮调用位置。
func envelopeInfo(body []byte) (string, int) {
	var v struct {
		ConversationState struct {
			CurrentMessage struct {
				UserInputMessage struct {
					ModelID string `json:"modelId"`
				} `json:"userInputMessage"`
			} `json:"currentMessage"`
		} `json:"conversationState"`
		CallPosition int `json:"callPosition"`
	}
	_ = json.Unmarshal(body, &v)
	return v.ConversationState.CurrentMessage.UserInputMessage.ModelID, v.CallPosition
}

func (p *Provider) forwardClaude(ctx context.Context, cc *CallContext, fw *ClaudeForward, stream bool) (*http.Response, error) {
	u, err := cc.Cred.ClaudeMessagesURL()
	if err != nil {
		return nil, err
	}
	if cc.Cred.ClaudeAPIKey == "" {
		return nil, errors.New("Claude API 账号缺少 API Key")
	}
	cl, err := p.client(cc.Cred)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(fw.Body))
	if err != nil {
		return nil, err
	}
	ver := fw.AnthropicVersion
	if ver == "" {
		ver = "2023-06-01"
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", cc.Cred.ClaudeAPIKey)
	req.Header.Set("anthropic-version", ver)
	if stream {
		req.Header.Set("accept", "text/event-stream")
	}
	if fw.AnthropicBeta != "" {
		req.Header.Set("anthropic-beta", fw.AnthropicBeta)
	}
	return cl.Do(req)
}

func readSmall(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	return string(b)
}

func (p *Provider) maxRetries() (int, int) {
	total := p.tm.TotalCount()
	return total, min(total*maxRetriesPerCredential, maxTotalRetries)
}

// CallMessages 与 Kiro 账号同一套重试。选中 Claude 账号时转发原始请求（fw 为 nil 时跳过 Claude 账号），
// 选中 Kiro 账号时发 kiroBody（为空时跳过 Kiro 账号）。同一请求里每个 Claude 账号只试一次，
// 超时和 5xx 只换号不累计失败，401/403 计一次失败。
func (p *Provider) CallMessages(ctx context.Context, kiroBody []byte, fw *ClaudeForward, stream bool) (*UpstreamResponse, error) {
	total, maxRetries := p.maxRetries()
	apiType := "非流式"
	if stream {
		apiType = "流式"
	}
	var lastErr error
	var lastTarget *Target
	forceRefreshed := map[uint64]bool{}
	claudeTried := map[uint64]bool{}
	attempts := map[uint64]int{}
	model, callPosition := envelopeInfo(kiroBody)
	budget := AttemptBudget(callPosition)
	if model == "" && fw != nil {
		var v struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(fw.Body, &v)
		model = v.Model
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cc, err := p.tm.AcquireContext(ctx, model)
		if err != nil {
			return nil, err
		}

		if cc.Cred.IsClaudeAPI() {
			if claudeTried[cc.ID] || fw == nil {
				p.tm.SwitchToNext(ctx)
				if len(claudeTried) >= total {
					break
				}
				continue
			}
			claudeTried[cc.ID] = true
			resp, err := p.forwardClaude(ctx, cc, fw, stream)
			if err != nil {
				slog.Warn(fmt.Sprintf("Claude API 账号 #%d 转发失败，换下一个账号: %v", cc.ID, err))
				lastErr = err
				p.tm.SwitchToNext(ctx)
				continue
			}
			if resp.StatusCode/100 == 2 {
				p.tm.ReportSuccess(cc.ID)
				return &UpstreamResponse{Resp: resp, Target: Target{CredentialID: cc.ID, Account: accountName(cc.Cred, cc.ID), Endpoint: "claude"}, Passthrough: true}, nil
			}
			slog.Warn(fmt.Sprintf("Claude API 账号 #%d 返回 %d，换下一个账号", cc.ID, resp.StatusCode))
			switch resp.StatusCode {
			case 429:
				cd := RateLimitDelay(resp.Header, time.Duration(p.tm.Config().RateLimitCooldownSecs)*time.Second)
				p.tm.ReportRateLimited(cc.ID, cd)
				lastErr = &CoolingDownError{RetryAfter: cd}
			case 401, 403:
				p.tm.ReportFailure(cc.ID)
				lastErr = fmt.Errorf("Claude API %d", resp.StatusCode)
			default:
				lastErr = fmt.Errorf("Claude API %d", resp.StatusCode)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			p.tm.SwitchToNext(ctx)
			continue
		}

		if len(kiroBody) == 0 {
			p.tm.SwitchToNext(ctx)
			lastErr = errors.New("请求无法转换成 Kiro，且当前账号不是 Claude API")
			continue
		}

		cfg := p.tm.Config()
		mid := MachineIDFor(cc.Cred, cfg)
		ep, err := p.endpointFor(cc.Cred)
		if err != nil {
			lastErr = err
			p.tm.ReportFailure(cc.ID)
			continue
		}
		rc := &RequestContext{Cred: cc.Cred, Token: cc.Token, MachineID: mid, Config: cfg}
		px := cc.Cred.EffectiveProxy(p.tm.GlobalProxy())
		info := p.models.Lookup(ctx, cc.ID, rc, px, model)
		body, err := ResponsesFromKiro(InjectProfileArn(string(kiroBody), cc.Cred.ProfileArn), cfg.AgentMode, info)
		if err != nil {
			return nil, err
		}
		attempts[cc.ID]++
		req := ResponsesRequest(rc, []byte(body), attempts[cc.ID], budget)
		target := Target{CredentialID: cc.ID, Account: accountName(cc.Cred, cc.ID), Endpoint: ep.Name(),
			OutboundHeaders: redact.EncodeHeaders(RedactedHeaders(req)), OutboundBody: body}
		lastTarget = &target

		resp, err := httpx.DoIDE(ctx, px, req, upstreamTimeout)
		if err != nil {
			// 网络错误多半是链路抖动，不禁用也不换号，免得一阵抖动把所有凭据都禁掉。
			slog.Warn(fmt.Sprintf("API 请求发送失败（尝试 %d/%d）: %v", attempt+1, maxRetries, err))
			lastErr = err
			if attempt+1 < maxRetries {
				sleepCtx(ctx, retryDelay(attempt))
			}
			continue
		}
		status := resp.StatusCode
		if status/100 == 2 {
			p.tm.ReportSuccess(cc.ID)
			return &UpstreamResponse{Resp: resp, Target: target}, nil
		}
		cooldown := RateLimitDelay(resp.Header, time.Duration(cfg.RateLimitCooldownSecs)*time.Second)
		text := readSmall(resp.Body)
		resp.Body.Close()
		fail := fmt.Errorf("%s API 请求失败: %d %s", apiType, status, text)

		switch {
		case status == 429:
			p.tm.ReportRateLimited(cc.ID, cooldown)
			lastErr = &CoolingDownError{RetryAfter: cooldown}
			continue
		case status == 402 && IsMonthlyRequestLimit(text):
			slog.Warn(fmt.Sprintf("API 请求失败（额度已用尽，禁用凭据并切换，尝试 %d/%d）: %d", attempt+1, maxRetries, status))
			if !p.tm.ReportQuotaExhausted(cc.ID) {
				return nil, fmt.Errorf("%s API 请求失败（所有凭据已用尽）: %d %s", apiType, status, text)
			}
			lastErr = fail
			continue
		case status == 400:
			return nil, tagUpstream(fail, &target)
		case status == 401 || status == 403:
			slog.Warn(fmt.Sprintf("API 请求失败（可能为凭据错误，尝试 %d/%d）: %d %s", attempt+1, maxRetries, status, text))
			if IsBearerTokenInvalid(text) && !forceRefreshed[cc.ID] {
				forceRefreshed[cc.ID] = true
				if p.tm.ForceRefresh(ctx, cc.ID) == nil {
					slog.Info(fmt.Sprintf("凭据 #%d token 强制刷新成功，重试请求", cc.ID))
					continue
				}
				slog.Warn(fmt.Sprintf("凭据 #%d token 强制刷新失败，计入失败", cc.ID))
			}
			if !p.tm.ReportFailure(cc.ID) {
				return nil, fmt.Errorf("%s API 请求失败（所有凭据已用尽）: %d %s", apiType, status, text)
			}
			lastErr = fail
			continue
		case status == 408 || status >= 500:
			slog.Warn(fmt.Sprintf("API 请求失败（上游瞬态错误，尝试 %d/%d）: %d %s", attempt+1, maxRetries, status, text))
			lastErr = fail
		case status >= 400:
			return nil, tagUpstream(fail, &target)
		default:
			lastErr = fail
		}
		if attempt+1 < maxRetries {
			sleepCtx(ctx, retryDelay(attempt))
		}
	}
	if lastErr != nil {
		return nil, tagUpstream(lastErr, lastTarget)
	}
	return nil, fmt.Errorf("%s API 请求失败：已达到最大重试次数（%d次）", apiType, maxRetries)
}

// CallMCP WebSearch 等工具调用，不按模型过滤，跳过 Claude 账号。
func (p *Provider) CallMCP(ctx context.Context, body []byte) (*http.Response, error) {
	total, maxRetries := p.maxRetries()
	var lastErr error
	forceRefreshed := map[uint64]bool{}
	skipped := map[uint64]bool{}
	attempts := map[uint64]int{}
	for attempt := 0; attempt < maxRetries; attempt++ {
		cc, err := p.tm.AcquireContext(ctx, "")
		if err != nil {
			return nil, err
		}
		if cc.Cred.IsClaudeAPI() {
			skipped[cc.ID] = true
			p.tm.SwitchToNext(ctx)
			if len(skipped) >= total {
				break
			}
			continue
		}
		cfg := p.tm.Config()
		rc := &RequestContext{Cred: cc.Cred, Token: cc.Token, MachineID: MachineIDFor(cc.Cred, cfg), Config: cfg}
		attempts[cc.ID]++
		req, err := MCPRequest(rc, body, attempts[cc.ID])
		if err != nil {
			return nil, err
		}
		resp, err := httpx.DoIDE(ctx, cc.Cred.EffectiveProxy(p.tm.GlobalProxy()), req, upstreamTimeout)
		if err != nil {
			slog.Warn(fmt.Sprintf("MCP 请求发送失败（尝试 %d/%d）: %v", attempt+1, maxRetries, err))
			lastErr = err
			if attempt+1 < maxRetries {
				sleepCtx(ctx, retryDelay(attempt))
			}
			continue
		}
		status := resp.StatusCode
		if status/100 == 2 {
			p.tm.ReportSuccess(cc.ID)
			return resp, nil
		}
		cooldown := RateLimitDelay(resp.Header, time.Duration(cfg.RateLimitCooldownSecs)*time.Second)
		text := readSmall(resp.Body)
		resp.Body.Close()
		fail := fmt.Errorf("MCP 请求失败: %d %s", status, text)
		switch {
		case status == 429:
			p.tm.ReportRateLimited(cc.ID, cooldown)
			lastErr = &CoolingDownError{RetryAfter: cooldown}
			continue
		case status == 402 && IsMonthlyRequestLimit(text):
			if !p.tm.ReportQuotaExhausted(cc.ID) {
				return nil, fmt.Errorf("MCP 请求失败（所有凭据已用尽）: %d %s", status, text)
			}
			lastErr = fail
			continue
		case status == 400:
			return nil, fail
		case status == 401 || status == 403:
			if IsBearerTokenInvalid(text) && !forceRefreshed[cc.ID] {
				forceRefreshed[cc.ID] = true
				if p.tm.ForceRefresh(ctx, cc.ID) == nil {
					continue
				}
			}
			if !p.tm.ReportFailure(cc.ID) {
				return nil, fmt.Errorf("MCP 请求失败（所有凭据已用尽）: %d %s", status, text)
			}
			lastErr = fail
			continue
		case status == 408 || status >= 500:
			lastErr = fail
		case status >= 400:
			return nil, fail
		default:
			lastErr = fail
		}
		if attempt+1 < maxRetries {
			sleepCtx(ctx, retryDelay(attempt))
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("MCP 请求失败：已达到最大重试次数（%d次）", maxRetries)
}
