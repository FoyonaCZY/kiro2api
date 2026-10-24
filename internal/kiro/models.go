package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/httpx"
)

// ModelInfo ListAvailableModels 里和请求构造有关的部分（additionalModelRequestFieldsSchema）。
type ModelInfo struct {
	ID                     string
	ThinkingToggleable     bool
	DefaultThinkingEnabled bool
	EffortLevels           []string
	DefaultEffort          string
}

// Native 模型声明了思考开关或 effort，IDE 这时用 reasoning 字段而不是提示词。
func (m *ModelInfo) Native() bool {
	return m != nil && (m.ThinkingToggleable || len(m.EffortLevels) > 0)
}

// Reasoning 照 Kiro 1.4.26 的 additionalModelRequestFields → reasoning 规则生成。
func (m *ModelInfo) Reasoning(t *ThinkingRequest) *Reasoning {
	if !m.Native() {
		return nil
	}
	r := &Reasoning{}
	thinkingOff := false
	if m.ThinkingToggleable {
		r.Type = "disabled"
		if t != nil && t.Enabled {
			r.Type = "adaptive"
		}
		thinkingOff = r.Type == "disabled"
	}
	levels := m.EffortLevels
	if thinkingOff {
		// 关掉思考时不允许 xhigh / max。
		levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return l == "xhigh" || l == "max" })
	}
	if len(levels) > 0 {
		def := levels[0]
		if m.DefaultEffort != "" {
			def = levels[len(levels)-1]
			if slices.Contains(levels, m.DefaultEffort) {
				def = m.DefaultEffort
			}
		}
		r.Effort = def
		if t != nil && slices.Contains(levels, t.Effort) {
			r.Effort = t.Effort
		}
	}
	if r.Type == "" && r.Effort == "" {
		return nil
	}
	return r
}

// Reasoning /v1/responses 的 reasoning 字段。
type Reasoning struct {
	Type   string `json:"type,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type rawModel struct {
	ModelID string `json:"modelId"`
	Schema  *struct {
		Properties struct {
			Thinking *struct {
				Properties struct {
					Type *schemaEnum `json:"type"`
				} `json:"properties"`
			} `json:"thinking"`
			OutputConfig *effortHolder `json:"output_config"`
			Reasoning    *effortHolder `json:"reasoning"`
		} `json:"properties"`
	} `json:"additionalModelRequestFieldsSchema"`
}

type schemaEnum struct {
	Enum    []string `json:"enum"`
	Default string   `json:"default"`
}

type effortHolder struct {
	Properties struct {
		Effort *schemaEnum `json:"effort"`
	} `json:"properties"`
}

func parseModel(r rawModel) ModelInfo {
	m := ModelInfo{ID: r.ModelID}
	if r.Schema == nil {
		return m
	}
	p := r.Schema.Properties
	if p.Thinking != nil {
		if e := p.Thinking.Properties.Type; e != nil && slices.Contains(e.Enum, "disabled") && slices.Contains(e.Enum, "adaptive") {
			m.ThinkingToggleable = true
			m.DefaultThinkingEnabled = e.Default == "" || e.Default != "disabled"
		}
	}
	// output_config 优先于 reasoning，与 IDE 的查找顺序相同。
	for _, h := range []*effortHolder{p.OutputConfig, p.Reasoning} {
		if h != nil && h.Properties.Effort != nil && len(h.Properties.Effort.Enum) > 0 {
			m.EffortLevels = h.Properties.Effort.Enum
			m.DefaultEffort = h.Properties.Effort.Default
			break
		}
	}
	return m
}

const (
	modelCacheTTL     = time.Hour
	modelCacheFailTTL = 5 * time.Minute
	modelListTimeout  = 15 * time.Second
	modelListMaxPages = 10
)

type modelEntry struct {
	models  map[string]ModelInfo
	fetched time.Time
	ok      bool
}

// modelCache 每个凭据一份模型列表，IDE 启动和切账号时也会拉。
type modelCache struct {
	mu      sync.Mutex
	entries map[uint64]*modelEntry
	fetch   func(ctx context.Context, rc *RequestContext, px *httpx.Proxy) (map[string]ModelInfo, error)
}

func newModelCache() *modelCache {
	return &modelCache{entries: map[uint64]*modelEntry{}, fetch: fetchModels}
}

// Lookup 取凭据下某个模型；拉取失败或没有该模型时返回 nil（按提示词方案处理）。
func (c *modelCache) Lookup(ctx context.Context, id uint64, rc *RequestContext, px *httpx.Proxy, model string) *ModelInfo {
	c.mu.Lock()
	e := c.entries[id]
	fresh := e != nil && ((e.ok && time.Since(e.fetched) < modelCacheTTL) || (!e.ok && time.Since(e.fetched) < modelCacheFailTTL))
	c.mu.Unlock()
	if !fresh {
		fctx, cancel := context.WithTimeout(ctx, modelListTimeout)
		models, err := c.fetch(fctx, rc, px)
		cancel()
		e = &modelEntry{models: models, fetched: time.Now(), ok: err == nil}
		if err != nil {
			slog.Warn("拉取模型列表失败，按提示词方式处理思考", "credential_id", id, "err", err)
		}
		c.mu.Lock()
		c.entries[id] = e
		c.mu.Unlock()
	}
	if m, ok := e.models[model]; ok {
		return &m
	}
	return nil
}

// Forget 凭据刷新或删除后丢掉缓存。
func (c *modelCache) Forget(id uint64) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}

func fetchModels(ctx context.Context, rc *RequestContext, px *httpx.Proxy) (map[string]ModelInfo, error) {
	out := map[string]ModelInfo{}
	next := ""
	for page := 0; page < modelListMaxPages; page++ {
		resp, err := httpx.DoIDE(ctx, px, ListModelsRequest(rc, next), modelListTimeout)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("ListAvailableModels %d: %s", resp.StatusCode, body)
		}
		var r struct {
			Models    []rawModel `json:"models"`
			NextToken string     `json:"nextToken"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		for _, m := range r.Models {
			if m.ModelID != "" {
				out[m.ModelID] = parseModel(m)
			}
		}
		if r.NextToken == "" {
			break
		}
		next = r.NextToken
	}
	return out, nil
}
