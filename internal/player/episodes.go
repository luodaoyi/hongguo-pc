package player

import (
	"context"
	"sort"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// 连播导航（方案 §5.4 / §6.2 / §7.1）。
//
// 选集规则：只挑 Playable=true 的集；服务端禁止播放的分集置灰且不参与连播。
// 顺序一律按 number 升序（store 已按 number 排序，这里再排一次以免调用方顺序不同）。

// nextInSeries 返回当前集之后的下一个可播分集。
func (m *Machine) nextInSeries(ctx context.Context, cur *session) (string, bool, error) {
	if cur == nil || cur.seriesID == "" {
		return "", false, nil
	}
	return m.nextAfter(ctx, cur.seriesID, cur.number)
}

// nextAfter 返回指定剧里 number 之后的下一个可播分集。
func (m *Machine) nextAfter(ctx context.Context, seriesID string, number int) (string, bool, error) {
	eps, err := m.sortedEpisodes(ctx, seriesID)
	if err != nil {
		return "", false, err
	}
	next, ok := nextPlayable(eps, number)
	return next, ok, nil
}

// numberOf 返回分集的集号；找不到返回 0。
func (m *Machine) numberOf(ctx context.Context, seriesID, episodeID string) int {
	if seriesID == "" || episodeID == "" {
		return 0
	}
	eps, err := m.sortedEpisodes(ctx, seriesID)
	if err != nil {
		m.logf("player: list episodes of %s: %v", seriesID, err)
		return 0
	}
	for _, ep := range eps {
		if ep.ID == episodeID {
			return ep.Number
		}
	}
	return 0
}

// sortedEpisodes 读一部剧的分集并按 number 升序排好。
func (m *Machine) sortedEpisodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error) {
	eps, err := m.catalog.Episodes(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(eps, func(i, j int) bool { return eps[i].Number < eps[j].Number })
	return eps, nil
}

// nextPlayable 在按 number 升序的分集里找 number 之后第一个可播的集。
func nextPlayable(eps []model.EpisodeDTO, number int) (string, bool) {
	for _, ep := range eps {
		if ep.Number > number && ep.Playable && ep.ID != "" {
			return ep.ID, true
		}
	}
	return "", false
}

// prevPlayable 在按 number 升序的分集里找 number 之前最后一个可播的集。
func prevPlayable(eps []model.EpisodeDTO, number int) (string, bool) {
	for i := len(eps) - 1; i >= 0; i-- {
		ep := eps[i]
		if ep.Number < number && ep.Playable && ep.ID != "" {
			return ep.ID, true
		}
	}
	return "", false
}
