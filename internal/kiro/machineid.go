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

// SDKVersion 额度查询仍走旧的 CodeWhisperer 客户端时用的 SDK 版本。
const SDKVersion = "3.997.37"

// RuntimeClientVersion Kiro 1.4.26 里 KiroRuntimeClient 的包版本，serviceId 是 KiroRuntime。
const RuntimeClientVersion = "1.0.0"

// RuntimeAPIName smithy 会把 serviceId 转成小写再放进 UA。
const RuntimeAPIName = "kiroruntime"

func runtimeCustom(cfg *config.Config, machineID string) string {
	// smithy 会把自定义 UA 里的空格换成连字符。
	return "KiroIDE-" + cfg.KiroVersion + "-" + machineID
}

// RuntimeUserAgent 对话请求的 user-agent，对齐 1.4.26 的 KiroRuntimeClient。
func RuntimeUserAgent(cfg *config.Config, machineID string) string {
	return "aws-sdk-js/" + RuntimeClientVersion + " ua/2.1 os/" + cfg.SystemVersion + " lang/js md/nodejs#" + cfg.NodeVersion +
		" api/" + RuntimeAPIName + "#" + RuntimeClientVersion + " m/E " + runtimeCustom(cfg, machineID)
}

// RuntimeAmzUserAgent 对话请求的 x-amz-user-agent：只保留 aws-sdk-js 和自定义段。
func RuntimeAmzUserAgent(cfg *config.Config, machineID string) string {
	return "aws-sdk-js/" + RuntimeClientVersion + " " + runtimeCustom(cfg, machineID)
}

// SDKUserAgent 对齐旧的 AWS SDK UA，额度查询还在用。
func SDKUserAgent(cfg *config.Config, machineID, apiName string) string {
	return "aws-sdk-js/" + SDKVersion + " ua/2.1 os/" + cfg.SystemVersion + " lang/js md/nodejs#" + cfg.NodeVersion +
		" api/" + apiName + "#" + SDKVersion + " m/E KiroIDE " + cfg.KiroVersion + " " + machineID
}

// SocialRefreshUserAgent Social 刷新的 UA。
func SocialRefreshUserAgent(cfg *config.Config, machineID string) string {
	return "KiroIDE-" + cfg.KiroVersion + "-" + machineID
}
