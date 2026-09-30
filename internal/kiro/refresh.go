package kiro

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/google/uuid"
)

// RefreshTokenInvalidError 上游返回 invalid_grant，凭据要立即禁用。
type RefreshTokenInvalidError struct{ Message string }

func (e *RefreshTokenInvalidError) Error() string { return e.Message }

func expiringWithin(c *Credentials, d time.Duration) (bool, bool) {
	if c.ExpiresAt == "" {
		return false, false
	}
	t, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		return false, false
	}
	return !t.After(time.Now().Add(d)), true
}

// TokenExpired 提前 5 分钟判断；没有过期时间按已过期处理。
func TokenExpired(c *Credentials) bool {
	v, ok := expiringWithin(c, 5*time.Minute)
	return !ok || v
}

// TokenExpiringSoon 10 分钟内过期。
func TokenExpiringSoon(c *Credentials) bool {
	v, _ := expiringWithin(c, 10*time.Minute)
	return v
}

// ValidateRefreshToken 拒绝空的和被截断的 refreshToken。
func ValidateRefreshToken(c *Credentials) error {
	rt := c.RefreshToken
	if rt == "" {
		return errors.New("缺少 refreshToken")
	}
	if len(rt) < 100 || strings.Contains(rt, "...") {
		return fmt.Errorf("refreshToken 已被截断（长度: %d 字符）。\n这通常是 Kiro IDE 为了防止凭证被第三方工具使用而故意截断的。", len(rt))
	}
	return nil
}

func sha256HexStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func maskAPIKey(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] >= 0x80 {
			return "***"
		}
	}
	if len(key) > 16 {
		return key[:4] + "..." + key[len(key)-4:]
	}
	return "***"
}

func doJSON(ctx context.Context, client *http.Client, method, u string, body any, headers map[string]string, host string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if host != "" {
		req.Host = host
	}
	req.Close = true
	return client.Do(req)
}

func invalidGrant(status int, body string) bool {
	return status == 400 && strings.Contains(body, `"invalid_grant"`) && strings.Contains(body, "Invalid refresh token provided")
}

// RefreshToken 按 authMethod 选 Social 或 IdC 刷新，返回新凭据副本。
func RefreshToken(ctx context.Context, c *Credentials, cfg *config.Config, px *httpx.Proxy) (*Credentials, error) {
	if c.IsAPIKey() {
		return nil, errors.New("API Key 凭据不支持刷新 Token")
	}
	if c.IsClaudeAPI() {
		return nil, errors.New("Claude API 账号没有 Token 可刷新")
	}
	if err := ValidateRefreshToken(c); err != nil {
		return nil, err
	}
	method := c.AuthMethod
	if method == "" {
		method = "social"
		if c.ClientID != "" && c.ClientSecret != "" {
			method = "idc"
		}
	}
	client, err := httpx.New(px, 60*time.Second)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(method) {
	case "idc", "builder-id", "iam":
		return refreshIdc(ctx, client, c, cfg)
	default:
		return refreshSocial(ctx, client, c, cfg)
	}
}

type refreshResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    *int64 `json:"expiresIn"`
}

func applyRefresh(c *Credentials, data refreshResp) *Credentials {
	n := c.Clone()
	n.AccessToken = data.AccessToken
	if data.RefreshToken != "" {
		n.RefreshToken = data.RefreshToken
	}
	if data.ProfileArn != "" {
		n.ProfileArn = data.ProfileArn
	}
	if data.ExpiresIn != nil {
		n.ExpiresAt = time.Now().UTC().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
	}
	return n
}

func refreshSocial(ctx context.Context, client *http.Client, c *Credentials, cfg *config.Config) (*Credentials, error) {
	slog.Info("正在刷新 Social Token...", "credential_id", c.ID)
	region := c.EffectiveAuthRegion(cfg)
	domain := "prod." + region + ".auth.desktop.kiro.dev"
	mid := MachineIDFor(c, cfg)
	resp, err := doJSON(ctx, client, http.MethodPost, "https://"+domain+"/refreshToken",
		map[string]string{"refreshToken": c.RefreshToken}, map[string]string{
			"Accept":          "application/json, text/plain, */*",
			"Content-Type":    "application/json",
			"User-Agent":      SocialRefreshUserAgent(cfg, mid),
			"Accept-Encoding": "gzip, compress, deflate, br",
		}, domain)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readMaybeCompressed(resp)
	if resp.StatusCode/100 != 2 {
		if invalidGrant(resp.StatusCode, string(body)) {
			return nil, &RefreshTokenInvalidError{"Social refreshToken 已失效 (invalid_grant): " + string(body)}
		}
		return nil, fmt.Errorf("%s: %d %s", refreshErrMsg(resp.StatusCode, false), resp.StatusCode, body)
	}
	var data refreshResp
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return applyRefresh(c, data), nil
}

func refreshIdc(ctx context.Context, client *http.Client, c *Credentials, cfg *config.Config) (*Credentials, error) {
	slog.Info("正在刷新 IdC Token...", "credential_id", c.ID)
	if c.ClientID == "" {
		return nil, errors.New("IdC 刷新需要 clientId")
	}
	if c.ClientSecret == "" {
		return nil, errors.New("IdC 刷新需要 clientSecret")
	}
	region := c.EffectiveAuthRegion(cfg)
	host := "oidc." + region + ".amazonaws.com"
	ua := SDKUserAgent(cfg, MachineIDFor(c, cfg), "sso-oidc")
	resp, err := doJSON(ctx, client, http.MethodPost, "https://"+host+"/token", map[string]string{
		"clientId": c.ClientID, "clientSecret": c.ClientSecret, "refreshToken": c.RefreshToken, "grantType": "refresh_token",
	}, map[string]string{
		"content-type":          "application/json",
		"x-amz-user-agent":      ua,
		"user-agent":            ua,
		"amz-sdk-invocation-id": uuid.NewString(),
		"amz-sdk-request":       "attempt=1; max=4",
	}, host)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readMaybeCompressed(resp)
	if resp.StatusCode/100 != 2 {
		if invalidGrant(resp.StatusCode, string(body)) {
			return nil, &RefreshTokenInvalidError{"IdC refreshToken 已失效 (invalid_grant): " + string(body)}
		}
		return nil, fmt.Errorf("%s: %d %s", refreshErrMsg(resp.StatusCode, true), resp.StatusCode, body)
	}
	var data refreshResp
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return applyRefresh(c, data), nil
}

func refreshErrMsg(status int, idc bool) string {
	switch {
	case status == 401 && idc:
		return "IdC 凭证已过期或无效，需要重新认证"
	case status == 401:
		return "OAuth 凭证已过期或无效，需要重新认证"
	case status == 403:
		return "权限不足，无法刷新 Token"
	case status == 429:
		return "请求过于频繁，已被限流"
	case status >= 500 && idc:
		return "服务器错误，AWS OIDC 服务暂时不可用"
	case status >= 500:
		return "服务器错误，AWS OAuth 服务暂时不可用"
	case idc:
		return "IdC Token 刷新失败"
	}
	return "Token 刷新失败"
}

// UsageLimits getUsageLimits 响应。
type UsageLimits struct {
	NextDateReset    *float64 `json:"nextDateReset"`
	SubscriptionInfo *struct {
		SubscriptionTitle string `json:"subscriptionTitle"`
	} `json:"subscriptionInfo"`
	UsageBreakdownList []struct {
		CurrentUsageWithPrecision float64  `json:"currentUsageWithPrecision"`
		UsageLimitWithPrecision   float64  `json:"usageLimitWithPrecision"`
		NextDateReset             *float64 `json:"nextDateReset"`
		Bonuses                   []struct {
			CurrentUsage float64 `json:"currentUsage"`
			UsageLimit   float64 `json:"usageLimit"`
			Status       string  `json:"status"`
		} `json:"bonuses"`
		FreeTrialInfo *struct {
			CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
			UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
			FreeTrialStatus           string  `json:"freeTrialStatus"`
		} `json:"freeTrialInfo"`
	} `json:"usageBreakdownList"`
}

// SubscriptionTitle 订阅等级。
func (u *UsageLimits) SubscriptionTitle() string {
	if u.SubscriptionInfo == nil {
		return ""
	}
	return u.SubscriptionInfo.SubscriptionTitle
}

// Totals 基础额度 + 激活的试用 + 激活的奖励。
func (u *UsageLimits) Totals() (usage, limit float64) {
	if len(u.UsageBreakdownList) == 0 {
		return 0, 0
	}
	b := u.UsageBreakdownList[0]
	usage, limit = b.CurrentUsageWithPrecision, b.UsageLimitWithPrecision
	if b.FreeTrialInfo != nil && b.FreeTrialInfo.FreeTrialStatus == "ACTIVE" {
		usage += b.FreeTrialInfo.CurrentUsageWithPrecision
		limit += b.FreeTrialInfo.UsageLimitWithPrecision
	}
	for _, bo := range b.Bonuses {
		if bo.Status == "ACTIVE" {
			usage += bo.CurrentUsage
			limit += bo.UsageLimit
		}
	}
	return usage, limit
}

// FetchUsageLimits 查询额度。
func FetchUsageLimits(ctx context.Context, c *Credentials, cfg *config.Config, token string, px *httpx.Proxy) (*UsageLimits, error) {
	region := c.EffectiveAPIRegion(cfg)
	host := "q." + region + ".amazonaws.com"
	ua := SDKUserAgent(cfg, MachineIDFor(c, cfg), "codewhispererruntime")
	u := "https://" + host + "/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"
	if c.ProfileArn != "" {
		u += "&profileArn=" + url.QueryEscape(c.ProfileArn)
	}
	client, err := httpx.New(px, 60*time.Second)
	if err != nil {
		return nil, err
	}
	h := map[string]string{
		"x-amz-user-agent":      ua,
		"user-agent":            ua,
		"amz-sdk-invocation-id": uuid.NewString(),
		"amz-sdk-request":       "attempt=1; max=1",
		"Authorization":         "Bearer " + token,
	}
	if tt := c.UpstreamTokenType(); tt != "" {
		h["TokenType"] = tt
	}
	resp, err := doJSON(ctx, client, http.MethodGet, u, nil, h, host)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readMaybeCompressed(resp)
	if resp.StatusCode/100 != 2 {
		msg := "获取使用额度失败"
		switch {
		case resp.StatusCode == 401:
			msg = "认证失败，Token 无效或已过期"
		case resp.StatusCode == 403:
			msg = "权限不足，无法获取使用额度"
		case resp.StatusCode == 429:
			msg = "请求过于频繁，已被限流"
		case resp.StatusCode >= 500:
			msg = "服务器错误，AWS 服务暂时不可用"
		}
		return nil, fmt.Errorf("%s: %d %s", msg, resp.StatusCode, body)
	}
	var out UsageLimits
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
