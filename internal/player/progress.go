package player

import (
	"context"
	"errors"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
)

// 断点落盘（方案 §5.4 / §6.5）。
//
// 规则：timeupdate 每 5 秒最多落一次盘；暂停、切集、退出、播完时强制落盘。
// 断点主键是 series_id，因此切集不丢上一集的位置——同一部剧连续看时，
// 后一次覆盖前一次，与「继续观看」的语义一致。

// stageProgress 把一次进度写进内存待落盘表。
//
// episodeID 找不到所属剧时返回 nil 并跳过：剧未知只影响断点，不影响播放。
func (m *Machine) stageProgress(episodeID string, positionSec float64, finished bool) error {
	var seriesID string
	var number, duration int
	m.mu.Lock()
	if m.cur != nil && m.cur.episodeID == episodeID {
		seriesID, number, duration = m.cur.seriesID, m.cur.number, m.cur.durationSec
	}
	m.mu.Unlock()
	if seriesID == "" {
		seriesID, number, duration = m.locate(ctxBackground(), episodeID, nil)
	}
	if seriesID == "" {
		return nil
	}
	h := model.HistoryDTO{
		SeriesID:      seriesID,
		EpisodeID:     episodeID,
		EpisodeNumber: number,
		PositionSec:   positionSec,
		DurationSec:   duration,
		Finished:      finished,
		UpdatedAt:     m.now().Unix(),
	}
	m.mu.Lock()
	m.pending[seriesID] = h
	m.mu.Unlock()
	return nil
}

// flush 把待落盘断点写进 store。
//
// force 为 false 时按 historyThrottle（5 秒）节流：距上次写盘不足 5 秒就跳过，
// 断点留在 m.pending 里等下一次。
func (m *Machine) flush(ctx context.Context, force bool) error {
	now := m.now()
	m.mu.Lock()
	due := make([]model.HistoryDTO, 0, len(m.pending))
	for seriesID, h := range m.pending {
		if !force {
			if last, ok := m.lastWrite[seriesID]; ok && now.Sub(last) < historyThrottle {
				continue
			}
		}
		due = append(due, h)
	}
	m.mu.Unlock()

	var errs []error
	for _, h := range due {
		if err := m.st.HistoryPut(ctx, h); err != nil {
			errs = append(errs, err)
			continue
		}
		m.mu.Lock()
		delete(m.pending, h.SeriesID)
		m.lastWrite[h.SeriesID] = now
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// resumePoint 读断点算续播位置（方案 §6.5：未看完从 position-3s 起）。
func (m *Machine) resumePoint(ctx context.Context, seriesID, episodeID string, durationSec int) float64 {
	if seriesID == "" {
		return 0
	}
	h, err := m.st.HistoryGet(ctx, seriesID)
	if err != nil || h == nil {
		return 0
	}
	// 断点落在别的集（用户从选集跳到另一集）：不强行续播。
	if h.EpisodeID != "" && h.EpisodeID != episodeID {
		return 0
	}
	if h.Finished || h.PositionSec <= resumeBackoff {
		return 0
	}
	if durationSec > 0 && h.PositionSec >= float64(durationSec)-endTolerance {
		return 0
	}
	return h.PositionSec - resumeBackoff
}

// emitProgress 发 player:progress 事件（方案 §8.4），1 秒节流。
func (m *Machine) emitProgress(episodeID string, positionSec float64) {
	now := m.now()
	m.mu.Lock()
	if now.Sub(m.lastEmit) < time.Second {
		m.mu.Unlock()
		return
	}
	m.lastEmit = now
	duration := 0
	if m.cur != nil && m.cur.episodeID == episodeID {
		duration = m.cur.durationSec
	}
	m.mu.Unlock()
	m.emit("player:progress", map[string]any{
		"episodeId":   episodeID,
		"positionSec": positionSec,
		"durationSec": duration,
	})
}

// emitQuota 发额度变化事件（方案 §8.4 quota:changed）。
func (m *Machine) emitQuota() {
	status := m.pool.Status()
	m.emit("quota:changed", map[string]any{
		"healthy":   status.Healthy,
		"usedToday": status.UsedToday,
	})
	m.emit("devices:changed", map[string]any{"slots": status.Slots})
}

// modelExpired 判断当前播放模型是否已到过期门（方案 §7.5 / F15）。
//
// ExpiresAt>0 用「剩余不足 60 秒」；ExpiresAt==0 时按「解析后 15 分钟」兜底。
func (m *Machine) modelExpired(s *session) bool {
	if s == nil || s.playback == nil {
		return true
	}
	if s.playback.ExpiresAt > 0 {
		return m.now().Unix() > s.playback.ExpiresAt-60
	}
	return m.now().Sub(s.resolvedAt) > 15*time.Minute
}

// 重新解析的节流参数（方案 §7.5：同一集 5 分钟内最多 2 次）。
const (
	reResolveWindow = 5 * time.Minute
	reResolveLimit  = 2
)

// noteReResolve 记一次重新解析并检查节流。超出上限返回 Failure。
func (m *Machine) noteReResolve(episodeID string) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	marks := m.resolveLog[episodeID]
	kept := marks[:0]
	for _, t := range marks {
		if now.Sub(t) < reResolveWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= reResolveLimit {
		m.resolveLog[episodeID] = kept
		return fail(CodeReResolveThrottled, "地址获取失败，请稍后重试", true, nil)
	}
	m.resolveLog[episodeID] = append(kept, now)
	return nil
}

// locate 找分集所属的剧、集号与时长。找不到剧时返回空串（不当作错误）。
func (m *Machine) locate(ctx context.Context, episodeID string, pb *sdkx.Playback) (seriesID string, number int, durationSec int) {
	if pb != nil {
		durationSec = pb.DurationSeconds
	}
	seriesID, ok := m.catalog.SeriesOfEpisode(ctx, episodeID)
	if !ok {
		return "", 0, durationSec
	}
	eps, err := m.catalog.Episodes(ctx, seriesID)
	if err != nil {
		m.logf("player: list episodes of %s: %v", seriesID, err)
		return seriesID, 0, durationSec
	}
	for _, ep := range eps {
		if ep.ID == episodeID {
			number = ep.Number
			if ep.DurationSec > 0 {
				durationSec = ep.DurationSec
			}
			return seriesID, number, durationSec
		}
	}
	return seriesID, 0, durationSec
}

// ctxBackground 给不接收 ctx 的内部调用提供背景上下文。
func ctxBackground() context.Context { return context.Background() }
