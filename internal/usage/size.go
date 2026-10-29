package usage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

const (
	// 超过上限后删到上限的这个比例，免得每次检查都在删。
	sizeLowWater = 0.8
	// 首次压缩旧库时删得更狠，压缩需要的临时空间约等于保留的数据量。
	compactLowWater = 0.5
	// 一次只动这么多行。请求体很大时，整表删完会占住写锁，请求页和正在写用量的模型请求一起卡住。
	pruneBatch = 40
	// 每写入这么多次，最多连续清几批，然后把锁让出去。
	maxBatchesPerTick = 4
	// 每次最多把这么多空页还给系统。4096 页大约 16MB。
	vacuumPageCap = 4096
	// 超过这个大小就不要在启动时 VACUUM INTO 整库。
	vacuumInlineMax = 512 << 20
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

// pruneBySize 超限时只删最旧的一批请求，并最多回收 vacuumPageCap 个空页。
// 汇总表不受影响。more 为 true 表示还没收到目标，调用方应先放开写锁再继续。
func pruneBySize(db *sql.DB, maxBytes int64, lowWater float64) (deleted int64, more bool, err error) {
	if maxBytes <= 0 {
		return 0, false, nil
	}
	total, used, err := dbSize(db)
	if err != nil {
		return 0, false, err
	}
	target := int64(float64(maxBytes) * lowWater)
	if target < 1 {
		target = 1
	}
	if used > target {
		n := int64(pruneBatch)
		var rowBytes int64
		scanErr := db.QueryRow(`SELECT COALESCE(length(inbound_body),0)+COALESCE(length(outbound_body),0)+COALESCE(length(response_body),0)+COALESCE(length(inbound_headers),0)+COALESCE(length(outbound_headers),0)+2048 FROM requests ORDER BY id LIMIT 1`).Scan(&rowBytes)
		if scanErr == sql.ErrNoRows {
			n = 0
		} else if scanErr != nil {
			return 0, false, scanErr
		} else if rowBytes > 0 {
			n = min(max((used-target)/rowBytes, 1), int64(pruneBatch))
		}
		if n > 0 {
			res, err := db.Exec(`DELETE FROM requests WHERE id IN (SELECT id FROM requests ORDER BY id LIMIT ?)`, n)
			if err != nil {
				return 0, false, err
			}
			deleted, _ = res.RowsAffected()
			total, used, err = dbSize(db)
			if err != nil {
				return deleted, false, err
			}
		}
	}
	shrunk := false
	if total > maxBytes {
		before := total
		if err := reclaim(db, vacuumPageCap); err != nil {
			return deleted, false, err
		}
		total, _, err = dbSize(db)
		if err != nil {
			return deleted, false, err
		}
		shrunk = total < before
	}
	more = (used > target && deleted > 0) || shrunk
	return deleted, more, nil
}

// reclaim 把最多 pages 个空页还给系统。没开 incremental auto_vacuum 时无操作。
// incremental_vacuum 每返回一行释放一页，要读完才算做完。
func reclaim(db *sql.DB, pages int) error {
	if pages <= 0 {
		return nil
	}
	var mode int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		return nil
	}
	rows, err := db.Query(`PRAGMA incremental_vacuum(` + strconv.Itoa(pages) + `)`)
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
	total, _, err := dbSize(db)
	if err != nil {
		return db, err
	}
	if total > vacuumInlineMax {
		slog.Warn("用量库太大，跳过整库压缩，改为分批清理", "total_mb", total>>20)
		return db, nil
	}
	if n, _, err := pruneBySize(db, maxBytes, compactLowWater); err != nil {
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
