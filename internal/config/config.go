// Package config 读取与 kiro.rs 兼容的 config.json。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// CacheEmulation 模拟 prompt cache 计费。
type CacheEmulation struct {
	Enabled       bool    `json:"enabled"`
	ReadRatio     float64 `json:"readRatio"`
	CreationRatio float64 `json:"creationRatio"`
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
		CacheEmulation:        CacheEmulation{ReadRatio: 1, CreationRatio: 1},
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
	return cfg, nil
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
