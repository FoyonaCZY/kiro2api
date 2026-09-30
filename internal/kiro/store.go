package kiro

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go sqlite 驱动，不需要 cgo
)

// AccountsDBFile 与 kiro.rs 相同的文件名，线上库可直接接管。
const AccountsDBFile = "accounts.sqlite"

// Store 凭据库，每条凭据整条 JSON 存储。
type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	path string
}

// OpenSQLite 打开 sqlite，启用 WAL 和忙等待，单连接串行写。
func OpenSQLite(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// OpenStore 打开 dir/accounts.sqlite。
func OpenStore(dir string) (*Store, error) {
	path := filepath.Join(dir, AccountsDBFile)
	db, err := OpenSQLite(path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return initStore(db, path)
}

// OpenMemoryStore 测试用。
func OpenMemoryStore() (*Store, error) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		return nil, err
	}
	return initStore(db, "")
}

func initStore(db *sql.DB, path string) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS credentials (
		id INTEGER PRIMARY KEY,
		position INTEGER NOT NULL,
		data TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

// Path 库文件路径。
func (s *Store) Path() string { return s.path }

// IsEmpty 库里没有凭据。
func (s *Store) IsEmpty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credentials`).Scan(&n); err != nil {
		return true
	}
	return n == 0
}

// Load 按保存顺序读出全部凭据。
func (s *Store) Load() ([]*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, data FROM credentials ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Credentials
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		c := &Credentials{}
		if err := json.Unmarshal([]byte(data), c); err != nil {
			return nil, fmt.Errorf("凭据 #%d 数据损坏: %w", id, err)
		}
		c.ID = uint64(id)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Save 在一个事务里整份替换。
func (s *Store) Save(creds []*Credentials) error {
	for _, c := range creds {
		if c.ID == 0 {
			return errors.New("凭据缺少 id，不能入库")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM credentials`); err != nil {
		return err
	}
	for pos, c := range creds {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO credentials(id, position, data, updated_at) VALUES (?, ?, ?, ?)`,
			int64(c.ID), pos, string(data), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Close 关闭库。
func (s *Store) Close() error { return s.db.Close() }
