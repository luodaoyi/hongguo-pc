// 浏览器预览用的假 @wailsio/runtime：vite dev 模式经 alias 指到本文件，
// 生产构建不经过（vite.config.ts 里按 mode 区分）。Call.ByID 按方法 ID
// 返回假数据；Events 全部 no-op。预览数据只服务 UI 调试，不落数据库。

import { CancellablePromise } from '@wailsio/runtime/cancellable';

export { CancellablePromise };

// —— 方法 ID（与 frontend/bindings 生成物一致，改绑定后需同步） ——
const LIB = {
  Discover: 2900537317,
  GetSeries: 4240405863,
  ListEpisodes: 1081984016,
  Recommend: 63635000,
  RefreshEpisodes: 1573789319,
  Search: 776862906,
  Seasons: 1251447340,
};
const SETTINGS = { All: 3563431281, Get: 3010027910, Set: 3740077226 };
const DEVICES = { Status: 2543767308 };

/** 生成一张 3:4 的占位封面（SVG data URL），字取标题首字。 */
function cover(title: string, hue: number): string {
  const ch = title.slice(0, 1) || '剧';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="300" height="400"><rect width="300" height="400" fill="hsl(${hue},45%,38%)"/><text x="150" y="235" font-size="140" fill="hsl(${hue},60%,86%)" text-anchor="middle" font-family="serif">${ch}</text></svg>`;
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

const TITLES = [
  '满院亲戚全是上古大妖', '末世轮回：我以尸躯化神明', '大唐第一军火商', '重生囤百亿物资',
  '装穷提亲，娶到全家嫌弃的她', '逆江湖之苍生为局', '回到都市，坏消息修为没了',
  '末日宿舍求生：从剧广告开始变强', '全民副本：我能打造九九八十一难', '洪武十，我是李景隆',
  '魏婴，现代社团往事', '持刀驸马', '把酒当歌', '废土打工人', '现代修仙指南',
];

function makeSeries(i: number, withDesc: boolean) {
  const title = TITLES[i % TITLES.length] + (i >= TITLES.length ? `（续${Math.floor(i / TITLES.length)}）` : '');
  return {
    id: `mock-${i}`,
    title,
    coverUrl: cover(title, (i * 47) % 360),
    episodeCount: [0, 12, 31, 52, 100, 310, 521][i % 7],
    description: withDesc ? '十八岁高考结束那天，陈玄为了救下被失控货车撞飞的女友，意外激活了祖传的破败道观系统。' : '',
    status: i % 4 === 0 ? 'unknown' : ['updating', 'finished', 'updated_today', 'stopped'][i % 4],
    payType: 0,
    platform: 0,
    fetchedAt: Math.floor(Date.now() / 1000),
  };
}

function discoverPage(cursor: string) {
  const base = cursor === '' ? 0 : Number(cursor);
  const items = Array.from({ length: 20 }, (_, k) => makeSeries(base + k, (base + k) % 3 !== 0));
  return { items, cursor: String(base + 20), hasMore: base < 80 };
}

function searchPage(query: string) {
  // 模拟真实搜索响应：一部分结果带简介与封面，一部分只有标题（上游搜索
  // 主分支缺 CoverURL/Status 的真实形态），用于验证空态降级与布局。
  const items = TITLES.map((t, i) => ({ ...makeSeries(i, i % 2 === 0), title: `${t}` }));
  const matched = items.filter((s) => s.title.includes(query) || query === '李景隆');
  return { items: matched.length > 0 ? matched : items.slice(0, 8), cursor: '', hasMore: false };
}

function episodesOf(seriesId: string) {
  return Array.from({ length: 24 }, (_, k) => ({
    id: `${seriesId}-ep-${k + 1}`,
    seriesId,
    number: k + 1,
    title: `第${k + 1}集`,
    durationSec: 120 + ((k * 37) % 180),
    playable: true,
    isNew: k > 20,
    coverUrl: '',
  }));
}

const handlers: Record<number, (...args: any[]) => unknown> = {
  [LIB.Discover]: (channel, cursor) => discoverPage(cursor ?? ''),
  [LIB.Search]: (query, cursor) => searchPage(query ?? ''),
  [LIB.GetSeries]: (id) => ({ ...makeSeries(Number(id.replace('mock-', '')) || 0, true), id }),
  [LIB.ListEpisodes]: (seriesId) => episodesOf(seriesId),
  [LIB.Recommend]: () => Array.from({ length: 8 }, (_, i) => makeSeries(i + 5, true)),
  [LIB.Seasons]: (id) => ({
    id: '',
    seriesId: id,
    seasonCount: 1,
    seasons: [{ seriesId: id, title: '第一季', episodeCount: 24, index: 1 }],
  }),
  [LIB.RefreshEpisodes]: () => ({ added: [], updated: [], removed: [] }),
  [SETTINGS.All]: () => ({ ffmpegPath: '', deviceSlots: '4', autoNextEpisode: 'true' }),
  [SETTINGS.Get]: (key) => (key === 'autoNextEpisode' ? 'true' : ''),
  [SETTINGS.Set]: () => undefined,
  [DEVICES.Status]: () => ({ healthy: 4, usedToday: 2, slots: [] }),
};

export const Call = {
  ByID(id: number, ...args: any[]): CancellablePromise<unknown> {
    const handler = handlers[id];
    // CancellablePromise 构造器是标准 Promise executor + oncancelled 回调。
    return new CancellablePromise((resolve) => {
      try {
        resolve(handler ? handler(...args) : null);
      } catch {
        resolve(null);
      }
    }, () => {});
  },
  ByName(name: string, ...args: any[]): CancellablePromise<unknown> {
    void name;
    return new CancellablePromise((resolve) => resolve(null), () => {});
  },
};

export const Events = {
  On: (_name: string, _cb: (e: unknown) => void) => () => {},
  Off: (_name: string) => {},
  Emit: (_name: string, _data?: unknown) => {},
  Once: (_name: string, _cb: (e: unknown) => void) => () => {},
  Subscribe: (_name: string, _cb: (e: unknown) => void) => () => {},
  Unsubscribe: (_name: string) => {},
};

// 其余子模块按需透传空实现：页面只用 Call / Events。
// Create 是绑定生成物的模型构造器钩子：mock 数据已是纯 JSON。
// 注意签名语义：Create.Array(fn) 返回「遍历数组逐元素调 fn」的新函数，
// 不是直接转换——mock 成恒等函数会把整个数组喂给单个 createFrom（实测崩溃）。
export const Create = {
  Any: <T>(v: T): T => v,
  ByteSlice: <T>(v: T): T => v,
  Array:
    <T>(element: (source: unknown) => T) =>
    (source: unknown[] | null): T[] => {
      if (source === null) return [];
      for (let i = 0; i < source.length; i += 1) {
        source[i] = element(source[i]);
      }
      return source as T[];
    },
  Map: <T>(v: T): T => v,
  Nullable: <T>(v: T): T => v,
  Struct: <T>(v: T): T => v,
  DateFromTime: <T>(v: T): T => v,
};

export const Application = {}, Browser = {}, Clipboard = {}, Dialogs = {}, Screens = {},
  System = {}, Window = {}, Flags = {}, WML = {}, Updater = {};
export default { Call, Events };
