// Package redact 记录请求头时把密钥打码。
package redact

import (
	"encoding/json"
	"strings"
)

// Mask 打码后的占位值。
const Mask = "***"

// HeaderSensitive 与 kiro.rs 相同的判定。
func HeaderSensitive(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
		return true
	}
	return strings.Contains(n, "api-key") || strings.Contains(n, "secret") || (strings.Contains(n, "token") && n != "tokentype")
}

// EncodeHeaders 编成 JSON 对象，敏感头打码。
func EncodeHeaders(pairs [][2]string) string {
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		v := kv[1]
		if HeaderSensitive(kv[0]) {
			v = Mask
		}
		m[kv[0]] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}
