package sdkx

import (
	"time"

	sdkmodel "github.com/beilunyang/openhongguo/sdk/model"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// SDK 模型 → DTO 的转换集中在本文件，上层与绑定层只见 DTO（方案 §2.1 L6）。
// 转换必须显式写全字段：SDK 的模型会随版本变，编译器会替我们盯着。

// PlatformUnknownSummary 用在只拿到 SeriesSummary 的场景：此时不知道 platform，
// 用 -1 与「0 = 非视频内容」（F13）区分开。
const PlatformUnknownSummary = -1

// ToSeriesDTO 把搜索 / 书城的一行（SeriesSummary）转成 DTO。
//
// 列表页没有 status / pay_type / platform 字段，状态填 unknown、platform 填 -1；
// 需要判定非视频内容时先 GetSeries 取详情。
func ToSeriesDTO(s sdkmodel.SeriesSummary, fetchedAt int64) model.SeriesDTO {
	return model.SeriesDTO{
		ID:           s.ID,
		Title:        s.Title,
		CoverURL:     s.CoverURL,
		EpisodeCount: s.EpisodeCount,
		Status:       model.StatusUnknown,
		PayType:      model.PayTypeUnknown,
		Platform:     PlatformUnknownSummary,
		FetchedAt:    fetchedAt,
	}
}

// ToSeriesDTOs 批量转换列表行。
func ToSeriesDTOs(items []sdkmodel.SeriesSummary, fetchedAt int64) []model.SeriesDTO {
	out := make([]model.SeriesDTO, 0, len(items))
	for _, s := range items {
		out = append(out, ToSeriesDTO(s, fetchedAt))
	}
	return out
}

// ToSeriesDetailDTO 把详情（Series 内嵌 SeriesSummary）转成 DTO。
// AlbumID 取 SDK 的 RelatedAlbumID（各季共用的合集 id）。
func ToSeriesDetailDTO(s *sdkmodel.Series, fetchedAt int64) model.SeriesDTO {
	if s == nil {
		return model.SeriesDTO{Status: model.StatusUnknown, PayType: model.PayTypeUnknown, Platform: PlatformUnknownSummary, FetchedAt: fetchedAt}
	}
	dto := ToSeriesDTO(s.SeriesSummary, fetchedAt)
	dto.Status = s.Status.String()
	dto.PayType = s.PayType.String()
	dto.Platform = int(s.Platform)
	dto.RightText = s.RightText
	dto.IsMultiSeason = s.IsMultiSeason
	dto.SeasonIndex = s.SeasonIndex
	dto.AlbumID = s.RelatedAlbumID
	return dto
}

// ToEpisodeDTO 把一集转成 DTO。DurationSec 由 SDK 的 time.Duration 换算，
// 因为后者是 json:"-"，不跨 JSON 边界（F12）。
func ToEpisodeDTO(e sdkmodel.Episode) model.EpisodeDTO {
	return model.EpisodeDTO{
		ID:          e.ID,
		SeriesID:    e.SeriesID,
		Number:      e.Number,
		Title:       e.Title,
		DurationSec: int(e.Duration / time.Second),
		Playable:    e.Playable,
		IsNew:       e.IsNew,
		CoverURL:    e.CoverURL,
	}
}

// ToEpisodeDTOs 批量转换分集。
func ToEpisodeDTOs(eps []sdkmodel.Episode) []model.EpisodeDTO {
	out := make([]model.EpisodeDTO, 0, len(eps))
	for _, e := range eps {
		out = append(out, ToEpisodeDTO(e))
	}
	return out
}

// ToAlbumDTO 把合集转成 DTO（方案 §7.6）。
func ToAlbumDTO(a *sdkmodel.Album) model.AlbumDTO {
	if a == nil {
		return model.AlbumDTO{Seasons: []model.SeasonDTO{}}
	}
	seasons := make([]model.SeasonDTO, 0, len(a.Seasons))
	for _, s := range a.Seasons {
		seasons = append(seasons, model.SeasonDTO{
			SeriesID:     s.SeriesID,
			Title:        s.Title,
			Index:        s.Index,
			CoverURL:     s.CoverURL,
			EpisodeCount: s.EpisodeCount,
			Status:       s.Status.String(),
		})
	}
	return model.AlbumDTO{
		ID:          a.ID,
		Title:       a.Title,
		CoverURL:    a.CoverURL,
		SeasonCount: a.SeasonCount,
		Seasons:     seasons,
	}
}

// ToPlaybackInfo 把一个播放模型压成可公开的摘要。
//
// 只搬清晰度标签、codec、宽高、码率、是否加密、有效期与是否有独立音轨。
// **URL 与密钥材料一律不搬**：URL 只交给媒体层（经 Playback 原对象），
// 密钥只在 Keyring 里（I2）。keyInMemory 只是布尔标记，不含密钥值。
func ToPlaybackInfo(pb *sdkmodel.Playback, keyInMemory bool) model.PlaybackInfo {
	if pb == nil {
		return model.PlaybackInfo{KeyInMemory: keyInMemory}
	}
	streams := make([]model.StreamInfo, 0, len(pb.Streams))
	for _, s := range pb.Streams {
		streams = append(streams, model.StreamInfo{
			Definition: s.Definition,
			Quality:    s.Quality,
			Codec:      s.Codec,
			Width:      s.Width,
			Height:     s.Height,
			Bitrate:    s.Bitrate,
			HLS:        s.HLS,
			Dash:       s.Dash,
			Protected:  s.Protected,
			Encryption: s.Encryption,
		})
	}
	return model.PlaybackInfo{
		EpisodeID:   pb.EpisodeID,
		VideoID:     pb.VideoID,
		MediaType:   pb.MediaType,
		DurationSec: pb.DurationSeconds,
		Protected:   pb.Protected,
		ExpiresAt:   pb.ExpiresAt,
		HasAudio:    pb.AudioURL != "",
		KeyInMemory: keyInMemory,
		Streams:     streams,
	}
}
