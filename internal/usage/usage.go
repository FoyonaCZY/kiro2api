// Package usage 请求明细、模型单价、账号成本。表结构与 kiro.rs 的 usage.sqlite 一致，可直接接管线上库。
package usage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	retainRequests         = 20_000
	pruneEvery             = 100
	maxBlob                = 256 * 1024
	lengthErrorBlob        = 4 * 1024 * 1024
	cacheReadMultiplier    = 0.1
	cacheWrite5mMultiplier = 1.25
	cacheWrite1hMultiplier = 2.0
)

// ModelPrice 每百万 token 单价；缓存单价为空时按输入价倍率。
type ModelPrice struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Aliases          []string `json:"aliases"`
	InputPerM        float64  `json:"inputPerM"`
	OutputPerM       float64  `json:"outputPerM"`
	CacheReadPerM    *float64 `json:"cacheReadPerM"`
	CacheWrite5mPerM *float64 `json:"cacheWrite5mPerM"`
	CacheWrite1hPerM *float64 `json:"cacheWrite1hPerM"`
}

// Tokens 计费 token。
type Tokens struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h int64
}

// RequestRecord 一条请求。
type RequestRecord struct {
	ID                 uint64  `json:"id"`
	Time               string  `json:"time"`
	Model              string  `json:"model"`
	Stream             bool    `json:"stream"`
	Status             int     `json:"status"`
	DurationMs         uint64  `json:"durationMs"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	Error              *string `json:"error"`
	Account            string  `json:"account"`
	Endpoint           string  `json:"endpoint"`
	RequestBytes       uint64  `json:"requestBytes"`
	StopReason         string  `json:"stopReason"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheWrite5mTokens int64   `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	// Credits 上游 usage 里报的 kiro:credits；nil 表示上游没报（老记录、请求没到上游）。
	Credits *float64 `json:"credits"`
}

// tokens 失败请求（含断流）不计费。
func (r *RequestRecord) tokens() Tokens {
	if r.Status >= 400 {
		return Tokens{}
	}
	return Tokens{r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens}
}

// RequestView 列表项。
type RequestView struct {
	RequestRecord
	CostUSD *float64 `json:"costUsd"`
}

// RequestDetail 详情。
type RequestDetail struct {
	RequestView
	InboundHeaders  string `json:"inboundHeaders"`
	InboundBody     string `json:"inboundBody"`
	OutboundHeaders string `json:"outboundHeaders"`
	OutboundBody    string `json:"outboundBody"`
	ResponseBody    string `json:"responseBody"`
	PayloadProfile  string `json:"payloadProfile"`
}

// ModelUsage 模型汇总。
type ModelUsage struct {
	Model              string  `json:"model"`
	Requests           uint64  `json:"requests"`
	Errors             uint64  `json:"errors"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheWrite5mTokens int64   `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	CostUSD            float64 `json:"costUsd"`
	Credits            float64 `json:"credits"`
}

// Summary 总汇总。
type Summary struct {
	Requests           uint64       `json:"requests"`
	Errors             uint64       `json:"errors"`
	InputTokens        int64        `json:"inputTokens"`
	OutputTokens       int64        `json:"outputTokens"`
	CacheReadTokens    int64        `json:"cacheReadTokens"`
	CacheWrite5mTokens int64        `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64        `json:"cacheWrite1hTokens"`
	CostUSD            float64      `json:"costUsd"`
	Credits            float64      `json:"credits"`
	UnpricedRequests   uint64       `json:"unpricedRequests"`
	ByModel            []ModelUsage `json:"byModel"`
}

// AccountCost 一个凭据的人民币成本和按当前单价重算的累计消耗。
type AccountCost struct {
	CredentialID       uint64   `json:"credentialId"`
	Label              string   `json:"label"`
	Removed            bool     `json:"removed"`
	CostCNY            *float64 `json:"costCny"`
	UsageUSD           float64  `json:"usageUsd"`
	CostRatio          *float64 `json:"costRatio"`
	Requests           uint64   `json:"requests"`
	Errors             uint64   `json:"errors"`
	UnpricedRequests   uint64   `json:"unpricedRequests"`
	InputTokens        int64    `json:"inputTokens"`
	OutputTokens       int64    `json:"outputTokens"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWrite5mTokens int64    `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64    `json:"cacheWrite1hTokens"`
	Credits            float64  `json:"credits"`
}

// AccountCostReport 账号成本报表。
type AccountCostReport struct {
	Accounts       []AccountCost `json:"accounts"`
	CostedAccounts uint64        `json:"costedAccounts"`
	CostCNY        float64       `json:"costCny"`
	CostedUsageUSD float64       `json:"costedUsageUsd"`
	UsageUSD       float64       `json:"usageUsd"`
	CostRatio      *float64      `json:"costRatio"`
	Credits        float64       `json:"credits"`
}

func costRatio(cny, usd float64) *float64 {
	if cny > 0 && usd > 0 {
		v := cny / usd
		return &v
	}
	return nil
}

// NewRequest 一次请求的入库内容。CredentialID 为 0 表示没打到上游。
type NewRequest struct {
	CredentialID       uint64
	Model              string
	Stream             bool
	Status             int
	DurationMs         uint64
	InputTokens        int64
	OutputTokens       int64
	Error              string
	Account            string
	Endpoint           string
	RequestBytes       uint64
	StopReason         string
	CacheReadTokens    int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	// Credits 上游实际扣的 credits，nil 表示没报。失败请求也照记，扣了就是扣了。
	Credits         *float64
	InboundHeaders  string
	InboundBody     string
	OutboundHeaders string
	OutboundBody    string
	ResponseBody    string
}

// Log usage.sqlite。
type Log struct {
	mu         sync.Mutex
	db         *sql.DB
	retain     int64
	maxBytes   int64
	sincePrune atomic.Uint64
}

// Open path 为空时用内存库，不限大小。
func Open(path string) *Log { return openWith(path, retainRequests, 0) }

// OpenLimited 库里在用的数据超过 maxBytes 时从最旧的请求明细删起，汇总不受影响。
func OpenLimited(path string, maxBytes int64) *Log { return openWith(path, retainRequests, maxBytes) }

func openDB(path string) (*sql.DB, error) {
	dsn := "file::memory:?_pragma=busy_timeout(2000)"
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		dsn = "file:" + path + "?_pragma=busy_timeout(2000)&_pragma=auto_vacuum(INCREMENTAL)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=journal_size_limit(67108864)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, db.Ping()
}

func openWithRetain(path string, retain int64) *Log { return openWith(path, retain, 0) }

func openWith(path string, retain, maxBytes int64) *Log {
	db, err := openDB(path)
	if err != nil {
		slog.Error("打开用量数据库失败，改用内存库", "err", err)
		db, _ = openDB("")
	}
	if err := initSchema(db); err != nil {
		slog.Error("初始化用量数据库失败", "err", err)
	}
	if path != "" {
		if err := importLegacyJSON(db, path); err != nil {
			slog.Warn("导入旧的 usage.json 失败", "err", err)
		}
	}
	if err := excludeFailedTokens(db); err != nil {
		slog.Warn("扣回失败请求 token 失败", "err", err)
	}
	_ = prune(db, retain)
	if path != "" && maxBytes > 0 {
		ndb, err := ensureIncrementalVacuum(db, path, maxBytes)
		if err != nil {
			slog.Warn("用量库空间回收没开成，旧数据仍会按上限删除", "err", err)
		}
		if ndb == nil {
			if ndb, err = openDB(path); err != nil {
				slog.Error("重新打开用量数据库失败，改用内存库", "err", err)
				ndb, _ = openDB("")
				_ = initSchema(ndb)
			}
		}
		db = ndb
		if n, err := pruneBySize(db, maxBytes, sizeLowWater); err != nil {
			slog.Warn("按大小清理用量库失败", "err", err)
		} else if n > 0 {
			slog.Info("用量库超过上限，已删除最旧的请求明细", "rows", n)
		}
	}
	return &Log{db: db, retain: retain, maxBytes: maxBytes}
}

func nowRFC3339() string { return time.Now().UTC().Format("2006-01-02T15:04:05.999999999-07:00") }

func normalizeAlias(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Clip 按字节截断到 UTF-8 边界，并注明截掉多少。
func Clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return fmt.Sprintf("%s\n…[truncated %d bytes]", s[:end], len(s)-end)
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func nz(v int64) int64 { return max(v, 0) }

// Record 插入一行并累加模型和账号汇总。失败请求只计次数，token 不进汇总。
func (l *Log) Record(r NewRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	model := truncRunes(r.Model, 200)
	key := normalizeAlias(model)
	in, out := nz(r.InputTokens), nz(r.OutputTokens)
	cr, c5, c1 := nz(r.CacheReadTokens), nz(r.CacheWrite5mTokens), nz(r.CacheWrite1hTokens)
	var errText any
	if r.Error != "" {
		errText = r.Error
	}
	errs := int64(0)
	if r.Status >= 400 {
		errs = 1
	}
	lengthErr := strings.Contains(r.Error, "CONTENT_LENGTH_EXCEEDS_THRESHOLD") || strings.Contains(r.Error, "Input is too long")
	profile := PayloadProfile(r.OutboundBody)
	if lengthErr {
		slog.Warn("上游拒绝：输入过长，字段长度", "profile", profile)
	}
	outLimit := maxBlob
	if lengthErr {
		outLimit = lengthErrorBlob
	}
	var cred, credits any
	if r.CredentialID != 0 {
		cred = int64(r.CredentialID)
	}
	spent := 0.0
	if c := r.Credits; c != nil && *c >= 0 && !math.IsInf(*c, 0) && !math.IsNaN(*c) {
		credits, spent = *c, *c
	}
	tx, err := l.db.Begin()
	if err != nil {
		slog.Warn("记录请求失败", "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`INSERT INTO requests(time, model, stream, status, duration_ms, input_tokens, output_tokens, error, account, endpoint, request_bytes, stop_reason, inbound_headers, inbound_body, outbound_headers, outbound_body, response_body, payload_profile, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credential_id, credits)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nowRFC3339(), model, boolInt(r.Stream), r.Status, int64(r.DurationMs), in, out, errText,
		Clip(r.Account, 200), Clip(r.Endpoint, 80), int64(r.RequestBytes), Clip(r.StopReason, 80),
		Clip(r.InboundHeaders, maxBlob), Clip(r.InboundBody, maxBlob), Clip(r.OutboundHeaders, maxBlob),
		Clip(r.OutboundBody, outLimit), Clip(r.ResponseBody, maxBlob), Clip(profile, 16*1024), cr, c5, c1, cred, credits); err != nil {
		slog.Warn("记录请求失败", "err", err)
		return
	}
	bill := func(v int64) int64 {
		if errs == 0 {
			return v
		}
		return 0
	}
	if _, err := tx.Exec(`INSERT INTO model_totals(model, requests, errors, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credits)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET requests = requests + 1, errors = errors + excluded.errors,
		  input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
		  cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
		  cache_write_5m_tokens = cache_write_5m_tokens + excluded.cache_write_5m_tokens,
		  cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
		  credits = credits + excluded.credits`,
		key, errs, bill(in), bill(out), bill(cr), bill(c5), bill(c1), spent); err != nil {
		slog.Warn("累计用量失败", "err", err)
		return
	}
	if r.CredentialID != 0 {
		if err := addAccountTotals(tx, r.CredentialID, key, 1, errs, Tokens{bill(in), bill(out), bill(cr), bill(c5), bill(c1)}, spent); err != nil {
			slog.Warn("累计账号用量失败", "err", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("提交用量失败", "err", err)
		return
	}
	tick := l.sincePrune.Add(1) - 1
	if l.retain <= 100 || tick%pruneEvery == pruneEvery-1 {
		if err := prune(l.db, l.retain); err != nil {
			slog.Warn("清理旧请求失败", "err", err)
		}
		if n, err := pruneBySize(l.db, l.maxBytes, sizeLowWater); err != nil {
			slog.Warn("按大小清理用量库失败", "err", err)
		} else if n > 0 {
			slog.Info("用量库超过上限，已删除最旧的请求明细", "rows", n)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func addAccountTotals(db execer, cred uint64, model string, requests, errs int64, t Tokens, credits float64) error {
	_, err := db.Exec(`INSERT INTO account_totals(credential_id, model, requests, errors, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credits)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(credential_id, model) DO UPDATE SET requests = requests + excluded.requests, errors = errors + excluded.errors,
		  input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
		  cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
		  cache_write_5m_tokens = cache_write_5m_tokens + excluded.cache_write_5m_tokens,
		  cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
		  credits = credits + excluded.credits`,
		int64(cred), model, requests, errs, nz(t.Input), nz(t.Output), nz(t.CacheRead), nz(t.CacheWrite5m), nz(t.CacheWrite1h), credits)
	return err
}

const recordColumns = "id, time, model, stream, status, duration_ms, input_tokens, output_tokens, error, account, endpoint, request_bytes, stop_reason, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credits"

type scanner interface{ Scan(...any) error }

func scanRecord(s scanner, extra ...any) (*RequestRecord, error) {
	var r RequestRecord
	var id, stream, status, dur int64
	var errText, account, endpoint, stop sql.NullString
	var reqBytes, cr, c5, c1 sql.NullInt64
	var credits sql.NullFloat64
	dest := append([]any{&id, &r.Time, &r.Model, &stream, &status, &dur, &r.InputTokens, &r.OutputTokens, &errText, &account, &endpoint, &reqBytes, &stop, &cr, &c5, &c1, &credits}, extra...)
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	r.ID, r.Stream, r.Status, r.DurationMs = uint64(id), stream != 0, int(status), uint64(max(dur, 0))
	if errText.Valid {
		r.Error = &errText.String
	}
	r.Account, r.Endpoint, r.StopReason = account.String, endpoint.String, stop.String
	r.RequestBytes = uint64(max(reqBytes.Int64, 0))
	r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens = cr.Int64, c5.Int64, c1.Int64
	if credits.Valid {
		r.Credits = &credits.Float64
	}
	return &r, nil
}

// List 最近 limit 条（1~500）。
func (l *Log) List(limit int) []RequestView {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit = min(max(limit, 1), 500)
	prices, err := loadPrices(l.db)
	if err != nil {
		slog.Warn("读取定价失败", "err", err)
	}
	rows, err := l.db.Query("SELECT "+recordColumns+" FROM requests ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		slog.Warn("读取请求失败", "err", err)
		return []RequestView{}
	}
	defer rows.Close()
	out := []RequestView{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, RequestView{RequestRecord: *r, CostUSD: Charge(prices, r.Model, r.tokens())})
	}
	return out
}

// Get 详情。
func (l *Log) Get(id uint64) *RequestDetail {
	l.mu.Lock()
	defer l.mu.Unlock()
	prices, _ := loadPrices(l.db)
	var ih, ib, oh, ob, rb, pp sql.NullString
	r, err := scanRecord(l.db.QueryRow("SELECT "+recordColumns+", inbound_headers, inbound_body, outbound_headers, outbound_body, response_body, payload_profile FROM requests WHERE id = ?", int64(id)),
		&ih, &ib, &oh, &ob, &rb, &pp)
	if err != nil {
		return nil
	}
	return &RequestDetail{
		RequestView:    RequestView{RequestRecord: *r, CostUSD: Charge(prices, r.Model, r.tokens())},
		InboundHeaders: ih.String, InboundBody: ib.String, OutboundHeaders: oh.String, OutboundBody: ob.String,
		ResponseBody: rb.String, PayloadProfile: pp.String,
	}
}

// Summary 按模型汇总，费用按当前单价计算。
func (l *Log) Summary() Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	prices, _ := loadPrices(l.db)
	s := Summary{ByModel: []ModelUsage{}}
	rows, err := l.db.Query(`SELECT model, requests, errors, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credits FROM model_totals`)
	if err != nil {
		slog.Warn("读取用量汇总失败", "err", err)
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var m ModelUsage
		var req, errs int64
		var t Tokens
		if rows.Scan(&m.Model, &req, &errs, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h, &m.Credits) != nil {
			continue
		}
		m.Requests, m.Errors = uint64(max(req, 0)), uint64(max(errs, 0))
		m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWrite5mTokens, m.CacheWrite1hTokens = t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h
		if c := Charge(prices, m.Model, t); c != nil {
			m.CostUSD = *c
		} else {
			s.UnpricedRequests += m.Requests
		}
		s.ByModel = append(s.ByModel, m)
	}
	sort.SliceStable(s.ByModel, func(i, j int) bool { return s.ByModel[i].CostUSD > s.ByModel[j].CostUSD })
	for _, m := range s.ByModel {
		s.Requests += m.Requests
		s.Errors += m.Errors
		s.InputTokens += m.InputTokens
		s.OutputTokens += m.OutputTokens
		s.CacheReadTokens += m.CacheReadTokens
		s.CacheWrite5mTokens += m.CacheWrite5mTokens
		s.CacheWrite1hTokens += m.CacheWrite1hTokens
		s.CostUSD += m.CostUSD
		s.Credits += m.Credits
	}
	return s
}

// Prices 全部单价。
func (l *Log) Prices() []ModelPrice {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, _ := loadPrices(l.db)
	if p == nil {
		p = []ModelPrice{}
	}
	return p
}

func cleanAliases(raw []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range raw {
		a = normalizeAlias(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// UpsertPrice 校验后新增或更新；别名不能被其它单价占用。
func (l *Log) UpsertPrice(p ModelPrice) (ModelPrice, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Aliases = cleanAliases(p.Aliases)
	if p.Name == "" {
		return p, errors.New("名称不能为空")
	}
	if len(p.Aliases) == 0 {
		return p, errors.New("至少要有一个模型别名")
	}
	if p.InputPerM < 0 || p.OutputPerM < 0 {
		return p, errors.New("单价不能为负数")
	}
	for _, v := range []*float64{p.CacheReadPerM, p.CacheWrite5mPerM, p.CacheWrite1hPerM} {
		if v != nil && (*v < 0 || math.IsInf(*v, 0) || math.IsNaN(*v)) {
			return p, errors.New("缓存单价不能为负数")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	existing, err := loadPrices(l.db)
	if err != nil {
		return p, err
	}
	for _, o := range existing {
		if o.ID == p.ID {
			continue
		}
		for _, a := range o.Aliases {
			for _, b := range p.Aliases {
				if a == b {
					return p, fmt.Errorf("别名已被「%s」占用", o.Name)
				}
			}
		}
	}
	aliases, _ := json.Marshal(p.Aliases)
	_, err = l.db.Exec(`INSERT INTO prices(id, name, aliases, input_per_m, output_per_m, cache_read_per_m, cache_write_5m_per_m, cache_write_1h_per_m)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, aliases = excluded.aliases, input_per_m = excluded.input_per_m,
		  output_per_m = excluded.output_per_m, cache_read_per_m = excluded.cache_read_per_m,
		  cache_write_5m_per_m = excluded.cache_write_5m_per_m, cache_write_1h_per_m = excluded.cache_write_1h_per_m`,
		p.ID, p.Name, string(aliases), p.InputPerM, p.OutputPerM, p.CacheReadPerM, p.CacheWrite5mPerM, p.CacheWrite1hPerM)
	return p, err
}

// DeletePrice 删除单价。
func (l *Log) DeletePrice(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	res, err := l.db.Exec(`DELETE FROM prices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("定价不存在")
	}
	return nil
}

// SetAccountCost nil 表示清空（未知），0 表示免费账号。
func (l *Log) SetAccountCost(cred uint64, cost *float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cost == nil {
		_, err := l.db.Exec(`DELETE FROM account_costs WHERE credential_id = ?`, int64(cred))
		return err
	}
	if math.IsNaN(*cost) || math.IsInf(*cost, 0) || *cost < 0 {
		return errors.New("成本不能为负数")
	}
	_, err := l.db.Exec(`INSERT INTO account_costs(credential_id, cost_cny, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(credential_id) DO UPDATE SET cost_cny = excluded.cost_cny, updated_at = excluded.updated_at`,
		int64(cred), *cost, nowRFC3339())
	return err
}

// Label 当前配置里的凭据 (id, 显示名)。
type Label struct {
	ID    uint64
	Label string
}

// AccountReport 消耗每次按当前单价从 token 重算；已删除但还有历史的凭据标为 removed。
func (l *Log) AccountReport(labels []Label) AccountCostReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	prices, _ := loadPrices(l.db)
	costs := map[uint64]float64{}
	if rows, err := l.db.Query(`SELECT credential_id, cost_cny FROM account_costs`); err == nil {
		for rows.Next() {
			var id int64
			var c float64
			if rows.Scan(&id, &c) == nil {
				costs[uint64(id)] = c
			}
		}
		rows.Close()
	}
	type tot struct {
		model         string
		requests, err int64
		t             Tokens
		credits       float64
	}
	totals := map[uint64][]tot{}
	if rows, err := l.db.Query(`SELECT credential_id, model, requests, errors, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens, credits FROM account_totals`); err == nil {
		for rows.Next() {
			var id int64
			var x tot
			if rows.Scan(&id, &x.model, &x.requests, &x.err, &x.t.Input, &x.t.Output, &x.t.CacheRead, &x.t.CacheWrite5m, &x.t.CacheWrite1h, &x.credits) == nil {
				totals[uint64(id)] = append(totals[uint64(id)], x)
			}
		}
		rows.Close()
	}
	type ord struct {
		id      uint64
		label   string
		removed bool
	}
	order := make([]ord, 0, len(labels))
	known := map[uint64]bool{}
	for _, lb := range labels {
		order = append(order, ord{lb.ID, lb.Label, false})
		known[lb.ID] = true
	}
	orph := map[uint64]bool{}
	for id := range totals {
		if !known[id] {
			orph[id] = true
		}
	}
	for id := range costs {
		if !known[id] {
			orph[id] = true
		}
	}
	ids := make([]uint64, 0, len(orph))
	for id := range orph {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		order = append(order, ord{id, "#" + strconv.FormatUint(id, 10), true})
	}
	rep := AccountCostReport{Accounts: []AccountCost{}}
	for _, o := range order {
		row := AccountCost{CredentialID: o.id, Label: o.label, Removed: o.removed}
		if c, ok := costs[o.id]; ok {
			c := c
			row.CostCNY = &c
		}
		for _, x := range totals[o.id] {
			row.Requests += uint64(max(x.requests, 0))
			row.Errors += uint64(max(x.err, 0))
			row.InputTokens += x.t.Input
			row.OutputTokens += x.t.Output
			row.CacheReadTokens += x.t.CacheRead
			row.CacheWrite5mTokens += x.t.CacheWrite5m
			row.CacheWrite1hTokens += x.t.CacheWrite1h
			row.Credits += x.credits
			if c := Charge(prices, x.model, x.t); c != nil {
				row.UsageUSD += *c
			} else {
				row.UnpricedRequests += uint64(max(x.requests-x.err, 0))
			}
		}
		if row.CostCNY != nil {
			row.CostRatio = costRatio(*row.CostCNY, row.UsageUSD)
			rep.CostedAccounts++
			rep.CostCNY += *row.CostCNY
			rep.CostedUsageUSD += row.UsageUSD
		}
		rep.UsageUSD += row.UsageUSD
		rep.Credits += row.Credits
		rep.Accounts = append(rep.Accounts, row)
	}
	rep.CostRatio = costRatio(rep.CostCNY, rep.CostedUsageUSD)
	return rep
}

// BackfillAccounts 升级前没有 credential_id 的请求，按 account 标签（邮箱或 #id）回填。可重复调用。
func (l *Log) BackfillAccounts(emailToID map[string]uint64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	resolve := func(label string) (uint64, bool) {
		label = strings.TrimSpace(label)
		if s, ok := strings.CutPrefix(label, "#"); ok {
			if id, err := strconv.ParseUint(s, 10, 64); err == nil {
				return id, true
			}
		}
		id, ok := emailToID[label]
		return id, ok
	}
	rows, err := l.db.Query(`SELECT account, model, status, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens FROM requests WHERE credential_id IS NULL AND account != ''`)
	if err != nil {
		slog.Warn("读取待回填请求失败", "err", err)
		return 0
	}
	type key struct {
		id    uint64
		model string
	}
	type agg struct {
		req, errs int64
		t         Tokens
	}
	grouped := map[key]*agg{}
	labels := map[string]uint64{}
	for rows.Next() {
		var account, model string
		var status int64
		var t Tokens
		var cr, c5, c1 sql.NullInt64
		if rows.Scan(&account, &model, &status, &t.Input, &t.Output, &cr, &c5, &c1) != nil {
			continue
		}
		t.CacheRead, t.CacheWrite5m, t.CacheWrite1h = cr.Int64, c5.Int64, c1.Int64
		id, ok := resolve(account)
		if !ok {
			continue
		}
		labels[account] = id
		k := key{id, normalizeAlias(model)}
		a := grouped[k]
		if a == nil {
			a = &agg{}
			grouped[k] = a
		}
		a.req++
		if status >= 400 {
			a.errs++
		} else {
			a.t.Input += nz(t.Input)
			a.t.Output += nz(t.Output)
			a.t.CacheRead += nz(t.CacheRead)
			a.t.CacheWrite5m += nz(t.CacheWrite5m)
			a.t.CacheWrite1h += nz(t.CacheWrite1h)
		}
	}
	rows.Close()
	if len(grouped) == 0 {
		return 0
	}
	tx, err := l.db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback() //nolint:errcheck
	for k, a := range grouped {
		if err := addAccountTotals(tx, k.id, k.model, a.req, a.errs, a.t, 0); err != nil {
			slog.Warn("回填账号用量失败", "err", err)
			return 0
		}
	}
	updated := 0
	for account, id := range labels {
		res, err := tx.Exec(`UPDATE requests SET credential_id = ? WHERE credential_id IS NULL AND account = ?`, int64(id), account)
		if err != nil {
			slog.Warn("回填账号用量失败", "err", err)
			return 0
		}
		n, _ := res.RowsAffected()
		updated += int(n)
	}
	if tx.Commit() != nil {
		return 0
	}
	slog.Info("已按凭据回填历史用量", "requests", updated)
	return updated
}

// Charge 按别名找单价；找不到返回 nil。
func Charge(prices []ModelPrice, model string, t Tokens) *float64 {
	m := normalizeAlias(model)
	for _, p := range prices {
		for _, a := range p.Aliases {
			if a != m {
				continue
			}
			per := func(n int64, rate float64) float64 { return float64(max(n, 0)) / 1_000_000 * rate }
			read, w5, w1 := p.InputPerM*cacheReadMultiplier, p.InputPerM*cacheWrite5mMultiplier, p.InputPerM*cacheWrite1hMultiplier
			if p.CacheReadPerM != nil {
				read = *p.CacheReadPerM
			}
			if p.CacheWrite5mPerM != nil {
				w5 = *p.CacheWrite5mPerM
			}
			if p.CacheWrite1hPerM != nil {
				w1 = *p.CacheWrite1hPerM
			}
			v := per(t.Input, p.InputPerM) + per(t.Output, p.OutputPerM) + per(t.CacheRead, read) + per(t.CacheWrite5m, w5) + per(t.CacheWrite1h, w1)
			return &v
		}
	}
	return nil
}

func loadPrices(db *sql.DB) ([]ModelPrice, error) {
	rows, err := db.Query(`SELECT id, name, aliases, input_per_m, output_per_m, cache_read_per_m, cache_write_5m_per_m, cache_write_1h_per_m FROM prices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelPrice
	for rows.Next() {
		var p ModelPrice
		var aliases string
		var cr, c5, c1 sql.NullFloat64
		if err := rows.Scan(&p.ID, &p.Name, &aliases, &p.InputPerM, &p.OutputPerM, &cr, &c5, &c1); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &p.Aliases)
		if p.Aliases == nil {
			p.Aliases = []string{}
		}
		if cr.Valid {
			p.CacheReadPerM = &cr.Float64
		}
		if c5.Valid {
			p.CacheWrite5mPerM = &c5.Float64
		}
		if c1.Valid {
			p.CacheWrite1hPerM = &c1.Float64
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func prune(db *sql.DB, retain int64) error {
	_, err := db.Exec(`DELETE FROM requests WHERE id < (SELECT MIN(id) FROM (SELECT id FROM requests ORDER BY id DESC LIMIT ?))`, max(retain, 1))
	return err
}

func ensureColumn(db *sql.DB, table, column, def string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk) == nil && name == column {
			found = true
		}
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + def)
	return err
}

func initSchema(db *sql.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS requests (id INTEGER PRIMARY KEY, time TEXT NOT NULL, model TEXT NOT NULL, stream INTEGER NOT NULL, status INTEGER NOT NULL, duration_ms INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, error TEXT)`,
		`CREATE TABLE IF NOT EXISTS model_totals (model TEXT PRIMARY KEY, requests INTEGER NOT NULL, errors INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS prices (id TEXT PRIMARY KEY, name TEXT NOT NULL, aliases TEXT NOT NULL, input_per_m REAL NOT NULL, output_per_m REAL NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS account_costs (credential_id INTEGER PRIMARY KEY, cost_cny REAL NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS account_totals (credential_id INTEGER NOT NULL, model TEXT NOT NULL, requests INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0, cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (credential_id, model))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	cols := [][3]string{
		{"requests", "account", "TEXT NOT NULL DEFAULT ''"}, {"requests", "endpoint", "TEXT NOT NULL DEFAULT ''"},
		{"requests", "request_bytes", "INTEGER NOT NULL DEFAULT 0"}, {"requests", "stop_reason", "TEXT NOT NULL DEFAULT ''"},
		{"requests", "inbound_headers", "TEXT NOT NULL DEFAULT ''"}, {"requests", "inbound_body", "TEXT NOT NULL DEFAULT ''"},
		{"requests", "outbound_headers", "TEXT NOT NULL DEFAULT ''"}, {"requests", "outbound_body", "TEXT NOT NULL DEFAULT ''"},
		{"requests", "response_body", "TEXT NOT NULL DEFAULT ''"}, {"requests", "payload_profile", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, t := range []string{"requests", "model_totals"} {
		for _, c := range []string{"cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens"} {
			cols = append(cols, [3]string{t, c, "INTEGER NOT NULL DEFAULT 0"})
		}
	}
	for _, c := range []string{"cache_read_per_m", "cache_write_5m_per_m", "cache_write_1h_per_m"} {
		cols = append(cols, [3]string{"prices", c, "REAL"})
	}
	cols = append(cols, [3]string{"requests", "credential_id", "INTEGER"}, [3]string{"requests", "credits", "REAL"},
		[3]string{"model_totals", "credits", "REAL NOT NULL DEFAULT 0"}, [3]string{"account_totals", "credits", "REAL NOT NULL DEFAULT 0"})
	for _, c := range cols {
		if err := ensureColumn(db, c[0], c[1], c[2]); err != nil {
			return err
		}
	}
	return nil
}

func metaDone(db *sql.DB, key string) (bool, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// excludeFailedTokens 早期版本把失败请求的 token 也计进了汇总，这里一次性扣回（meta 标记只做一次）。
func excludeFailedTokens(db *sql.DB) error {
	const key = "failed_tokens_excluded"
	if done, err := metaDone(db, key); err != nil || done {
		return err
	}
	failed := map[string]*Tokens{}
	rows, err := db.Query(`SELECT model, input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens FROM requests WHERE status >= 400`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m string
		var t Tokens
		if rows.Scan(&m, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h) != nil {
			continue
		}
		k := normalizeAlias(m)
		s := failed[k]
		if s == nil {
			s = &Tokens{}
			failed[k] = s
		}
		s.Input += nz(t.Input)
		s.Output += nz(t.Output)
		s.CacheRead += nz(t.CacheRead)
		s.CacheWrite5m += nz(t.CacheWrite5m)
		s.CacheWrite1h += nz(t.CacheWrite1h)
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for m, t := range failed {
		if _, err := tx.Exec(`UPDATE model_totals SET input_tokens = max(input_tokens - ?, 0), output_tokens = max(output_tokens - ?, 0),
			cache_read_tokens = max(cache_read_tokens - ?, 0), cache_write_5m_tokens = max(cache_write_5m_tokens - ?, 0),
			cache_write_1h_tokens = max(cache_write_1h_tokens - ?, 0) WHERE model = ?`,
			t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h, m); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES(?, '1')`, key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(failed) > 0 {
		slog.Info("已从模型汇总扣回失败请求的 token", "models", len(failed))
	}
	return nil
}

func importLegacyJSON(db *sql.DB, sqlitePath string) error {
	if done, err := metaDone(db, "legacy_json"); err != nil || done {
		return err
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(sqlitePath), "usage.json"))
	if err == nil {
		var legacy struct {
			Prices   []ModelPrice    `json:"prices"`
			Requests []RequestRecord `json:"requests"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			tx, err := db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback() //nolint:errcheck
			for _, p := range legacy.Prices {
				a, _ := json.Marshal(p.Aliases)
				if _, err := tx.Exec(`INSERT OR IGNORE INTO prices(id, name, aliases, input_per_m, output_per_m) VALUES (?, ?, ?, ?, ?)`, p.ID, p.Name, string(a), p.InputPerM, p.OutputPerM); err != nil {
					return err
				}
			}
			for _, r := range legacy.Requests {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO requests(id, time, model, stream, status, duration_ms, input_tokens, output_tokens, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					int64(r.ID), r.Time, r.Model, boolInt(r.Stream), r.Status, int64(r.DurationMs), r.InputTokens, r.OutputTokens, r.Error); err != nil {
					return err
				}
				errs := 0
				if r.Status >= 400 {
					errs = 1
				}
				if _, err := tx.Exec(`INSERT INTO model_totals(model, requests, errors, input_tokens, output_tokens) VALUES (?, 1, ?, ?, ?)
					ON CONFLICT(model) DO UPDATE SET requests = requests + 1, errors = errors + excluded.errors,
					input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens`,
					normalizeAlias(r.Model), errs, nz(r.InputTokens), nz(r.OutputTokens)); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('legacy_json', '1')`); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	_, err = db.Exec(`INSERT INTO meta(key, value) VALUES('legacy_json', '1')`)
	return err
}

// MaxCredentialID 历史数据里出现过的最大凭据 id。
func (l *Log) MaxCredentialID() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var v sql.NullInt64
	_ = l.db.QueryRow(`SELECT MAX(id) FROM (
		SELECT MAX(credential_id) AS id FROM requests UNION ALL
		SELECT MAX(credential_id) FROM account_totals UNION ALL
		SELECT MAX(credential_id) FROM account_costs)`).Scan(&v)
	return uint64(max(v.Int64, 0))
}

// MoveAccount 把一个凭据的历史（明细、汇总、成本）整体挪到另一个 id。to 必须是没用过的 id。
func (l *Log) MoveAccount(from, to uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, t := range []string{"requests", "account_totals", "account_costs"} {
		if _, err := tx.Exec(`UPDATE `+t+` SET credential_id = ? WHERE credential_id = ?`, int64(to), int64(from)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
