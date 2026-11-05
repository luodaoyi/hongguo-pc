// Package bindings 是 Wails v3 服务层（方案 §8.3 / §9.4）。
//
// 边界（contracts.md §1）：本包只做 DTO 转换、服务方法与事件发射。
// **不得碰密钥**：不 import devices.Keyring、不读 Playback.KeyMaterial、
// 不把 KeyHex 放进任何返回值或事件载荷。所有播放源以 model.SourceDTO 交付，
// 密钥留在 Go 侧（I2）。
//
// 错误统一为 {code,message,retryable}：见 MarshalError，挂在 ServiceOptions 上，
// 前端拿到的每个 rejected promise 都是这个形状。
package bindings

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/library"
	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/player"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

// 事件名（方案 §8.4）。前端按这些名字监听；载荷形状见各调用点的注释。
//
//	player:state     {state, episodeId}
//	player:error     {code, message, retryable, episodeId}
//	player:progress  {episodeId, positionSec, durationSec}
//	episode:updated  {seriesId, added, updated, removed}
//	quota:changed    {healthy, usedToday}
//	quota:exhausted  {}
//	media:preparing  {episodeId, mode, stage}
//	devices:changed  {slots:[{id,state,...}]}
//
// 刻意不调用 application.RegisterEvent：本包的事件载荷是 map[string]any，
// 而 RegisterEvent 会按注册的 Go 类型做精确校验（类型不等即丢弃事件）。
// 默认构建下未注册只是无操作（events_loose.go），不产生日志噪音。
const (
	EventPlayerState    = "player:state"
	EventPlayerError    = "player:error"
	EventPlayerProgress = "player:progress"
	EventEpisodeUpdated = "episode:updated"
	EventQuotaChanged   = "quota:changed"
	EventQuotaExhausted = "quota:exhausted"
	EventMediaPreparing = "media:preparing"
	EventDevicesChanged = "devices:changed"
)

// ErrorDTO 是前端拿到的统一错误形状（方案 §8.3）。
type ErrorDTO struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	EpisodeID string `json:"episodeId,omitempty"`
}

// MarshalError 把任意错误转成 {code,message,retryable} 的 JSON。
//
// 挂到 application.ServiceOptions.MarshalError 上，服务方法的 error 返回值
// 就以这个形状到达前端。绝不返回底层错误串（可能含带签名的地址）。
func MarshalError(err error) []byte {
	if err == nil {
		return nil
	}
	f := player.Describe(err)
	payload, marshalErr := json.Marshal(ErrorDTO{
		Code:      f.Code,
		Message:   f.Message,
		Retryable: f.Retryable,
		EpisodeID: f.EpisodeID,
	})
	if marshalErr != nil {
		return nil
	}
	return payload
}

// emitEvent 把事件发给前端（方案 §8.4 的 app.Event.Emit 落点）。
//
// 用 application.Get() 取全局 App：服务在 application.New 之前构造，
// 事件都在 Run 之后发出；App 尚未就绪时静默丢弃（启动期没有窗口可收）。
func emitEvent(name string, payload any) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(name, payload)
}

// ErrorEntry 是诊断页的一行（方案 §9.6 / §8.3 Diag.RecentErrors）。
type ErrorEntry struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	At   int64  `json:"at"`
}

// errorRing 是最近 N 条错误码的内存环形缓冲（方案 §8.1 /debug 页）。
type errorRing struct {
	mu      sync.Mutex
	size    int
	entries []ErrorEntry
	next    int
	filled  bool
}

// newErrorRing 建一个容量为 size 的环形缓冲。
func newErrorRing(size int) *errorRing {
	if size <= 0 {
		size = 50
	}
	return &errorRing{size: size, entries: make([]ErrorEntry, size)}
}

// add 记一条错误；不记录原始错误串，只记归一后的码与用户文案。
func (r *errorRing) add(code, msg string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = ErrorEntry{Code: code, Msg: msg, At: time.Now().Unix()}
	r.next = (r.next + 1) % r.size
	if r.next == 0 {
		r.filled = true
	}
}

// list 按时间顺序返回已记录的错误（最近的在后）。
func (r *errorRing) list() []ErrorEntry {
	if r == nil {
		return []ErrorEntry{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ErrorEntry, 0, r.size)
	if r.filled {
		out = append(out, r.entries[r.next:]...)
		out = append(out, r.entries[:r.next]...)
		return out
	}
	return append(out, r.entries[:r.next]...)
}

// Core 是服务层共享的依赖容器。由 cmd/app 装配。
type Core struct {
	Pool     *devices.Pool
	Store    store.Store
	Library  *library.Service
	Machine  *player.Machine
	Settings *SettingsStore
	Errors   *errorRing
	// DataDir 是数据目录（%LOCALAPPDATA%\<app>），仅用于展示与排障。
	DataDir string
	// Logf 是诊断日志，默认丢弃。
	Logf func(format string, args ...any)
}

// CoreOptions 是 NewCore 的入参。除 DataDir/Logf 外都必填。
type CoreOptions struct {
	Pool     *devices.Pool
	Store    store.Store
	Library  *library.Service
	Machine  *player.Machine
	Settings *SettingsStore
	DataDir  string
	Logf     func(format string, args ...any)
}

// NewCore 装配服务层共享依赖。
func NewCore(opts CoreOptions) (*Core, error) {
	if opts.Pool == nil {
		return nil, fmt.Errorf("bindings: device pool is required")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("bindings: store is required")
	}
	if opts.Library == nil {
		return nil, fmt.Errorf("bindings: library service is required")
	}
	if opts.Machine == nil {
		return nil, fmt.Errorf("bindings: player machine is required")
	}
	if opts.Settings == nil {
		return nil, fmt.Errorf("bindings: settings store is required")
	}
	return &Core{
		Pool:     opts.Pool,
		Store:    opts.Store,
		Library:  opts.Library,
		Machine:  opts.Machine,
		Settings: opts.Settings,
		Errors:   newErrorRing(50),
		DataDir:  opts.DataDir,
		Logf:     opts.Logf,
	}, nil
}

// Emit 把事件发给前端（供 cmd/app 接线 player/library 的 Emit 参数）。
func Emit(name string, payload any) {
	emitEvent(name, payload)
}

// EmitEpisodeUpdated 是 library 增量刷新的回调落点（方案 §7.3 的 emit）。
func EmitEpisodeUpdated(seriesID string, added, updated, removed int) {
	emitEvent(EventEpisodeUpdated, map[string]any{
		"seriesId": seriesID,
		"added":    added,
		"updated":  updated,
		"removed":  removed,
	})
}

// record 把一次失败记进诊断环形缓冲。
func (c *Core) record(err error) {
	if c == nil || err == nil || c.Errors == nil {
		return
	}
	f := player.Describe(err)
	c.Errors.add(f.Code, f.Message)
}

func (c *Core) logf(format string, args ...any) {
	if c == nil || c.Logf == nil {
		return
	}
	c.Logf(format, args...)
}

// Library 是目录服务（方案 §8.3 的 Library.*）。
type Library struct {
	Core *Core
}

// Discover 读书城频道，返回 {items,cursor,hasMore}（0 额度）。
func (l *Library) Discover(ctx context.Context, channel, cursor string) (model.SeriesPageDTO, error) {
	page, err := l.Core.Library.Discover(ctx, channel, cursor)
	if err != nil {
		l.Core.record(err)
		return model.SeriesPageDTO{}, err
	}
	return page, nil
}

// Search 搜索短剧（0 额度）。
func (l *Library) Search(ctx context.Context, query, cursor string) (model.SeriesPageDTO, error) {
	page, err := l.Core.Library.Search(ctx, query, cursor)
	if err != nil {
		l.Core.record(err)
		return model.SeriesPageDTO{}, err
	}
	return page, nil
}

// GetSeries 读详情（0 额度）。Platform==0 是非视频内容，前端据此拦截（方案 §7.7）。
func (l *Library) GetSeries(ctx context.Context, id string) (model.SeriesDTO, error) {
	dto, err := l.Core.Library.GetSeries(ctx, id)
	if err != nil {
		l.Core.record(err)
		return model.SeriesDTO{}, err
	}
	return dto, nil
}

// ListEpisodes 返回一部剧的全部分集（全量，无分页；0 额度）。
func (l *Library) ListEpisodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error) {
	eps, err := l.Core.Library.ListEpisodes(ctx, seriesID)
	if err != nil {
		l.Core.record(err)
		return nil, err
	}
	return eps, nil
}

// Seasons 返回多季合集；读不到合集时返回单季（不报错，方案 §7.6）。
func (l *Library) Seasons(ctx context.Context, seriesID string) (model.AlbumDTO, error) {
	album, err := l.Core.Library.Seasons(ctx, seriesID)
	if err != nil {
		l.Core.record(err)
		return model.AlbumDTO{}, err
	}
	return album, nil
}

// Recommend 返回相关推荐（仅展示，不自动播放，方案 F11）。
func (l *Library) Recommend(ctx context.Context, seriesID string) ([]model.SeriesDTO, error) {
	items, err := l.Core.Library.Recommend(ctx, seriesID)
	if err != nil {
		l.Core.record(err)
		return nil, err
	}
	return items, nil
}

// EpisodeDiffDTO 是「刷新剧集」的返回（方案 §7.3）。
type EpisodeDiffDTO struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
}

// RefreshEpisodes 全量拉取并做增量合并（按 id 合并、number 排序、下架软删），
// 完成后发 episode:updated 事件（回调挂在 library.Options.OnEpisodesRefreshed）。
func (l *Library) RefreshEpisodes(ctx context.Context, seriesID string) (EpisodeDiffDTO, error) {
	added, updated, removed, err := l.Core.Library.RefreshEpisodes(ctx, seriesID)
	if err != nil {
		l.Core.record(err)
		return EpisodeDiffDTO{}, err
	}
	return EpisodeDiffDTO{Added: added, Updated: updated, Removed: removed}, nil
}
