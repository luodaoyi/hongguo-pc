package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

type sqliteStore struct {
	db        *sql.DB
	path      string
	recovered string
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) Recovered() string { return s.recovered }

// UpsertSeries 写入或更新一部短剧的目录行（主键 id）。
func (s *sqliteStore) UpsertSeries(ctx context.Context, dto model.SeriesDTO) error {
	if dto.ID == "" {
		return fmt.Errorf("store: series id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO series(id, title, cover_url, episode_count, status, pay_type, platform,
			is_multi_season, season_index, album_id, right_text, fetched_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title, cover_url=excluded.cover_url,
			episode_count=excluded.episode_count, status=excluded.status,
			pay_type=excluded.pay_type, platform=excluded.platform,
			is_multi_season=excluded.is_multi_season, season_index=excluded.season_index,
			album_id=excluded.album_id, right_text=excluded.right_text,
			fetched_at=excluded.fetched_at`,
		dto.ID, dto.Title, nullString(dto.CoverURL), dto.EpisodeCount,
		model.SeriesStatusCode(dto.Status), model.PayTypeCode(dto.PayType), dto.Platform,
		boolInt(dto.IsMultiSeason), dto.SeasonIndex, nullString(dto.AlbumID),
		nullString(dto.RightText), dto.FetchedAt)
	if err != nil {
		return fmt.Errorf("store: upsert series %s: %w", dto.ID, err)
	}
	return nil
}

// GetSeries 读一部短剧，没有则返回 (nil, nil)。
func (s *sqliteStore) GetSeries(ctx context.Context, id string) (*model.SeriesDTO, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, title, COALESCE(cover_url,''), COALESCE(episode_count,0), COALESCE(status,0),
			COALESCE(pay_type,0), COALESCE(platform,0), COALESCE(is_multi_season,0),
			COALESCE(season_index,0), COALESCE(album_id,''), COALESCE(right_text,''), fetched_at
		FROM series WHERE id=?`, id)
	var (
		dto                 model.SeriesDTO
		multi               int
		statusCode, payCode int
	)
	if err := row.Scan(&dto.ID, &dto.Title, &dto.CoverURL, &dto.EpisodeCount, &statusCode,
		&payCode, &dto.Platform, &multi, &dto.SeasonIndex, &dto.AlbumID, &dto.RightText,
		&dto.FetchedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get series %s: %w", id, err)
	}
	dto.Status = model.SeriesStatusName(statusCode)
	dto.PayType = model.PayTypeName(payCode)
	dto.IsMultiSeason = multi != 0
	return &dto, nil
}

// Episodes 读一部剧未软删的分集，按 number 升序。
func (s *sqliteStore) Episodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, series_id, number, COALESCE(title,''), COALESCE(duration_sec,0),
			playable, is_new, COALESCE(cover_url,'')
		FROM episode WHERE series_id=? AND removed_at IS NULL
		ORDER BY number ASC, id ASC`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("store: list episodes of %s: %w", seriesID, err)
	}
	defer rows.Close()
	out := make([]model.EpisodeDTO, 0, 64)
	for rows.Next() {
		var (
			ep              model.EpisodeDTO
			playable, isNew int
		)
		if err := rows.Scan(&ep.ID, &ep.SeriesID, &ep.Number, &ep.Title, &ep.DurationSec,
			&playable, &isNew, &ep.CoverURL); err != nil {
			return nil, fmt.Errorf("store: scan episode: %w", err)
		}
		ep.Playable = playable != 0
		ep.IsNew = isNew != 0
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate episodes of %s: %w", seriesID, err)
	}
	return out, nil
}

// ReplaceEpisodes 用服务端全量结果做增量合并（方案 §7.3），返回增删改集合。
//
// 合并键是 id，排序键是 number；软删保证 history 不指向空行。
// number 发生变化时先按 id 删除旧行再插入新行（方案 r5：episode 只有 id 主键，
// 不存在唯一键冲突，但排序视图需要一次显式重写）。
func (s *sqliteStore) ReplaceEpisodes(ctx context.Context, seriesID string, fresh []model.EpisodeDTO) (model.EpisodeDiff, error) {
	diff := model.EpisodeDiff{
		Added:   []string{},
		Updated: []string{},
		Removed: []string{},
		IsNew:   []string{},
	}
	if seriesID == "" {
		return diff, fmt.Errorf("store: series id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return diff, fmt.Errorf("store: begin episode merge: %w", err)
	}
	defer tx.Rollback()

	type oldRow struct {
		number      int
		title       string
		durationSec int
		playable    bool
		isNew       bool
		coverURL    string
		removed     bool
	}
	old := make(map[string]oldRow, len(fresh))
	maxNumber := 0
	rows, err := tx.QueryContext(ctx, `
		SELECT id, number, COALESCE(title,''), COALESCE(duration_sec,0), playable, is_new,
			COALESCE(cover_url,''), COALESCE(removed_at,0)
		FROM episode WHERE series_id=?`, seriesID)
	if err != nil {
		return diff, fmt.Errorf("store: read current episodes of %s: %w", seriesID, err)
	}
	for rows.Next() {
		var (
			id              string
			r               oldRow
			playable, isNew int
			removedAt       int64
		)
		if err := rows.Scan(&id, &r.number, &r.title, &r.durationSec, &playable, &isNew,
			&r.coverURL, &removedAt); err != nil {
			rows.Close()
			return diff, fmt.Errorf("store: scan current episode: %w", err)
		}
		r.playable = playable != 0
		r.isNew = isNew != 0
		r.removed = removedAt != 0
		old[id] = r
		if r.number > maxNumber {
			maxNumber = r.number
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return diff, fmt.Errorf("store: iterate current episodes of %s: %w", seriesID, err)
	}
	rows.Close()

	now := time.Now().Unix()
	seen := make(map[string]bool, len(fresh))
	for _, ep := range fresh {
		if ep.ID == "" {
			continue
		}
		seen[ep.ID] = true
		prev, exists := old[ep.ID]
		changed := !exists || prev.number != ep.Number || prev.title != ep.Title ||
			prev.durationSec != ep.DurationSec || prev.playable != ep.Playable ||
			prev.isNew != ep.IsNew || prev.coverURL != ep.CoverURL || prev.removed
		switch {
		case !exists:
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO episode(id, series_id, number, title, duration_sec, playable, is_new, cover_url, fetched_at)
				VALUES(?,?,?,?,?,?,?,?,?)`,
				ep.ID, seriesID, ep.Number, nullString(ep.Title), ep.DurationSec,
				boolInt(ep.Playable), boolInt(ep.IsNew), nullString(ep.CoverURL), now); err != nil {
				return diff, fmt.Errorf("store: insert episode %s: %w", ep.ID, err)
			}
			diff.Added = append(diff.Added, ep.ID)
		case prev.number != ep.Number:
			// 集数变了：先删旧行再插入，避免排序视图留下旧 number。
			if _, err := tx.ExecContext(ctx, `DELETE FROM episode WHERE id=?`, ep.ID); err != nil {
				return diff, fmt.Errorf("store: delete episode %s before renumbering: %w", ep.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO episode(id, series_id, number, title, duration_sec, playable, is_new, cover_url, fetched_at)
				VALUES(?,?,?,?,?,?,?,?,?)`,
				ep.ID, seriesID, ep.Number, nullString(ep.Title), ep.DurationSec,
				boolInt(ep.Playable), boolInt(ep.IsNew), nullString(ep.CoverURL), now); err != nil {
				return diff, fmt.Errorf("store: reinsert episode %s: %w", ep.ID, err)
			}
			diff.Updated = append(diff.Updated, ep.ID)
		case changed:
			if _, err := tx.ExecContext(ctx, `
				UPDATE episode SET number=?, title=?, duration_sec=?, playable=?, is_new=?,
					cover_url=?, fetched_at=?, removed_at=NULL WHERE id=?`,
				ep.Number, nullString(ep.Title), ep.DurationSec, boolInt(ep.Playable),
				boolInt(ep.IsNew), nullString(ep.CoverURL), now, ep.ID); err != nil {
				return diff, fmt.Errorf("store: update episode %s: %w", ep.ID, err)
			}
			diff.Updated = append(diff.Updated, ep.ID)
		}
		// 新集双保险：服务端 IsNew，或「确实是新插入的行」且集数超过刷新前的最大集数
		// （方案 §7.3）。首次拉取时旧集数上界是 0，不能把整部剧都标成新集；
		// 仅仅集数变动（重排）也不算新集。
		if ep.IsNew || (!exists && maxNumber > 0 && ep.Number > maxNumber) {
			diff.IsNew = append(diff.IsNew, ep.ID)
		}
	}

	for id, prev := range old {
		if seen[id] || prev.removed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE episode SET removed_at=? WHERE id=?`, now, id); err != nil {
			return diff, fmt.Errorf("store: soft delete episode %s: %w", id, err)
		}
		diff.Removed = append(diff.Removed, id)
	}

	if err := tx.Commit(); err != nil {
		return diff, fmt.Errorf("store: commit episode merge: %w", err)
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Updated)
	sort.Strings(diff.Removed)
	sort.Strings(diff.IsNew)
	return diff, nil
}

// MarkRemoved 把指定分集软删（写 removed_at）。
func (s *sqliteStore) MarkRemoved(ctx context.Context, seriesID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin mark removed: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE episode SET removed_at=? WHERE id=? AND series_id=?`, now, id, seriesID); err != nil {
			return fmt.Errorf("store: mark episode %s removed: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit mark removed: %w", err)
	}
	return nil
}

// HistoryList 按 updated_at 倒序读最近 limit 条断点。
func (s *sqliteStore) HistoryList(ctx context.Context, limit int) ([]model.HistoryDTO, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT series_id, COALESCE(episode_id,''), COALESCE(episode_number,0), position_sec,
			COALESCE(duration_sec,0), finished, updated_at
		FROM history ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list history: %w", err)
	}
	defer rows.Close()
	out := make([]model.HistoryDTO, 0, limit)
	for rows.Next() {
		var (
			h        model.HistoryDTO
			finished int
		)
		if err := rows.Scan(&h.SeriesID, &h.EpisodeID, &h.EpisodeNumber, &h.PositionSec,
			&h.DurationSec, &finished, &h.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan history: %w", err)
		}
		h.Finished = finished != 0
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate history: %w", err)
	}
	return out, nil
}

// HistoryGet 读一部剧的断点，没有则返回 (nil, nil)。
func (s *sqliteStore) HistoryGet(ctx context.Context, seriesID string) (*model.HistoryDTO, error) {
	var (
		h        model.HistoryDTO
		finished int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT series_id, COALESCE(episode_id,''), COALESCE(episode_number,0), position_sec,
			COALESCE(duration_sec,0), finished, updated_at
		FROM history WHERE series_id=?`, seriesID).Scan(&h.SeriesID, &h.EpisodeID,
		&h.EpisodeNumber, &h.PositionSec, &h.DurationSec, &finished, &h.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get history %s: %w", seriesID, err)
	}
	h.Finished = finished != 0
	return &h, nil
}

// HistoryPut 写入或覆盖一部剧的断点（主键 series_id）。
func (s *sqliteStore) HistoryPut(ctx context.Context, h model.HistoryDTO) error {
	if h.SeriesID == "" {
		return fmt.Errorf("store: history series id is required")
	}
	updatedAt := h.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO history(series_id, episode_id, episode_number, position_sec, duration_sec, finished, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(series_id) DO UPDATE SET
			episode_id=excluded.episode_id, episode_number=excluded.episode_number,
			position_sec=excluded.position_sec, duration_sec=excluded.duration_sec,
			finished=excluded.finished, updated_at=excluded.updated_at`,
		h.SeriesID, nullString(h.EpisodeID), h.EpisodeNumber, h.PositionSec, h.DurationSec,
		boolInt(h.Finished), updatedAt)
	if err != nil {
		return fmt.Errorf("store: put history %s: %w", h.SeriesID, err)
	}
	return nil
}

// HistoryClear 清空全部断点。
func (s *sqliteStore) HistoryClear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM history`); err != nil {
		return fmt.Errorf("store: clear history: %w", err)
	}
	return nil
}

// CacheGet 读一条播放模型缓存，没有则返回 (nil, nil)。
func (s *sqliteStore) CacheGet(ctx context.Context, episodeID string) (*CacheEntry, error) {
	var (
		e       CacheEntry
		hasKey  int
		series  sql.NullString
		expires sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT episode_id, series_id, payload, resolved_at, expires_at, has_key
		FROM playback_cache WHERE episode_id=?`, episodeID).Scan(&e.EpisodeID, &series,
		&e.Payload, &e.ResolvedAt, &expires, &hasKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get playback cache %s: %w", episodeID, err)
	}
	e.SeriesID = series.String
	e.ExpiresAt = expires.Int64
	e.HasKey = hasKey != 0
	return &e, nil
}

// CachePut 写入或覆盖一条缓存。payload 必须是 Playback 的 JSON 且不含密钥（I2）。
func (s *sqliteStore) CachePut(ctx context.Context, e CacheEntry) error {
	if e.EpisodeID == "" {
		return fmt.Errorf("store: cache episode id is required")
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("store: cache payload for %s is empty", e.EpisodeID)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO playback_cache(episode_id, series_id, payload, resolved_at, expires_at, has_key)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(episode_id) DO UPDATE SET
			series_id=excluded.series_id, payload=excluded.payload,
			resolved_at=excluded.resolved_at, expires_at=excluded.expires_at,
			has_key=excluded.has_key`,
		e.EpisodeID, nullString(e.SeriesID), e.Payload, e.ResolvedAt, e.ExpiresAt,
		boolInt(e.HasKey))
	if err != nil {
		return fmt.Errorf("store: put playback cache %s: %w", e.EpisodeID, err)
	}
	return nil
}

// CacheDelete 删除一条缓存（地址过期即删，方案 §7.5）。
func (s *sqliteStore) CacheDelete(ctx context.Context, episodeID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM playback_cache WHERE episode_id=?`, episodeID); err != nil {
		return fmt.Errorf("store: delete playback cache %s: %w", episodeID, err)
	}
	return nil
}

// CachePurgeExpired 删除 expires_at 已过期的缓存，返回删除行数。
func (s *sqliteStore) CachePurgeExpired(ctx context.Context, now int64) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM playback_cache WHERE expires_at IS NOT NULL AND expires_at > 0 AND expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("store: purge expired playback cache: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge expired playback cache: %w", err)
	}
	return int(n), nil
}

// DeviceSlots 读全部设备槽记账，按 id 升序。
func (s *sqliteStore) DeviceSlots(ctx context.Context) ([]DeviceSlotRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, profile_path, state, COALESCE(quota_used,0), COALESCE(quota_day,0),
			COALESCE(cool_until,0), COALESCE(last_used,0), COALESCE(reject_count,0)
		FROM device_slot ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list device slots: %w", err)
	}
	defer rows.Close()
	out := make([]DeviceSlotRecord, 0, 8)
	for rows.Next() {
		var r DeviceSlotRecord
		if err := rows.Scan(&r.ID, &r.ProfilePath, &r.State, &r.QuotaUsed, &r.QuotaDay,
			&r.CoolUntil, &r.LastUsed, &r.RejectCount); err != nil {
			return nil, fmt.Errorf("store: scan device slot: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate device slots: %w", err)
	}
	return out, nil
}

// DeviceSlotPut 写入或覆盖一个设备槽记账。
func (s *sqliteStore) DeviceSlotPut(ctx context.Context, r DeviceSlotRecord) error {
	if r.ID == "" {
		return fmt.Errorf("store: device slot id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_slot(id, profile_path, state, quota_used, quota_day, cool_until, last_used, reject_count)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			profile_path=excluded.profile_path, state=excluded.state,
			quota_used=excluded.quota_used, quota_day=excluded.quota_day,
			cool_until=excluded.cool_until, last_used=excluded.last_used,
			reject_count=excluded.reject_count`,
		r.ID, r.ProfilePath, r.State, r.QuotaUsed, r.QuotaDay, r.CoolUntil, r.LastUsed,
		r.RejectCount)
	if err != nil {
		return fmt.Errorf("store: put device slot %s: %w", r.ID, err)
	}
	return nil
}

// nullString 把空串写成 NULL，让 COALESCE 与可选列语义一致。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// boolInt 把布尔写成 SQLite 的 0/1。
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
