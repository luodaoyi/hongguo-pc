// 与 internal/model/dto.go 一一对应的前端类型（字段名取 Go 的 json tag）。
//
// 依据：方案 §5.2 与 internal/model/{dto,playback,devices}.go。
// 后端 DTO 加了字段这里要跟着加；不要在前端私有结构里「顺手」改名。

export type SeriesStatus =
  | 'updating'
  | 'finished'
  | 'updated_today'
  | 'stopped'
  | 'unknown';

export type PayType = 'free' | 'vip' | 'single' | 'unknown';

/** model.SeriesDTO */
export interface Series {
  id: string;
  title: string;
  coverUrl?: string;
  episodeCount: number;
  status: SeriesStatus | string;
  payType: PayType | string;
  /** 0 = 书 / 有声书等非视频内容；-1 = 只有列表数据、平台未知 */
  platform: number;
  rightText?: string;
  isMultiSeason: boolean;
  seasonIndex?: number;
  albumId?: string;
  fetchedAt: number;
}

/** model.EpisodeDTO */
export interface Episode {
  id: string;
  seriesId: string;
  number: number;
  title?: string;
  durationSec?: number;
  playable: boolean;
  isNew: boolean;
  coverUrl?: string;
}

/** model.SourceDTO；mode 只有 direct 与 local 两种，前端一视同仁。 */
export interface Source {
  mode: 'direct' | 'local' | string;
  url: string;
  mime?: string;
  expiresAt?: number;
}

/** model.StreamInfo（清晰度选项，刻意不含 URL） */
export interface StreamInfo {
  definition?: string;
  quality?: string;
  codec?: string;
  width?: number;
  height?: number;
  bitrate?: number;
  hls?: boolean;
  dash?: boolean;
  protected?: boolean;
  encryption?: string;
}

/** model.PlaybackInfo（可公开摘要） */
export interface PlaybackInfo {
  episodeId: string;
  videoId?: string;
  mediaType?: string;
  durationSec?: number;
  protected?: boolean;
  expiresAt?: number;
  hasAudio?: boolean;
  keyInMemory?: boolean;
  streams?: StreamInfo[];
}

/** internal/player.PrepareResult（字段名照 internal/player 的 json tag） */
export interface PrepareResult {
  source: Source;
  qualities: StreamInfo[];
  info?: PlaybackInfo;
  state?: string;
  episodeId?: string;
  seriesId?: string;
  episodeNumber?: number;
  durationSec?: number;
  /** 续播起播点（§6.5）：未看完从 position-3s 起 */
  resumeSec?: number;
  /** 本次实际走的通道：direct | local */
  mode?: string;
}

/** model.HistoryDTO */
export interface HistoryEntry {
  seriesId: string;
  episodeId: string;
  episodeNumber: number;
  positionSec: number;
  durationSec?: number;
  finished: boolean;
  updatedAt: number;
}

/** model.SeriesPageDTO */
export interface SeriesPage {
  items: Series[];
  cursor?: string;
  hasMore: boolean;
}

/** model.EpisodeDiff；T9 也可能直接返回计数，用 normalizeDiff 统一。 */
export interface EpisodeDiff {
  added?: string[] | number;
  updated?: string[] | number;
  removed?: string[] | number;
  isNew?: string[] | number;
}

/** model.SlotStatus */
export interface SlotStatus {
  id: string;
  state: string;
  quotaUsed: number;
  quotaDay?: number;
  coolUntil?: number;
  lastUsed?: number;
  rejects: number;
}

/** model.DevicesStatus */
export interface DevicesStatus {
  healthy: number;
  cooling: number;
  exhausted: number;
  rejected: number;
  usedToday: number;
  nonPlaybackSwaps: number;
  slots?: SlotStatus[];
}

/** Media.DetectFFmpeg 的返回 */
export interface FFmpegInfo {
  path?: string;
  version?: string;
  ok?: boolean;
  license?: string;
}

/** 归一后的错误（player:error 事件与 Prepare 失败的公共形状） */
export interface PlayerError {
  code: string;
  message: string;
  retryable: boolean;
  episodeId?: string;
}
