// Package config 读取与 kiro.rs 兼容的 config.json。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// CacheEmulation 模拟 prompt cache 计费。
type CacheEmulation struct {
	Enabled       bool    `json:"enabled"`
	ReadRatio     float64 `json:"readRatio"`
	CreationRatio float64 `json:"creationRatio"`
	// HitRate 大于 0 时按这个命中率出账（每个请求上下浮动 1 个点），不再按前缀匹配估算；存量数据启动时按它重算。
	HitRate float64 `json:"hitRate"`
}

// Config 字段名与 kiro.rs 的 camelCase JSON 一致，线上配置可直接复用。
type Config struct {
	Host                  string         `json:"host"`
	Port                  int            `json:"port"`
	Region                string         `json:"region"`
	AuthRegion            string         `json:"authRegion,omitempty"`
	APIRegion             string         `json:"apiRegion,omitempty"`
	KiroVersion           string         `json:"kiroVersion"`
	KASVersion            string         `json:"kasVersion"`
	MachineID             string         `json:"machineId,omitempty"`
	APIKey                string         `json:"apiKey,omitempty"`
	SystemVersion         string         `json:"systemVersion"`
	NodeVersion           string         `json:"nodeVersion"`
	AgentMode             string         `json:"agentMode"`
	TLSBackend            string         `json:"tlsBackend,omitempty"`
	CountTokensAPIURL     string         `json:"countTokensApiUrl,omitempty"`
	CountTokensAPIKey     string         `json:"countTokensApiKey,omitempty"`
	CountTokensAuthType   string         `json:"countTokensAuthType"`
	ProxyURL              string         `json:"proxyUrl,omitempty"`
	ProxyUsername         string         `json:"proxyUsername,omitempty"`
	ProxyPassword         string         `json:"proxyPassword,omitempty"`
	AdminAPIKey           string         `json:"adminApiKey,omitempty"`
	LoadBalancingMode     string         `json:"loadBalancingMode"`
	ExtractThinking       bool           `json:"extractThinking"`
	RateLimitCooldownSecs int            `json:"rateLimitCooldownSecs"`
	CacheEmulation        CacheEmulation `json:"cacheEmulation"`
	DefaultEndpoint       string         `json:"defaultEndpoint"`
	// MaxDiskGB 数据目录占用上限，超过时丢最旧的请求明细；0 表示不限。
	MaxDiskGB float64                    `json:"maxDiskGB"`
	Endpoints map[string]json.RawMessage `json:"endpoints,omitempty"`

	path string
}

// Default 默认值。Region 跟 Kiro IDE 一样兜底 us-east-1；KASVersion 是 1.4.26 内置 Kiro Agent 的版本。
func Default() *Config {
	return &Config{
		Host:                  "127.0.0.1",
		Port:                  8080,
		Region:                "us-east-1",
		KiroVersion:           "1.4.26",
		KASVersion:            "0.66.56",
		SystemVersion:         "win32#10.0.26200",
		NodeVersion:           "24.21.0",
		AgentMode:             "vibe",
		TLSBackend:            "rustls",
		CountTokensAuthType:   "x-api-key",
		LoadBalancingMode:     "priority",
		ExtractThinking:       true,
		RateLimitCooldownSecs: 60,
		CacheEmulation:        CacheEmulation{ReadRatio: 1, CreationRatio: 1, HitRate: 0.97},
		DefaultEndpoint:       "krs",
		MaxDiskGB:             10,
	}
}

// Load 文件不存在时返回默认配置。
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	cfg.path = path
	cfg.floorVersions()
	return cfg, nil
}

// floorVersions 配置里写的 IDE 版本比内置的旧时改用内置版本，免得旧配置把指纹钉在老版本上。
// 管理端改设置写回 config.json 时，新版本号会一并写进去。
func (c *Config) floorVersions() {
	d := Default()
	for _, f := range []struct {
		name     string
		cur      *string
		fallback string
	}{
		{"kiroVersion", &c.KiroVersion, d.KiroVersion},
		{"kasVersion", &c.KASVersion, d.KASVersion},
		{"nodeVersion", &c.NodeVersion, d.NodeVersion},
	} {
		if olderVersion(*f.cur, f.fallback) {
			if *f.cur != "" {
				slog.Warn("配置里的版本比内置的旧，改用内置版本", "field", f.name, "config", *f.cur, "using", f.fallback)
			}
			*f.cur = f.fallback
		}
	}
}

// olderVersion 按点分数字比较，a 为空或解析不了也算旧。
func olderVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimSpace(a), "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			n, err := strconv.Atoi(pa[i])
			if err != nil {
				return true
			}
			x = n
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Path 配置文件路径。
func (c *Config) Path() string { return c.path }

// EffectiveAuthRegion Token 刷新用的区域。
func (c *Config) EffectiveAuthRegion() string {
	if c.AuthRegion != "" {
		return c.AuthRegion
	}
	return c.Region
}

// UsageMaxBytes 用量库的大小上限：总上限留 1GB 给镜像、日志和其它库。
func (c *Config) UsageMaxBytes() int64 {
	if c.MaxDiskGB <= 0 {
		return 0
	}
	total := int64(c.MaxDiskGB * (1 << 30))
	if total <= 2<<30 {
		return total / 2
	}
	return total - 1<<30
}

// Save 写回原配置文件。
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("配置文件路径未知，无法保存配置")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0o600)
}
