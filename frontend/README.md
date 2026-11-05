# frontend（红果播放器前端）

Vite + TypeScript + React，深色主题，简体中文。不引 UI 组件库、hls.js、远程字体或 CDN（离线可用）。

依据：`docs/hongguo-player-plan.md` §8.1–§8.6、§9.5；类型照 `internal/model/*.go` 的 json tag 写。

## 构建

```bash
npm ci        # 按 package-lock.json 可重复安装
npx tsc --noEmit
npm run build # 产物 frontend/dist，由 cmd/app 的 Assets handler 伺服
```

`node_modules`、`dist`、`bindings` 均不入库（根 `.gitignore` 已覆盖）。

## 脚本

| 命令 | 作用 |
|---|---|
| `npm run dev` | 单独跑前端（无 Wails 宿主，绑定理所当然不可用） |
| `npm run typecheck` | `tsc --noEmit` |
| `npm run build` | 先类型检查再 `vite build` |

## 绑定与事件：两个唯一出口

- **`src/api.ts`**：所有 Go 方法调用的唯一出口。用 `import.meta.glob('../bindings/**/*.{ts,js}')` 把
  `wails3 generate bindings` 的产物整棵收进来，按「服务名 + 方法名」宽松解析。生成物文件名或符号名
  与下表有出入时，**只改这个文件**。
- **`src/events.ts`**：所有事件订阅的唯一出口，用 `@wailsio/runtime` 的 `Events.On(name, cb)`，
  载荷取 `event.data`。运行时不可用时降级为「无事件」，不伪造事件。

### 前端调用的方法与参数顺序（按方案 §8.3，供 T9 对齐）

已按 `internal/bindings/*.go` 的实际导出方法核对过一遍（2026-11-05）：服务名 `Library`、`Player`、
`Devices`、`History`、`Media`、`Settings`、`Diag` 与下表一致，唯一差异是 `History.List` 需要 `limit`，
前端已传 100。

| 服务 | 方法 | 前端传参 | 期望返回 |
|---|---|---|---|
| `Library` | `Discover` | `(channel: string, cursor: string)` | `{items, cursor, hasMore}` |
| `Library` | `Search` | `(q: string, cursor: string)` | `{items, cursor, hasMore}` |
| `Library` | `GetSeries` | `(id: string)` | `Series` |
| `Library` | `ListEpisodes` | `(seriesId: string)` | `Episode[]` |
| `Library` | `RefreshEpisodes` | `(seriesId: string)` | `{added, updated, removed}`，数组或计数都接受 |
| `Player` | `Prepare` | `(episodeId: string, preferQuality: string)` | `{source, qualities[], state, resumeSec?, mode?}` |
| `Player` | `SwitchQuality` | `(episodeId: string, quality: string)` | `{mode,url,mime}` |
| `Player` | `ReportProgress` | `(episodeId: string, positionSec: number)` | — |
| `Player` | `Ended` | `(episodeId: string, positionSec: number)` | `{state, hasNext, nextEpisodeId}`（前端暂未消费） |
| `History` | `List` | `(limit: number)`，前端传 100 | `HistoryDTO[]` |
| `History` | `Clear` | `()` | — |
| `Devices` | `Status` | `()` | `DevicesStatus` |
| `Devices` | `Reset` | `(slotId: string)` | `{ok}`（前端暂未接按钮） |
| `Media` | `DetectFFmpeg` | `()` | `{path, version, ok, license}` |
| `Settings` | `Get` | `(key: string)` | `string` |
| `Settings` | `Set` | `(key: string, value: string)` | — |

未使用：`Library.Seasons` / `Library.Recommend` / `Player.Next` / `Player.Prev` / `Player.OpenExternal` /
`Queue.*` / `Diag.RecentErrors`（见下方 Roadmap）。`Player.Next/Prev` 的语义由前端按已加载的剧集数组实现。

### 事件（方案 §8.4，前端已订阅）

`player:state`、`player:error`、`player:progress`、`episode:updated`、`quota:changed`、
`quota:exhausted`、`media:preparing`、`devices:changed`（后者当前只有设置页会用到，尚未渲染）。

## 页面与路由（hash 路由，不依赖服务端 SPA 回退）

| 路由 | 页面 | 数据源 |
|---|---|---|
| `#/` | 书城 | `Library.Discover`，IntersectionObserver 无限流 |
| `#/search` | 搜索 | `Library.Search`，提交+加载更多 |
| `#/series/:id` | 详情 | `GetSeries` + `ListEpisodes` + `RefreshEpisodes` |
| `#/play/:seriesId/:episodeId?t=秒` | 播放 | `Player.Prepare`，右侧选集抽屉，不整页刷新换集 |
| `#/history` | 历史 | `History.List`，续播带 `?t=` |
| `#/settings` | 设置 | `Media.DetectFFmpeg`、`Devices.Status`、`Settings.*`、`History.Clear` |

剧集网格 `src/components/EpisodeGrid.tsx` 是窗口化渲染（ResizeObserver 量视口 + 绝对定位只挂可见行），
集数与 DOM 数量无关；`Playable=false` 的格子置灰禁用。

## 已知未实现（Roadmap）

1. 简介：`model.SeriesDTO` 没有简介字段，详情页只展示状态/付费/集数/角标文案；补 DTO 后再加。
2. 多季/合集切换：`Library.Seasons`、`AlbumDTO` 未接。
3. 相关推荐：`Library.Recommend` 未接（§8.1 详情页的「相关推荐」）。
4. 播放页「即将播放第 N+1 集」浮层与取消（§8.2 浮层）：依赖后端预取事件，未做。
5. 置顶 / 迷你模式 / 内容保护：属 Wails 窗口 API（§8.5），前端只做了网页全屏（Fullscreen API），
   未接窗口服务；需要 T9 暴露 Window 服务后再加。
6. 快捷键只做了 Space、←/→、N/P、S、F；音量 ↑/↓、M、Q、T、I、Ctrl+1..9、Shift+Del 未做（§8.6）。
7. 诊断页 `/debug`（§8.1 隐藏页）未做。
8. 设备槽单台重置（`Devices.Reset`）没接按钮，破坏性操作待确认交互。
9. 清空数据只清观看历史；播放缓存、设备档案、临时文件的清理接口后端未暴露。
10. 书城频道切换（§8.1）：`Discover` 的 channel 固定传空串。
11. 未做浏览器内 UI 冒烟：本机没有可自动化的 Wails 宿主，构建与类型检查通过不等于运行验证通过。
