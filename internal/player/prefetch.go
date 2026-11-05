package player

import (
	"context"
	"time"
)

// 预取（方案 §6.2 / p4）。
//
// 三闸门缺一不可：
//  1. 剩余时长 ≤ min(30s, 时长 × 25%)；
//  2. 当前集已播 ≥60 秒；
//  3. 池内 healthy ≥2。
//
// **预取不可回收**：请求一旦发出，额度就已消耗——用户随后切集、切剧或退出都不退还。
// 因此本文件只在三闸门全过时发一次，并对同一集做去重（m.prefetched）；
// 用户切走时也无法取消已发出的预取，已发出的照常完成并写入缓存供将来命中。

// maybePrefetch 检查三闸门，满足则后台解析下一集。
func (m *Machine) maybePrefetch(episodeID string, positionSec float64) {
	m.mu.Lock()
	cur := m.cur
	mode := m.mode
	if cur == nil || cur.episodeID != episodeID {
		m.mu.Unlock()
		return
	}
	duration := cur.durationSec
	seriesID := cur.seriesID
	number := cur.number
	m.mu.Unlock()

	if mode != ModeSequential || seriesID == "" {
		return // single 不预取；跨剧（series_chained）本期未实现，也不预取
	}
	if !m.autoNext() {
		return
	}
	if !m.prefetchOn() {
		return // 设置项「连播预取」关闭：每集省 1 台设备，下一集起播稍慢
	}
	if positionSec < prefetchMinPlayed.Seconds() {
		return // 闸门 2：已播 ≥60 秒
	}
	if duration <= 0 {
		return
	}
	remaining := float64(duration) - positionSec
	window := float64(prefetchWindowMax)
	if quarter := float64(duration) * 0.25; quarter < window {
		window = quarter
	}
	if remaining > window || remaining < 0 {
		return // 闸门 1：剩余 ≤ min(30s, 时长 25%)
	}
	if m.pool.Status().Healthy < prefetchHealthy {
		return // 闸门 3：healthy ≥2
	}

	ctx, cancel := context.WithTimeout(m.baseCtx, 2*time.Minute)
	defer cancel()
	next, ok, err := m.nextAfter(ctx, seriesID, number)
	if err != nil || !ok {
		return
	}

	// 该集已有 Prepare 在飞时不预取：Prepare 自己会解析并写缓存，
	// 再预取一次等于为同一集烧两台设备（额度不可回收）。
	m.prepMu.Lock()
	preparing := m.preps[next] != nil
	m.prepMu.Unlock()
	if preparing {
		m.logf("player: skip prefetch %s (prepare in flight)", next)
		return
	}

	m.mu.Lock()
	if m.prefetched[next] || m.baseCtx.Err() != nil {
		m.mu.Unlock()
		return
	}
	m.prefetched[next] = true
	done := make(chan struct{})
	m.inflight[next] = done
	m.mu.Unlock()

	m.logf("player: prefetch next episode %s", next)
	m.prefetchWG.Add(1)
	go m.prefetch(next, done)
}

// awaitPrefetch 等一集的预取结束（没有在跑的预取则立即返回）。
func (m *Machine) awaitPrefetch(ctx context.Context, episodeID string) {
	m.mu.Lock()
	done, ok := m.inflight[episodeID]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// prefetch 后台解析一集，把播放模型与密钥写进缓存/内存表（0 或 1 额度，不可回收）。
func (m *Machine) prefetch(episodeID string, done chan struct{}) {
	defer m.prefetchWG.Done()
	defer func() {
		m.mu.Lock()
		delete(m.inflight, episodeID)
		m.mu.Unlock()
		close(done)
	}()
	ctx, cancel := context.WithTimeout(m.baseCtx, 2*time.Minute)
	defer cancel()
	if _, err := m.pool.ResolvePlayback(ctx, episodeID); err != nil {
		f := Describe(err)
		m.logf("player: prefetch %s failed: %s", episodeID, f.Code)
		return
	}
	m.emitQuota()
}
