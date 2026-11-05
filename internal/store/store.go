// Package store 是 SQLite 持久层（方案 §2.1 的 L5 层）。
//
// schema 严格照方案 §5.3 的七张表：series、episode、"queue"、queue_item、
// history、playback_cache、device_slot。三条要点：
//
//   - episode 只以 id 为主键，(series_id, number) 是普通索引；number 变更时
//     必须先按 id 删除旧行再插入（方案 r5 / §7.3）。
//   - playback_cache.has_key 只标记「该模型在**本次会话**是否有内存密钥」；
//     密钥本身绝不落库（方案 §5.3 密钥红线 / I2）。
//   - device_slot.quota_day 是 quota_used 所属的本地日期（YYYYMMDD），
//     跨日即视为新额度窗口；服务端真实重置时点仍待实测（附录 A1）。
//
// 依赖方向（方案 §2.3）：store 只依赖 internal/model，绝不反向依赖上层。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	// modernc.org/sqlite 是纯 Go 实现，无需 CGO，Windows 上可直接交叉编译。
	_ "modernc.org/sqlite"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// CacheEntry 是 playback_cache 的一行。
//
// Payload 是 Playback 的 JSON：**不含密钥**（KeyMaterial 带 json:"-"）。
// HasKey 只表示写入时本会话是否持有该集的内存密钥。
type CacheEntry struct {
	EpisodeID string
	SeriesID  string
	Payload   []byte
	// ResolvedAt / ExpiresAt 是 unix 秒；ExpiresAt 为 0 表示服务端没给（A8）。
	ResolvedAt int64
	ExpiresAt  int64
	HasKey     bool
}

// DeviceSlotRecord 是 device_slot 的一行，对应 model.SlotStatus 的持久化形态。
type DeviceSlotRecord struct {
	ID          string
	ProfilePath string
	State       string
	QuotaUsed   int
	// QuotaDay 是 QuotaUsed 所属的本地日期（YYYYMMDD），0 表示还没用过。
	QuotaDay int
	// CoolUntil / LastUsed 是 unix 秒，0 表示未设置。
	CoolUntil   int64
	LastUsed    int64
	RejectCount int
}

// Store 是持久层的全部能力。
//
// 队列（"queue"/queue_item）两张表本期已建，但读写方法留给 T9 的队列任务补齐：
// 队列语义（方案 §5.4）属于 player/library 层，store 只提供存储原语。
type Store interface {
	Close() error

	// Recovered 返回「迁移失败后重建」所用的备份文件路径，空串表示没发生过重建。
	Recovered() string

	// UpsertSeries 写入或更新一部短剧的目录行。
	UpsertSeries(ctx context.Context, s model.SeriesDTO) error
	// GetSeries 读一部短剧；没有则返回 (nil, nil)。
	GetSeries(ctx context.Context, id string) (*model.SeriesDTO, error)
	// Episodes 读一部剧未软删的分集，按 number 升序。
	Episodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error)
	// ReplaceEpisodes 用服务端全量结果做增量合并（方案 §7.3），返回增删改集合。
	ReplaceEpisodes(ctx context.Context, seriesID string, fresh []model.EpisodeDTO) (model.EpisodeDiff, error)
	// MarkRemoved 把指定分集软删（写 removed_at），保证 history 不会指向空行。
	MarkRemoved(ctx context.Context, seriesID string, ids []string) error

	// HistoryList 按 updated_at 倒序读最近 limit 条断点；limit<=0 取 50。
	HistoryList(ctx context.Context, limit int) ([]model.HistoryDTO, error)
	// HistoryGet 读一部剧的断点；没有则返回 (nil, nil)。
	HistoryGet(ctx context.Context, seriesID string) (*model.HistoryDTO, error)
	// HistoryPut 写入或覆盖一部剧的断点（主键 series_id）。
	HistoryPut(ctx context.Context, h model.HistoryDTO) error
	// HistoryClear 清空全部断点。
	HistoryClear(ctx context.Context) error

	// CacheGet 读一条播放模型缓存；没有则返回 (nil, nil)。payload 不含密钥。
	CacheGet(ctx context.Context, episodeID string) (*CacheEntry, error)
	// CachePut 写入或覆盖一条缓存。payload 必须是 Playback 的 JSON，且不含密钥。
	CachePut(ctx context.Context, e CacheEntry) error
	// CacheDelete 删除一条缓存（地址过期即删，不留陈旧 URL，方案 §7.5）。
	CacheDelete(ctx context.Context, episodeID string) error
	// CachePurgeExpired 删除 expires_at 已过期的缓存，返回删除行数。
	CachePurgeExpired(ctx context.Context, now int64) (int, error)

	// DeviceSlots 读全部设备槽记账，按 id 升序。
	DeviceSlots(ctx context.Context) ([]DeviceSlotRecord, error)
	// DeviceSlotPut 写入或覆盖一个设备槽记账。
	DeviceSlotPut(ctx context.Context, r DeviceSlotRecord) error
}

// Open 打开（必要时创建并迁移）数据库，返回可用的 Store。
//
// path 为空时返回错误；父目录不存在时自动创建。
func Open(path string) (Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store: database path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create database directory %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// 单连接：避免 SQLite 写并发下的 SQLITE_BUSY，队列/进度写入都是低 QPS。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := prepare(db); err != nil {
		db.Close()
		return nil, err
	}
	backup, err := migrate(db, path)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db, path: path, recovered: backup}, nil
}

// prepare 设置连接级 pragma。journal_mode=WAL 是方案 §5.3 首行的要求，
// 必须在事务外执行。
func prepare(db *sql.DB) error {
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA synchronous=NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	return nil
}
