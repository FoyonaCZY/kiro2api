package kiro

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/google/uuid"
)

const (
	EndpointIDE = "ide"
	EndpointKRS = "krs"
	krsHost     = "runtime.us-east-1.kiro.dev"
)

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

// Endpoint 请求侧的差异点。
type Endpoint interface {
	Name() string
	APIURL(*RequestContext) string
	MCPURL(*RequestContext) string
	DecorateAPI(*http.Request, *RequestContext)
	DecorateMCP(*http.Request, *RequestContext)
	TransformAPIBody(string, *RequestContext) string
	APILogHeaders(*RequestContext) [][2]string
}

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

func setTokenType(h http.Header, c *Credentials) {
	if tt := c.UpstreamTokenType(); tt != "" {
		h.Set("TokenType", tt)
	}
}

// IDE Amazon Q 端点。
type IDE struct{}

func (IDE) Name() string { return EndpointIDE }
func (IDE) host(ctx *RequestContext) string {
	return "q." + ctx.Cred.EffectiveAPIRegion(ctx.Config) + ".amazonaws.com"
}
func (e IDE) APIURL(ctx *RequestContext) string {
	return "https://" + e.host(ctx) + "/generateAssistantResponse"
}
func (e IDE) MCPURL(ctx *RequestContext) string { return "https://" + e.host(ctx) + "/mcp" }
func (IDE) ua(ctx *RequestContext) string {
	return SDKUserAgent(ctx.Config, ctx.MachineID, "codewhispererstreaming")
}

func (e IDE) DecorateAPI(r *http.Request, ctx *RequestContext) {
	ua := e.ua(ctx)
	h := r.Header
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("x-amzn-kiro-agent-mode", ctx.Config.AgentMode)
	h.Set("x-amz-user-agent", ua)
	h.Set("user-agent", ua)
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+ctx.Token)
	r.Host = e.host(ctx)
	setTokenType(h, ctx.Cred)
}

func (e IDE) DecorateMCP(r *http.Request, ctx *RequestContext) {
	ua := e.ua(ctx)
	h := r.Header
	h.Set("x-amz-user-agent", ua)
	h.Set("user-agent", ua)
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+ctx.Token)
	if ctx.Cred.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", ctx.Cred.ProfileArn)
	}
	r.Host = e.host(ctx)
	setTokenType(h, ctx.Cred)
}

func (IDE) TransformAPIBody(b string, ctx *RequestContext) string {
	return InjectProfileArn(b, ctx.Cred.ProfileArn)
}

func (e IDE) APILogHeaders(ctx *RequestContext) [][2]string {
	ua := e.ua(ctx)
	h := [][2]string{
		{"content-type", "application/json"}, {"Connection", "close"},
		{"x-amzn-codewhisperer-optout", "true"}, {"x-amzn-kiro-agent-mode", ctx.Config.AgentMode},
		{"x-amz-user-agent", ua}, {"user-agent", ua}, {"host", e.host(ctx)},
		{"amz-sdk-request", "attempt=1; max=3"}, {"Authorization", "Bearer ***"},
	}
	if tt := ctx.Cred.UpstreamTokenType(); tt != "" {
		h = append(h, [2]string{"TokenType", tt})
	}
	return h
}

// KRS Kiro Runtime Service，只接受 OAuth；MCP 仍走 Q。
type KRS struct{}

func (KRS) Name() string                  { return EndpointKRS }
func (KRS) APIURL(*RequestContext) string { return "https://" + krsHost + "/generateAssistantResponse" }
func (KRS) MCPURL(ctx *RequestContext) string {
	return "https://q." + ctx.Cred.EffectiveAPIRegion(ctx.Config) + ".amazonaws.com/mcp"
}
func (KRS) ua(ctx *RequestContext) string {
	return "KiroIDE " + ctx.Config.KiroVersion + " " + ctx.MachineID
}
func (KRS) amzUA(ctx *RequestContext) string {
	return "aws-sdk-js/" + SDKVersion + " KiroIDE-" + ctx.Config.KiroVersion + "-" + ctx.MachineID
}

func (e KRS) DecorateAPI(r *http.Request, ctx *RequestContext) {
	h := r.Header
	h.Set("Accept", "*/*")
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("x-amzn-kiro-agent-mode", ctx.Config.AgentMode)
	h.Set("x-amz-user-agent", e.amzUA(ctx))
	h.Set("user-agent", e.ua(ctx))
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+ctx.Token)
	if ctx.Cred.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", ctx.Cred.ProfileArn)
	}
	r.Host = krsHost
	setTokenType(h, ctx.Cred)
}

func (e KRS) DecorateMCP(r *http.Request, ctx *RequestContext) {
	h := r.Header
	h.Set("x-amz-user-agent", e.amzUA(ctx))
	h.Set("user-agent", e.ua(ctx))
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+ctx.Token)
	if ctx.Cred.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", ctx.Cred.ProfileArn)
	}
	r.Host = "q." + ctx.Cred.EffectiveAPIRegion(ctx.Config) + ".amazonaws.com"
	setTokenType(h, ctx.Cred)
}

func (KRS) TransformAPIBody(b string, ctx *RequestContext) string {
	return InjectProfileArn(b, ctx.Cred.ProfileArn)
}

func (e KRS) APILogHeaders(ctx *RequestContext) [][2]string {
	h := [][2]string{
		{"content-type", "application/json"}, {"Accept", "*/*"}, {"Connection", "close"},
		{"x-amzn-codewhisperer-optout", "true"}, {"x-amzn-kiro-agent-mode", ctx.Config.AgentMode},
		{"x-amz-user-agent", e.amzUA(ctx)}, {"user-agent", e.ua(ctx)}, {"host", krsHost},
		{"amz-sdk-request", "attempt=1; max=3"}, {"Authorization", "Bearer ***"},
	}
	if ctx.Cred.ProfileArn != "" {
		h = append(h, [2]string{"x-amzn-kiro-profile-arn", ctx.Cred.ProfileArn})
	}
	if tt := ctx.Cred.UpstreamTokenType(); tt != "" {
		h = append(h, [2]string{"TokenType", tt})
	}
	return h
}
