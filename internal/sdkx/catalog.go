package sdkx

import (
	"context"

	"github.com/beilunyang/openhongguo/sdk"
	sdkmodel "github.com/beilunyang/openhongguo/sdk/model"
)

// SearchOptions 是搜索的调用参数。零值即「综合」tab、第一页。
type SearchOptions struct {
	// Cursor 是上一页返回的游标；空串即第一页。
	Cursor string
	// Tab 取综合 / 剧集 / 视频 / 讨论 / 小说 / 用户 / 听书，空串即综合。
	Tab string
}

// DiscoverOptions 是书城频道的调用参数。零值即「推荐」第一页。
type DiscoverOptions struct {
	Cursor  string
	Channel string
}

// 目录类接口：全部 0 额度（方案 §4.5），但可能因 ErrDeviceRejected 触发换档（§4.4 M1）。
// 返回值是 SDK 模型；转 DTO 用 convert.go 里的 ToXxx。
// 分页三元组统一是 (items, cursor, hasMore)。

// Search 搜索短剧。
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) ([]sdkmodel.SeriesSummary, string, bool, error) {
	page, err := c.sdk.Search(ctx, query, sdk.SearchOptions{Cursor: opts.Cursor, Tab: opts.Tab})
	if err != nil {
		return nil, "", false, c.noteDeviceRejected(err)
	}
	if page == nil {
		return nil, "", false, nil
	}
	return page.Items, page.Cursor, page.HasMore, nil
}

// Discover 读书城的一个频道（空频道即推荐）。
func (c *Client) Discover(ctx context.Context, opts DiscoverOptions) ([]sdkmodel.SeriesSummary, string, bool, error) {
	page, err := c.sdk.Discover(ctx, sdk.DiscoverOptions{Cursor: opts.Cursor, Channel: opts.Channel})
	if err != nil {
		return nil, "", false, c.noteDeviceRejected(err)
	}
	if page == nil {
		return nil, "", false, nil
	}
	return page.Items, page.Cursor, page.HasMore, nil
}

// GetSeries 读一部短剧的详情。非视频内容用 Series.Platform==0 判定（F13）。
func (c *Client) GetSeries(ctx context.Context, seriesID string) (*sdkmodel.Series, error) {
	series, err := c.sdk.GetSeries(ctx, seriesID)
	return series, c.noteDeviceRejected(err)
}

// ListEpisodes 读一部短剧的全部分集。
//
// 没有分页：SDK 的 Cursor/HasMore 恒空（F8），故本方法不暴露分页参数。
// 空列表返回 ErrAnonymousLimited，与「被限流」不可区分，不能当作配额耗尽提示（§7.7）。
func (c *Client) ListEpisodes(ctx context.Context, seriesID string) ([]sdkmodel.Episode, error) {
	page, err := c.sdk.ListEpisodes(ctx, seriesID, sdk.EpisodeOptions{})
	if err != nil {
		return nil, c.noteDeviceRejected(err)
	}
	if page == nil {
		return nil, nil
	}
	return page.Items, nil
}

// Seasons 读一部剧的多季合集。内部三级回退（F10）；匿名读不到合集时
// 表现为 ErrContentUnavailable（M3），UI 必须容忍「只拿到 1 季」。
func (c *Client) Seasons(ctx context.Context, seriesID string) (*sdkmodel.Album, error) {
	album, err := c.sdk.Seasons(ctx, seriesID)
	return album, c.noteDeviceRejected(err)
}

// SeriesRecommend 读「相关推荐」。匿名结果是通用单元，不随该剧而定（F11），
// 只作展示，不用于自动播放。
func (c *Client) SeriesRecommend(ctx context.Context, seriesID string) (*sdkmodel.Recommend, error) {
	rec, err := c.sdk.SeriesRecommend(ctx, seriesID)
	return rec, c.noteDeviceRejected(err)
}
