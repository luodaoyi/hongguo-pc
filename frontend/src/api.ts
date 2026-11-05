// Wails 绑定适配层：前端所有对 Go 的调用都从这里出去，别在其他文件直接 import 绑定。
//
// 生成命令（改 Go 绑定后必须重跑，缺一不可）：
//   wails3 generate bindings -ts -d frontend/bindings ./cmd/app
// 说明：`-ts` 出 TypeScript（否则只出 .js + JSDoc，tsconfig 未开 allowJs 会解析失败）；
// `./cmd/app` 是包模式（不带则扫不到服务，得到 0 Services）；生成物入库，CI 直接 npm run build。
// 生成的代码不要手改；方法名/参数顺序变了只改本文件的映射。
//
// 前端仍保留 src/types.ts 的手写类型作为页面契约：生成模型与它字段一致，
// 但一个是类（required 字段多）、一个是 interface（可选字段多），
// 因此每个出口只做一次显式断言（cast），不在页面上撒 any。

import {
  Devices as DevicesBinding,
  History as HistoryBinding,
  Library as LibraryBinding,
  Media as MediaBinding,
  Player as PlayerBinding,
  Settings as SettingsBinding,
} from '../bindings/github.com/luodaoyi/hongguo-pc/internal/bindings/index.js';

import type {
  DevicesStatus,
  Episode,
  FFmpegInfo,
  HistoryEntry,
  PrepareResult,
  Series,
  SeriesPage,
  Source,
  StreamInfo,
} from './types';

/** 生成模型 → 前端契约类型的唯一收敛点。字段不一致时 tsc 会在页面侧报错。 */
function cast<T>(value: unknown): T {
  return value as T;
}

/** `Player.Ended` 的返回（internal/player.EndedResult 的 json 形状）。 */
export interface EndedResult {
  state?: string;
  hasNext: boolean;
  nextEpisodeId?: string;
  seriesFinished: boolean;
  /** 后端已经解析好的下一集；有它就直接换源，别再调 Prepare。 */
  prepared?: PrepareResult;
  skipped?: number;
  /**
   * 迟到/串台的 ended（换清晰度后旧集再触发、或路由已切走）。
   * 后端只记了断点、没推进连播：此时 hasNext/seriesFinished 无意义，
   * 页面不要显示「已是最后一集」。
   */
  stale?: boolean;
}

/* ---------- Library ---------- */

export function discover(channel: string, cursor: string): Promise<SeriesPage> {
  return cast<Promise<SeriesPage>>(LibraryBinding.Discover(channel, cursor));
}

export function search(query: string, cursor: string): Promise<SeriesPage> {
  return cast<Promise<SeriesPage>>(LibraryBinding.Search(query, cursor));
}

export function getSeries(id: string): Promise<Series> {
  return cast<Promise<Series>>(LibraryBinding.GetSeries(id));
}

export function listEpisodes(seriesId: string): Promise<Episode[]> {
  return cast<Promise<Episode[]>>(LibraryBinding.ListEpisodes(seriesId));
}

/** 后端返回的是计数（不是 diff 里的 ID 列表）：{added,updated,removed} 都是 number。 */
export async function refreshEpisodes(
  seriesId: string,
): Promise<{ added: number; updated: number; removed: number }> {
  const raw = await LibraryBinding.RefreshEpisodes(seriesId);
  return {
    added: raw?.added ?? 0,
    updated: raw?.updated ?? 0,
    removed: raw?.removed ?? 0,
  };
}

/* ---------- Player ---------- */

export function prepare(episodeId: string, preferQuality: string): Promise<PrepareResult> {
  return cast<Promise<PrepareResult>>(PlayerBinding.Prepare(episodeId, preferQuality));
}

export function switchQuality(episodeId: string, quality: string): Promise<Source> {
  return cast<Promise<Source>>(PlayerBinding.SwitchQuality(episodeId, quality));
}

export function reportProgress(episodeId: string, positionSec: number): Promise<void> {
  return PlayerBinding.ReportProgress(episodeId, positionSec);
}

/** 上报「本集播完」；返回值里带后端已备好的下一集（连播以 Go 侧为准）。 */
export async function reportEnded(episodeId: string, positionSec: number): Promise<EndedResult> {
  const raw = await PlayerBinding.Ended(episodeId, positionSec);
  return cast<EndedResult>(raw);
}

/** 强制落盘断点（暂停 / 切集 / 退出前调用）。 */
export function flushProgress(episodeId: string, positionSec: number): Promise<void> {
  return PlayerBinding.FlushProgress(episodeId, positionSec);
}

export function nextEpisode(): Promise<{ episodeId: string }> {
  return cast<Promise<{ episodeId: string }>>(PlayerBinding.Next());
}

export function prevEpisode(): Promise<{ episodeId: string }> {
  return cast<Promise<{ episodeId: string }>>(PlayerBinding.Prev());
}

/* ---------- History ---------- */

export function historyList(limit = 100): Promise<HistoryEntry[]> {
  return cast<Promise<HistoryEntry[]>>(HistoryBinding.List(limit));
}

export function historyClear(): Promise<void> {
  return HistoryBinding.Clear();
}

/* ---------- Devices ---------- */

export function devicesStatus(): Promise<DevicesStatus> {
  return cast<Promise<DevicesStatus>>(DevicesBinding.Status());
}

export function devicesReset(slotId: string): Promise<{ ok: boolean }> {
  return cast<Promise<{ ok: boolean }>>(DevicesBinding.Reset(slotId));
}

/* ---------- Media / Settings ---------- */

export function detectFFmpeg(): Promise<FFmpegInfo> {
  return cast<Promise<FFmpegInfo>>(MediaBinding.DetectFFmpeg());
}

export function settingsGet(key: string): Promise<string> {
  return SettingsBinding.Get(key);
}

export function settingsSet(key: string, value: string): Promise<void> {
  return SettingsBinding.Set(key, value);
}

/** 设置键（与 internal/bindings/settings.go 的 Key* 常量一致）。 */
export const SettingKey = {
  quality: 'quality',
  autoNextEpisode: 'autoNextEpisode',
  ffmpegPath: 'ffmpegPath',
  alwaysOnTop: 'alwaysOnTop',
  contentProtection: 'contentProtection',
  firstRunDone: 'firstRunDone',
} as const;

/** 清晰度标签列表（去重，保持后端顺序）。 */
export function qualityLabels(qualities: StreamInfo[]): string[] {
  const labels: string[] = [];
  for (const stream of qualities ?? []) {
    const label = stream.definition?.trim() || stream.quality?.trim() || '';
    if (label && !labels.includes(label)) labels.push(label);
  }
  return labels;
}
