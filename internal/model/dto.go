// Package model 定义跨绑定边界的数据传输对象（DTO），字段与方案 §5.1 对齐。
//
// 硬红线（方案 §2.2 I2）：本包任何结构都不得含内容密钥、KeyMaterial、
// 或任何可推导密钥的字段。内容密钥只存在于 internal/devices.Keyring 的
// 进程内存中，进程退出即失，不进 DTO、不进 SQLite、不进日志、不进事件载荷。
//
// 本包不 import 任何第三方包：DTO 可被 Wails 绑定层直接序列化给前端。
package model

// SeriesDTO 是一部短剧的展示信息（方案 §5.1）。
//
// Status / PayType 用字符串而非 SDK 的枚举整数，便于前端直接消费；
// 反向映射见 enums.go。
type SeriesDTO struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CoverURL     string `json:"coverUrl,omitempty"`
	EpisodeCount int    `json:"episodeCount"`
	// Description 是上游的简介原文（书城/搜索列表就带）。只用于瀑布流卡片摘要；
	// store 不落库，缓存读回时为空。
	Description string `json:"description,omitempty"`
	// Status 取 updating | finished | updated_today | stopped | unknown。
	Status string `json:"status"`
	// PayType 取 free | vip | single | unknown。
	PayType string `json:"payType"`
	// Platform 是 SDK 的 video_platform 原值：0 表示书 / 有声书等非视频内容，
	// 播放器不支持（方案 F13）；-1 表示只有列表数据、还没取详情，平台未知。
	// §5.1 未列此字段，但 §7.7 的拦截依赖它。
	Platform      int    `json:"platform"`
	RightText     string `json:"rightText,omitempty"`
	IsMultiSeason bool   `json:"isMultiSeason"`
	SeasonIndex   int    `json:"seasonIndex,omitempty"`
	AlbumID       string `json:"albumId,omitempty"`
	// FetchedAt 是这条记录从服务端取回的时间（unix 秒）。
	FetchedAt int64 `json:"fetchedAt"`
}

// EpisodeDTO 是一集（方案 §5.1）。
//
// DurationSec 由调用方从 SDK 的 Episode.Duration(time.Duration, json:"-") 转换而来，
// 该字段不跨 SDK 的 JSON 边界（方案 F12）。
type EpisodeDTO struct {
	ID          string `json:"id"`
	SeriesID    string `json:"seriesId"`
	Number      int    `json:"number"`
	Title       string `json:"title,omitempty"`
	DurationSec int    `json:"durationSec,omitempty"`
	Playable    bool   `json:"playable"`
	IsNew       bool   `json:"isNew"`
	CoverURL    string `json:"coverUrl,omitempty"`
}

// SourceDTO 是播放页拿到的可播放源（方案 §5.1）。
//
// Mode 只有两种取值：
//   - "direct"：未加密且编码受支持时的 CDN 直链，ExpiresAt 有值；
//   - "local"：本机 127.0.0.1 带 token 的地址，ExpiresAt 恒为 0。
//
// 本结构不含任何密钥；本地源由 internal/localsrv 生成，密钥留在 Go 侧（I2）。
type SourceDTO struct {
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	MIME      string `json:"mime"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

// QueueItemDTO 是播放队列中的一项（方案 §5.4）。
type QueueItemDTO struct {
	Pos         int    `json:"pos"`
	SeriesID    string `json:"seriesId"`
	SeriesTitle string `json:"seriesTitle"`
	EpisodeID   string `json:"episodeId,omitempty"`
	StartAt     int    `json:"startAt"`
}

// HistoryDTO 是一部剧的断点（方案 §5.4：history 主键是 series_id）。
//
// DurationSec 对应 schema 里的 duration_sec 列（§5.3）；§5.1 的 DTO 没列它，
// 但列既然存在就不能丢，故补上，留空为 0。
type HistoryDTO struct {
	SeriesID      string  `json:"seriesId"`
	EpisodeID     string  `json:"episodeId"`
	EpisodeNumber int     `json:"episodeNumber"`
	PositionSec   float64 `json:"positionSec"`
	DurationSec   int     `json:"durationSec,omitempty"`
	Finished      bool    `json:"finished"`
	UpdatedAt     int64   `json:"updatedAt"`
}

// SeriesPageDTO 对应服务方法 Library.Discover / Library.Search 的返回
// {items,cursor,hasMore}（方案 §8.3）。
type SeriesPageDTO struct {
	Items   []SeriesDTO `json:"items"`
	Cursor  string      `json:"cursor,omitempty"`
	HasMore bool        `json:"hasMore"`
}

// SeasonDTO 是合集里的一季（方案 §7.6）。
type SeasonDTO struct {
	SeriesID     string `json:"seriesId"`
	Title        string `json:"title,omitempty"`
	Index        int    `json:"index,omitempty"`
	CoverURL     string `json:"coverUrl,omitempty"`
	EpisodeCount int    `json:"episodeCount,omitempty"`
	Status       string `json:"status,omitempty"`
}

// AlbumDTO 是多季合集（方案 F10 / §7.6）。匿名读取常常只拿到 1 季，
// 前端必须容忍 SeasonCount==1 且 AlbumID 为空时不显示季切换器。
type AlbumDTO struct {
	ID          string      `json:"id"`
	Title       string      `json:"title,omitempty"`
	CoverURL    string      `json:"coverUrl,omitempty"`
	SeasonCount int         `json:"seasonCount"`
	Seasons     []SeasonDTO `json:"seasons"`
}

// EpisodeDiff 是一次「刷新剧集」的增量结果（方案 §7.3）。
//
// Added / Updated / Removed 是分集 ID；IsNew 是需要打「新集」圆点的 ID 集合
// （双保险：服务端 IsNew 为真，或集数大于刷新前的最大集数）。
type EpisodeDiff struct {
	Added   []string `json:"added"`
	Updated []string `json:"updated"`
	Removed []string `json:"removed"`
	IsNew   []string `json:"isNew"`
}
