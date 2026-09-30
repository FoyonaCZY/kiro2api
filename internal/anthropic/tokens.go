package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/httpx"
)

// CountTokensConfig 外部 count_tokens API，可选。
type CountTokensConfig struct {
	APIURL   string
	APIKey   string
	AuthType string
	Proxy    *httpx.Proxy
}

var countCfg CountTokensConfig

// InitCountTokens 启动时调用一次。
func InitCountTokens(c CountTokensConfig) { countCfg = c }

func isNonWestern(r rune) bool {
	switch {
	case r <= 0x024F, r >= 0x1E00 && r <= 0x1EFF, r >= 0x2C60 && r <= 0x2C7F,
		r >= 0xA720 && r <= 0xA7FF, r >= 0xAB30 && r <= 0xAB6F:
		return false
	}
	return true
}

// CountTokens 与 kiro.rs 相同的估算：非西文 4 单位、西文 1 单位，4 单位 1 token，短文本上浮。
func CountTokens(text string) uint64 {
	var units float64
	for _, r := range text {
		if isNonWestern(r) {
			units += 4
		} else {
			units++
		}
	}
	t := units / 4
	switch {
	case t < 100:
		t *= 1.5
	case t < 200:
		t *= 1.3
	case t < 300:
		t *= 1.25
	case t < 800:
		t *= 1.2
	}
	return uint64(t)
}

func schemaString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "{}"
	}
	b, _ := Marshal(v)
	return string(b)
}

func countLocal(system SystemList, messages []Message, tools []Tool) uint64 {
	var total uint64
	for _, s := range system {
		total += CountTokens(s.Text)
	}
	for _, m := range messages {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			total += CountTokens(s)
			continue
		}
		var arr []map[string]any
		if json.Unmarshal(m.Content, &arr) == nil {
			for _, it := range arr {
				if t, ok := it["text"].(string); ok {
					total += CountTokens(t)
				}
			}
		}
	}
	for _, t := range tools {
		total += CountTokens(t.Name) + CountTokens(t.Description) + CountTokens(schemaString(t.InputSchema))
	}
	return max(total, 1)
}

// CountAllTokens 配了远程 API 先调远程，失败回退本地。
func CountAllTokens(ctx context.Context, model string, system SystemList, messages []Message, tools []Tool) uint64 {
	if countCfg.APIURL != "" {
		n, err := countRemote(ctx, model, system, messages, tools)
		if err == nil {
			return n
		}
		slog.Warn("远程 count_tokens API 调用失败，回退到本地计算", "err", err)
	}
	return countLocal(system, messages, tools)
}

func countRemote(ctx context.Context, model string, system SystemList, messages []Message, tools []Tool) (uint64, error) {
	cl, err := httpx.New(countCfg.Proxy, 300*time.Second)
	if err != nil {
		return 0, err
	}
	body, _ := Marshal(CountTokensRequest{Model: model, Messages: messages, System: system, Tools: tools})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, countCfg.APIURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if countCfg.APIKey != "" {
		if countCfg.AuthType == "bearer" {
			req.Header.Set("Authorization", "Bearer "+countCfg.APIKey)
		} else {
			req.Header.Set("x-api-key", countCfg.APIKey)
		}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("API 返回错误状态: %d", resp.StatusCode)
	}
	var out struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return uint64(max(out.InputTokens, 0)), nil
}

// EstimateOutputTokens 非流式响应的输出估算。
func EstimateOutputTokens(content []map[string]any) int {
	total := 0
	for _, b := range content {
		if t, ok := b["text"].(string); ok {
			total += int(CountTokens(t))
		}
		if b["type"] == "tool_use" {
			if in, ok := b["input"]; ok {
				s, _ := Marshal(in)
				total += int(CountTokens(string(s)))
			}
		}
	}
	return max(total, 1)
}
