package anthropic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FoyonaCZY/kiro2api/internal/access"
	"github.com/FoyonaCZY/kiro2api/internal/config"
)

// 模拟 Anthropic prompt cache 的计费口径。上游请求一字不改，只估算官方 API 上会命中多少，
// 写进返回给客户端的 usage 和用量库。按接入 Key 隔离，状态只在进程内存里。

const (
	ttl5m         = 5 * time.Minute
	ttl1h         = time.Hour
	cacheLookback = 10
	maxEntriesNS  = 4096
	mediaTokens   = 1600
)

var (
	cacheCfgMu sync.RWMutex
	cacheCfg   config.CacheEmulation
	tracker    = newCacheTracker()
)

// InitCache 启动时设置模拟缓存配置。
func InitCache(c config.CacheEmulation) {
	cacheCfgMu.Lock()
	cacheCfg = c
	cacheCfgMu.Unlock()
}

func cacheConfig() (config.CacheEmulation, bool) {
	cacheCfgMu.RLock()
	defer cacheCfgMu.RUnlock()
	return cacheCfg, cacheCfg.Enabled
}

// CacheSplit 命中结果，按块 token 占比表示。
type CacheSplit struct {
	Read     float64
	Creation float64
	OneHour  bool
}

// CacheUsage 具体 token 拆分。InputTokens 为未命中也未写缓存的部分。
type CacheUsage struct {
	InputTokens  int
	CacheRead    int
	CacheWrite5m int
	CacheWrite1h int
}

// Creation 写缓存合计。
func (u CacheUsage) Creation() int { return u.CacheWrite5m + u.CacheWrite1h }

// JSON 官方 usage 形状。
func (u CacheUsage) JSON(output int) map[string]any {
	return map[string]any{
		"input_tokens":                u.InputTokens,
		"cache_creation_input_tokens": u.Creation(),
		"cache_read_input_tokens":     u.CacheRead,
		"cache_creation": map[string]any{
			"ephemeral_5m_input_tokens": u.CacheWrite5m,
			"ephemeral_1h_input_tokens": u.CacheWrite1h,
		},
		"output_tokens": output,
	}
}

func clamp01(v float64) float64 { return math.Min(math.Max(v, 0), 1) }

// Apply 按比例换算；ratio 小于 1 时少报的部分回到 input_tokens，总数不变。
func (s CacheSplit) Apply(total int, readRatio, creationRatio float64) CacheUsage {
	total = max(total, 0)
	scale := func(f, r float64) int { return int(math.Round(float64(total) * clamp01(f) * clamp01(r))) }
	read := min(scale(s.Read, readRatio), total)
	creation := min(scale(s.Creation, creationRatio), total-read)
	u := CacheUsage{InputTokens: total - read - creation, CacheRead: read}
	if s.OneHour {
		u.CacheWrite1h = creation
	} else {
		u.CacheWrite5m = creation
	}
	return u
}

// UsageJSON 客户端看到的 usage；未开启或没有命中数据时只有 input/output。
func UsageJSON(split *CacheSplit, input, output int) map[string]any {
	if cfg, ok := cacheConfig(); ok && split != nil {
		return split.Apply(input, cfg.ReadRatio, cfg.CreationRatio).JSON(output)
	}
	return map[string]any{"input_tokens": input, "output_tokens": output}
}

// UsageForLog 入库拆分，与 UsageJSON 同一套计算。
func UsageForLog(split *CacheSplit, input int) CacheUsage {
	if cfg, ok := cacheConfig(); ok && split != nil {
		return split.Apply(input, cfg.ReadRatio, cfg.CreationRatio)
	}
	return CacheUsage{InputTokens: max(input, 0)}
}

type cacheBlock struct {
	fp         [32]byte
	cumulative uint64
}

type breakpoint struct {
	index int
	ttl   time.Duration
}

// CacheProfile 一次请求的前缀画像。
type CacheProfile struct {
	blocks      []cacheBlock
	breakpoints []breakpoint
	total       uint64
}

type pending struct {
	value      any
	tag        string
	tokens     uint64
	ttl        time.Duration
	hasTTL     bool
	messageEnd bool
}

// Profile 从客户端原始请求体构建画像；未开启或不够长时返回 nil。
func Profile(body []byte) *CacheProfile {
	if _, ok := cacheConfig(); !ok {
		return nil
	}
	return profileFromBody(body)
}

func decodeValue(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}

func profileFromBody(body []byte) *CacheProfile {
	v, err := decodeValue(body)
	if err != nil {
		return nil
	}
	payload, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	model, _ := payload["model"].(string)
	items := flattenPayload(payload)
	if len(items) == 0 {
		return nil
	}
	clientMarked := false
	for _, it := range items {
		if it.hasTTL {
			clientMarked = true
		}
	}
	state := sha256.Sum256(canonical(map[string]any{"model": model, "tool_choice": payload["tool_choice"]}))
	last := len(items) - 1
	p := &CacheProfile{}
	var marks []breakpoint
	var cum uint64
	var active time.Duration
	for i, it := range items {
		cum += it.tokens
		h := sha256.New()
		h.Write([]byte(it.tag))
		h.Write([]byte{0})
		h.Write(canonical(it.value))
		bh := h.Sum(nil)
		c := sha256.New()
		c.Write(state[:])
		c.Write(bh)
		copy(state[:], c.Sum(nil))
		p.blocks = append(p.blocks, cacheBlock{fp: state, cumulative: cum})
		var ttl time.Duration
		switch {
		case !clientMarked:
			if it.messageEnd || i == last {
				ttl = ttl5m
			}
		case it.hasTTL:
			active = it.ttl
			ttl = it.ttl
		case it.messageEnd:
			ttl = active
		}
		if ttl > 0 {
			marks = append(marks, breakpoint{index: i, ttl: min(ttl, ttl1h)})
		}
	}
	minimum := minCacheable(model)
	for _, m := range marks {
		if p.blocks[m.index].cumulative >= minimum {
			p.breakpoints = append(p.breakpoints, m)
		}
	}
	if len(p.breakpoints) == 0 {
		return nil
	}
	p.total = cum
	return p
}

// minCacheable Opus 和 Haiku 4.5 门槛 4096，其余 1024。
func minCacheable(model string) uint64 {
	m := strings.ToLower(model)
	if strings.Contains(m, "opus") || (strings.Contains(m, "haiku") && (strings.Contains(m, "4-5") || strings.Contains(m, "4.5"))) {
		return 4096
	}
	return 1024
}

func ttlOf(v any) (time.Duration, bool) {
	obj, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	cc, ok := obj["cache_control"].(map[string]any)
	if !ok {
		return 0, false
	}
	if cc["ttl"] == "1h" {
		return ttl1h, true
	}
	return ttl5m, true
}

func flattenPayload(p map[string]any) []pending {
	var out []pending
	add := func(v any, tag string, tokens uint64, end bool) {
		ttl, has := ttlOf(v)
		out = append(out, pending{value: v, tag: tag, tokens: tokens, ttl: ttl, hasTTL: has, messageEnd: end})
	}
	if tools, ok := p["tools"].([]any); ok {
		for _, t := range tools {
			add(t, "tool", CountTokens(string(canonical(t))), false)
		}
	}
	switch s := p["system"].(type) {
	case []any:
		for _, it := range s {
			add(it, "system", blockTokens(it), false)
		}
	case string:
		if s != "" {
			out = append(out, pending{value: s, tag: "system", tokens: CountTokens(s)})
		}
	}
	msgs, _ := p["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		tag := "user"
		if mm["role"] == "assistant" {
			tag = "assistant"
		}
		switch c := mm["content"].(type) {
		case string:
			out = append(out, pending{value: c, tag: tag, tokens: CountTokens(c), messageEnd: true})
		case []any:
			for i, it := range c {
				add(it, tag, blockTokens(it), i == len(c)-1)
			}
		}
	}
	return out
}

func blockTokens(v any) uint64 {
	b, _ := v.(map[string]any)
	field := func(k string) uint64 {
		if s, ok := b[k].(string); ok {
			return CountTokens(s)
		}
		return 0
	}
	switch b["type"] {
	case "text":
		return field("text")
	case "thinking":
		return field("thinking")
	case "image", "document":
		return mediaTokens
	case "tool_result":
		switch c := b["content"].(type) {
		case string:
			return CountTokens(c)
		case []any:
			var n uint64
			for _, it := range c {
				n += blockTokens(it)
			}
			return n
		}
		return 0
	case "tool_use":
		return CountTokens(string(canonical(b["input"]))) + field("name")
	}
	return CountTokens(string(canonical(v)))
}

// canonical 键排序、去掉 cache_control 的紧凑 JSON。
func canonical(v any) []byte {
	var buf bytes.Buffer
	writeCanonical(&buf, v)
	return buf.Bytes()
}

func writeCanonical(buf *bytes.Buffer, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			if k != "cache_control" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			writeCanonical(buf, t[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, it := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonical(buf, it)
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(t.String())
	case float64:
		buf.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	default:
		b, _ := Marshal(t)
		buf.Write(b)
	}
}

type cacheEntry struct {
	ttl     time.Duration
	expires time.Time
}

type cacheTracker struct {
	mu      sync.Mutex
	entries map[string]map[[32]byte]*cacheEntry
}

func newCacheTracker() *cacheTracker {
	return &cacheTracker{entries: map[string]map[[32]byte]*cacheEntry{}}
}

// Observe 上游 2xx 后调用：按接入 Key 查命中，并记下本次前缀。
func Observe(ctx context.Context, p *CacheProfile) *CacheSplit {
	if p == nil {
		return nil
	}
	ns := "default"
	if id, ok := access.CurrentKeyID(ctx); ok {
		ns = "key:" + strconv.FormatInt(id, 10)
	}
	s := tracker.observe(ns, p, time.Now())
	return &s
}

func (t *cacheTracker) observe(ns string, p *CacheProfile, now time.Time) CacheSplit {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, es := range t.entries {
		for fp, e := range es {
			if !e.expires.After(now) {
				delete(es, fp)
			}
		}
		if len(es) == 0 {
			delete(t.entries, k)
		}
	}
	es := t.entries[ns]
	if es == nil {
		es = map[[32]byte]*cacheEntry{}
		t.entries[ns] = es
	}
	var matched uint64
	for i, n := len(p.breakpoints)-1, 0; i >= 0 && n < cacheLookback; i, n = i-1, n+1 {
		b := p.blocks[p.breakpoints[i].index]
		if e, ok := es[b.fp]; ok {
			e.expires = now.Add(e.ttl)
			matched = b.cumulative
			break
		}
	}
	last := p.breakpoints[len(p.breakpoints)-1]
	cachedTo := p.blocks[last.index].cumulative
	for _, bp := range p.breakpoints {
		fp := p.blocks[bp.index].fp
		e, ok := es[fp]
		if !ok {
			e = &cacheEntry{ttl: bp.ttl, expires: now}
			es[fp] = e
		}
		e.ttl = max(e.ttl, bp.ttl)
		if exp := now.Add(bp.ttl); exp.After(e.expires) {
			e.expires = exp
		}
	}
	if len(es) > maxEntriesNS {
		type kv struct {
			exp time.Time
			fp  [32]byte
		}
		all := make([]kv, 0, len(es))
		for fp, e := range es {
			all = append(all, kv{e.expires, fp})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].exp.Before(all[j].exp) })
		for _, x := range all[:len(es)-maxEntriesNS] {
			delete(es, x.fp)
		}
	}
	total := float64(max(p.total, 1))
	var creation uint64
	if cachedTo > matched {
		creation = cachedTo - matched
	}
	return CacheSplit{Read: float64(matched) / total, Creation: float64(creation) / total, OneHour: last.ttl >= ttl1h}
}
