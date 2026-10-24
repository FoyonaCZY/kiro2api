package kiro

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/FoyonaCZY/kiro2api/internal/httpx"
	"github.com/google/uuid"
)

const (
	EndpointIDE = "ide"
	EndpointKRS = "krs"
)

// SocialAuthHost Kiro IDE 的 Social 登录和刷新固定打 us-east-1；prod.eu-central-1.auth.desktop.kiro.dev 不存在。
const SocialAuthHost = "prod.us-east-1.auth.desktop.kiro.dev"

// ProfileArnRegion arn:aws:codewhisperer:{region}:... 里的区域，解析不出时为空。
func ProfileArnRegion(arn string) string {
	p := strings.SplitN(arn, ":", 5)
	if len(p) < 5 || p[0] != "arn" {
		return ""
	}
	return p[3]
}

// RuntimeHost runtime.{region}.kiro.dev：对话和 MCP。
func RuntimeHost(c *Credentials, cfg *config.Config) string {
	return "runtime." + c.EffectiveAPIRegion(cfg) + ".kiro.dev"
}

// ManagementHost management.{region}.kiro.dev：模型列表和额度。
func ManagementHost(c *Credentials, cfg *config.Config) string {
	return "management." + c.EffectiveAPIRegion(cfg) + ".kiro.dev"
}

// EffectiveEndpointName OAuth 未指定时走 krs（旧配置里的 ide 默认值也按 krs）；
// API Key 落到 krs 时改走 ide；凭据显式写的端点保留。
func EffectiveEndpointName(explicit, defaultEndpoint string, apiKey bool) string {
	name := strings.TrimSpace(explicit)
	if name == "" {
		d := strings.TrimSpace(defaultEndpoint)
		if d == "" || d == EndpointIDE {
			name = EndpointKRS
		} else {
			name = d
		}
	}
	if apiKey && name == EndpointKRS {
		return EndpointIDE
	}
	return name
}

// RequestContext 一次调用确定的运行时信息。
type RequestContext struct {
	Cred      *Credentials
	Token     string
	MachineID string
	Config    *config.Config
}

// Endpoint 历史上 ide 和 krs 是两套请求；Kiro 1.4.26 起两者完全相同，名字只为兼容凭据上的 endpoint 字段。
type Endpoint interface{ Name() string }

// IDE 历史名字。
type IDE struct{}

// Name 端点名。
func (IDE) Name() string { return EndpointIDE }

// KRS 历史名字。
type KRS struct{}

// Name 端点名。
func (KRS) Name() string { return EndpointKRS }

// Endpoints 按名称注册。
func Endpoints() map[string]Endpoint {
	return map[string]Endpoint{EndpointIDE: IDE{}, EndpointKRS: KRS{}}
}

// IsMonthlyRequestLimit 顶层或嵌套 reason 为 MONTHLY_REQUEST_COUNT。
func IsMonthlyRequestLimit(body string) bool {
	return strings.Contains(body, "MONTHLY_REQUEST_COUNT")
}

// IsBearerTokenInvalid 上游 token 失效。
func IsBearerTokenInvalid(body string) bool {
	return strings.Contains(body, "The bearer token included in the request is invalid")
}

// InjectProfileArn 在根对象写入 profileArn；不是 JSON 对象时原样返回。
func InjectProfileArn(body, arn string) string {
	if arn == "" {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil || m == nil {
		return body
	}
	v, _ := MarshalNoEscape(arn)
	m["profileArn"] = v
	out, err := MarshalNoEscape(m)
	if err != nil {
		return body
	}
	return string(out)
}

// AttemptBudget Kiro Agent 的重试预算：3 次，再按本轮第几次调用模型加 0~3 次。
func AttemptBudget(callPosition int) int {
	switch {
	case callPosition <= 2:
		return 3
	case callPosition <= 6:
		return 4
	case callPosition <= 11:
		return 5
	}
	return 6
}

// ResponsesRequest POST /v1/responses，头和顺序与 Kiro 1.4.26 的 createResponse 一致。
func ResponsesRequest(rc *RequestContext, body []byte, attempt, budget int) *httpx.IDERequest {
	host := RuntimeHost(rc.Cred, rc.Config)
	h := [][2]string{
		{"Content-Type", "application/json"},
		{"Authorization", "Bearer " + rc.Token},
		{"x-amzn-kiro-origin", OriginIDE},
		{"x-amzn-codewhisperer-optout", "true"},
	}
	if rc.Cred.ProfileArn != "" {
		h = append(h, [2]string{"x-amzn-kiro-profile", rc.Cred.ProfileArn})
	}
	if tt := rc.Cred.UpstreamTokenType(); tt != "" {
		h = append(h, [2]string{"TokenType", tt})
	}
	h = append(h,
		[2]string{"User-Agent", ResponsesUserAgent(rc.Config, rc.MachineID)},
		[2]string{"x-kiro-attempt", strconv.Itoa(attempt) + ";max=" + strconv.Itoa(budget)},
		[2]string{"Content-Length", strconv.Itoa(len(body))},
		[2]string{"Host", host},
		[2]string{"Connection", "keep-alive"},
	)
	return &httpx.IDERequest{Method: "POST", URL: "https://" + host + "/v1/responses", Header: h, Body: body}
}

// smithyJSONRequest awsJson1_0 的 POST /，头顺序取自 smithy 客户端实际发出的请求。
func smithyJSONRequest(host, target, tokenType, ua, amzUA, token string, body []byte, attempt, max int) *httpx.IDERequest {
	h := [][2]string{
		{"content-type", "application/x-amz-json-1.0"},
		{"x-amz-target", target},
		{"content-length", strconv.Itoa(len(body))},
	}
	if tokenType != "" {
		h = append(h, [2]string{"TokenType", tokenType})
	}
	h = append(h,
		[2]string{"x-amz-user-agent", amzUA},
		[2]string{"user-agent", ua},
		[2]string{"host", host},
		[2]string{"amz-sdk-invocation-id", uuid.NewString()},
		[2]string{"amz-sdk-request", fmt.Sprintf("attempt=%d; max=%d", attempt, max)},
		[2]string{"Authorization", "Bearer " + token},
		[2]string{"Connection", "keep-alive"},
	)
	return &httpx.IDERequest{Method: "POST", URL: "https://" + host + "/", Header: h, Body: body}
}

type mcpEnvelope struct {
	ID         json.RawMessage `json:"id"`
	Method     string          `json:"method"`
	ProfileArn string          `json:"profileArn,omitempty"`
	JSONRPC    string          `json:"jsonrpc"`
	Params     json.RawMessage `json:"params,omitempty"`
}

// MCPRequest KiroRuntimeService.InvokeMCP。profileArn 写进 body，字段顺序跟 smithy 序列化一致。
func MCPRequest(rc *RequestContext, jsonrpc []byte, attempt int) (*httpx.IDERequest, error) {
	var env mcpEnvelope
	if err := json.Unmarshal(jsonrpc, &env); err != nil {
		return nil, err
	}
	env.ProfileArn = rc.Cred.ProfileArn
	body, err := MarshalNoEscape(env)
	if err != nil {
		return nil, err
	}
	ua, amz := smithyUserAgents(rc.Config, SmithyClientVersion, "kiroruntime", agentSmithyCustom(rc.Config, rc.MachineID))
	return smithyJSONRequest(RuntimeHost(rc.Cred, rc.Config), "KiroRuntimeService.InvokeMCP", rc.Cred.UpstreamTokenType(),
		ua, amz, rc.Token, body, attempt, AttemptBudget(0)), nil
}

// ListModelsRequest KiroControlPlaneBearerService.ListAvailableModels。
func ListModelsRequest(rc *RequestContext, nextToken string) *httpx.IDERequest {
	body, _ := MarshalNoEscape(struct {
		Origin     string `json:"origin"`
		ProfileArn string `json:"profileArn,omitempty"`
		NextToken  string `json:"nextToken,omitempty"`
	}{OriginIDE, rc.Cred.ProfileArn, nextToken})
	ua, amz := smithyUserAgents(rc.Config, SmithyClientVersion, "kirocontrolplanebearer", agentSmithyCustom(rc.Config, rc.MachineID))
	return smithyJSONRequest(ManagementHost(rc.Cred, rc.Config), "KiroControlPlaneBearerService.ListAvailableModels",
		rc.Cred.UpstreamTokenType(), ua, amz, rc.Token, body, 1, 3)
}

// usageTokenType 扩展侧客户端只对 API Key 和外部 IdP 发 TokenType。
func usageTokenType(c *Credentials) string {
	if c.IsAPIKey() {
		return "API_KEY"
	}
	if strings.EqualFold(c.AuthMethod, "external_idp") {
		return "EXTERNAL_IDP"
	}
	return ""
}

// UsageLimitsRequest GET management.{region}.kiro.dev/getUsageLimits（restJson，查询参数按字母序）。
func UsageLimitsRequest(rc *RequestContext) *httpx.IDERequest {
	host := ManagementHost(rc.Cred, rc.Config)
	q := "origin=" + OriginIDE
	if rc.Cred.ProfileArn != "" {
		q += "&profileArn=" + url.QueryEscape(rc.Cred.ProfileArn)
	}
	q += "&resourceType=AGENTIC_REQUEST"
	ua, amz := smithyUserAgents(rc.Config, SmithyClientVersion, "codewhispererruntime", extensionSmithyCustom(rc.Config, rc.MachineID))
	var h [][2]string
	if tt := usageTokenType(rc.Cred); tt != "" {
		h = append(h, [2]string{"TokenType", tt})
	}
	h = append(h,
		[2]string{"x-amz-user-agent", amz},
		[2]string{"user-agent", ua},
		[2]string{"host", host},
		[2]string{"amz-sdk-invocation-id", uuid.NewString()},
		[2]string{"amz-sdk-request", "attempt=1; max=3"},
		[2]string{"Authorization", "Bearer " + rc.Token},
		[2]string{"Connection", "keep-alive"},
	)
	return &httpx.IDERequest{Method: "GET", URL: "https://" + host + "/getUsageLimits?" + q, Header: h}
}

// IdCTokenRequest SSO OIDC CreateToken（restJson POST /token），自定义 UA 只有 KiroIDE。
func IdCTokenRequest(cfg *config.Config, region string, body []byte) *httpx.IDERequest {
	host := "oidc." + region + ".amazonaws.com"
	ua, amz := smithyUserAgents(cfg, SSOOIDCVersion, "sso-oidc", "KiroIDE")
	return &httpx.IDERequest{Method: "POST", URL: "https://" + host + "/token", Body: body, Header: [][2]string{
		{"content-type", "application/json"},
		{"content-length", strconv.Itoa(len(body))},
		{"x-amz-user-agent", amz},
		{"user-agent", ua},
		{"host", host},
		{"amz-sdk-invocation-id", uuid.NewString()},
		{"amz-sdk-request", "attempt=1; max=4"},
		{"Connection", "keep-alive"},
	}}
}

// SocialAuthRequest Kiro 认证服务（axios）：/refreshToken 和 /oauth/token。
func SocialAuthRequest(cfg *config.Config, machineID, path string, body []byte) *httpx.IDERequest {
	return &httpx.IDERequest{Method: "POST", URL: "https://" + SocialAuthHost + path, Body: body, Header: [][2]string{
		{"Accept", "application/json, text/plain, */*"},
		{"Content-Type", "application/json"},
		{"User-Agent", SocialUserAgent(cfg, machineID)},
		{"Content-Length", strconv.Itoa(len(body))},
		{"Accept-Encoding", "gzip, compress, deflate, br"},
		{"Host", SocialAuthHost},
		{"Connection", "keep-alive"},
	}}
}

// RedactedHeaders 记录用：令牌打码。
func RedactedHeaders(r *httpx.IDERequest) [][2]string {
	out := make([][2]string, len(r.Header))
	for i, h := range r.Header {
		if strings.EqualFold(h[0], "Authorization") {
			h[1] = "Bearer ***"
		}
		out[i] = h
	}
	return out
}
