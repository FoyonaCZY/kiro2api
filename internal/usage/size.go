package usage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
)

const (
	// 超过上限后删到上限的这个比例，免得每次检查都在删。
	sizeLowWater = 0.8
	// 首次压缩旧库时删得更狠，压缩需要的临时空间约等于保留的数据量。
	compactLowWater = 0.5
	pruneBatch      = 200
)

// dbSize 返回库文件的页总大小和其中实际在用的大小。
func dbSize(db *sql.DB) (total, used int64, err error) {
	var pages, free, size int64
	if err = db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return
	}
	if err = db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return
	}
	if err = db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		return
	}
	return pages * size, (pages - free) * size, nil
}

// pruneBySize 在用数据超过 maxBytes 时从最旧的请求删起，删到 maxBytes*lowWater 以下，再把空页还给系统。
// 汇总表不受影响。返回删掉的行数。
func pruneBySize(db *sql.DB, maxBytes int64, lowWater float64) (int64, error) {
	if maxBytes <= 0 {
		return 0, nil
	}
	_, used, err := dbSize(db)
	if err != nil || used <= maxBytes {
		return 0, err
	}
	target := int64(float64(maxBytes) * lowWater)
	var deleted int64
	for used > target {
		// 按平均每行大小估算要删几行，一次最多 pruneBatch 行
		var rows int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&rows); err != nil {
			return deleted, err
		}
		if rows == 0 {
			break
		}
		n := min(max((used-target)*rows/used, 1), pruneBatch)
		res, err := db.Exec(`DELETE FROM requests WHERE id IN (SELECT id FROM requests ORDER BY id LIMIT ?)`, n)
		if err != nil {
			return deleted, err
		}
		got, _ := res.RowsAffected()
		if got == 0 {
			break
		}
		deleted += got
		if _, used, err = dbSize(db); err != nil {
			return deleted, err
		}
	}
	return deleted, reclaim(db)
}

// reclaim 把空页还给系统。incremental_vacuum 每返回一行释放一页，要读完才算做完。
func reclaim(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA incremental_vacuum`)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// ensureIncrementalVacuum 旧库没开 auto_vacuum，删数据文件也不会变小。
// 先删旧请求腾出空间，再 VACUUM INTO 一份新文件替换，返回可用的连接。
func ensureIncrementalVacuum(db *sql.DB, path string, maxBytes int64) (*sql.DB, error) {
	var mode int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode == 2 {
		return db, err
	}
	if n, err := pruneBySize(db, maxBytes, compactLowWater); err != nil {
		return db, err
	} else if n > 0 {
		slog.Info("用量库超过上限，已删除最旧的请求明细", "rows", n)
	}
	before, _, _ := dbSize(db)
	tmp := path + ".compact"
	_ = os.Remove(tmp)
	if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return db, err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return db, fmt.Errorf("压缩用量库失败: %w", err)
	}
	if err := db.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	ndb, err := openDB(path)
	if err != nil {
		return nil, err
	}
	after, _, _ := dbSize(ndb)
	slog.Info("用量库已压缩", "before_mb", before>>20, "after_mb", after>>20)
	return ndb, nil
}
