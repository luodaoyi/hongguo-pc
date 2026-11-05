// Package player 是播放器状态机（方案 §6）：解析、选流、解密、连播、预取、断点。
//
// 硬规矩（方案 §2.2）：
//   - I3：解析播放模型只能走 devices.Pool.ResolvePlayback（唯一消耗额度的入口）；
//     本包不 import sdk，也不直连 sdkx.Client。
//   - I2：内容密钥只从 devices.Pool.Key(episodeID) 取，只喂给 media.Input.KeyHex；
//     不出现在 DTO、事件、日志、错误信息里。
//
// 状态机（方案 §6.1）：
//
//	IDLE ─Prepare→ RESOLVING ─ok→ PLAYING ─Ended→ ENDED ─有下一集→ NEXT → RESOLVING
//	                     └err→ ERROR                      └无下一集→ IDLE
package player

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

// State 是播放器状态（方案 §5.2 的 PlayerState 联合类型）。
type State string

const (
	StateIdle      State = "idle"
	StateResolving State = "resolving"
	StatePlaying   State = "playing"
	StateEnded     State = "ended"
	StateNext      State = "next"
	StateError     State = "error"
)

// Mode 是队列模式（方案 §5.4）。本期只实现 single 与 sequential：
// loop 与 series_chained 依赖 queue / queue_item 表的读写方法，而 store.Store
// 本期没有队列接口（contracts.md §5 偏差 5），本包不改 store 文件，故留 TODO。
type Mode string

const (
	// ModeSingle：播完一集停在 ENDED，不预取、不连播。
	ModeSingle Mode = "single"
	// ModeSequential：播完同部下一集；末集 ENDED 并提示「本剧看完」。
	ModeSequential Mode = "sequential"
)

// 连播的失败跳过上限（方案 §6.3 / F14：连续第 3 集不可用即中止连播）。
const maxUnavailable = 3

// 预取与断点的阈值（方案 §6.2 / §6.5）。
const (
	prefetchMinPlayed = 60 * time.Second // 当前集已播 ≥60 秒才允许预取
	prefetchWindowMax = 30               // 剩余 ≤ min(30s, 时长 25%)
	prefetchHealthy   = 2                // 池内 healthy ≥2 才允许预取
	historyThrottle   = 5 * time.Second  // 断点落盘节流
	resumeBackoff     = 3.0              // 续播回退 3 秒
	endTolerance      = 5.0              // 「已看完」容差（秒）
)

// ErrNoNextEpisode 表示当前已是最后一集（或队列里没有下一集）。
var ErrNoNextEpisode = errors.New("player: no next episode")

// ErrNoPrevEpisode 表示当前已是第一集。
var ErrNoPrevEpisode = errors.New("player: no previous episode")

// 本层产生的错误码（SDK 错误码见 sdkx.Classify，这里只补本层语义）。
const (
	CodeQuotaExhausted      = "quota_exhausted"
	CodeRateLimitedLong     = "rate_limited_long"
	CodeKeyUnsupported      = "drm_unsupported"
	CodeQualityUnavailable  = "quality_unavailable"
	CodeNoNext              = "no_next_episode"
	CodeNoPrev              = "no_prev_episode"
	CodeChainAborted        = "unavailable_chain_aborted"
	CodeMediaFailed         = "media_failed"
	CodeReResolveThrottled  = "re_resolve_throttled"
	CodeSeriesUnknown       = "series_unknown"
	CodePlaybackModelAbsent = "playback_model_missing"
	CodeAddressExpired      = "address_expired"
	CodeFFmpegMissing       = "ffmpeg_missing"
)

// Failure 是归一后的错误（方案 §8.3：错误统一 {code,message,retryable}）。
//
// 本结构可安全序列化：不含原始错误、不含密钥材料、不含带签名的地址。
type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	EpisodeID string `json:"episodeId,omitempty"`

	cause error
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return f.Code + ": " + f.Message
}

// Unwrap 保留底层原因供排障；底层原因不得含密钥（sdkx 侧已保证）。
func (f *Failure) Unwrap() error { return f.cause }

// fail 构造一个 Failure 并挂上底层原因。
func fail(code, message string, retryable bool, cause error) *Failure {
	return &Failure{Code: code, Message: message, Retryable: retryable, cause: cause}
}

// Describe 把任意错误归一成 Failure（方案 §8.3 / §8.4 的统一错误形态）。
func Describe(err error) Failure {
	if err == nil {
		return Failure{Code: sdkx.CodeOK, Message: ""}
	}
	var f *Failure
	if errors.As(err, &f) && f != nil {
		return *f
	}
	switch {
	case errors.Is(err, devices.ErrQuotaExhausted):
		return Failure{Code: CodeQuotaExhausted, Message: "今日可播放集数已用尽（匿名额度限制），明天恢复", Retryable: false}
	case errors.Is(err, devices.ErrRateLimitedLong):
		return Failure{Code: CodeRateLimitedLong, Message: "服务端要求等待较久，已放弃本次重试，请稍后再试", Retryable: true}
	case errors.Is(err, ErrNoNextEpisode):
		return Failure{Code: CodeNoNext, Message: "已经是最后一集", Retryable: false}
	case errors.Is(err, ErrNoPrevEpisode):
		return Failure{Code: CodeNoPrev, Message: "已经是第一集", Retryable: false}
	}
	var ku *sdkx.KeyUnavailableError
	if errors.As(err, &ku) {
		return Failure{Code: CodeKeyUnsupported, Message: "该集加密方式暂不支持，已跳过", Retryable: false}
	}
	code, msg, retryable := sdkx.Classify(err)
	return Failure{Code: code, Message: msg, Retryable: retryable}
}

// skippable 判断「跳过这一集继续连播」是否合理（方案 §6.3）。
//
// 额度耗尽不在此列：那是整池没额度，跳集也播不了，必须把错误交给用户。
func skippable(f Failure) bool {
	switch f.Code {
	case sdkx.CodeContentUnavailable, sdkx.CodeAuthRequired, sdkx.CodeChallengeRequired,
		sdkx.CodeAnonymousLimited, CodeKeyUnsupported:
		return true
	}
	return false
}

// Catalog 是 player 需要的目录能力（由 internal/library 实现）。
type Catalog interface {
	// Episodes 读一部剧的本地分集列表（按 number 升序）。
	Episodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error)
	// SeriesOfEpisode 反查分集所属的剧；索引未建时 ok 为 false。
	SeriesOfEpisode(ctx context.Context, episodeID string) (seriesID string, ok bool)
}

// PrepareResult 是 Prepare 的返回（方案 §8.3 的 {source,qualities[],state} + 续播点）。
type PrepareResult struct {
	Source        model.SourceDTO    `json:"source"`
	Qualities     []model.StreamInfo `json:"qualities"`
	Info          model.PlaybackInfo `json:"info"`
	State         State              `json:"state"`
	EpisodeID     string             `json:"episodeId"`
	SeriesID      string             `json:"seriesId,omitempty"`
	EpisodeNumber int                `json:"episodeNumber,omitempty"`
	DurationSec   int                `json:"durationSec,omitempty"`
	// ResumeSec 是续播起播点（方案 §6.5）：未看完从 position-3s 起，否则 0。
	ResumeSec float64 `json:"resumeSec"`
	// Mode 是本次走的通道：direct | local。
	Mode string `json:"mode"`
}

// EndedResult 是 Ended 的返回：前端据此决定「自动播下一集」还是「本剧看完」。
type EndedResult struct {
	State State `json:"state"`
	// HasNext 表示同一部剧还有下一集。
	HasNext bool `json:"hasNext"`
	// NextEpisodeID 是下一集 ID（HasNext 为真时才有值）。
	NextEpisodeID string `json:"nextEpisodeId,omitempty"`
	// SeriesFinished 表示本剧已到末集（方案 §5.4：ENDED + 提示「本剧看完」）。
	SeriesFinished bool `json:"seriesFinished"`
	// Prepared 非空表示已自动解析好下一集，前端直接把 source 交给 <video> 即可。
	Prepared *PrepareResult `json:"prepared,omitempty"`
	// Skipped 是本次连播中因不可用被跳过的集数（方案 §6.3，最多 2）。
	Skipped int `json:"skipped"`
	// Stale 为真表示这次上报是迟到/串台的 ended（换清晰度后旧集再触发、
	// 或路由已切走），后端只记了断点、没有推进连播：前端据此不要显示
	// 「已是最后一集」，也据此判断 hasNext/seriesFinished 无意义。
	Stale bool `json:"stale,omitempty"`
}

// Config 是构造状态机的参数。Pool / Store / Catalog / Pipeline 必填。
type Config struct {
	Pool    *devices.Pool
	Store   store.Store
	Catalog Catalog
	// Pipeline 把播放模型变成可播地址（direct 或本地解密源）。
	Pipeline Pipeline
	// Mode 缺省为 sequential（客户端默认自动连播）。
	Mode Mode
	// AutoNext 可选：读「自动下一集」开关（设置项）。nil 视为开启。
	AutoNext func() bool
	// Emit 可选：事件发射器（bindings 接 app.Event.Emit）。nil 时不发事件。
	Emit func(event string, payload any)
	// Now 可选：注入时钟，默认 time.Now。
	Now func() time.Time
	// Logf 可选：诊断日志，默认丢弃。**不得**打印密钥或带签名的地址。
	Logf func(format string, args ...any)
}

// Machine 是播放器状态机。并发安全（方法间共享状态，内部一把锁）。
type Machine struct {
	pool     *devices.Pool
	st       store.Store
	catalog  Catalog
	pipe     Pipeline
	emit     func(string, any)
	now      func() time.Time
	logf     func(string, ...any)
	autoNext func() bool

	mu    sync.Mutex
	mode  Mode
	state State
	cur   *session
	// prefetched 记录已发起过预取的集（预取不可回收，不重复发）。
	prefetched map[string]bool
	// inflight 记录正在预取的集 → 完成信号，供 Prepare 等待，避免同一集被并发解析
	// 两次（同一集并发的 ResolvePlayback 会各烧一次额度）。
	inflight map[string]chan struct{}
	// pending 是按剧聚拢的待落盘断点，key 是 seriesID。
	pending     map[string]model.HistoryDTO
	lastWrite   map[string]time.Time
	lastEmit    time.Time
	resolveLog  map[string][]time.Time
	unavailable int

	baseCtx    context.Context
	baseCancel context.CancelFunc
	prefetchWG sync.WaitGroup
	closeOnce  sync.Once

	// prepMu / preps 是「按 episodeID 的 Prepare single-flight」：
	// 同一集的并发 Prepare（前端连点、换清晰度后旧请求未归）只解析一次，
	// 其余等待方复用同一份结果——否则每次 ResolvePlayback 都会各烧一次播放额度。
	prepMu sync.Mutex
	preps  map[string]*prepareCall
}

// prepareCall 是一次在飞的 Prepare。
type prepareCall struct {
	done chan struct{}
	res  PrepareResult
	err  error
}

// session 是当前正在播的一集的上下文。
type session struct {
	episodeID   string
	seriesID    string
	number      int
	durationSec int
	playback    *sdkx.Playback
	resolvedAt  time.Time
	quality     string
	mode        string
	source      model.SourceDTO
}

// New 构造状态机。
func New(cfg Config) (*Machine, error) {
	if cfg.Pool == nil {
		return nil, fmt.Errorf("player: device pool is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("player: store is required")
	}
	if cfg.Catalog == nil {
		return nil, fmt.Errorf("player: catalog is required")
	}
	if cfg.Pipeline == nil {
		return nil, fmt.Errorf("player: media pipeline is required")
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
	autoNext := cfg.AutoNext
	if autoNext == nil {
		autoNext = func() bool { return true }
	}
	mode := cfg.Mode
	if mode == "" {
		mode = ModeSequential
	}
	if mode != ModeSingle && mode != ModeSequential {
		return nil, fmt.Errorf("player: unsupported queue mode %q (this build supports single|sequential)", mode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Machine{
		pool:       cfg.Pool,
		st:         cfg.Store,
		catalog:    cfg.Catalog,
		pipe:       cfg.Pipeline,
		emit:       emit,
		now:        now,
		logf:       logf,
		autoNext:   autoNext,
		mode:       mode,
		state:      StateIdle,
		prefetched: make(map[string]bool, 8),
		inflight:   make(map[string]chan struct{}, 4),
		preps:      make(map[string]*prepareCall, 4),
		pending:    make(map[string]model.HistoryDTO, 4),
		lastWrite:  make(map[string]time.Time, 4),
		resolveLog: make(map[string][]time.Time, 4),
		baseCtx:    ctx,
		baseCancel: cancel,
	}, nil
}

// State 返回当前状态。
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Mode 返回当前的队列模式。
func (m *Machine) Mode() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// CurrentEpisode 返回当前正在播的分集 ID（没有则空串）。
func (m *Machine) CurrentEpisode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return ""
	}
	return m.cur.episodeID
}

// setState 迁移状态并发 player:state 事件（方案 §8.4）。
func (m *Machine) setState(next State, episodeID string) {
	m.mu.Lock()
	m.state = next
	m.mu.Unlock()
	m.emit("player:state", map[string]any{"state": string(next), "episodeId": episodeID})
}

// Prepare 解析并准备一集的播放源（方案 §10.1）。
//
// 编排固定为：pool.ResolvePlayback → pb.Select → pool.Key → media.Decide →
// direct 直链 或 media.Session + localsrv.URL。加密集返回 (pb, ErrDRMProtected)
// 是成功语义，设备池已把密钥写进内存并归一成 (pb, nil)。
//
// 同一集的并发调用只真正解析一次（single-flight，见 preps）：额度不可回收，
// 重复解析等于白烧一台设备（方案 I3）。
func (m *Machine) Prepare(ctx context.Context, episodeID, preferQuality string) (PrepareResult, error) {
	call, owner := m.beginPrepare(episodeID)
	if !owner {
		select {
		case <-call.done:
			return call.res, call.err
		case <-ctx.Done():
			f := Describe(ctx.Err())
			f.EpisodeID = episodeID
			return PrepareResult{State: StateError, EpisodeID: episodeID}, &f
		}
	}

	res, err := m.prepare(ctx, episodeID, preferQuality)
	if err != nil {
		f := Describe(err)
		f.EpisodeID = episodeID
		m.setState(StateError, episodeID)
		m.emit("player:error", f)
		m.finishPrepare(episodeID, call, PrepareResult{State: StateError, EpisodeID: episodeID}, &f)
		return PrepareResult{State: StateError, EpisodeID: episodeID}, &f
	}
	m.finishPrepare(episodeID, call, res, nil)
	return res, nil
}

// beginPrepare 登记一次在飞的 Prepare；第二个返回值表示本次调用是否执行者。
func (m *Machine) beginPrepare(episodeID string) (*prepareCall, bool) {
	m.prepMu.Lock()
	defer m.prepMu.Unlock()
	if call, ok := m.preps[episodeID]; ok {
		return call, false
	}
	call := &prepareCall{done: make(chan struct{})}
	m.preps[episodeID] = call
	return call, true
}

// finishPrepare 公布结果并唤醒等待方。
func (m *Machine) finishPrepare(episodeID string, call *prepareCall, res PrepareResult, err error) {
	call.res, call.err = res, err
	m.prepMu.Lock()
	delete(m.preps, episodeID)
	m.prepMu.Unlock()
	close(call.done)
}

// prepare 是 Prepare 的实际实现（不做事件与状态兜底，供连播内部复用）。
func (m *Machine) prepare(ctx context.Context, episodeID, preferQuality string) (PrepareResult, error) {
	if strings.TrimSpace(episodeID) == "" {
		return PrepareResult{}, fail(sdkx.CodeParamInvalid, "缺少分集 ID", false, nil)
	}
	m.setState(StateResolving, episodeID)

	// 0. 同一集正在预取时先等它结束：否则两次 ResolvePlayback 会各烧一次额度。
	m.awaitPrefetch(ctx, episodeID)

	// 1. 解析播放模型：1 额度，或缓存命中 0（方案 §4.4）。
	pb, err := m.pool.ResolvePlayback(ctx, episodeID)
	if err != nil {
		f := Describe(err)
		if f.Code == CodeQuotaExhausted {
			m.emit("quota:exhausted", struct{}{})
		}
		return PrepareResult{}, &f
	}
	if pb == nil {
		return PrepareResult{}, fail(CodePlaybackModelAbsent, "服务端没有返回播放模型", true, nil)
	}
	m.emitQuota()

	// 2. 选流：只委托 SDK 的 Select（方案 F6）。
	stream, err := pb.Select(preferQuality)
	if err != nil {
		return PrepareResult{}, fail(CodeQualityUnavailable, "该清晰度不可用："+err.Error(), false, err)
	}

	// 3. 密钥只从内存表取，只喂媒体层（I2）。
	keyHex, _ := m.pool.Key(episodeID)

	// 4. 交给管道：direct 或 media.Session + localsrv（内部用 media.Decide 判道）。
	rendered, err := m.pipe.Render(ctx, RenderRequest{
		EpisodeID: episodeID,
		Playback:  pb,
		Stream:    stream,
		KeyHex:    keyHex,
		Quality:   preferQuality,
	})
	if err != nil {
		return PrepareResult{}, err
	}

	seriesID, number, duration := m.locate(ctx, episodeID, pb)
	ses := &session{
		episodeID:   episodeID,
		seriesID:    seriesID,
		number:      number,
		durationSec: duration,
		playback:    pb,
		resolvedAt:  m.now(),
		quality:     preferQuality,
		mode:        rendered.Mode,
		source:      rendered.Source,
	}

	m.mu.Lock()
	m.cur = ses
	m.unavailable = 0
	m.mu.Unlock()

	res := PrepareResult{
		Source:        rendered.Source,
		Qualities:     sdkx.ToPlaybackInfo(pb, m.pool.Keys().Has(episodeID)).Streams,
		Info:          sdkx.ToPlaybackInfo(pb, m.pool.Keys().Has(episodeID)),
		State:         StatePlaying,
		EpisodeID:     episodeID,
		SeriesID:      seriesID,
		EpisodeNumber: number,
		DurationSec:   duration,
		ResumeSec:     m.resumePoint(ctx, seriesID, episodeID, duration),
		Mode:          rendered.Mode,
	}
	m.setState(StatePlaying, episodeID)
	return res, nil
}

// SwitchQuality 在同一份未过期的播放模型内换清晰度（0 额度，方案 M6 / §8.3）。
//
// 模型已过期时按 §7.5 重新解析（1 额度）：同一集 5 分钟内最多 2 次，超出即报错。
func (m *Machine) SwitchQuality(ctx context.Context, quality string) (model.SourceDTO, error) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur == nil {
		f := fail(CodePlaybackModelAbsent, "当前没有正在播放的分集，请先生成播放源", false, nil)
		m.emit("player:error", *f)
		return model.SourceDTO{}, f
	}
	episodeID := cur.episodeID
	if m.modelExpired(cur) {
		if err := m.noteReResolve(episodeID); err != nil {
			m.emit("player:error", Describe(err))
			return model.SourceDTO{}, err
		}
		res, err := m.Prepare(ctx, episodeID, quality)
		if err != nil {
			return model.SourceDTO{}, err
		}
		return res.Source, nil
	}

	stream, err := cur.playback.Select(quality)
	if err != nil {
		f := fail(CodeQualityUnavailable, "该清晰度不可用："+err.Error(), false, err)
		m.emit("player:error", *f)
		return model.SourceDTO{}, f
	}
	keyHex, _ := m.pool.Key(episodeID)
	rendered, rerr := m.pipe.Render(ctx, RenderRequest{
		EpisodeID: episodeID,
		Playback:  cur.playback,
		Stream:    stream,
		KeyHex:    keyHex,
		Quality:   quality,
	})
	if rerr != nil {
		f := Describe(rerr)
		m.emit("player:error", f)
		return model.SourceDTO{}, &f
	}
	m.mu.Lock()
	if m.cur != nil && m.cur.episodeID == episodeID {
		m.cur.quality = quality
		m.cur.mode = rendered.Mode
		m.cur.source = rendered.Source
	}
	m.mu.Unlock()
	return rendered.Source, nil
}

// Next 返回同一部剧的下一集（跳过 Playable=false 的集，方案 §7.1）。
//
// 只做导航，不解析；解析由前端随后调 Prepare（或 Ended 内部自动做）。
func (m *Machine) Next(ctx context.Context) (string, error) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur == nil || cur.seriesID == "" {
		return "", ErrNoNextEpisode
	}
	eps, err := m.catalog.Episodes(ctx, cur.seriesID)
	if err != nil {
		return "", err
	}
	if next, ok := nextPlayable(eps, cur.number); ok {
		return next, nil
	}
	return "", ErrNoNextEpisode
}

// Prev 返回同一部剧的上一集（跳过 Playable=false 的集）。
func (m *Machine) Prev(ctx context.Context) (string, error) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur == nil || cur.seriesID == "" {
		return "", ErrNoPrevEpisode
	}
	eps, err := m.catalog.Episodes(ctx, cur.seriesID)
	if err != nil {
		return "", err
	}
	if prev, ok := prevPlayable(eps, cur.number); ok {
		return prev, nil
	}
	return "", ErrNoPrevEpisode
}

// ReportProgress 记录播放进度（方案 §5.4：5 秒节流落盘）。
//
// 暂停/切集/退出时调用 FlushProgress 强制落盘。
func (m *Machine) ReportProgress(ctx context.Context, episodeID string, positionSec float64) error {
	if positionSec < 0 {
		positionSec = 0
	}
	if err := m.stageProgress(episodeID, positionSec, false); err != nil {
		return err
	}
	m.emitProgress(episodeID, positionSec)
	m.maybePrefetch(episodeID, positionSec)
	return m.flush(ctx, false)
}

// FlushProgress 强制把当前断点落盘（暂停、切集、退出时调用，方案 §5.4）。
func (m *Machine) FlushProgress(ctx context.Context) error {
	return m.flush(ctx, true)
}

// StageProgress 把一次进度写进待落盘表但不落盘（供 FlushProgress 配合使用）。
func (m *Machine) StageProgress(ctx context.Context, episodeID string, positionSec float64) error {
	if positionSec < 0 {
		positionSec = 0
	}
	return m.stageProgress(episodeID, positionSec, false)
}

// EndedAt 按前端上报的位置收尾本集（方案 §6.2）。
//
// episodeID 必须是前端「真正播完的那一集」：路由切走后的迟到 ended、
// 换清晰度触发的 video.load() 在末尾再触发的 ended，都会带着旧集的 ID 进来。
// 只有它与当前会话一致时才推进连播，否则只记该集断点——否则会把下一集
// 记成已看完，续播整集被跳过。
//
// 与 Ended 的区别还在「先按真实位置记一次已看完的断点」，不经过 ReportProgress，
// 因此不会触发预取（避免与随后的连播解析并发解析同一集、白烧一次额度）。
func (m *Machine) EndedAt(ctx context.Context, episodeID string, positionSec float64) (EndedResult, error) {
	if positionSec < 0 {
		positionSec = 0
	}
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if episodeID == "" && cur != nil {
		episodeID = cur.episodeID
	}
	if cur == nil || episodeID == "" || cur.episodeID != episodeID {
		// 迟到 / 串台的 ended：只落该集断点（positionSec<=0 视为未知，什么都不写），
		// 不动当前会话，也不判完成。
		if episodeID != "" && positionSec > 0 {
			_ = m.stageProgress(episodeID, positionSec, false)
		}
		m.logf("player: ignore stale ended for %s (current=%s)", episodeID, m.CurrentEpisode())
		return EndedResult{State: m.State(), Stale: true}, nil
	}
	// 本集真播完：按上报位置强制落 finished。positionSec<=0 表示位置未知，
	// 不许拿 cur.durationSec 冒充——宁可这次不写 finished，也不写错集/错进度。
	if positionSec > 0 {
		if err := m.stageProgress(episodeID, positionSec, true); err != nil {
			m.logf("player: stage finished history %s: %v", episodeID, err)
		}
	}
	return m.Ended(ctx)
}

// Ended 处理「一集播完」（方案 §6.2 / §10.2）：
//
//  1. 断点以 finished=true 强制落盘；
//  2. 状态进 ENDED；
//  3. single 模式停在这里；sequential 模式找下一集：
//     开场自动开且还有下一集 → 自动解析（缓存命中 0 额度），连续第 3 集不可用即中止连播。
//
// 剧终接队列下一部（series_chained）本期未实现（store 没有队列方法），见包注释与交付报告。
func (m *Machine) Ended(ctx context.Context) (EndedResult, error) {
	m.mu.Lock()
	cur := m.cur
	autoNext := m.autoNext()
	mode := m.mode
	m.mu.Unlock()
	if cur == nil {
		return EndedResult{State: m.State()}, nil
	}
	// 断点（finished=true）由 EndedAt 按真实位置写入；这里只负责状态与连播，
	// 不再用 cur.durationSec 覆盖一次（那会掩盖真实进度）。
	if err := m.flush(ctx, true); err != nil {
		m.logf("player: flush finished history: %v", err)
	}
	m.setState(StateEnded, cur.episodeID)

	next, hasNext, err := m.nextInSeries(ctx, cur)
	if err != nil {
		f := Describe(err)
		m.emit("player:error", f)
		return EndedResult{State: m.State(), SeriesFinished: true}, &f
	}
	if !hasNext {
		m.setState(StateIdle, "")
		return EndedResult{State: StateIdle, HasNext: false, SeriesFinished: true}, nil
	}
	if mode == ModeSingle || !autoNext {
		return EndedResult{State: StateEnded, HasNext: true, NextEpisodeID: next}, nil
	}

	// 连播：准备下一集，不可用就往后跳，连续第 3 集不可用即中止（方案 §6.3）。
	skipped := 0
	for hasNext {
		m.setState(StateNext, next)
		res, perr := m.Prepare(ctx, next, cur.quality)
		if perr == nil {
			return EndedResult{
				State:         StatePlaying,
				HasNext:       true,
				NextEpisodeID: next,
				Prepared:      &res,
				Skipped:       skipped,
			}, nil
		}
		f := Describe(perr)
		if !skippable(f) {
			return EndedResult{State: StateError, HasNext: false, Skipped: skipped}, &f
		}
		skipped++
		m.mu.Lock()
		m.unavailable++
		streak := m.unavailable
		m.mu.Unlock()
		if streak >= maxUnavailable {
			aborted := fail(CodeChainAborted, fmt.Sprintf("连续 %d 集不可用，已停止连播", streak), false, perr)
			m.setState(StateError, next)
			m.emit("player:error", *aborted)
			return EndedResult{State: StateError, HasNext: false, Skipped: skipped}, aborted
		}
		m.logf("player: skip unavailable episode %s (%s), streak=%d", next, f.Code, streak)
		cur = &session{seriesID: cur.seriesID, number: m.numberOf(ctx, cur.seriesID, next), quality: cur.quality, durationSec: cur.durationSec}
		var more bool
		next, more, err = m.nextAfter(ctx, cur.seriesID, cur.number)
		if err != nil {
			f := Describe(err)
			m.emit("player:error", f)
			return EndedResult{State: StateError, HasNext: false, Skipped: skipped}, &f
		}
		hasNext = more
	}
	// 跳完了整部剧。
	m.setState(StateIdle, "")
	return EndedResult{State: StateIdle, HasNext: false, SeriesFinished: true, Skipped: skipped}, nil
}

// Close 释放播放器资源：取消预取、落盘断点、关闭媒体管道。
func (m *Machine) Close(ctx context.Context) error {
	var errs []error
	m.closeOnce.Do(func() {
		m.baseCancel()
	})
	m.prefetchWG.Wait()
	if err := m.flush(ctx, true); err != nil {
		errs = append(errs, err)
	}
	if err := m.pipe.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
