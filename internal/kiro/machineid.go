package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"

	"github.com/FoyonaCZY/kiro2api/internal/config"
	"github.com/google/uuid"
)

var (
	fallbackMu  sync.Mutex
	fallbackIDs = map[uint64]string{}
)

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// NormalizeMachineID 64 位十六进制原样返回；UUID 去连字符后重复一次补到 64 位。
func NormalizeMachineID(v string) (string, bool) {
	t := strings.TrimSpace(v)
	if len(t) == 64 && isHex(t) {
		return t, true
	}
	d := strings.ReplaceAll(t, "-", "")
	if len(d) == 32 && isHex(d) {
		return d + d, true
	}
	return "", false
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// RandomMachineID 每次都不同。
func RandomMachineID() string { return sha256Hex("KiroInstall/" + uuid.NewString()) }

// MachineIDFor 凭据级 > 全局 > 进程内兜底。不从 token 或 key 派生。
func MachineIDFor(c *Credentials, cfg *config.Config) string {
	if n, ok := NormalizeMachineID(c.MachineID); ok {
		return n
	}
	if n, ok := NormalizeMachineID(cfg.MachineID); ok {
		return n
	}
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	if v, ok := fallbackIDs[c.ID]; ok {
		return v
	}
	v := sha256Hex("KiroFallback/" + uuid.NewString())
	slog.Warn("凭据缺少 machineId，使用进程内兜底值", "credential_id", c.ID)
	fallbackIDs[c.ID] = v
	return v
}

// EnsurePersistentMachineID 保证凭据带合法 machineId，返回是否需要持久化。
func EnsurePersistentMachineID(c *Credentials) bool {
	if n, ok := NormalizeMachineID(c.MachineID); ok {
		if n != c.MachineID {
			c.MachineID = n
			return true
		}
		return false
	}
	if c.MachineID != "" {
		slog.Warn("凭据 machineId 不是 64 位十六进制或 UUID，已重新生成", "credential_id", c.ID)
	}
	c.MachineID = RandomMachineID()
	return true
}

// 以下 UA 都是从 Kiro 1.4.26 自己的客户端代码里抓出来的原文格式。
const (
	// SmithyClientVersion KiroRuntime、KiroControlPlaneBearer、CodeWhispererRuntime 三个客户端包都是 1.0.0。
	SmithyClientVersion = "1.0.0"
	// SSOOIDCVersion IdC 刷新用的 @aws-sdk/client-sso-oidc。
	SSOOIDCVersion = "3.980.0"
	// ClientName ACP 客户端名，UA 里写成 acp-client/kiro-ide。
	ClientName = "kiro-ide"
	// smithyMetrics smithy 写在 UA 里的特性标记。
	smithyMetrics = "m/N,E"
)

// smithyEscape 和 smithy 一样把 UA 段里不允许的字符换成连字符。
func smithyEscape(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("!$%&'*+-.^_`|~/", r) {
			return r
		}
		return '-'
	}, s)
}

// ResponsesUserAgent /v1/responses 的 User-Agent。这条请求是 Node https 直接发的，不经过 smithy。
func ResponsesUserAgent(cfg *config.Config, machineID string) string {
	return "KiroIDE " + cfg.KiroVersion + " " + machineID + " KAS/" + cfg.KASVersion + " acp-client/" + ClientName
}

// agentSmithyCustom Kiro Agent 里 smithy 客户端的自定义 UA（MCP、模型列表）。
func agentSmithyCustom(cfg *config.Config, machineID string) string {
	return smithyEscape("KiroIDE "+cfg.KiroVersion+" "+machineID+" KAS") + "/" + cfg.KASVersion + " acp-client/" + ClientName
}

// extensionSmithyCustom 扩展侧 smithy 客户端（额度查询）的自定义 UA。
func extensionSmithyCustom(cfg *config.Config, machineID string) string {
	return smithyEscape("KiroIDE " + cfg.KiroVersion + " " + machineID)
}

// smithyUserAgents 返回 user-agent 和 x-amz-user-agent。
func smithyUserAgents(cfg *config.Config, version, api, custom string) (string, string) {
	ua := "aws-sdk-js/" + version + " ua/2.1 os/" + cfg.SystemVersion + " lang/js md/nodejs#" + cfg.NodeVersion +
		" api/" + api + "#" + version + " " + smithyMetrics + " " + custom
	return ua, "aws-sdk-js/" + version + " " + custom
}

// SocialUserAgent Social 登录和刷新（axios）的 User-Agent。
func SocialUserAgent(cfg *config.Config, machineID string) string {
	return "KiroIDE-" + cfg.KiroVersion + "-" + machineID
}
