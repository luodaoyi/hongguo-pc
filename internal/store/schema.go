package store

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// schemaVersion 是当前实现认的 schema 版本，存在 SQLite 的 PRAGMA user_version 里。
// 改表结构时：+1，把旧版本的升级语句写进 migrate 的 switch，别改 v1 的建表语句。
const schemaVersion = 1

// schemaV1 严格照抄方案 §5.3。七张表 + 一个普通索引。
//
// 注意 "queue" 带引号：queue 在较新的 SQLite 里是保留字。
var schemaV1 = []string{
	`CREATE TABLE IF NOT EXISTS series(
		id TEXT PRIMARY KEY, title TEXT NOT NULL, cover_url TEXT, episode_count INT,
		status INT, pay_type INT, platform INT, is_multi_season INT, season_index INT,
		album_id TEXT, right_text TEXT, fetched_at INT NOT NULL)`,

	// episode 仅 id 为主键；number 只建普通索引，避免服务端改集数时的唯一键冲突（方案 r5）。
	`CREATE TABLE IF NOT EXISTS episode(
		id TEXT PRIMARY KEY, series_id TEXT NOT NULL, number INT NOT NULL, title TEXT,
		duration_sec INT, playable INT NOT NULL DEFAULT 1, is_new INT NOT NULL DEFAULT 0,
		cover_url TEXT, fetched_at INT NOT NULL, removed_at INT)`,

	`CREATE INDEX IF NOT EXISTS idx_ep_series ON episode(series_id, number)`,

	`CREATE TABLE IF NOT EXISTS "queue"(
		id INTEGER PRIMARY KEY, name TEXT, mode TEXT NOT NULL
		CHECK(mode IN ('single','sequential','loop','series_chained')),
		active INT NOT NULL DEFAULT 0, created_at INT)`,

	`CREATE TABLE IF NOT EXISTS queue_item(
		id INTEGER PRIMARY KEY,
		queue_id INT NOT NULL REFERENCES "queue"(id) ON DELETE CASCADE,
		pos INT NOT NULL, series_id TEXT NOT NULL, episode_id TEXT,
		UNIQUE(queue_id, series_id))`,

	`CREATE TABLE IF NOT EXISTS history(
		series_id TEXT PRIMARY KEY, episode_id TEXT, episode_number INT,
		position_sec REAL NOT NULL, duration_sec INT, finished INT NOT NULL,
		updated_at INT NOT NULL)`,

	// has_key 只标记该模型在本次会话是否有内存密钥，密钥本身绝不落库（I2）。
	`CREATE TABLE IF NOT EXISTS playback_cache(
		episode_id TEXT PRIMARY KEY, series_id TEXT, payload BLOB NOT NULL,
		resolved_at INT NOT NULL, expires_at INT, has_key INTEGER NOT NULL DEFAULT 0)`,

	// quota_day：quota_used 所属的本地日期(YYYYMMDD)，跨日即视为新额度窗口。
	`CREATE TABLE IF NOT EXISTS device_slot(
		id TEXT PRIMARY KEY, profile_path TEXT NOT NULL, state TEXT NOT NULL,
		quota_used INT DEFAULT 0, quota_day INT, cool_until INT, last_used INT,
		reject_count INT DEFAULT 0)`,
}

// migrate 把数据库升到 schemaVersion。
//
// 返回的字符串是「失败后重建」用到的备份路径，空串表示正常迁移。
//
// 版本高于本实现时拒绝操作（直接报错），不擅自降级或清库。
// 迁移失败时：先用 VACUUM INTO 备份，再删表重建空 schema，保证应用能启动，
// 备份留在 <db>.bak-<时间戳>，由调用方通过 Store.Recovered() 提示用户。
func migrate(db *sql.DB, path string) (string, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return "", fmt.Errorf("store: read schema version: %w", err)
	}
	switch {
	case version == schemaVersion:
		return "", nil
	case version > schemaVersion:
		return "", fmt.Errorf("store: database schema v%d is newer than this build supports (v%d); refusing to touch it",
			version, schemaVersion)
	}
	if err := applySchema(db); err != nil {
		backup, rebuildErr := rebuild(db, path, err)
		if rebuildErr != nil {
			return "", rebuildErr
		}
		return backup, nil
	}
	return "", nil
}

// applySchema 在一个事务里建表并写入 user_version。
func applySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin schema transaction: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range schemaV1 {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: apply schema: %w", err)
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = ` + strconv.Itoa(schemaVersion)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit schema: %w", err)
	}
	return nil
}

// rebuild 备份后删表重建。返回备份文件路径。
func rebuild(db *sql.DB, path string, cause error) (string, error) {
	backup, err := backupDatabase(db, path)
	if err != nil {
		return "", fmt.Errorf("store: migration failed (%v) and backup failed (%v); refusing to rebuild", cause, err)
	}
	if err := dropAll(db); err != nil {
		return backup, fmt.Errorf("store: migration failed (%v); backup at %s; dropping tables failed: %w", cause, backup, err)
	}
	if err := applySchema(db); err != nil {
		return backup, fmt.Errorf("store: migration failed (%v); backup at %s; rebuild failed: %w", cause, backup, err)
	}
	return backup, nil
}

// dropAll 删掉七张表，供重建路径使用。
func dropAll(db *sql.DB) error {
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS queue_item`,
		`DROP TABLE IF EXISTS "queue"`,
		`DROP TABLE IF EXISTS episode`,
		`DROP TABLE IF EXISTS series`,
		`DROP TABLE IF EXISTS history`,
		`DROP TABLE IF EXISTS playback_cache`,
		`DROP TABLE IF EXISTS device_slot`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// backupDatabase 用 VACUUM INTO 做一份完整副本，返回备份文件路径。
//
// VACUUM INTO 不能在事务内执行，此处必须用已提交的连接直接跑。
// 路径以 SQL 字面量内联（单引号转义），不经参数绑定，避免不同驱动对
// VACUUM 参数的支持差异。
func backupDatabase(db *sql.DB, path string) (string, error) {
	backup := path + ".bak-" + time.Now().Format("20060102-150405")
	if _, err := os.Stat(backup); err == nil {
		// 同一秒内重复触发（测试或快速重启）：补一个纳秒后缀，VACUUM INTO 拒绝覆盖。
		backup = path + ".bak-" + time.Now().Format("20060102-150405.000000000")
	}
	stmt := `VACUUM INTO '` + strings.ReplaceAll(backup, `'`, `''`) + `'`
	if _, err := db.Exec(stmt); err != nil {
		return "", fmt.Errorf("store: %s: %w", stmt, err)
	}
	return backup, nil
}
