package player

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/localsrv"
	"github.com/luodaoyi/hongguo-pc/internal/media"
	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// MediaPipeline 是 Pipeline 的生产实现：media.Decider 判道 → direct 直链
// 或 media.Session 解密 + localsrv 本地地址（方案 §3.2 / §10.1）。
//
// 密钥纪律（I2）：KeyHex 只从 RenderRequest 抄进 media.Input，然后进 ffmpeg 命令行；
// 本文件不打印、不序列化、不回传密钥。
type MediaPipeline struct {
	decider media.Decider
	session *media.Session
	server  *localsrv.Server
	now     func() time.Time
	logf    func(string, ...any)
	emit    func(string, any)

	// mu / done 缓存「已解出且文件仍在」的本地地址，key 是 episodeID|quality。
	//
	// 为什么需要：同一集可能被前端连调两次 Prepare（例如 Ended 已在服务端准备好
	// 下一集，前端又按自己的连播逻辑再 Prepare 一次）。没有这层缓存时第二次会
	// 再拉一个 ffmpeg 进程写另一份文件，白烧 CPU 与磁盘。
	mu   sync.Mutex
	done map[string]renderedEntry
}

// renderedEntry 是一次成功解密的产物。
type renderedEntry struct {
	source model.SourceDTO
	path   string
	at     time.Time
}

// localReuseWindow 是本地解密地址的复用窗口；超过就重新起 ffmpeg。
const localReuseWindow = 30 * time.Minute

// MediaPipelineConfig 是构造参数。Server 必填；Session 可以为空——ffmpeg 缺失时
// 应用仍要能启动（方案 §13.3 首启环境自检：缺少 ffmpeg 只影响加密集）。
type MediaPipelineConfig struct {
	// Session 是 media 包的解密会话（临时目录 + ffmpeg 进程 + LRU 保留）。
	// 为 nil 时所有需要解密的集都返回 ffmpeg_missing。
	Session *media.Session
	// Server 是 localsrv 的本地 Range 服务（127.0.0.1 + token）。
	Server *localsrv.Server
	// Policy 是方案 §3.2 的策略开关，零值取 media.Current。
	Policy media.Policy
	// Now 可选：注入时钟，默认 time.Now。
	Now func() time.Time
	// Logf 可选：诊断日志，默认丢弃。
	Logf func(format string, args ...any)
	// Emit 可选：事件发射器，用于 media:preparing（方案 §8.4）。
	Emit func(event string, payload any)
}

// NewMediaPipeline 构造媒体管道。
func NewMediaPipeline(cfg MediaPipelineConfig) (*MediaPipeline, error) {
	if cfg.Server == nil {
		return nil, fmt.Errorf("player: localsrv server is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	emit := cfg.Emit
	if emit == nil {
		emit = func(string, any) {}
	}
	policy := cfg.Policy
	if policy.DirectSkipCodecs == nil {
		policy = media.Current
	}
	return &MediaPipeline{
		decider: media.NewDecider(policy),
		session: cfg.Session,
		server:  cfg.Server,
		now:     now,
		logf:    logf,
		emit:    emit,
		done:    make(map[string]renderedEntry, 4),
	}, nil
}

// Render 把播放模型变成可播地址（方案 §3.2 的判定顺序）。
func (p *MediaPipeline) Render(ctx context.Context, req RenderRequest) (RenderResult, error) {
	if req.Playback == nil {
		return RenderResult{}, fail(CodePlaybackModelAbsent, "播放模型为空", true, nil)
	}
	in := media.Input{
		EpisodeID: req.EpisodeID,
		SourceURL: req.Stream.URL,
		AudioURL:  req.Playback.AudioURL,
		KeyHex:    req.KeyHex,
		Protected: req.Stream.Protected || req.Playback.Protected,
		HLS:       req.Stream.HLS,
		Dash:      req.Stream.Dash,
		Codec:     req.Stream.Codec,
	}
	decision := p.decider.Decide(in, req.Playback.ExpiresAt, p.now())
	if decision.NeedsResolve {
		// 过期门：地址剩余不足 1 分钟，必须重新解析（方案 §7.5，代价 1 台设备）。
		return RenderResult{}, fail(CodeAddressExpired, "播放地址已临期，需要重新解析", true, media.ErrExpiredAddress)
	}

	if decision.Mode == media.ModeDirect {
		return RenderResult{
			Mode: "direct",
			Source: model.SourceDTO{
				Mode:      "direct",
				URL:       in.SourceURL,
				MIME:      media.MIMEVideoMP4,
				ExpiresAt: req.Playback.ExpiresAt,
			},
		}, nil
	}

	// B1：ffmpeg 解密重封装成渐进 fMP4，再由 localsrv 用带 token 的 127.0.0.1 地址端出去。
	if p.session == nil {
		return RenderResult{}, fail(CodeFFmpegMissing, "未找到 ffmpeg，加密集无法播放；请在设置页指定 ffmpeg 路径", false, nil)
	}
	reuseKey := req.EpisodeID + "|" + req.Stream.Definition + "|" + req.Stream.Quality
	if reused, ok := p.reuse(reuseKey); ok {
		return reused, nil
	}
	p.emit("media:preparing", map[string]any{
		"episodeId": req.EpisodeID,
		"mode":      string(decision.Mode),
		"stage":     "start",
	})
	p.logf("player: decrypt episode %s (codec=%s hls=%v dash=%v audio=%v)", req.EpisodeID, in.Codec, in.HLS, in.Dash, in.AudioURL != "")
	out, err := p.session.Start(ctx, in)
	if err != nil {
		return RenderResult{}, fail(CodeMediaFailed, err.Error(), false, err)
	}
	url, err := p.server.URL(req.EpisodeID, out.Path)
	if err != nil {
		return RenderResult{}, fail(CodeMediaFailed, "本地播放服务不可用："+err.Error(), true, err)
	}
	p.emit("media:preparing", map[string]any{
		"episodeId": req.EpisodeID,
		"mode":      string(decision.Mode),
		"stage":     "ready",
	})
	result := RenderResult{
		Mode: "local",
		Source: model.SourceDTO{
			Mode: "local",
			URL:  url,
			MIME: media.MIMEVideoMP4,
		},
	}
	p.mu.Lock()
	p.done[reuseKey] = renderedEntry{source: result.Source, path: out.Path, at: p.now()}
	p.mu.Unlock()
	return result, nil
}

// reuse 复用同一集同一清晰度已经解好的本地地址（文件仍存在且在窗口内）。
func (p *MediaPipeline) reuse(key string) (RenderResult, bool) {
	p.mu.Lock()
	entry, ok := p.done[key]
	p.mu.Unlock()
	if !ok || p.now().Sub(entry.at) > localReuseWindow {
		return RenderResult{}, false
	}
	if fi, err := os.Stat(entry.path); err != nil || fi.Size() <= 0 {
		return RenderResult{}, false
	}
	return RenderResult{Mode: "local", Source: entry.source}, true
}

// Close 关闭解密会话（终止 ffmpeg 进程、清空临时目录）。
//
// localsrv 的生命周期归 cmd/app 管（与窗口同生共死），这里只关媒体会话。
func (p *MediaPipeline) Close() error {
	p.mu.Lock()
	p.done = make(map[string]renderedEntry, 4)
	p.mu.Unlock()
	if p.session == nil {
		return nil
	}
	return p.session.Close()
}
