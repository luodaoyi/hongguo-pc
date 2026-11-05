package bindings

import (
	"context"
	"errors"
	"fmt"

	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/player"
)

// Player 是播放服务（方案 §8.3 的 Player.*）。
//
// 所有方法都可能消耗额度：Prepare 1 台（缓存命中且持内存密钥则 0），
// SwitchQuality 在同一未过期模型内 0，Ended 的自动连播视下一集是否已预取。
type Player struct {
	Core *Core
}

// EpisodeRef 是 Next/Prev 的返回（方案 §8.3 的 {episodeId}）。
type EpisodeRef struct {
	EpisodeID string `json:"episodeId"`
}

// Prepare 解析并准备一集的播放源。
//
// 返回的 SourceDTO 里没有密钥：direct 是 CDN 直链，local 是 127.0.0.1 带 token 的地址。
func (p *Player) Prepare(ctx context.Context, episodeID, preferQuality string) (player.PrepareResult, error) {
	res, err := p.Core.Machine.Prepare(ctx, episodeID, preferQuality)
	if err != nil {
		p.Core.record(err)
		return res, err
	}
	return res, nil
}

// SwitchQuality 换清晰度（同一未过期模型内 0 额度）。
func (p *Player) SwitchQuality(ctx context.Context, episodeID, quality string) (model.SourceDTO, error) {
	if episodeID != "" {
		if current := p.Core.Machine.CurrentEpisode(); current != "" && current != episodeID {
			err := &player.Failure{
				Code:    player.CodePlaybackModelAbsent,
				Message: fmt.Sprintf("当前播放的是 %s，不能直接切换 %s 的清晰度", current, episodeID),
			}
			p.Core.record(err)
			return model.SourceDTO{}, err
		}
	}
	src, err := p.Core.Machine.SwitchQuality(ctx, quality)
	if err != nil {
		p.Core.record(err)
		return model.SourceDTO{}, err
	}
	return src, nil
}

// Ended 上报「本集播完」（前端 ended 事件）。
//
// episodeID 必须是真正播完的那一集：换清晰度触发的 load() 或路由切走后的迟到 ended
// 都会带旧集 ID，后端据此只记断点、不推进连播（否则下一集被记成已看完）。
//
// 返回 EndedResult：HasNext/SeriesFinished 决定提示，Prepared 非空则已自动解析好下一集，
// 前端直接把 Prepared.Source 交给 <video> 即可（方案 §10.2）。
func (p *Player) Ended(ctx context.Context, episodeID string, positionSec float64) (player.EndedResult, error) {
	res, err := p.Core.Machine.EndedAt(ctx, episodeID, positionSec)
	if err != nil {
		p.Core.record(err)
		return res, err
	}
	return res, nil
}

// ReportProgress 上报播放进度（5 秒节流落盘，方案 §5.4）。
func (p *Player) ReportProgress(ctx context.Context, episodeID string, positionSec float64) error {
	if err := p.Core.Machine.ReportProgress(ctx, episodeID, positionSec); err != nil {
		p.Core.record(err)
		return err
	}
	return nil
}

// FlushProgress 强制落盘断点：暂停、切集、退出前调用（方案 §5.4）。
//
// 这是 §8.3 方法表之外补的一个方法（报告里已说明）：只有把节流开关交给调用方，
// 「暂停即落盘」才可能做到。
func (p *Player) FlushProgress(ctx context.Context, episodeID string, positionSec float64) error {
	if episodeID != "" {
		if err := p.Core.Machine.StageProgress(ctx, episodeID, positionSec); err != nil {
			p.Core.record(err)
			return err
		}
	}
	if err := p.Core.Machine.FlushProgress(ctx); err != nil {
		p.Core.record(err)
		return err
	}
	return nil
}

// Next 返回同剧下一集（跳过不可播分集）。
func (p *Player) Next(ctx context.Context) (EpisodeRef, error) {
	id, err := p.Core.Machine.Next(ctx)
	if err != nil {
		if !errors.Is(err, player.ErrNoNextEpisode) {
			p.Core.record(err)
		}
		return EpisodeRef{}, err
	}
	return EpisodeRef{EpisodeID: id}, nil
}

// Prev 返回同剧上一集（跳过不可播分集）。
func (p *Player) Prev(ctx context.Context) (EpisodeRef, error) {
	id, err := p.Core.Machine.Prev(ctx)
	if err != nil {
		if !errors.Is(err, player.ErrNoPrevEpisode) {
			p.Core.record(err)
		}
		return EpisodeRef{}, err
	}
	return EpisodeRef{EpisodeID: id}, nil
}

// State 返回当前播放器状态（idle|resolving|playing|ended|next|error）。
func (p *Player) State(ctx context.Context) player.State {
	return p.Core.Machine.State()
}
