# 红果短剧桌面播放器 · 技术方案 v3

| 项 | 值 |
|---|---|
| 版本 | **v3（定稿）**：v1 初稿 + 修订增量 + T3 核验修正 + 都察院复核修正合并。**v3 变更摘要**：修 R1–R5（密钥缓存判据／合规口径／尺寸约束断言／采样入 M0／新增 §13）、r1–r6、p1–p6，见 §0.3 修正表与附录 A |
| 日期 | 2026-11-05 |
| 上游依据 | openhongguo `main@42b73f2`（beilunyang/openhongguo）；Wails `v3.1.0`（2026-11-01 发布） |
| 本机实测 | `go1.26.2 windows/amd64`；`ffmpeg 9.0.2-full_build-www.gyan.dev`（含 `--enable-gpl --enable-version3`，即 GPLv3 构建）；`ffplay 9.0.2` 同构建；**`mpv` 未安装**、`vlc` 未安装 |
| 目标平台 | Windows 10/11 x64，Wails v3 + WebView2 |
| 证据约定 | 断言给「仓库路径 + 函数名或原文片段」，**不写行号**；标「T1/T3」者为翰林院/大理寺独立复算的转引，非本文作者复算 |
| 未确证标注 | 性能数字标「估算」；SDK 与服务端未实证行为标「待核验」 |

## 0 结论与判断
### 0.1 一句话结论
技术可行，但工程重心不在界面。两条硬约束：一是「1 个播放模型 ≈ 1 台匿名设备的一次额度」；二是**设备供给是否充足无法从源码判定**（T3）——注册流程纯本地，但服务端风控不可见且已见封禁迹象。故主路径定为 **B1**（ffmpeg 解密落盘 + 127.0.0.1 Range 服务），A（直链 `<video>`）降为**可关闭快速通道**，C′（外部 ffplay/mpv）作逃生舱，C（libmpv 内嵌）暂不立项，D（WebCodecs/EME）否决。
### 0.2 五问速览
| 问题 | 落点 | 方案 | 主要失败路径 |
|---|---|---|---|
| 解码怎么弄 | §3 | 主路径 B1：`ffmpeg -decryption_key <32hex> -c copy` 输出渐进 fMP4 → 本地带 token 的 Range 服务 → `<video>`；A 为可关闭快速通道；C′ 兜底 | 编码不支持 / ffmpeg 缺失 / 配额耗尽 / 增长中文件的 Range 语义 |
| 播放列表怎么写 | §5 | Go DTO + TS type + **SQLite 七表**；队列四种语义；密钥只进内存不落库 | 去重冲突 / 进度写放大 / 下架条目残留 / 重启后加密集须重解析 |
| 界面怎么设置 | §8 | **七个页面（含隐藏 `/debug`）** + 播放页三分区；服务方法与事件见 §8.3/§8.4；窗口 4 态；键位见 §8.6 | 全屏后焦点丢失 / 迷你尺寸约束被 Fullscreen 重置 |
| 自动下一部怎么实现 | §6 | 状态机 IDLE→RESOLVING→PLAYING→ENDED→NEXT；剩余 ≤30 秒（估算）预取；连续第 3 集不可用即中止（最多跳 2 集） | 预取吃配额 / 剧终无下一部 / 续播位置错 |
| 剧集怎么选择和刷新 | §7 | 全量拉取 + 虚拟滚动网格；diff 增量合并；定时与手动刷新；过期 re-resolve 节流 | 无分页 / 空列表与限流不可区分 / 多季拿不到 |

### 0.3 相对初稿的四处理论修正
| # | 初稿 | 定稿 | 依据 |
|---|---|---|---|
| 1 | 主路径 A，B 为降级 | 主路径 **B1**，A 为可关闭快速通道 | 加密占比未知且浏览器无法解密（§3.1/§3.2） |
| 2 | 「只有播放接口会换设备」 | 只对**空结果**成立；**`ErrDeviceRejected` 在任何接口都换设备并覆盖 ProfileStore** | T3：`sdk/reset.go` `needsFreshDevice = errors.Is(err, ErrDeviceRejected) \|\| (playback && errors.Is(err, ErrAnonymousLimited))` |
| 3 | 「捕获 `ErrAlbumUnavailable`」 | 该哨兵**无生产者**，不得作分支；匿名 `GetAlbum` 的 101001 归为 `ErrContentUnavailable` | T3：`sdk/errors.go` 仅声明未返回；`internal/protocol/codes.go` 把 101001 归入 `KindContentGone` |
| 4 | 「设备池供给充足」当结论 | 降级为**待验证假设**，配池规模实测（§4.7） | T3：`internal/protocol/register.go` 可证注册纯本地，服务端策略不可证；已见 429 / 100 FAST_REJECT + SvrBanApiModel / 101143 FREQUENCY_BLOCK / 101144 HIT_SHARK_RULE / 注册头 `x-vc-bdturing-sdk-version` |

### 0.4 三条前提性假设（M0 必须验证，任一不成立即回退）
| 假设 | 若不成立 | 回退动作 |
|---|---|---|
| H1 设备注册可持续获得可用设备（§4.7） | 后续集拿不到播放模型 | 降级「单设备串行、每集一台」模式，并明示可播集数上限 |
| H2 ffmpeg 解密链路在 Windows 可用（上游仅 macOS/Linux 验证，T3） | B1 主路径不可行 | 全量退到 C′，或 A（仅未加密集） |
| H3 加密占比 p 与 A 成功率 s 满足阈值（§3.2） | A 只增加分支 | `DirectFastPath = false`，一行开关 |

## 1 需求边界与事实基线
### 1.1 术语
完整定义见**附录 B**。本文高频词：播放模型（`model.Playback`）、内容密钥（`model.ContentKey.Hex`）、额度（单台匿名设备约一次播放模型）、设备槽 slot（一份独立设备档案 + 一个 `sdk.Client`）、本地源（本机 127.0.0.1 提供的可播放 URL）。
### 1.2 事实基线（F1–F20，全部带锚点）
| # | 事实 | 证据锚点 |
|---|---|---|
| F1 | `Playback{EpisodeID,VideoID,MediaType,DurationSeconds,ExpiresAt,Protected,Streams,AudioURL,KeyMaterial(json:"-")}` | `sdk/model/media.go` 类型定义 |
| F2 | `Stream{Definition,Quality,Codec,Width,Height,Bitrate,Size,URL,BackupURL,HLS,Dash,Protected,Encryption}` | 同上 |
| F3 | 加密集返回 `(pb, ErrDRMProtected)` 双值，`err != nil` **不得丢弃模型** | `sdk/media.go` `ResolvePlayback`：`if playback.Protected { return playback, ErrDRMProtected }` |
| F4 | 密钥从 `pb.KeyMaterial`（`encrypt_info.spade_a`）本地解包；**kid 不参与推导**；同集各清晰度共用密钥的**原因是各清晰度带相同 spade_a**，不是「只有一个 KeyMaterial 字段」 | `sdk/model/media.go` KeyMaterial 注释；`sdk/media.go` `ContentKey` 注释「同一集的每种清晰度都带有相同的 spade_a 和 kid，2026-10-24 观察」；推导实现 `internal/drm/spade.go`（T1） |
| F5 | 密钥 16 字节 → 32 位小写 hex；消费端接受 32 或 64 位 | `sdk/model/media.go` `ContentKey.Hex` 注释；`internal/media/media.go` `normalizeKey`：`key must be 32 or 64 hex characters` |
| F6 | `Playback.Select(quality)` 已实现选流：空参取最高分辨率再最高码率，`worst` 取最低，其余按 `Definition`/`Quality`/`Codec` 忽略大小写匹配，**空 URL 的流永不选中** | `sdk/model/media.go` `Select`/`heightRank` |
| F7 | `PlaybackOptions`、`AlbumOptions` 均为空结构体占位 | `sdk/media.go` `type PlaybackOptions struct{}`；`sdk/series.go` `type AlbumOptions struct{}` |
| F8 | `ListEpisodes` **无分页**，`Cursor`/`HasMore` 恒空；空列表返回 `ErrAnonymousLimited` | `sdk/series.go` `ListEpisodes`：`if opts.Cursor != "" \|\| opts.PageSize < 0 { return nil, fmt.Errorf("episode pagination is not verified") }`、`if len(episodes) == 0 { return nil, ErrAnonymousLimited }` |
| F9 | `GetSeriesBatch` 每批 10 部；服务端接受 20、拒绝 30 并回 100001 | `sdk/series.go` `const seriesBatchSize = 10` 及其注释 |
| F10 | `Seasons()` 三级回退：详情页系列剧行 → `GetAlbum` → 搜索 + 批量详情；合集接口对匿名普遍 101001 | `sdk/series.go` `Seasons` 注释「先退回合集接口（在所有抽样的合集上，它都以 101001 拒绝匿名读取）」 |
| F11 | `SeriesRecommend` 匿名返回通用「更多好剧」cell（show_type 401），**不随该剧而定、每次调用都变、不带 session id** | `sdk/series.go` `SeriesRecommend` 注释 |
| F12 | `Episode.Duration` 是 `time.Duration` 且 `json:"-"`，不跨绑定边界 | `sdk/model/catalog.go` `Episode` 结构体 |
| F13 | 非视频内容用 `Series.Platform == PlatformUnknown(0)` 判定，播放回 101000 | `internal/cliapp/play.go` `requireVideo`；`sdk/model/catalog.go` `PlatformType` |
| F14 | 连续第 3 集不可用即中止，即**最多跳 2 集**；续播回退 3 秒 | `internal/cliapp/play.go` `const maxUnavailable = 3` 与 `unavailable++; if unavailable >= maxUnavailable`；`const resumeBackoff = 3` |
| F15 | 过期判据：`ExpiresAt > 0` 且距现在不足 1 分钟 | `internal/cliapp/play.go` `expired()` |
| F16 | `Retryable(err)`、`RetryAfter(err, fallback)`；SDK 默认 `RetryAttempts=1` 即**不重试**；退避 300ms×2ⁿ、上限 5s，单次等待 >10s 直接返回错 | `sdk/errors.go`；`sdk/options.go`；README「重试与超时」。`RetryAttempts=1` 默认值与退避参数为**转引 T3，未独立复核** |
| F17 | 换设备条件：`ErrDeviceRejected`（任何接口）或 `ErrAnonymousLimited`（仅 playback）；默认 `DeviceResets=2`；并发只注册一台；换设备**覆盖**档案 | **转引 T3，未独立复核**：`sdk/reset.go` `needsFreshDevice`；`sdk/options.go` `DeviceResets` 注释；README「自动更换设备」三则说明 |
| F18 | mpv/ffplay **按上游代码与注释支持**自解密并保持 seek（**本机未实测**：mpv/vlc 未安装，ffplay 未实际播放）；「ffmpeg → MPEG-TS 管道 → 播放器读 stdin」**不能 seek** | `internal/media/play.go`：`--demuxer-lavf-o=decryption_key=` / `-decryption_key`；`playPipe` 注释「Such a stream cannot be seeked」 |
| F19 | 拉流不设 UA/Referer/Cookie，http.Client 无 cookie jar | `internal/media/media.go` `New()`：`&http.Client{Timeout: 0, Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}}`；`internal/cliapp/play.go` `--url` 分支注释「public and expire on their own」 |
| F20 | 容器是**单文件 MP4/MOV**（短剧默认），`HLS`/`Dash` 是能力位 | `internal/media/media.go` `Decrypt` 注释「rewrites a CENC (cenc-aes-ctr) MP4 into a clear MP4 … through its mov demuxer」；`testdata/protocol/drm-playback.json` 的 `main_url` 以 `.mp4` 结尾、`gear_des_key` 含 `0:MP4\|1:encr`（**转引 T1，未独立复核**） |

### 1.3 两处含糊点：双解与推荐
**A.「自动下一部」**
| 解 | 含义 | 默认 | 依据 |
|---|---|---|---|
| A1 自动下一集 | 播完继续同部 N+1 集 | **开** | 单集短、连看同部是主场景；`--autoplay` 即此语义 |
| A2 剧终接下一部 | 一部播完接队列下一部或推荐 | **关**，仅队列上下文开启 | 跨剧是主动排队意图；`SeriesRecommend` 匿名结果每次都变且与该剧无关（F11） |
> 结论：两开关并存（`autoNextEpisode` 开、`autoNextSeries` 关）；只实现一个则实现 A1。

**B.「刷新剧集」**
| 解 | 含义 | 额度 | 入口 |
|---|---|---|---|
| B1 刷新分集列表 | 重拉 `GetSeries`/`ListEpisodes` | **0** | 常驻按钮 |
| B2 刷新播放地址 | 重新 `ResolvePlayback` | **1 台** | 只在失败时给「重试」 |
> 结论：UI 文案专指 B1；B2 由系统在地址临期时自动做（§7.5）。
### 1.4 合规边界（与设备池做法保持一致）
本方案**以多份本地匿名设备档案分摊单台设备的播放额度**：设备池逐集消耗匿名设备额度，客观效果就是分摊单设备限额。明确不做：不注册账号、不伪造付费凭证、不破解内容、不去水印、不批量抓取与再分发、不实现 DRM 规避。**该做法是否违反上游 ToS 由使用者自行判断**，UI 必须明示（落点见 §8.1 与 §13.3）。
另：本期只做**简体中文 + 深色主题**，不做多语言与主题切换——这是决策，不是遗漏。
## 2 总体架构
### 2.1 分层与包边界
| 层 | 包 | 职责 | 硬性禁止 |
|---|---|---|---|
| L0 客户端 | `internal/sdkx` | 构造 `sdk.Client`、错误归一、日志脱敏、重试策略 | 裸调 `ResolvePlayback` |
| L1 设备配额 | `internal/devices` | 设备池、单飞、冷却、记账 | 触碰 UI |
| L2 媒体 | `internal/media` | 策略判定、ffmpeg 进程、临时文件生命周期 | 认识 Wails 或 sdk 类型（只吃 URL 与密钥串） |
| L3 本地服务 | `internal/localsrv` | 127.0.0.1 随机端口、token、Range | 对外网暴露 |
| L4 领域 | `internal/library`、`internal/player` | 目录、队列、状态机、历史 | 直接开 ffmpeg |
| L5 存储 | `internal/store` | SQLite 七表、迁移 | 存内容密钥 |
| L6 绑定 | `internal/bindings` | DTO、服务方法、事件发射 | 泄露 `KeyMaterial` |
| L7 前端 | `frontend/` | 页面、`<video>`、快捷键 | 持有内容密钥 |

### 2.2 三条不变量
- **I1** 一个 `Playback` 只对应一次 `ResolvePlayback`：同一未过期模型内切清晰度不重解析（F6；过期门 §3.2）。
- **I2（硬红线）** 内容密钥绝不进 DTO、SQLite、日志、事件载荷；只允许进程内 `map[episodeID]hex`，进程退出即失。可选 DPAPI 落盘默认关闭（§5.3）。
- **I3** 任何额度消耗必经 `devices.Pool` 记账，评审禁止裸调 `ResolvePlayback`。
### 2.3 依赖方向
`bindings → player/library → media/devices/sdkx → store`，单向；`store` 不反向依赖上层。
## 3 解码与播放选型
### 3.1 六案对比
| 维度 | A 直链 `<video>` | B1 解密落盘 + 本地 Range | B2 增量 fMP4/HLS | C libmpv 内嵌 | C′ 外部 ffplay/mpv | D WebCodecs/EME |
|---|---|---|---|---|---|---|
| 起播（估算） | 0.3–1.5 s | 1.5–5 s（渐进可至 1–3 s） | 同 B1 | 0.5–2 s | 0.5–2 s | ≥3 s |
| seek | 原生，依赖 CDN Range | 文件写完后完整；写入中只到已写前沿 | 仅已生成窗口 | 原生 | **上游称原生且解密内联（理论上优于 B1），本机未实测** | 自实现 |
| 解密 | 无（拿不到密钥） | 有 | 有 | 有 | 有（F18） | 理论有、工程禁止 |
| 切清晰度 | 换 `src` 重载 | 重跑 ffmpeg | 重跑 ffmpeg | 换 URL | 需重启播放器 | 重走解码链 |
| 复杂度 | 低 | 中高 | 高 | 很高 | 低 | 极高 |
| 分发体积（估算） | +0 | +0（不随包分发 ffmpeg） | 同左 | +40 MB 起 | +0 | +0 |
| 许可 | 无 | 用户自装 ffmpeg（本机 GPLv3 构建） | 同左 | mpv GPL / libmpv LGPLv2.1+ | 同左 | 无 |
| 结论 | **快速通道，可关** | **主路径** | 条件启用 | **暂不立项** | **逃生舱** | **否决** |

C 不立项：`gen2brain/go-mpv` v0.4.0 仅 `RenderGL`/`RenderSW` 两条渲染路径、**不自开窗**；mpv `--wid` 在 Windows 的语义与 Wails v3 窗口句柄 API 均未验证（转引 T3，未独立复核）。D 否决：WebView2 仅 PlayReady、明确缺 Widevine（WebView2Feedback #4828 open），ClearKey 无官方证据，且 SDK 只给 `spade_a` + 明文 MP4 URL、不给 PSSH/license（转引 T3/M7，未独立复核）。
### 3.2 A / B 判定规则（含过期门）
```
decide(pb, stream):
  if pb == nil                              -> 错误，不重试
  if pb.ExpiresAt > 0 && now > pb.ExpiresAt-60s -> needsResolve = true   # 重解析代价 1 台（M6）
  if pb.Protected                           -> B1
  if stream.Dash || pb.AudioURL != ""       -> B1
  if !codecOK(stream.Codec)                 -> B1
  if !Policy.DirectFastPath                 -> B1
  return A
codecOK: h264/avc1 + aac -> true；hevc/h265/vp9/av1 -> 默认 false（待核验 WebView2 系统解码器）
```
单常量开关（采样后只改这一处）：
```go
// internal/media/policy.go
type Policy struct {
    DirectFastPath   bool            // 未加密且编码受支持时先试直链
    DirectSkipCodecs map[string]bool // 实测 WebView2 不支持的编码
}
var Current = Policy{DirectFastPath: true, DirectSkipCodecs: map[string]bool{}}
```
采样判定式（结果直接粘贴）：`p = 加密集数/样本数`、`s = A 试播成功率`；**p ≥ 0.5 或 s < 0.90 → `DirectFastPath = false`**。
运行时兜底（不额外耗额度）：A 触发 `MEDIA_ERR_SRC_NOT_SUPPORTED`/`MEDIA_ERR_DECODE`/`MEDIA_ERR_NETWORK` → 用**同一个 pb** 走 B1；同剧 A 连续失败 ≥2 次则后续集直接 B1。
### 3.3 B1 与 B2
**B1（主路径）**：与 `internal/media/media.go` `Decrypt` 同构（F20/F5），参数见 §3.8。优点：随机 seek 完整、`<video>` 原生、不依赖 hls.js；代价：起播需等首块——用渐进分片消掉。
**B2（条件启用）**：仅当 `Stream.HLS == true` 真实出现；输出 fMP4 分片、本地服务动态生成 m3u8 且只列已生成分片；seek 仅在窗口内，越界带 `-ss` 重启 ffmpeg（多一次解密，**不动额度**）。短剧默认不走（F20）。
### 3.4 本地服务为什么存在（理由已改写）
**不是**绕防盗链——F19 表明拉流无 UA/Referer/Cookie、地址 public 且自行过期。真实理由：① **统一 Range 语义**（增长中文件、完成文件、B2 分片三种形态对前端同构）；② **统一过期重取与备份兜底**（主 URL 失败按 `Stream.BackupURL` 重试一次，对齐 `withBackup`）；③ **密钥留在 Go 侧**（前端只见带 token 的本地 URL，见 I2）。
安全：`127.0.0.1:0` 系统分配端口；路径 `/v/<32 字节随机 token>/<episodeID>.mp4`；token 失败直接 403 不解释；会话级一次性；不写明文 URL 日志；去掉 `Server` 头；退出关监听并清临时文件。
**临时文件与回看（p3）**：同一会话保留最近 N 集解密产物以便回看（建议 N=5，估算）；目录总量上限 5 GB（估算），超限按 LRU 淘汰最旧产物；**切集不立即删除**当前集产物（用户可能回退重看）；应用退出时清空本会话目录；启动时扫描并删除遗留的 `tmp/*`。
### 3.5 水印与字幕
`model.Playback`/`model.Stream` 字段清单（F1/F2）无字幕轨、无水印参数；片源水印烧录在画面内，SDK 无移除能力。本方案不做去除、不做外挂字幕（无数据来源）。**字幕功能视为不存在**，若后续提出需单独立项并先解决数据来源。
### 3.6 ffmpeg 分发与许可
| 方案 | 优点 | 风险 | 结论 |
|---|---|---|---|
| 运行时探测 PATH + 设置页手填 | 零许可负担、零体积 | 用户需自装 | **默认且短期唯一** |
| 随包附 LGPL 构建 | 开箱可用 | 须确认不含 GPL-only 组件，附 license 与源码获取方式（待核验） | M3 后评估，需法务确认 |
| 首次运行在线下载 | 体积小 | 供应链与合规风险高 | 不做 |

本机构建必须写明：`--enable-gpl --enable-version3` = **GPLv3**，**禁止随包分发**；检测到用户装的是 GPL 构建时仅在设置页显示许可提示。能力已实测：`ffmpeg -h demuxer=mov` 有 `-decryption_key <binary>  The default media decryption key (hex)`；`ffmpeg -h muxer=mp4` 有 `empty_moov`、`faststart`、`frag_keyframe`。
### 3.7 C′：外部播放器作为「完整功能降级入口」
定位：M1/M2 的对比基准 + 长期逃生舱，不进主路径。
```go
// Player.OpenExternal(episodeID, startSec)
mpv:    --no-resume-playback --start=<sec> --demuxer-lavf-o=decryption_key=<hex> <url>
        // 有独立音轨时追加 --audio-file=<pb.AudioURL>
ffplay: -autoexit -loglevel warning -ss <sec> -decryption_key <hex> <url>   // 仅当 AudioURL == ""（M4）
```
能力矩阵（按「能否原生解密 + 有无独立音轨」分档，M4）：
| 条件 | 可用播放器 | seek | 说明 |
|---|---|---|---|
| `AudioURL == ""`（单文件 MP4） | mpv 或 ffplay | 保持 | demuxer 内联解密（F18） |
| `AudioURL != ""`（独立音轨/分段/长片） | **只有 mpv** | 保持 | ffplay 无 `--audio-file`，会落到管道模式 |
| `Stream.Dash == true` | **只有 mpv** | 保持 | 同上 |
| 指定 vlc 或播放器不支持自解密 | vlc | **失效** | 走 `-f mpegts pipe:1`，`playPipe` 注释明说不可 seek |

上表的 seek 行为来自上游代码与注释（F18），**本机未实测**；C′ 的「seek 优于 B1」是代码层面的推断，不是基准测试结论。
即：**带独立音轨或 DASH 的片子固定用 mpv**（本机未装，需用户在设置页指定或自行安装）；否则接受不可 seek，或改用 B1。
不作主路径的理由（按权重）：① **安全**——密钥须出现在子进程命令行，Windows 上其他本机进程可读，B1 全程不出进程；② **进度上报**——只能拿退出码，续播精度退化到本集起点；③ **脱离应用**——无法全屏/置顶/迷你、无法联动选集抽屉、无法参与队列状态机与预取；④ **窗口管理**——独立窗口与 §8.5 四态互斥。退出码语义复用上游：ffplay `123`、mpv `4` = 用户主动关闭（`closedByUser`）。
### 3.8 解密链路默认参数与四级降级链
```go
args := []string{
  "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
  "-decryption_key", hex,                  // 32 位小写 hex；未加密则整段省略
  "-i", srcURL,
  // AudioURL != "" 时追加: "-i", audioURL, "-map", "0:v:0", "-map", "1:a:0"
  "-map", "0", "-c", "copy",               // 只解密不转码
  "-movflags", "frag_keyframe+empty_moov", // 起播优先；default_base_moof 待核验
  "-f", "mp4", tmpPath,
}
```
**硬约束 ①：`+faststart` 与分片输出互斥（M5）。** 本机 `ffmpeg -h muxer=mp4` 对 `faststart` 的说明是「Run a **second pass** to put the index (moov atom) at the beginning of the file」；ffmpeg 官方格式文档进一步写明 faststart「will not work in various situations such as fragmented output」（**转引 T3，未独立复核**）。二者目的互斥，**不得叠加**。
**硬约束 ②：增长中文件的 Range 只能到已写入前沿（M5）。** handler 必须返回**当前**文件大小作 `Content-Length`；越界回 `416` 并带最新长度让播放器自行重试；支持 `If-Range`；**禁止 chunked**（会让 `<video>` 放弃 seek）。
**不做「完成后后台转 faststart」**：渐进 fMP4 写完即完整可 seek 的标准 MP4（moov 在前 + moof 索引），转写只换来「从片头瞬跳到片尾」的边缘收益，成本是整文件二次读写与磁盘峰值翻倍。
```
① 渐进 fMP4 + Range 边写边播（默认）
   ↓ Range 期间播放器报错且无法恢复
② 等解密完成再整文件播放（同一文件，不重跑 ffmpeg）
   ↓ ffmpeg 缺失 / 解密报错
③ C′ 外部播放器（带独立音轨时仅 mpv，见 §3.7）
   ↓ 播放器也未装
④ 报错「本集需要 ffmpeg 才能播放」+ 设置页跳转
```
## 4 播放地址与设备配额
### 4.1 配额事实
服务端自 2026-10-29 起基本只给每台匿名设备返回一次播放模型，之后 `ResolvePlayback` 返回空 → `ErrAnonymousLimited`（README「自动更换设备」；`sdk/options.go` `DeviceResets` 注释同述）。该错误**无法区分「被限」与「结果本来为空」**，故列表空结果也报它；但只有 playback 接口会因空结果换设备（F17）。
### 4.2 关键决定：关闭 SDK 自动换设备
```go
client, _ := adapters.NewClient(profilePath, sdk.Options{
    DeviceResets: sdk.Int(0), // go1.26+ 亦可写 new(0)；本机 go1.26.2
    RetryAttempts: 2,
})
```
理由（按 M1 改写）：**防止 SDK 越过设备池直接换档导致账目失真**。`ErrDeviceRejected`（F17）在**任何接口**都会触发换设备并覆盖档案，SDK 自换后池里的 `QuotaUsed`/`LastUsed`/`CoolUntil` 全部与真实档案脱节；逐设备统计只是附带收益。
### 4.3 设备槽模型
```go
type SlotState string // healthy | cooling | exhausted | rejected
type Slot struct { ID, ProfilePath string; State SlotState; QuotaUsed int
                   CoolUntil, LastUsed time.Time; Rejects int }
type Pool struct { mu sync.Mutex; slots []*Slot
                   inflight map[string]*call // 单飞：key = slotID
                   maxPerResolution int }    // 默认 2，与 SDK 默认 DeviceResets 对齐
```
### 4.4 解析算法
```
Resolve(episodeID):
  if c := cache.Get(episodeID); c != nil && !c.Expired(60s):
      if c.HasKey || !c.Protected: return c     # 0 额度：密钥仍在进程内存（仅同会话）
      # 否则是「加密集 + 内存无密钥」→ 不 return，继续走完整解析（代价 1 台）
  for attempt := 1; attempt <= maxPerResolution; attempt++ {
      slot, err := pool.Acquire(ctx)        # 单飞 + 排除 cooling/exhausted
      if err != nil { return ErrQuotaExhausted }
      pb, err := slot.Client.ResolvePlayback(ctx, episodeID, sdk.PlaybackOptions{})
      switch {
      case errors.Is(err, sdk.ErrAnonymousLimited):
          pool.MarkExhausted(slot); continue            # 跨日或距上次使用满 24h 视为新额度窗口（真实重置时点待核验，A1）
      case errors.Is(err, sdk.ErrDeviceRejected):
          slot.Client.ResetDevice(ctx); pool.MarkCooling(slot, 10*time.Minute); continue
      case errors.Is(err, sdk.ErrRateLimited):
          w := sdk.RetryAfter(err, 5*time.Second)
          if w > 10*time.Second { return ErrRateLimitedLong }   # 不排队，直接降级（F16）
          time.Sleep(w); continue
      case err == nil, errors.Is(err, sdk.ErrDRMProtected) && pb != nil:
          pool.Commit(slot); cache.Put(episodeID, pb); return pb  # 加密集也是成功（F3）
      default:
          return err
      }
  }
  return ErrQuotaExhausted
```
**口径锚点**：以下各处引用额度结论时，一律以本节判据为准（**同会话 + 持内存密钥 + 未过 `ExpiresAt-60s`**），其他章节不再重复条件。**M1 补充——非播放入口同样会烧设备**：`GetSeries`/`ListEpisodes`/`Search`/`Seasons`/`SeriesRecommend` 返回 `ErrDeviceRejected` 时 SDK 也会换设备。故 `sdkx` 层对所有方法统一走 `pool.NoteDeviceRejected(slot)`，账本记一行「非播放换档」，诊断页单列计数；否则池状态与真实档案长期漂移无人察觉。
### 4.5 额度代价表
| 动作 | 调 `ResolvePlayback` | 代价 |
|---|---|---|
| 首次播放某集 | 是 | **1 台设备** |
| 播放中切清晰度（同一**未过期**模型内） | 否（F6） | 0 |
| 切清晰度但已过 `ExpiresAt-60s` | 是（F15/M6） | **1** |
| 缓存未过期重播（**仅同一进程会话内**，且该模型仍持内存密钥） | 否 | 0 |
| **进程重启后首次播放加密集**（内存密钥已丢、`payload` 不含 `KeyMaterial`） | 是 | **1**（未加密集不受影响） |
| 地址过期 re-resolve | 是 | **1** |
| 预取下一集 | 是 | **1** |
| 刷新分集/详情/搜索/多季/推荐 | 否 | 0（但可能因 `ErrDeviceRejected` 换档，见 §4.4） |
| A 失败改 B（同一 pb） | 否 | 0 |

### 4.6 配额耗尽的用户可见降级
| 触发 | 文案 | 行为 |
|---|---|---|
| `healthy` 槽 = 0 | 「今日可播放集数已用尽（匿名额度限制），明天恢复」 | 播放按钮禁用 + 说明；浏览/搜索/选集/历史全可用 |
| 预取前 `healthy` ≤ 1 | 不提示 | 跳过预取，正常播当前集 |
| 换满 `maxPerResolution` 台仍失败 | 「换个剧试试，或稍后重试」 | 停止自动重试，回详情页 |
| 连续第 3 集不可用 | 「当前内容连续不可用」 | 停止连播（F14：最多跳 2 集） |
| `healthy` ≥1 但少于队列剩余 | 「本队列预计可播 N 集」 | 队列页顶部提示条，不阻断 |

### 4.7 池规模实测（H1 的验收；**默认不执行，需用户明确同意**）
本节实测项会真实注册多台匿名设备，属对上游服务的主动探测。**默认不执行**；仅在用户明确同意后以小样本（≤10 台、单轮、不重试）运行一次，结果只写入本地报告，不上报、不共享。
**可证**（T3）：注册纯本地——`crypto/rand` 生成 CDID/OpenUDID/ClientUDID/OAID/ReqID + 机型（默认小米 14）+ 本地 TTEncrypt，无账号、无签名器，SDK 内无上限计数（`internal/protocol/register.go`）。**不可证**：服务端策略；已见风控——HTTP 429 → `ErrRateLimited`、业务码 100 `FAST_REJECT` + `SvrBanApiModel` 封禁窗口、101143 `FREQUENCY_BLOCK`、101144 `HIT_SHARK_RULE`、注册头 `x-vc-bdturing-sdk-version`。
| 验收项 | 方法 | 通过标准 |
|---|---|---|
| 首次成功率 | 连续注册 N=10 台，各解析 1 集 | 成功率 <50% 则 H1 不成立 |
| 封禁命中 | 统计 429/100/101143/101144 | 任一出现即记「风控已生效」 |
| 单设备上限 | 单槽连续解析 5 集 | 记录第几次开始返回空 |
| 重置时点 | 失败后 1h/6h/24h 各重试 | 确定 `exhausted` 恢复策略 |
**降级路径（H1 不成立）**：单设备串行 → 每集一台 → 明示「本机今日最多可播 K 集」（K 按实测成功率推算，估算）；该模式下不预取、不批量、不做任何后台解析。
## 5 播放列表数据模型
### 5.1 Go DTO（跨绑定边界，全字段带 json tag）
```go
type SeriesDTO struct { ID, Title string `json:"id"`; CoverURL string `json:"coverUrl,omitempty"`
    EpisodeCount int `json:"episodeCount"`; Status, PayType string; RightText string `json:"rightText,omitempty"`
    IsMultiSeason bool `json:"isMultiSeason"`; SeasonIndex int `json:"seasonIndex,omitempty"`
    AlbumID string `json:"albumId,omitempty"`; FetchedAt int64 `json:"fetchedAt"` }
type EpisodeDTO struct { ID, SeriesID string; Number int; Title string `json:"title,omitempty"`
    DurationSec int `json:"durationSec,omitempty"` // F12：SDK 的 Duration 是 json:"-"，必须自转
    Playable, IsNew bool; CoverURL string `json:"coverUrl,omitempty"` }
type SourceDTO struct { Mode string `json:"mode"`; URL, MIME string `json:"url"`; ExpiresAt int64 }
    // Mode: direct | local；ExpiresAt 仅 direct 有值
type QueueItemDTO struct { Pos int; SeriesID, SeriesTitle string; EpisodeID string `json:"episodeId,omitempty"`; StartAt int }
type HistoryDTO struct { SeriesID, EpisodeID string; EpisodeNumber int; PositionSec float64
                         Finished bool; UpdatedAt int64 }
```
### 5.2 TS type
```ts
export type Series = { id: string; title: string; coverUrl?: string; episodeCount: number;
  status: "updating"|"finished"|"stopped"|"unknown"; payType: "free"|"vip"|"single";
  rightText?: string; isMultiSeason: boolean; seasonIndex?: number; albumId?: string; fetchedAt: number };
export type Episode = { id: string; seriesId: string; number: number; title?: string;
  durationSec?: number; playable: boolean; isNew: boolean; coverUrl?: string };
export type Source = { mode: "direct"|"local"; url: string; mime: string; expiresAt?: number };
export type QueueItem = { pos: number; seriesId: string; seriesTitle: string; episodeId?: string; startAt: number };
export type History = { seriesId: string; episodeId: string; episodeNumber: number;
  positionSec: number; finished: boolean; updatedAt: number };
export type PlayerState = "idle"|"resolving"|"playing"|"ended"|"next"|"error";
```
### 5.3 SQLite schema
```sql
PRAGMA journal_mode=WAL;
CREATE TABLE series(id TEXT PRIMARY KEY, title TEXT NOT NULL, cover_url TEXT, episode_count INT,
  status INT, pay_type INT, platform INT, is_multi_season INT, season_index INT, album_id TEXT,
  right_text TEXT, fetched_at INT NOT NULL);
CREATE TABLE episode(id TEXT PRIMARY KEY, series_id TEXT NOT NULL, number INT NOT NULL, title TEXT,
  duration_sec INT, playable INT NOT NULL DEFAULT 1, is_new INT NOT NULL DEFAULT 0, cover_url TEXT,
  fetched_at INT NOT NULL, removed_at INT);            -- 仅 id 为主键，number 只建普通索引（r5）
CREATE INDEX idx_ep_series ON episode(series_id, number);
CREATE TABLE "queue"(id INTEGER PRIMARY KEY, name TEXT, mode TEXT NOT NULL
  CHECK(mode IN ('single','sequential','loop','series_chained')), active INT NOT NULL DEFAULT 0, created_at INT);
CREATE TABLE queue_item(id INTEGER PRIMARY KEY, queue_id INT NOT NULL REFERENCES "queue"(id) ON DELETE CASCADE,
  pos INT NOT NULL, series_id TEXT NOT NULL, episode_id TEXT, UNIQUE(queue_id, series_id));
CREATE TABLE history(series_id TEXT PRIMARY KEY, episode_id TEXT, episode_number INT,
  position_sec REAL NOT NULL, duration_sec INT, finished INT NOT NULL, updated_at INT NOT NULL);
CREATE TABLE playback_cache(episode_id TEXT PRIMARY KEY, series_id TEXT, payload BLOB NOT NULL,
  resolved_at INT NOT NULL, expires_at INT, has_key INTEGER NOT NULL DEFAULT 0);
  -- has_key：密钥不落库，此列只标记该模型在**本次会话**是否有内存密钥（R1）
CREATE TABLE device_slot(id TEXT PRIMARY KEY, profile_path TEXT NOT NULL, state TEXT NOT NULL,
  quota_used INT DEFAULT 0, quota_day INT, cool_until INT, last_used INT, reject_count INT DEFAULT 0);
  -- quota_day：quota_used 所属的本地日期(YYYYMMDD)，跨日即视为新额度窗口；
  -- 服务端真实重置时点仍待实测（附录 A1），当前实现按「跨日 + 保守 24h」双判据复原
```
**密钥红线（I2）**：`playback_cache.payload` 是 `Playback` JSON，**不含密钥**（`KeyMaterial` 带 `json:"-"`，F1）；密钥只进进程内 map，进程退出即失。由此产生一条硬后果：**进程重启后，加密集即使命中缓存也没有密钥**，必须重解析（1 台设备），`has_key` 列正是为此判据而设（§4.4、§4.5）。确需持久化时另加 `key_blob BLOB`（DPAPI 加密），默认建表不启用。
### 5.4 队列语义
| 模式 | 播完一集 | 播完一部 | 预取 |
|---|---|---|---|
| `single` | 停在 ENDED | 无动作 | 不预取 |
| `sequential` | 同部 N+1 | ENDED + 提示「本剧看完」 | 剩余 ≤30 s（估算）预取 N+1 |
| `loop` | 同部 N+1；末集回第 1 集 | 回第 1 集 | 末集预取第 1 集 |
| `series_chained` | 同部 N+1 | 取 `queue_item` 下一部第 1 集（或按 history 续播点） | 跨剧**不预取**，等 ENDED 再解析 |
去重：`queue_item` 对 `(queue_id, series_id)` 唯一，重复加入即移到队首。断点：`history` 主键 `series_id`，`timeupdate` 每 5 秒（估算）落一次，暂停/切集/退出时强制落盘。
## 6 自动下一集与自动下一部
### 6.1 状态机
```
IDLE ─play()→ RESOLVING ─ok→ PLAYING ─ended/timeupdate→ ENDED ─有下一集→ NEXT → RESOLVING
                 └err可重试→ RESOLVING(退避)    └err致命→ ERROR          └无下一集→ IDLE
```
| 状态 | 进入条件 | 退出事件 |
|---|---|---|
| IDLE | 启动、停止、播完无下一部 | `play()` |
| RESOLVING | 需 pb 且（缓存未命中 / 已过期 / **缓存无内存密钥即 `!HasKey && Protected`**） | 成功→PLAYING；失败→ERROR |
| PLAYING | 已拿到 Source 且 `<video>` 就绪 | `ended`、用户操作 |
| ENDED | `ended`；mpv 按 end-file reason；管道按退出码 | 有下一集→NEXT |
| NEXT | 选中下一集 | 立即→RESOLVING |
| ERROR | 致命错误 | 用户操作 |

### 6.2 触发点与阈值
| 触发 | 条件 | 动作 | 额度 |
|---|---|---|---|
| 预取 | `remaining <= min(30s, duration*0.25)` 且 `currentTime >= 60s`（估算）且 healthy ≥2 且 `autoNextEpisode` 开 | 后台解析下一集入缓存 | 1 |
| 切换 | `ended` | NEXT → 立即播（缓存命中 0） | 0 |
| 兜底 | `ended` 丢失 | `timeupdate` 判 `duration-currentTime <= 0.5s` 且 1 秒内未见 `ended`，强制 NEXT | 0 |
| 剧终 | 已是末集 | `series_chained` 取队列下一部；否则 IDLE | 视下一部 |

**预取语义（p4）**：① 预取请求一旦发出，额度即已消耗、**不可回收**——随后取消播放、切剧或退出都不退还；② 因此加双闸门：池内 `healthy ≥ 2` **且**当前集已播 ≥60 秒（估算）才发；③ 用户切走时只取消**尚未发出**的预取，已发出的照常完成并写入缓存供将来命中。
### 6.3 失败重试与跳过
| 错误 | 判定 | 策略 |
|---|---|---|
| `ErrAnonymousLimited` | 匿名不可播 | 池内换设备（≤2 台）；仍失败→跳过，连续计数 +1 |
| `ErrRateLimited` | 限流（含 429） | `RetryAfter` ≤10 s 才等待重试一次；>10 s 直接降级（F16） |
| `ErrDRMProtected` + `pb != nil` | **正常加密集，非错误** | 走 `sdk.ContentKey`（F3） |
| `ErrDRMProtected` + 解包失败 | 密钥材料不识别 | 跳过并提示「该集加密方式暂不支持」 |
| `ErrContentUnavailable` | 已下架/无权限（101001/101104/101109/101115） | 直接跳过，记 history 备注 |
| `ErrAuthenticationRequired` | 需登录 | 跳过并提示 |
| `Retryable(err) == false` | 重发不会成功 | 不重试 |
| 连续第 3 集不可用 | `maxUnavailable=3` 真实语义 | **中止连播**（最多跳 2 集，F14） |
> **M3**：`sdk.ErrAlbumUnavailable` 在已读源码中**无任何 return 点**（仅 `sdk/errors.go` 声明），是「无生产者的哨兵」，**不得作为分支条件**。匿名 `GetAlbum` 的 101001 经 `internal/protocol/codes.go` 归入 `KindContentGone`，`errors.Is(err, sdk.ErrContentUnavailable)` 为真；本节及 §7.6/§7.7 一律按 `ErrContentUnavailable` 分流。
### 6.4 剧终之后
优先级：① 队列 `queue_item` 下一部 → ② history 最近未完成的一部 → ③ `SeriesRecommend` 首条**仅作「换个剧看看」卡片展示，不自动播放**（F11）。
### 6.5 续播与「播完」判定
| 场景 | 判据 |
|---|---|
| 未看完 | 从 `position - 3s` 起播（`resumeBackoff = 3`） |
| 已看完且非末集 | 从下一集 0 秒 |
| ffplay 分支播完 | `played >= duration - 5s`（`endTolerance = 5.0`，**仅 ffplay 生效**） |
| mpv 分支播完 | `end-file` reason：`eof` 播完、`quit`/`stop` 用户关闭、`error` 失败 |
| 管道（mpegts）分支 | 退出码 nil 即判成功（该路径不报位置） |

## 7 剧集选择与刷新
### 7.1 选集 UI
播放页右侧抽屉 + 详情页内嵌网格共用同一组件：每格数字按钮（`minmax(56px,1fr)`）；新集右上角圆点（`IsNew`）；`Playable=false` 置灰加锁且不参与连播；当前集高亮。抽屉顶部：季胶囊（多季时）+「刷新剧集」+ 上次刷新时间。集数 >120 启用窗口化渲染（短剧常态 60–120 集，估算）。
### 7.2 `ListEpisodes` 的真实能力
| 项 | 实际 | 影响 |
|---|---|---|
| 游标分页 | **不存在**（F8） | 不能「加载更多」；全量 + 本地虚拟滚动 |
| 返回页 | `Cursor`/`HasMore` 恒空 | 进度显示用 `Series.EpisodeCount` |
| 空结果 | `ErrAnonymousLimited`，与「被限」不可区分 | 不可当作配额耗尽提示 |
| 时长 | `Episode.Duration` 是 `json:"-"`（F12） | 必须转 `DurationSec` |

### 7.3 增量刷新
```
RefreshEpisodes(seriesID):
  fresh := client.ListEpisodes(ctx, seriesID, sdk.EpisodeOptions{})   # 全量，0 额度
  added := fresh - old(bynumber) ; updated := 字段有变的交集 ; removed := old - fresh
  tx { upsert added/updated ; len(removed)>0 → MarkRemoved（软删，写 removed_at） }
  emit("episode:updated", {seriesId, added, updated, removed})
```
合并键 `id`，排序键 `number`；软删保证 history 不指向空行；新集双保险 `IsNew` + `number > max(old.number)`。**`number` 变更时先按 `id` 删除旧行再插入新行**（r5：`episode` 仅 `id` 做主键、`(series_id, number)` 是普通索引，故不存在唯一键冲突，但排序视图需要一次显式重写）。
### 7.4 定时与手动刷新
| 入口 | 触发 | 频率 | 备注 |
|---|---|---|---|
| 手动 | 详情页/抽屉按钮 | 用户 | 按 seriesID 30 秒（估算）去抖 |
| 定时 | 详情页或播放页可见时 | 5–10 分钟（估算） | 不可见停表；`status` 为 finished/stopped 时停用 |
| 进播放页 / 启动 | 各一次 | — | 保证抽屉与续播点是最新 |
定时刷新只调目录接口，**0 额度**；`GetSeries` 与 `ListEpisodes` 内部同走 `seriesDetail`，只调后者。
### 7.5 地址过期后的 re-resolve
| 项 | 规则 |
|---|---|
| 判据 | `ExpiresAt > 0 && now > ExpiresAt - 60s`（F15，**已确证**）；`ExpiresAt == 0` 时按「解析后 15 分钟（估算）」兜底 |
| 触发 | 播放前检查；播放中 `direct` 源报网络错误时判失效 |
| 代价 | **每次 1 台**（§4.5） |
| 节流 | 同 `episodeID` 5 分钟（估算）内最多 2 次，超出提示「地址获取失败，请稍后重试」 |
| 缓存 | 过期即删，不留陈旧 URL |
| 提示 | 正常 re-resolve 静默；失败才提示 |

### 7.6 多季 / 合集切换
数据来自 `Seasons(seriesID)`，内部三级回退（F10）：详情页系列剧行 → `GetAlbum` → 搜索 + 批量详情。匿名 `GetAlbum` 普遍 101001，该错误表现为 **`ErrContentUnavailable`**（M3），按此分流。UI 必须容忍「只拿到 1 季」：`SeasonCount == 1` 且无 `AlbumID` 时不显示季切换器。切季 = 换 `seriesID` 重建选集与队列，`history` 按 `series_id` 天然隔离。
### 7.7 空列表与下架
| 现象 | 判定 | UI | 耗额度 |
|---|---|---|---|
| `ErrAnonymousLimited` | 可能真空也可能被限（F8） | 「该剧暂无可播放分集」+ 重试，不提配额 | 否 |
| `ErrContentUnavailable` | 已下架/不存在 | 「该内容已下架」+ 从队列移除 | 否 |
| `Platform == PlatformUnknown(0)` | 书/有声书（F13） | 「这是书籍/有声书，播放器不支持」 | 否 |
| `Episode.Playable == false` | 服务端禁止播放 | 置灰加锁，不参与连播 | 否 |

## 8 界面设计
### 8.1 页面清单
| 页面 | 路由 | 内容 | 数据源 |
|---|---|---|---|
| 书城 | `/discover` | 频道切换、无限流卡片 | `Discover`/`DiscoverPager` |
| 搜索 | `/search` | 输入框、热榜、联想、结果网格 | `HotSearch`/`Suggest`/`Search` |
| 详情 | `/series/:id` | 封面、简介、季胶囊、选集网格、相关推荐 | `GetSeries`/`Seasons`/`ListEpisodes`/`SeriesRecommend` |
| 播放 | `/play/:seriesId/:episodeId` | 三分区 | `ResolvePlayback` + 本地服务 |
| 历史 | `/history` | 继续观看、清空 | `store.History` |
| 设置 | `/settings` | 清晰度、连播开关、ffmpeg/mpv 路径、池状态、缓存体积、**「关于与合规」常驻入口** | 本地 |
| 诊断（隐藏） | `/debug` | 槽状态、最近 50 条错误码、配额计数、非播放换档计数 | 内存环形缓冲 |

**界面范围决策（p1）**：本期只做**简体中文 + 深色主题**，不做多语言与主题切换。
**免责声明落点（R5）**：① 首次启动引导第 2 步强制勾选确认（§13.3）；② 设置页「关于与合规」常驻入口可随时回看。两处文案一致，须写明「以多份本地匿名设备档案分摊单台设备额度」与「是否违反上游 ToS 由使用者自行判断」。
### 8.2 播放页布局
```
┌────────────────────────────────────────────────┐
│ 顶部条：返回 | 剧名 第 N 集 | 清晰度 | 置顶 迷你 全屏 │
├──────────────────────────────┬─────────────────┤
│       <video> 16:9 / 竖屏居中   │  选集抽屉（可收起） │
├──────────────────────────────┴─────────────────┤
│ 控制条：播放/暂停 | 进度 | 时间 | 音量 | 下一集   │
└────────────────────────────────────────────────┘
浮层：`ended` 前 10 s（估算）淡入「即将播放 第 N+1 集」+ 取消
```
### 8.3 服务方法表（`application.NewService(&Svc{})` 暴露）
| 方法 | 入参 | 返回 | 说明 |
|---|---|---|---|
| `Library.Discover` / `.Search` | `channel,cursor` / `q,page` | `{items,cursor,hasMore}` | 0 额度 |
| `Library.GetSeries` | `id` | `Series` | 0 额度 |
| `Library.ListEpisodes` | `seriesId` | `Episode[]` | 全量，0 额度（F8） |
| `Library.Seasons` | `seriesId` | `{albumId,title,seasons[]}` | 容忍单季（F10） |
| `Library.Recommend` | `seriesId` | `Series[]` | 仅展示（F11） |
| `Player.Prepare` | `episodeId,preferQuality` | `{source,qualities[],state}` | **1 额度**；同会话缓存命中且持内存密钥 = 0；选流全部委托 `Playback.Select`（F6/F7） |
| `Player.SwitchQuality` | `episodeId,quality` | `Source` | 同一未过期模型内 0 额度（M6） |
| `Player.OpenExternal` | `episodeId,startSec` | `{ok}` | C′ 入口（§3.7），二次确认 |
| `Player.Ended` / `.ReportProgress` | `episodeId,positionSec` | — | 前端上报，节流落盘 |
| `Player.Next` / `.Prev` | — | `{episodeId}` | 队列内导航 |
| `Queue.Add/Remove/Move/List` | 见 §5.4 | `QueueItem[]` | 0 额度 |
| `History.List/Get/Clear` | — | `History[]` | 本地 |
| `Devices.Status` | — | `{healthy,cooling,exhausted,usedToday,nonPlaybackSwaps}` | 只读 |
| `Devices.Reset` | `slotId` | `{ok}` | 显式操作，二次确认 |
| `Settings.Get/Set` | `key,value` | — | 本地 |
| `Media.DetectFFmpeg` / `.DetectPlayer` | — | `{path,version,ok,license}` | `license` 用于提示 GPL 构建 |
| `Diag.RecentErrors` | — | `[{code,msg,at}]` | 只读 |
> 方法名是设计契约；生成后的 TS 符号名由 `wails3 generate bindings` 决定（契约不变，符号名以产物为准）。
### 8.4 事件表（`app.Event.Emit(name, data...)`）
| 事件 | 载荷 | 时机 |
|---|---|---|
| `player:state` | `{state,episodeId?}` | 每次状态迁移 |
| `player:error` | `{code,message,retryable,episodeId}` | 归一后错误 |
| `player:progress` | `{episodeId,positionSec,durationSec}` | 播放中 1 Hz（估算） |
| `episode:updated` | `{seriesId,added,updated,removed}` | 增量刷新后 |
| `quota:changed` / `quota:exhausted` | `{healthy,usedToday}` / `{}` | 额度变化 / 归零 |
| `devices:changed` | `{slots:[{id,state}]}` | 池状态变化 |
| `media:preparing` | `{episodeId,mode,stage}` | B1 解密进度 |
> 前端 → Go 一律走方法调用，不用事件，便于返回值与错误处理。
### 8.5 窗口行为
| 形态 | 实现要点 |
|---|---|
| 普通 | 1280×720，最小 960×540；记住尺寸与位置 |
| 全屏 | `window.Fullscreen/UnFullscreen/ToggleFullscreen/IsFullscreen`。**尺寸约束与全屏/最大化的真实交互在 v3.1.0 待实测**：该版 release notes 含 #6387「keep size constraints through a fullscreen that starts there」与 #6388「do not resize a fullscreen or maximised window to fit a new constraint」，与旧文档「Fullscreen 会 DisableSizeConstraints、UnFullscreen 才恢复」的说法冲突 → 见附录 A12 |
| 置顶 | `window.SetAlwaysOnTop`，状态持久化，可与迷你叠加 |
| 迷你 | 400×225，`SetMinSize`+`SetMaxSize` 锁定同尺寸；退出恢复上次普通尺寸。**防御性写法**：进入全屏前、退出全屏后、进入/退出迷你后各显式重设一次 min/max，不依赖框架自动恢复 |
| 无边框 | `window.SetFrameless`，自绘标题栏与拖拽区 |
| 内容保护 | 可选 `window.SetContentProtection`（防截屏），默认关 |
| 竖屏片 | `Episode.Vertical == true` 时容器改 9:16 居中 |
全屏/迷你切换时暂停快捷键拦截，避免与网页全屏 API 冲突；切换后重新聚焦 `<video>`，否则方向键失效。
### 8.6 快捷键
| 键 | 动作 | 键 | 动作 |
|---|---|---|---|
| Space | 播放/暂停 | ← / → | ∓5 s（Shift ±30 s） |
| ↑ / ↓ | 音量 ±5% | M | 静音 |
| F / F11 | 全屏 | Esc | 退全屏→退迷你→返回详情 |
| N / P | 下一集 / 上一集 | S | 选集抽屉 |
| Q | 切清晰度 | T | 置顶 |
| I | 迷你模式 | Ctrl+O | ffmpeg/mpv 设置 |
| Ctrl+1..9 | 队列第 1–9 部 | Shift+Del | 从队列移除当前剧 |
输入框聚焦时全部快捷键失效（Esc 除外）。
## 9 工程结构与绑定面
### 9.1 目录树
```
openhongguo-player/
├─ cmd/app/main.go            # application.New(...) + app.Run()
├─ internal/{sdkx,devices,media,localsrv,library,player,store,bindings}/
│  ├─ media/    decide.go policy.go ffmpeg.go probe.go tmpgc.go external.go
│  ├─ player/   machine.go queue.go prefetch.go
│  └─ store/    db.go migrate.go series.go episode.go queue.go history.go cache.go devices.go
├─ frontend/src/{pages,components,bindings,stores}/
├─ data/                      # %LOCALAPPDATA%：app.db、devices/*.json、tmp/<session>/
└─ Taskfile.yml  wails.json  go.mod
```
### 9.2 包依赖方向
`bindings → player/library → media/devices/sdkx → store`；`media` 只吃 URL 与密钥字符串，不 import `sdk`；`store` 不反向依赖上层。
### 9.3 go.mod
```
module <module>/openhongguo-player
go 1.26                                    // 以所选 Wails tag 的 go.mod 为准
require (
    github.com/beilunyang/openhongguo v0.x.y   // 锁精确版本；未发 1.0，次版本可能不兼容
    github.com/wailsapp/wails/v3 v3.1.0        // 锁精确 tag，不用 @latest
    modernc.org/sqlite vX.Y.Z                  // 纯 Go，免 Windows cgo（版本待核验）
)
```
**Go 版本依据（M9）**：v3.0.0-beta.31 的 release notes 起声明 `go 1.26.0`，但迁移文档示例仍写 `1.25.0`，二者不一致 → 写「go 1.26（以 v3.1.0 的 go.mod 为准）」，M0 用 `go list -m -json github.com/wailsapp/wails/v3` 核对；本机 `go1.26.2` 已满足。安装：`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`。SQLite 选 `modernc.org/sqlite` 而非 `mattn/go-sqlite3` 是为免 Windows 额外 gcc 工具链（选型理由，非实测）。
### 9.4 Wails v3 绑定面（符号以此为准）
| 用途 | 正确写法 |
|---|---|
| 应用与服务 | `application.New(application.Options{Name: ..., Services: []application.Service{application.NewService(&Svc{})}})`，随后 `app.Run()` |
| 动态注册 | `app.RegisterService(...)`；**`Run` 之后再注册会被丢弃** |
| 事件 | Go→前端 `app.Event.Emit(name, data...)`；监听 `app.Event.On(name, func(*application.CustomEvent))`；窗口级 `window.EmitEvent(name, data...)` |
| 绑定生成 | `wails3 generate bindings` |
| 静态资源/自定义路由 | `application.Options{Assets: application.AssetOptions{Handler: http.Handler, Middleware: ..., DisableLogging: ...}}`；服务路由 `assets.AttachServiceHandler(route, handler)` |
| 窗口 | `app.Window.New/NewWithOptions/Current`；`Fullscreen/UnFullscreen/ToggleFullscreen/IsFullscreen`、`SetAlwaysOnTop`、`SetMinSize`、`SetMaxSize`、`SetFrameless`、`SetContentProtection` |
| 生命周期 | `OnApplicationEvent(events.Common.ApplicationStarted)`、`app.OnShutdown` |
| 前端运行时 | `@wailsio/runtime` |

**禁止出现的 v2 残留**（评审逐条 grep）：`wails.Run` 配 `options.App` 的旧式入口、`runtime.EventsEmit` 事件写法、从 `wailsjs` 目录导入生成绑定、`wails generate module` 命令。
### 9.5 前端播放入口（M7）
| 容器 | 前端能力 | 结论 |
|---|---|---|
| 单文件 fMP4/MP4（本地服务） | `<video src>` 原生 | **主路径**，无额外依赖 |
| 明文 HLS | WebView2 无原生 HLS；hls.js 支持 Edge Win10+，fMP4 段可直通 MSE | 仅当 B2 真实出现时引入 |
| cenc 加密 HLS | **hls.js 不支持 ClearKey**（README「Not Supported」条目：可识别但无法注入 keyID/key，issue #2934） | 禁止此路线 |
| EME | WebView2 仅 PlayReady、无 Widevine；ClearKey 无权威条目（Microsoft Learn FAQ 404） | **不进主路径**；仅留一条真机验证 `navigator.requestMediaKeySystemAccess('org.w3.clearkey', ...)`，结果记入附录 A |

### 9.6 日志与诊断（p2）
| 项 | 规定 |
|---|---|
| 落点 | `%LOCALAPPDATA%\<app>\logs\app-YYYYMMDD.log` |
| 轮转与保留 | 按日轮转，保留 7 天，超期在启动时删除 |
| 级别 | `info` / `warn` / `error` 三级；error 必带错误码与 `episodeID` |
| 脱敏 | URL 只保留 scheme+host+path，查询串整体替换为 `?…`；设备档案里的 DeviceID/CDID/OpenUDID/OAID **一律打码为前 4 后 4**；`KeyMaterial` 与内容密钥**永不写日志** |
| 导出诊断包 | 设置页一键导出 `logs/` + 池状态快照 + 版本信息为 zip；导出前二次确认，**不打包设备档案原文** |
| `/debug` 页 | 只显示内存环形缓冲（最近 50 条）的实时视图，不读磁盘日志 |

### 9.7 异常恢复（p5）
| 场景 | 处理 |
|---|---|
| SQLite 迁移失败 | 迁移前 `VACUUM INTO` 备份到 `data/backup/app-<ts>.db` → 新建空库 → 启动后提示「数据已备份并重置」并给出备份路径；**不静默删库** |
| 库损坏（打开失败或 `PRAGMA integrity_check` 不通过） | 同上：备份 → 重建 → 提示；history 不可恢复时明确告知 |
| ffmpeg 子进程被外部杀死 | `Wait` 返回错误即判会话失效：关该集本地服务、删半成品、前端回 `ERROR` 并给「重试本集」。**重试不消耗额度**（限同会话且加密集仍持内存密钥；否则重试按 1 台重新解析）；连续 2 次则建议改用 C′ |

## 10 关键流程时序
**10.1 首次播放（B1 主路径）**
```
前端 → Player.Prepare → devices.Acquire → ResolvePlayback → (pb[, ErrDRMProtected])
     → sdk.ContentKey(pb)（纯本地）→ decide(pb)=B1 → spawn ffmpeg(§3.8) 写 tmp.mp4
     → localsrv 返回 http://127.0.0.1:<port>/v/<token>/<ep>.mp4 → <video src>
```
**10.2 剧终自动下一集**
```
timeupdate(remaining<=30s) → 预取下一集 [1 额度，healthy>=2 且已播>=60s 才做]
ended → 写 history(finished) → ENDED → 同会话命中且持密钥? PLAYING(0) : RESOLVING(1)
末集 → series_chained ? 取 queue_item 下一部 : ENDED→IDLE + 提示
```
**10.3 配额耗尽**
```
ResolvePlayback → ErrAnonymousLimited → MarkExhausted → 换槽（≤2）
全失败 → ErrQuotaExhausted → emit quota:exhausted → 播放按钮禁用 + 说明条（0 额度接口仍可用）
```
**10.4 地址过期 re-resolve**
```
Prepare → 命中缓存但 now > ExpiresAt-60s → 删条目 → 完整解析（1 额度）
播放中 direct 源网络错误 → 判失效 → Prepare(force)（受 5min 2 次节流）
```
## 11 里程碑与验收
| 阶段 | 范围 | 验收标准（可执行） |
|---|---|---|
| **M0 打通**（估 2–3 天） | 锁版本、SDK 冒烟、设备池骨架、**形态采样**（附录 A3/A5/A7：≥30 集，统计 protected 比例 p、直链试播成功率 s、codec 分布、单集时长）；池规模实测（§4.7）**为可选，需用户同意后才做** | 三命令复核（go/ffmpeg/mpv，本文已过前两项）；`cmd/probe` 连续解析 3 集并打印槽用量；**跑通一次真实单集解密**（H2 前置）；**输出采样报告 + 一行判定常量已写入 `media.Policy`**（§3.2） |
| **M1 能播**（估 4–6 天） | B1 主路径 + A 快速通道 + 播放页 + C′ | 加密集与未加密集各 1 部：起播成功、任意位置 seek 准确、切清晰度**不新增**槽消耗（用 `Devices.Status` 前后对比证明） |
| **M2 能连看**（估 4–6 天） | 队列、状态机、预取、续播 | 连看 5 集不断流；杀进程重开续播误差 ±3 s；配额耗尽提示正确且不崩 |
| **M3 好用**（估 5–7 天） | 七页 + 多季 + 设置 + 诊断 + 日志与诊断包（§9.6） | 新集发布后手动刷新可见并标 `IsNew`；多季切换不串集；改 ffmpeg 路径立即生效；**日志按日落盘于 `%LOCALAPPDATA%\logs\`、保留 7 天、导出诊断包不含设备档案原文** |
| **M4 打磨**（估 5–7 天） | 窗口四态、快捷键、B2、崩溃兜底、NSIS 打包与首启引导、（可选）libmpv 原型 | 24 h 连续播放不泄漏 ffmpeg 进程与临时文件；迷你↔全屏互转后尺寸约束正确；干净 Win10/11 可启动；**NSIS 安装包在干净 Win10/11 上可安装，首启三步（环境自检→强制免责声明确认→可播集数说明）全部走通，WebView2 Evergreen 缺失时给官方 Bootstrapper 而不静默安装**（§13） |
每阶段末做「配额预算复核」：统计当日 resolve 次数与实际可播集数，偏离 1:1（估算基线）则回 §4.5 重估。
## 12 风险与合规
| # | 风险 | 影响 | 对策 |
|---|---|---|---|
| 1 | Wails v3 周更节奏下的小回归（v3.0.0 GA 2026-10-26、v3.1.0 2026-11-01；minor 随时间推进不代表功能里程碑） | 升级引入回归 | 锁精确 tag；升级走单独 PR 并跑 M1 清单；不用 `@latest` |
| 2 | 服务端无公开文档、随时改版 | 解析失败 | 错误码集中映射；pb 解析失败不 panic，降级「该集暂不可播」；关注上游每日接口契约检查 |
| 3 | 匿名播放配额（2026-10-29 起每设备约一次） | 用户「只能看几集」 | 设备池 + 明确降级文案（§4.6）；启动展示剩余可播集数（估算） |
| 4 | **设备供给假设 H1 未证**，已见风控（429 / 100 FAST_REJECT / 101143 / 101144 / bdturing） | 池可能整体不可用 | §4.7 小样本实测（**需用户明确同意，默认不做**）+ 单设备串行降级；不做夜间批量注册、不做自动重注册 |
| 5 | DRM 与法律边界、平台 ToS | 合规风险 | 只用 SDK 已提供的匿名接口；密钥由 `sdk.ContentKey` 本地计算；**不注册账号、不伪造付费凭证、不破解、不去水印、不批量抓取**；设备池分摊额度的做法在首启与设置页明示「是否违反上游 ToS 由使用者自行判断」（§1.4、§13.3） |
| 6 | ffmpeg 许可（本机为 GPLv3 构建） | 法律风险 | 不分发二进制，运行时探测；随包必须用 LGPL 构建并附 license 与源码获取方式（待核验，需法务确认） |
| 7 | **解密链路在 Windows 无上游实测**（上游 `internal/media` 测试用 `/bin/sh` 脚本模拟播放器；docs/cli.md 平台节称 Windows 仅交叉编译） | 主路径可能整条不通 | M0 第一件事就是跑真实单集解密；不通过则启用 §0.4 的 H2 回退 |
| 8 | 临时文件与磁盘 | 长时播放撑爆磁盘 | 同会话保留最近 N=5 集、**切集不立即删除**（§3.4）+ 每会话独立目录 + 退出清理 + 启动扫遗留 + 总量上限（建议 5 GB，估算）LRU 删除 |
| 9 | 本地 HTTP 服务被本机其他进程访问 | 内容泄露 | 127.0.0.1 + 随机 token + 会话生命周期（§3.4） |
| 10 | 密钥泄露（前端/日志/命令行） | 安全 | I2 红线；C′ 把密钥放命令行是可接受但需明示的降级（§3.7） |
| 11 | 上游 SDK 未发 1.0，次版本可能不兼容 | 升级成本 | 锁版本 + `sdkx` 适配层包住全部 SDK 调用 |
| 12 | **密钥不落库的代价**：进程重启后加密集即使命中缓存也须重解析（1 台） | 用户感觉「缓存没起作用」 | §4.5 单列该行、§5.3 `has_key` 判据、首启说明；如需消除可开启 DPAPI 持久化（默认关闭） |

## 13 构建、分发与首次运行
### 13.1 安装包形态（NSIS vs MSIX）
| 形态 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| **NSIS** | 按用户安装、可自由写 `%LOCALAPPDATA%`、可选安装路径、无 MSIX 签名链成本 | 需自建卸载/更新逻辑；未签名时 SmartScreen 更敏感 | **推荐** |
| MSIX | 系统级安装卸载、支持增量更新与包签名 | 对写入位置与后台进程有沙箱约束（localsrv 监听、ffmpeg/mpv 子进程需逐项验证，待核验） | 暂不选 |
**结论**：选 **NSIS**，按用户安装（不写 `Program Files`），数据落 `%LOCALAPPDATA%\<app>\`。理由：本方案要监听本机端口、拉起 ffmpeg/mpv 子进程、自由读写自有数据目录，MSIX 的容器约束会额外增加验证面。
### 13.2 代码签名与 SmartScreen
| 项 | 结论 |
|---|---|
| 是否必须 | 不强制，但**未签名 exe 首启会触发 SmartScreen「未知发布者」**（**经验描述，未验证**），直接影响首次运行 |
| 建议 | 对外分发时购买 OV/EV 代码签名证书，对 `app.exe` 与安装包**同时签名**；EV 对声誉建立更快 |
| 不签名时 | 安装向导内提供「为什么会出现 SmartScreen 提示」说明页；**不引导用户关闭系统安全功能** |
| 更新包 | 与主体同证书签名，否则更新后声誉归零（§13.5） |

### 13.3 首次运行引导（三步）
| 步 | 内容 | 通过条件 |
|---|---|---|
| 1 | **环境自检**：检测 `ffmpeg`、`mpv`；缺 ffmpeg 时给「去设置页指定路径 / 查看安装说明」，**不自动下载安装**（§3.6 许可结论） | ffmpeg 可用，或用户显式选「稍后再说」（此时只有未加密且编码受支持的内容可播） |
| 2 | **免责声明与合规确认（强制）**：写明「以多份本地匿名设备档案分摊单台设备额度」「是否违反上游 ToS 由使用者自行判断」「仅限个人学习研究」 | 已勾选 |
| 3 | **可播集数说明**：按池内 `healthy` 槽数（或上次实测结果，估算）展示「本机当前约可播放 N 集」，并说明预取也会消耗额度 | 点击「知道了」 |
三步状态存 `settings`；第 1、3 步可跳过，**第 2 步不可跳过**。之后进入书城。
### 13.4 WebView2 运行时策略
| 策略 | 体积 | 更新 | 结论 |
|---|---|---|---|
| **Evergreen**（检测系统 WebView2，缺失时引导官方 Bootstrapper） | 安装包 +0，运行时随 Edge 维护（[Microsoft WebView2 官方文档](https://learn.microsoft.com/microsoft-edge/webview2/)，**以官方文档为准**） | 自动 | **推荐** |
| Fixed Version（内嵌固定运行时） | +100 MB 级（估算） | 需随应用发版 | 仅离线/受控内网环境考虑 |
**结论**：默认 Evergreen + 首启检测；检测失败给官方 Bootstrapper 链接，**不静默安装**。
### 13.5 自动更新
**M4 之后单独立项，本期不做。** 若实现，必须与 §13.2 的代码签名联动（更新包同证书签名 + 校验清单签名），否则等于给 SmartScreen 与供应链风险开口子。
### 13.6 密码学要点
本期**零条**：不落盘密钥、不自建加密容器、不做证书固定。唯一涉及的密码学动作是 SDK 内部的 `spade_a` 本地推导（`sdk.ContentKey`），输出只在进程内存存活（I2）。
## 附录 A 待验证清单（合并 T3 无法核验项）
「架构」列标 ★ 者，结果会改变主路径或分层；其余为实现细节。
| # | 待验证 | 影响 | 方法 | 架构 |
|---|---|---|---|---|
| A1 | 设备池可持续性（首次成功率、封禁命中、单设备上限、重置时点） | 能否「一集一台」 | §4.7 实测 | ★ |
| A2 | ffmpeg 解密链路在 Windows 的可用性 | B1 主路径成立性 | M0 跑真实单集 | ★ |
| A3 | 加密占比 p 与 A 试播成功率 s | `DirectFastPath` 取值 | M0 采样 ≥30 集 | ★ |
| A4 | `<video>` 直链在 WebView2 能否播（编码/容器层面） | A 通道可行性 | 空壳页试播并记录 `error.code` | ★ |
| A5 | `Stream.Codec` 实际取值分布（h264/hevc/av1） | 编码白名单 | **≥30 集（与 A3 同批，一轮采样覆盖三项）** | ★ |
| A6 | `Stream.HLS`/`Dash` 实际出现频率 | B2 是否需要 | 采样统计 | 否 |
| A7 | 短剧单集实际时长分布 | 预取阈值、B1 可行性 | **≥30 集（与 A3 同批，一轮采样覆盖三项）**，记录 `DurationSeconds` | 否 |
| A8 | `Playback.ExpiresAt` 是否总被填充 | 过期判据能否只用它 | 解析 20 集统计 `>0` 比例 | 否 |
| A9 | `Episode.Duration` 是否真被填充（SDK 里是 `json:"-"`） | 预取与进度显示 | M0 打印原始 model | 否 |
| A10 | `default_base_moof` 是否被本机构建支持 | 分片 seek 质量 | `ffmpeg -h muxer=mp4` 查 | 否 |
| A11 | Wails v3 生成器实际产出的符号名 | 前端调用写法 | M0 空壳跑 `wails3 generate bindings` | 否 |
| A12 | **v3.1.0 下尺寸约束与全屏/最大化的真实交互**（#6387/#6388 与旧文档表述冲突，见 R3） | §8.5 全屏与迷你的尺寸恢复写法 | M0 逐项试：普通→全屏→退出→迷你→退出，检查 min/max 是否被清空 | 否 |
| A13 | ffmpeg 解密单集耗时与 CPU 占用 | B1 起播是否可接受 | 实测 720p 单集 | 否 |
| A14 | `SeriesRecommend` 结果稳定性 | 「剧终接下一部」是否只能靠队列 | 同剧连调 3 次比对 | 否 |
| A15 | `Platform==0` 判定是否稳定 | 非视频内容拦截 | 采样 10 部非视频 | 否 |
| A16 | WebView2 ClearKey 可用性（T3 无法核验项） | 仅记录 | 真机执行 `navigator.requestMediaKeySystemAccess('org.w3.clearkey', ...)` | 否 |
| A17 | mpv `--wid` 在 Windows 与 Wails v3 窗口句柄的兼容性（T3 无法核验项） | C 方案是否立项 | 有原型需求时再验 | 否 |
| A18 | 上游接口契约检查能否提前预警改版 | 是否需本地契约测试 | 读 `.github/workflows/contract.yml` 并订阅状态 | 否 |

## 附录 B 术语表
| 术语 | 定义 |
|---|---|
| 短剧 / 分集 | 一部剧一个短剧 ID；剧中每集有分集 ID，序数写作「第 1 集」 |
| 季 / 合集 | 多季短剧的每一季本身是一部短剧；各季组成一个合集（album） |
| 匿名设备 | SDK 注册的虚拟手机，所有请求以它发出，无需登录 |
| 设备档案 | 保存匿名设备信息的 JSON（`anonymous.json` 或本方案的 `devices/dNNN.json`），含 CDID/OpenUDID/ClientUDID/OAID/ReqID/DeviceID 等 |
| 播放模型 | 一个分集的播放信息：各清晰度地址、编码、码率、有效期、是否加密 |
| 内容密钥 | 解密 cenc-aes-ctr 分集的 16 字节 AES 密钥，以 32 位 hex 表示 |
| spade_a | 服务端包装过的密钥材料，客户端据此本地推导内容密钥 |
| 额度 | 服务端对单台匿名设备约只回一次播放模型的限制 |
| 设备槽 slot | 本方案中「一份独立设备档案 + 一个 sdk.Client」的组合单位 |
| A / B1 / B2 / C / C′ / D | 见 §3.1 的六种播放实现方案 |
| 渐进 fMP4 | 以 `frag_keyframe+empty_moov` 输出、可边写边播的分片 MP4 |
| 快速通道 | 未加密且编码受支持时直接用 CDN 直链播放的可关闭捷径（A） |
| 逃生舱 | 主路径全部失败时由用户手工指定的外部播放器（C′） |
