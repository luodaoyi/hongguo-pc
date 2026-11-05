// Package library 是目录层（方案 §7）：书城、搜索、详情、分集、多季、相关推荐。
//
// 硬规矩（方案 §2.2 I3）：本包**不得**直连 sdkx.Client，所有网络调用一律走
// devices.Pool.Do（0 额度，但仍经设备池记账——非播放换档同样烧设备）。
// 播放解析不在这里，唯一入口是 devices.Pool.ResolvePlayback（player 层用）。
//
// 分页（方案 §7.2 / F8）：ListEpisodes 没有游标分页，一次全量返回；
// 集数以 Series.EpisodeCount 为准。本包不提供「加载更多」。
package library

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

// DefaultRefreshDebounce 是同一部剧两次自动刷新之间的最小间隔（方案 §7.4 的 30 秒去抖）。
const DefaultRefreshDebounce = 30 * time.Second

// Options 是构造目录服务的参数。Pool 与 Store 必填。
type Options struct {
	// Pool 是设备池：所有目录调用都经 Pool.Do 取槽，0 额度但记账。
	Pool *devices.Pool
	// Store 是本地缓存：详情、分集落库，断网时仍能读分集。
	Store store.Store
	// Now 可选：注入时钟，默认 time.Now。
	Now func() time.Time
	// Logf 可选：诊断日志，默认丢弃。**不得**在里面打印设备档案或密钥。
	Logf func(format string, args ...any)
	// OnEpisodesRefreshed 可选：一次增量刷新完成后的回调，bindings 用它发
	// episode:updated 事件（方案 §7.3 的 emit 落点）。
	OnEpisodesRefreshed func(seriesID string, added, updated, removed int)
	// CoverProxy 可选：把封面 URL 改写成本地代理（上游书城封面是 HEIC，
	// WebView2 解不了，需经 localsrv 转 JPEG；见 localsrv.ImageURL）。
	// nil = 原样返回。store 里落库的仍是原始 URL，改写只发生在读出时。
	CoverProxy func(url string) string
}

// Service 是目录层服务。并发安全。
type Service struct {
	pool       *devices.Pool
	st         store.Store
	now        func() time.Time
	logf       func(string, ...any)
	onRefresh  func(seriesID string, added, updated, removed int)
	coverProxy func(url string) string

	mu sync.Mutex
	// lastRefresh 记每部剧的上次刷新时间与结果，用于 30 秒去抖（方案 §7.4）：
	// 窗口内重复点「刷新剧集」直接返回上次结果，不再打网络。
	lastRefresh map[string]time.Time
	lastDiff    map[string][3]int
	// epSeries 是 episodeID → seriesID 的内存索引。
	//
	// store 没有「按分集反查剧」的方法（不属本期 store 接口），而 player 的
	// Prepare(episodeID) 只拿得到分集 ID，写 history 与连播都需要剧 ID。
	// 索引在每次读/刷新分集时填充；进程重启后由前端打开详情页时重新填充。
	epSeries map[string]string
}

// New 构造目录服务。
func New(opts Options) (*Service, error) {
	if opts.Pool == nil {
		return nil, fmt.Errorf("library: device pool is required")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("library: store is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{
		pool:        opts.Pool,
		st:          opts.Store,
		now:         now,
		logf:        logf,
		onRefresh:   opts.OnEpisodesRefreshed,
		coverProxy:  opts.CoverProxy,
		lastRefresh: make(map[string]time.Time, 16),
		lastDiff:    make(map[string][3]int, 16),
		epSeries:    make(map[string]string, 256),
	}, nil
}

// proxyCover 统一改写封面地址：nil 时原样返回。store 落库原始 URL，
// 改写只发生在 DTO 读出时，签名失效后重拉数据即可拿到新签名。
func (s *Service) proxyCover(u string) string {
	if s.coverProxy == nil {
		return u
	}
	return s.coverProxy(u)
}

// proxySeriesCovers 就地改写一批剧的封面。
func (s *Service) proxySeriesCovers(items []model.SeriesDTO) {
	for i := range items {
		items[i].CoverURL = s.proxyCover(items[i].CoverURL)
	}
}

// Discover 读书城的一个频道（空频道即推荐），0 额度（方案 §8.3 Library.Discover）。
func (s *Service) Discover(ctx context.Context, channel, cursor string) (model.SeriesPageDTO, error) {
	var page model.SeriesPageDTO
	err := s.pool.DoReadOnly(ctx, "discover", func(ctx context.Context, c *sdkx.Client) error {
		items, next, hasMore, err := c.Discover(ctx, sdkx.DiscoverOptions{Channel: channel, Cursor: cursor})
		if err != nil {
			return err
		}
		page = model.SeriesPageDTO{
			Items:   sdkx.ToSeriesDTOs(items, s.now().Unix()),
			Cursor:  next,
			HasMore: hasMore,
		}
		s.proxySeriesCovers(page.Items)
		return nil
	})
	if err != nil {
		return model.SeriesPageDTO{}, err
	}
	return page, nil
}

// Search 搜索短剧，0 额度（方案 §8.3 Library.Search）。
func (s *Service) Search(ctx context.Context, query, cursor string) (model.SeriesPageDTO, error) {
	if strings.TrimSpace(query) == "" {
		return model.SeriesPageDTO{}, fmt.Errorf("library: search query is required")
	}
	var page model.SeriesPageDTO
	err := s.pool.DoReadOnly(ctx, "search", func(ctx context.Context, c *sdkx.Client) error {
		items, next, hasMore, err := c.Search(ctx, query, sdkx.SearchOptions{Cursor: cursor})
		if err != nil {
			return err
		}
		page = model.SeriesPageDTO{
			Items:   sdkx.ToSeriesDTOs(items, s.now().Unix()),
			Cursor:  next,
			HasMore: hasMore,
		}
		s.proxySeriesCovers(page.Items)
		return nil
	})
	if err != nil {
		return model.SeriesPageDTO{}, err
	}
	return page, nil
}

// GetSeries 读一部短剧的详情并写入本地缓存（0 额度）。
//
// Platform==0 表示书 / 有声书等非视频内容（方案 F13 / §7.7），
// 本层照常返回，拦截由界面按 Platform 判定。
func (s *Service) GetSeries(ctx context.Context, seriesID string) (model.SeriesDTO, error) {
	if strings.TrimSpace(seriesID) == "" {
		return model.SeriesDTO{}, fmt.Errorf("library: series id is required")
	}
	var dto model.SeriesDTO
	err := s.pool.DoReadOnly(ctx, "getSeries", func(ctx context.Context, c *sdkx.Client) error {
		detail, err := c.GetSeries(ctx, seriesID)
		if err != nil {
			return err
		}
		dto = sdkx.ToSeriesDetailDTO(detail, s.now().Unix())
		return nil
	})
	if err != nil {
		return model.SeriesDTO{}, err
	}
	dto.CoverURL = s.proxyCover(dto.CoverURL)
	if err := s.st.UpsertSeries(ctx, dto); err != nil {
		// 缓存失败不影响展示，只记日志。
		s.logf("library: cache series %s: %v", seriesID, err)
	}
	return dto, nil
}

// Episodes 读本地缓存的分集（不发请求）。
//
// 没有缓存时返回空切片，不返回错误：调用方（bindings 的 Library.ListEpisodes）
// 用 ListEpisodes 走「空则刷新一次」的路径。
func (s *Service) Episodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, fmt.Errorf("library: series id is required")
	}
	eps, err := s.st.Episodes(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	s.index(seriesID, eps)
	for i := range eps {
		eps[i].CoverURL = s.proxyCover(eps[i].CoverURL)
	}
	return eps, nil
}

// ListEpisodes 返回一部剧的全部分集：优先本地缓存，缓存为空时刷新一次（方案 §7.2）。
//
// 只有缓存为空才会发网络请求；已有缓存时再刷新由 Library.RefreshEpisodes 显式触发
// （方案 §7.4：进播放页/启动各一次，定时与手动另算）。
func (s *Service) ListEpisodes(ctx context.Context, seriesID string) ([]model.EpisodeDTO, error) {
	eps, err := s.Episodes(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if len(eps) > 0 {
		return eps, nil
	}
	if _, _, _, rerr := s.refresh(ctx, seriesID, true); rerr != nil {
		return nil, rerr
	}
	return s.Episodes(ctx, seriesID)
}

// RefreshEpisodes 全量拉取分集并做增量合并（方案 §7.3），返回增删改条数。
//
// 合并键是 id、排序键是 number、下架软删，全部由 store.ReplaceEpisodes 完成；
// 本层只负责取数与事件回调。0 额度。
//
// 30 秒内对同一部剧重复调用直接返回上次的计数，不再打网络（方案 §7.4 的去抖）。
func (s *Service) RefreshEpisodes(ctx context.Context, seriesID string) (added, updated, removed int, err error) {
	return s.refresh(ctx, seriesID, false)
}

// refresh 是刷新的实现。force 为 false 时按 30 秒去抖（方案 §7.4）。
func (s *Service) refresh(ctx context.Context, seriesID string, force bool) (added, updated, removed int, err error) {
	if strings.TrimSpace(seriesID) == "" {
		return 0, 0, 0, fmt.Errorf("library: series id is required")
	}
	if !force {
		s.mu.Lock()
		last, seen := s.lastRefresh[seriesID]
		cached := s.lastDiff[seriesID]
		s.mu.Unlock()
		if seen && s.now().Sub(last) < DefaultRefreshDebounce {
			return cached[0], cached[1], cached[2], nil
		}
	}
	var fresh []model.EpisodeDTO
	if err := s.pool.DoReadOnly(ctx, "listEpisodes", func(ctx context.Context, c *sdkx.Client) error {
		eps, err := c.ListEpisodes(ctx, seriesID)
		if err != nil {
			return err
		}
		fresh = sdkx.ToEpisodeDTOs(eps)
		return nil
	}); err != nil {
		return 0, 0, 0, err
	}
	if len(fresh) == 0 {
		// 空结果在匿名环境里与「被限」不可区分（方案 F8 / §7.7）：
		// 绝不能拿空列表去合并，否则已有分集会被整体软删。
		s.logf("library: refresh %s returned no episodes, keeping cached rows", seriesID)
		return 0, 0, 0, nil
	}
	diff, err := s.st.ReplaceEpisodes(ctx, seriesID, fresh)
	if err != nil {
		return 0, 0, 0, err
	}
	s.index(seriesID, fresh)
	added, updated, removed = len(diff.Added), len(diff.Updated), len(diff.Removed)
	s.mu.Lock()
	s.lastRefresh[seriesID] = s.now()
	s.lastDiff[seriesID] = [3]int{added, updated, removed}
	s.mu.Unlock()

	if s.onRefresh != nil {
		s.onRefresh(seriesID, added, updated, removed)
	}
	s.logf("library: refresh %s: added=%d updated=%d removed=%d", seriesID, added, updated, removed)
	return added, updated, removed, nil
}

// Seasons 读多季合集（方案 §7.6 / F10）。
//
// sdkx.Client.Seasons 内部已做三级回退；仍失败时**只返回单季、不报错**：
// 匿名环境读不到合集是常态，界面必须容忍「只有 1 季且没有 albumID」。
func (s *Service) Seasons(ctx context.Context, seriesID string) (model.AlbumDTO, error) {
	if strings.TrimSpace(seriesID) == "" {
		return model.AlbumDTO{}, fmt.Errorf("library: series id is required")
	}
	var album model.AlbumDTO
	err := s.pool.DoReadOnly(ctx, "seasons", func(ctx context.Context, c *sdkx.Client) error {
		a, err := c.Seasons(ctx, seriesID)
		if err != nil {
			return err
		}
		album = sdkx.ToAlbumDTO(a)
		album.CoverURL = s.proxyCover(album.CoverURL)
		for i := range album.Seasons {
			album.Seasons[i].CoverURL = s.proxyCover(album.Seasons[i].CoverURL)
		}
		return nil
	})
	if err == nil && len(album.Seasons) > 0 {
		return album, nil
	}
	if err != nil {
		code, _, _ := sdkx.Classify(err)
		s.logf("library: seasons %s unavailable (%s), falling back to single season", seriesID, code)
	}
	return s.singleSeason(ctx, seriesID), nil
}

// Recommend 读相关推荐（仅展示，不用于自动播放，方案 F11）。
func (s *Service) Recommend(ctx context.Context, seriesID string) ([]model.SeriesDTO, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, fmt.Errorf("library: series id is required")
	}
	var items []model.SeriesDTO
	err := s.pool.DoReadOnly(ctx, "recommend", func(ctx context.Context, c *sdkx.Client) error {
		rec, err := c.SeriesRecommend(ctx, seriesID)
		if err != nil {
			return err
		}
		if rec == nil {
			items = []model.SeriesDTO{}
			return nil
		}
		items = sdkx.ToSeriesDTOs(rec.Items, s.now().Unix())
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.proxySeriesCovers(items)
	return items, nil
}

// SeriesOfEpisode 返回分集所属的剧 ID。索引未命中时 ok 为 false。
//
// 索引由 Episodes / RefreshEpisodes 填充：界面进入详情页或播放页时都会先列分集，
// 所以正常路径下必命中；进程刚启动就直连播放页时可能未命中，player 层按
// 「剧未知」降级（不写断点、不连播），不当作错误。
func (s *Service) SeriesOfEpisode(ctx context.Context, episodeID string) (string, bool) {
	if episodeID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.epSeries[episodeID]
	return id, ok
}

// singleSeason 在没有合集信息时构造「只有一季」的 AlbumDTO（方案 §7.6）。
func (s *Service) singleSeason(ctx context.Context, seriesID string) model.AlbumDTO {
	season := model.SeasonDTO{SeriesID: seriesID}
	if cached, err := s.st.GetSeries(ctx, seriesID); err == nil && cached != nil {
		season.Title = cached.Title
		season.CoverURL = s.proxyCover(cached.CoverURL)
		season.EpisodeCount = cached.EpisodeCount
		season.Status = cached.Status
		season.Index = cached.SeasonIndex
	}
	return model.AlbumDTO{
		ID:          "",
		SeasonCount: 1,
		Seasons:     []model.SeasonDTO{season},
	}
}

// index 把分集 → 剧的映射写进内存索引。
func (s *Service) index(seriesID string, eps []model.EpisodeDTO) {
	if seriesID == "" || len(eps) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ep := range eps {
		if ep.ID != "" {
			s.epSeries[ep.ID] = seriesID
		}
	}
}
