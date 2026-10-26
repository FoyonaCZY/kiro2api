// Package access 管理接入 Key 和调度分组。表结构与 kiro.rs 的 access.sqlite 一致。
package access

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	DefaultGroupID   = "default"
	defaultGroupName = "默认"
	defaultKeyName   = "默认"
)

// Scope 一次请求可用的分组和凭据。
type Scope struct {
	GroupID string
	Allowed map[uint64]bool
	KeyID   int64
	HasKey  bool
}

type scopeKey struct{}

// WithScope 把分组范围放进 context。
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

func scopeOf(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	return s
}

// CurrentGroup 请求所属分组；没有范围时返回空。
func CurrentGroup(ctx context.Context) (string, bool) {
	if s := scopeOf(ctx); s != nil {
		return s.GroupID, true
	}
	return "", false
}

// CredentialAllowed 没有范围时全部放行。
func CredentialAllowed(ctx context.Context, id uint64) bool {
	if s := scopeOf(ctx); s != nil {
		return s.Allowed[id]
	}
	return true
}

// CurrentKeyID 当前接入 Key，模拟缓存按它隔离。
func CurrentKeyID(ctx context.Context) (int64, bool) {
	if s := scopeOf(ctx); s != nil && s.HasKey {
		return s.KeyID, true
	}
	return 0, false
}

// Key 接入 Key。
type Key struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Secret    string `json:"secret"`
	Prefix    string `json:"prefix"`
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	Disabled  bool   `json:"disabled"`
}

// Group 调度分组。
type Group struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	KeyCount  int64    `json:"keyCount"`
	Members   []uint64 `json:"members"`
	IsDefault bool     `json:"isDefault"`
}

// Store access.sqlite。
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// Open path 为空时用内存库。
func Open(path string) *Store {
	dsn := "file::memory:?_pragma=busy_timeout(2000)"
	if path != "" {
		dsn = "file:" + path + "?_pragma=busy_timeout(2000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err == nil {
		db.SetMaxOpenConns(1)
		err = initSchema(db)
	}
	if err != nil {
		slog.Error("打开接入库失败，改用内存库", "err", err)
		db, _ = sql.Open("sqlite", "file::memory:")
		db.SetMaxOpenConns(1)
		_ = initSchema(db)
	}
	return &Store{db: db}
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func initSchema(db *sql.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS access_groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS access_keys (id INTEGER PRIMARY KEY, name TEXT NOT NULL, secret TEXT NOT NULL, group_id TEXT NOT NULL, disabled INTEGER NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS group_members (group_id TEXT NOT NULL, credential_id INTEGER NOT NULL, PRIMARY KEY (group_id, credential_id))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO access_groups(id, name, created_at) VALUES (?, ?, ?)`, DefaultGroupID, defaultGroupName, now())
	return err
}

// ImportInitialKey 库里没有 Key 时把配置里的 apiKey 导入为「默认」。
func (s *Store) ImportInitialKey(secret string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM access_keys`).Scan(&n)
	if n > 0 {
		return
	}
	if _, err := s.db.Exec(`INSERT INTO access_keys(name, secret, group_id, disabled, created_at) VALUES (?, ?, ?, 0, ?)`,
		defaultKeyName, secret, DefaultGroupID, now()); err != nil {
		slog.Error("导入初始接入 Key 失败", "err", err)
	}
}

// Authenticate 常量时间比较，返回启用的 Key。
func (s *Store) Authenticate(secret string) *Key {
	var found *Key
	for _, k := range s.ListKeys() {
		k := k
		if subtle.ConstantTimeCompare([]byte(k.Secret), []byte(secret)) == 1 && !k.Disabled && found == nil {
			found = &k
		}
	}
	return found
}

func (s *Store) membership() map[string]map[uint64]bool {
	out := map[string]map[uint64]bool{}
	rows, err := s.db.Query(`SELECT group_id, credential_id FROM group_members`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		var id int64
		if rows.Scan(&g, &id) == nil {
			if out[g] == nil {
				out[g] = map[uint64]bool{}
			}
			out[g][uint64(id)] = true
		}
	}
	return out
}

// AllowedCredentials 分组显式成员；默认组还包含没加入任何组的凭据。
func (s *Store) AllowedCredentials(groupID string, ids []uint64) map[uint64]bool {
	s.mu.Lock()
	m := s.membership()
	s.mu.Unlock()
	inAny := map[uint64]bool{}
	for _, set := range m {
		for id := range set {
			inAny[id] = true
		}
	}
	out := map[uint64]bool{}
	for _, id := range ids {
		if m[groupID][id] || (groupID == DefaultGroupID && !inAny[id]) {
			out[id] = true
		}
	}
	return out
}

// AssignedCredentials 加入过任何分组的凭据。
func (s *Store) AssignedCredentials() map[uint64]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[uint64]bool{}
	for _, set := range s.membership() {
		for id := range set {
			out[id] = true
		}
	}
	return out
}

// ListKeys 全部 Key。
func (s *Store) ListKeys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT k.id, k.name, k.secret, k.group_id, g.name, k.disabled
		FROM access_keys k LEFT JOIN access_groups g ON g.id = k.group_id ORDER BY k.id`)
	if err != nil {
		slog.Warn("读取接入 Key 失败", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var gname sql.NullString
		var disabled int64
		if rows.Scan(&k.ID, &k.Name, &k.Secret, &k.GroupID, &gname, &disabled) != nil {
			continue
		}
		k.GroupName = "未知分组"
		if gname.Valid {
			k.GroupName = gname.String
		}
		k.Disabled = disabled != 0
		k.Prefix = prefix(k.Secret)
		out = append(out, k)
	}
	return out
}

func (s *Store) groupExists(id string) error {
	var found string
	err := s.db.QueryRow(`SELECT id FROM access_groups WHERE id = ?`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("分组不存在")
	}
	return err
}

// CreateKey secret 为空时生成 sk-<uuid>。
func (s *Store) CreateKey(name, secret, groupID string) (*Key, error) {
	name = cleanName(name)
	if name == "" {
		return nil, errors.New("名称不能为空")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "sk-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	groupID = normalizeGroup(groupID)
	s.mu.Lock()
	if err := s.groupExists(groupID); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO access_keys(name, secret, group_id, disabled, created_at) VALUES (?, ?, ?, 0, ?)`, name, secret, groupID, now())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for _, k := range s.ListKeys() {
		if k.ID == id {
			k := k
			return &k, nil
		}
	}
	return nil, errors.New("写入后读取失败")
}

// UpdateKey nil 表示不改。
func (s *Store) UpdateKey(id int64, name, groupID *string, disabled *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found int64
	if err := s.db.QueryRow(`SELECT id FROM access_keys WHERE id = ?`, id).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("接入 Key 不存在")
		}
		return err
	}
	if name != nil {
		n := cleanName(*name)
		if n == "" {
			return errors.New("名称不能为空")
		}
		if _, err := s.db.Exec(`UPDATE access_keys SET name = ? WHERE id = ?`, n, id); err != nil {
			return err
		}
	}
	if groupID != nil {
		g := normalizeGroup(*groupID)
		if err := s.groupExists(g); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE access_keys SET group_id = ? WHERE id = ?`, g, id); err != nil {
			return err
		}
	}
	if disabled != nil {
		v := 0
		if *disabled {
			v = 1
		}
		if _, err := s.db.Exec(`UPDATE access_keys SET disabled = ? WHERE id = ?`, v, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteKey 不能删最后一把。
func (s *Store) DeleteKey(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM access_keys`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("不能删除最后一把接入 Key")
	}
	res, err := s.db.Exec(`DELETE FROM access_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return errors.New("接入 Key 不存在")
	}
	return nil
}

// ListGroups 默认组排第一。
func (s *Store) ListGroups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.membership()
	rows, err := s.db.Query(`SELECT g.id, g.name, COUNT(k.id) FROM access_groups g
		LEFT JOIN access_keys k ON k.group_id = g.id GROUP BY g.id
		ORDER BY CASE WHEN g.id = 'default' THEN 0 ELSE 1 END, g.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if rows.Scan(&g.ID, &g.Name, &g.KeyCount) != nil {
			continue
		}
		g.Members = []uint64{}
		for id := range m[g.ID] {
			g.Members = append(g.Members, id)
		}
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i] < g.Members[j] })
		g.IsDefault = g.ID == DefaultGroupID
		out = append(out, g)
	}
	return out
}

// CreateGroup 新分组。
func (s *Store) CreateGroup(name string) (*Group, error) {
	name = cleanName(name)
	if name == "" {
		return nil, errors.New("分组名不能为空")
	}
	id := "g_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO access_groups(id, name, created_at) VALUES (?, ?, ?)`, id, name, now()); err != nil {
		return nil, err
	}
	return &Group{ID: id, Name: name, Members: []uint64{}}, nil
}

// RenameGroup 改名。
func (s *Store) RenameGroup(id, name string) error {
	name = cleanName(name)
	if name == "" {
		return errors.New("分组名不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE access_groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return errors.New("分组不存在")
	}
	return nil
}

// DeleteGroup 默认组和仍有 Key 绑定的组不能删。
func (s *Store) DeleteGroup(id string) error {
	if id == DefaultGroupID {
		return errors.New("不能删除默认分组")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM access_keys WHERE group_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("仍有接入 Key 绑定该分组")
	}
	if _, err := s.db.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM access_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return errors.New("分组不存在")
	}
	return nil
}

// SetMembers 整体替换成员。默认组不能把没去别处的凭据移出去。
func (s *Store) SetMembers(groupID string, members, known []uint64) error {
	groupID = normalizeGroup(groupID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.groupExists(groupID); err != nil {
		return err
	}
	if groupID == DefaultGroupID {
		m := s.membership()
		wanted := map[uint64]bool{}
		for _, id := range members {
			wanted[id] = true
		}
		for _, id := range known {
			if wanted[id] {
				continue
			}
			elsewhere := false
			for g, set := range m {
				if g != DefaultGroupID && set[id] {
					elsewhere = true
				}
			}
			if !elsewhere {
				return errors.New("凭据未加入其他分组，不能移出默认组")
			}
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, id := range members {
		if _, err := tx.Exec(`INSERT INTO group_members(group_id, credential_id) VALUES (?, ?)`, groupID, int64(id)); err != nil {
			return fmt.Errorf("写入成员: %w", err)
		}
	}
	return tx.Commit()
}

func normalizeGroup(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return DefaultGroupID
	}
	return id
}

func cleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > 64 {
		return string([]rune(name)[:64])
	}
	return name
}

func prefix(secret string) string {
	r := []rune(secret)
	if len(r) <= 10 {
		return "••••"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// MaxMemberID 分组里出现过的最大凭据 id。
func (s *Store) MaxMemberID() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(credential_id) FROM group_members`).Scan(&v)
	return uint64(max(v.Int64, 0))
}

// RemoveCredential 账号删除后从所有分组移出。
func (s *Store) RemoveCredential(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM group_members WHERE credential_id = ?`, int64(id))
	return err
}
