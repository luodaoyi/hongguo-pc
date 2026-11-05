# 红果桌面播放器 · 接口契约（T7 产出）

| 项 | 值 |
|---|---|
| 日期 | 2026-11-05 |
| 依据 | `docs/hongguo-player-plan.md` v3（本文件不改其设计，只把签名钉死） |
| 已实现 | `internal/model`、`internal/sdkx`、`internal/devices`、`internal/store`、`cmd/probe` |
| 待实现（本文件只给签名） | `internal/media`（T8）、`internal/localsrv`（T8）、`internal/player` + `internal/library`（T9）、`internal/bindings` + `frontend`（T10） |
| 实测依赖 | `github.com/beilunyang/openhongguo v0.2.0`（tag 指向 `42b73f2`，与方案一致）；`github.com/wailsapp/wails/v3 v3.1.0`；`modernc.org/sqlite v1.61.1`；`go 1.26.0`（go 命令把 `go 1.26` 规范化为 `1.26.0`） |

## 1 包边界（硬规矩）

依赖方向（方案 §2.3，单向）：

```
bindings → player/library → media / localsrv → devices → sdkx → sdk
                                        ↘        ↓
                                          store（只依赖 internal/model）
```

| 包 | 可以做 | 禁止 |
|---|---|---|
| `internal/sdkx` | 构造 sdk.Client、归一错误、脱敏日志、SDK 模型 → DTO | 决定用哪台设备；持有设备池状态 |
| `internal/devices` | 设备池、单飞、冷却、记账、内存密钥表 | 碰 UI / Wails；把密钥传给媒体层之外的任何地方 |
| `internal/store` | 七表 SQL、迁移、缓存、断点、槽记账 | 存内容密钥；反向 import 上层 |
| `internal/media`（T8） | 策略判定、ffmpeg 进程、临时文件生命周期 | **import `openhongguo/sdk` 或 `internal/sdkx`**；认识 Playback 类型；自己解析 URL 之外的业务 |
| `internal/localsrv`（T8） | 127.0.0.1 随机端口、token、Range | 对外网暴露；持有密钥（它只端已解密的文件） |
| `internal/player`、`internal/library`（T9） | 队列、状态机、预取、续播 | 直接开 ffmpeg；裸调 `sdkx.Client.ResolvePlayback` |
| `internal/bindings`（T10） | DTO、服务方法、事件发射 | **碰密钥**：不得 import `devices.Keyring`、不得读 `Playback.KeyMaterial`、不得把 `KeyHex` 放进任何返回值或事件 |

三条不变量（方案 §2.2）在代码里的落点：

- **I1**（一个 Playback 只对应一次 Resolve）：`devices.Pool.ResolvePlayback` 先查缓存，再走额度；
- **I2**（密钥绝不外流）：密钥只存在 `devices.Keyring` 的 `map[string]string`，唯一的出口是 `Pool.Key(episodeID)`，只允许喂给 `media.Input.KeyHex`。`store.CachePut` 前还有一道防御：payload 里出现 `KeyMaterial` 就拒绝写库；
- **I3**（额度必经池）：只有 `devices` 包调用 `sdkx.Client.ResolvePlayback`；其余层用 `Pool.ResolvePlayback` / `Pool.Do`。

## 2 已实现签名（可直接对接）

### 2.1 `internal/model`（纯 DTO，无第三方 import）

```go
type SeriesDTO struct { ID, Title, CoverURL string; EpisodeCount int
    Status, PayType string; Platform int; RightText string
    IsMultiSeason bool; SeasonIndex int; AlbumID string; FetchedAt int64 }
type EpisodeDTO struct { ID, SeriesID string; Number int; Title string
    DurationSec int; Playable, IsNew bool; CoverURL string }
type SourceDTO struct { Mode, URL, MIME string; ExpiresAt int64 }   // Mode: direct | local
type QueueItemDTO struct { Pos int; SeriesID, SeriesTitle, EpisodeID string; StartAt int }
type HistoryDTO struct { SeriesID, EpisodeID string; EpisodeNumber int
    PositionSec float64; DurationSec int; Finished bool; UpdatedAt int64 }
type SeriesPageDTO struct { Items []SeriesDTO; Cursor string; HasMore bool }
type SeasonDTO / AlbumDTO / EpisodeDiff
type StreamInfo struct { Definition, Quality, Codec string; Width, Height int
    Bitrate int64; HLS, Dash, Protected bool; Encryption string }   // 刻意不含 URL
type PlaybackInfo struct { EpisodeID, VideoID, MediaType string; DurationSec int
    Protected bool; ExpiresAt int64; HasAudio, KeyInMemory bool; Streams []StreamInfo }
type SlotStatus / DevicesStatus   // 对应 Devices.Status（方案 §8.3）
```

字段约定：`Status` 取 `updating|finished|updated_today|stopped|unknown`；
`PayType` 取 `free|vip|single|unknown`；`Platform` 为 `-1` 表示只拿到列表、平台未知，
`0` 表示书 / 有声书等非视频内容（方案 F13）。

### 2.2 `internal/sdkx`（SDK 唯一出口）

```go
type Options struct { ProfilePath string; RetryAttempts int
    APIOrigin, RegisterOrigin, DistributionOrigin string
    Logger sdk.Logger; OnDeviceRejected func(err error) }
func New(opts Options) (*Client, error)
func (c *Client) ProfilePath() string

// 目录（0 额度）
func (c *Client) Search(ctx, query string, opts SearchOptions) ([]sdkmodel.SeriesSummary, string, bool, error)
func (c *Client) Discover(ctx, opts DiscoverOptions) ([]sdkmodel.SeriesSummary, string, bool, error)
func (c *Client) GetSeries(ctx, seriesID string) (*sdkmodel.Series, error)
func (c *Client) ListEpisodes(ctx, seriesID string) ([]sdkmodel.Episode, error)
func (c *Client) Seasons(ctx, seriesID string) (*sdkmodel.Album, error)
func (c *Client) SeriesRecommend(ctx, seriesID string) (*sdkmodel.Recommend, error)

// 播放（1 额度，只有 devices 可以调用）
func (c *Client) ResolvePlayback(ctx, episodeID string) (*sdkmodel.Playback, error)
func (c *Client) ResetDevice(ctx) error
func (c *Client) ContentKey(pb *sdkmodel.Playback) (hex string, err error)

// 错误归一
func Classify(err error) (code, userMsg string, retryable bool)
func RetryAfter(err error, fallback time.Duration) time.Duration
func IsRetryable(err error) bool
```

> 注：`sdkmodel` = `github.com/beilunyang/openhongguo/sdk/model`。设备池与播放模型必须用它的原始类型：
> 媒体层要读 `Stream.URL` / `AudioURL`，密钥推导要读 `KeyMaterial`。

`Options` 里 **`DeviceResets` 恒为 0**（方案 §4.2），没有开关：SDK 自动换设备会让池的记账与真实档案脱节。

错误码（`Classify` 的 `code`）：

| code | 触发 | 用户文案（摘） | retryable |
|---|---|---|---|
| `ok` | err == nil | — | false |
| `anonymous_limited` | `ErrAnonymousLimited` | 匿名请求返回空结果：该内容暂无可播放数据，或该设备的额度已用尽 | false |
| `device_rejected` | `ErrDeviceRejected` | 匿名设备被服务端拒绝，正在更换设备 | false |
| `rate_limited` | `ErrRateLimited` | 请求过于频繁，请稍后重试 | true |
| `content_unavailable` | `ErrContentUnavailable`（101001 等） | 该内容已下架或暂不可用 | false |
| `auth_required` | `ErrAuthenticationRequired` | 该内容需要登录 | false |
| `challenge_required` | `ErrChallengeRequired` | 服务端要求验证码 | false |
| `drm_protected` | `ErrDRMProtected` | 加密内容（正常状态） | false |
| `not_protected` | `ErrNotProtected` | 该分集未加密 | false |
| `transport` | `ErrTransport` | 网络请求失败 | true |
| `signature_rejected` | `ErrSignatureRejected` | 请求签名被拒绝 | false |
| `param_invalid` / `end_of_list` / `invalid_options` / `canceled` / `unknown` | 对应哨兵 | 见代码 | 视情况 |

`sdkx.KeyUnavailableError{EpisodeID, Err}` 表示「模型拿到了、密钥解不开」，
对应方案 §6.3 的「该集加密方式暂不支持」，上层应跳过而不是重试。

### 2.3 `internal/devices`（设备池）

```go
type Config struct { Dir string; Slots, MaxPerResolution int
    CoolDown, QuotaWindow time.Duration
    Store store.Store; Now func() time.Time; Logf func(string, ...any)
    NewClient func(profilePath string, onRejected func(error)) (*sdkx.Client, error) }
func NewPool(cfg Config) (*Pool, error)

// 播放解析：唯一会消耗额度的入口
func (p *Pool) ResolvePlayback(ctx context.Context, episodeID string) (*sdkmodel.Playback, error)

// 目录调用：取一个槽跑只读回调，单飞与记账与播放共用
func (p *Pool) Do(ctx context.Context, name string, fn func(ctx context.Context, c *sdkx.Client) error) error

// 记账原语
func (p *Pool) Acquire(ctx context.Context) (*Slot, error)
func (p *Pool) Release(s *Slot)
func (p *Pool) Commit(s *Slot)                       // 成功一次 = 1 次额度；成功后槽立即转 exhausted——1 次成功解析就耗掉该设备全部额度（方案 §4.1；见 T14 修复）
func (p *Pool) MarkExhausted(s *Slot)                // ErrAnonymousLimited
func (p *Pool) MarkCooling(s *Slot, d time.Duration) // ErrDeviceRejected 后
func (p *Pool) NoteDeviceRejected(s *Slot)           // sdkx 钩子落点（非播放换档）
func (p *Pool) Reset(ctx context.Context, slotID string) error  // §8.3 Devices.Reset

func (p *Pool) Status() model.DevicesStatus
func (p *Pool) Keys() *Keyring
func (p *Pool) Key(episodeID string) (hex string, ok bool)

var ErrQuotaExhausted, ErrRateLimitedLong
```

`ResolvePlayback` 的行为逐条对齐方案 §4.4：

1. 缓存命中（未过期 + 持内存密钥 或 未加密）→ 0 额度返回；
2. 最多 `MaxPerResolution`（默认 2）次尝试，`Acquire` 按 `last_used` 最久优先、同槽单飞；
3. `ErrAnonymousLimited` → `MarkExhausted` 换下一台；`ErrDeviceRejected` → `ResetDevice` + 冷却 10 分钟换下一台；
   `ErrRateLimited` → `RetryAfter` ≤10 s 才等，>10 s 返回 `ErrRateLimitedLong`；
4. `err == nil` 或 `(ErrDRMProtected 且 pb != nil)` → `Commit` + 写缓存 + 返回；
5. 走完仍无解 → `ErrQuotaExhausted`。

**额度窗口**：跨本地日，或距 `last_used` 满 24 小时，即视为新额度窗口并清零 `quota_used`
（`devices.newQuotaWindow`）。方案附录 A1 明确「服务端真实重置时点未实测」，这是保守复原，
实测结论回来只改这一个函数。

`NoteDeviceRejected` 与方案 §4.4 M1 补充的差异见 §5。

### 2.4 `internal/store`（SQLite 七表）

```go
func Open(path string) (Store, error)

type Store interface {
    Close() error
    Recovered() string                       // 迁移失败重建时用的备份路径，空串=正常
    UpsertSeries(ctx, model.SeriesDTO) error
    GetSeries(ctx, id string) (*model.SeriesDTO, error)
    Episodes(ctx, seriesID string) ([]model.EpisodeDTO, error)
    ReplaceEpisodes(ctx, seriesID string, fresh []model.EpisodeDTO) (model.EpisodeDiff, error)
    MarkRemoved(ctx, seriesID string, ids []string) error
    HistoryList(ctx, limit int) ([]model.HistoryDTO, error)
    HistoryGet(ctx, seriesID string) (*model.HistoryDTO, error)
    HistoryPut(ctx, model.HistoryDTO) error
    HistoryClear(ctx) error
    CacheGet(ctx, episodeID string) (*CacheEntry, error)
    CachePut(ctx, CacheEntry) error
    CacheDelete(ctx, episodeID string) error
    CachePurgeExpired(ctx, now int64) (int, error)
    DeviceSlots(ctx) ([]DeviceSlotRecord, error)
    DeviceSlotPut(ctx, DeviceSlotRecord) error
}
```

- 七表与索引严格照方案 §5.3：`series`、`episode`、`"queue"`、`queue_item`、`history`、
  `playback_cache`、`device_slot` + `idx_ep_series(series_id, number)`；
- `episode` 只有 `id` 主键，`number` 是普通索引；集数变动时先按 id 删旧行再插入；
- 迁移用 `PRAGMA user_version`（当前 v1）。版本高于本实现时**拒绝操作**；迁移失败时
  `VACUUM INTO` 备份到 `<db>.bak-<时间戳>`，再删表重建，路径经 `Recovered()` 暴露；
- `playback_cache.payload` 是 Playback 的 JSON，**不含密钥**；`has_key` 只是「本次会话是否
  有内存密钥」的布尔快照。

`"queue"` / `queue_item` 两张表本期只建表，队列读写方法留给 T9 补（方案 §5.4 的语义属于 player 层）。

## 3 待实现层的预期签名（T8 / T9 按此对接，可微调但要回改本文件）

### 3.1 `internal/media`（T8）

```go
// 方案 §3.2 的单常量开关
type Policy struct {
    DirectFastPath   bool
    DirectSkipCodecs map[string]bool
}
var Current = Policy{DirectFastPath: true, DirectSkipCodecs: map[string]bool{}}

// Input 是媒体层的唯一输入：**只有 URL 与密钥串**，不 import sdk / sdkx。
type Input struct {
    EpisodeID string
    SourceURL string   // Stream.URL
    AudioURL  string   // pb.AudioURL，空表示单文件 MP4
    KeyHex    string   // 32 位小写 hex；未加密为空。来自 devices.Keyring
    Protected bool
    HLS, Dash bool
}

type Mode string // "direct" | "decrypted"

type Decision struct {
    Mode     Mode
    Fallback bool   // 直链失败后同 pb 走 B1（§3.2 运行时兜底，不额外耗额度）
}

type Decider struct{ /* Policy */ }
func (d Decider) Decide(in Input, expiresAt int64, now time.Time) Decision
func (d Decider) CodecOK(codec string) bool   // h264/avc1 + aac 为真

type SessionConfig struct {
    FFmpegPath string    // 运行时探测，不随包分发（§3.6）
    Dir        string    // 每会话独立目录，退出清空
    KeepLast   int       // 默认 5 集
    MaxBytes   int64     // 默认 5 GB，LRU 淘汰
}
func NewSession(cfg SessionConfig) (*Session, error)
func (s *Session) Start(ctx context.Context, in Input) (Output, error) // 拉起 ffmpeg，渐进 fMP4
func (s *Session) Close() error
type Output struct { Path string; SizeAt func() int64 }  // Path 供 localsrv 端出去
```

约束：`+faststart` 与分片输出互斥（§3.8 硬约束 ①）；增长中文件的 Range 只到已写前沿、
越界回 416 且带最新长度、禁止 chunked（硬约束 ②）。

### 3.2 `internal/localsrv`（T8）

```go
type Config struct { Root string }                       // 临时文件根目录
func New(cfg Config) (*Server, error)
func (s *Server) URL(episodeID, filePath string) (string, error) // /v/<32字节token>/<episodeID>.mp4
func (s *Server) Remove(episodeID string)
func (s *Server) Close() error
```

`127.0.0.1:0` 系统分配端口；token 失败直接 403 不解释；不写明文 URL 日志；去掉 `Server` 头。

### 3.3 `internal/player` / `internal/library`（T9）

```go
type State string // idle|resolving|playing|ended|next|error（与方案 §5.2 一致）

type PrepareResult struct {
    Source    model.SourceDTO
    Qualities []model.StreamInfo
    Info      model.PlaybackInfo
    State     State
}

func (m *Machine) Prepare(ctx context.Context, episodeID, preferQuality string) (PrepareResult, error)
func (m *Machine) SwitchQuality(ctx context.Context, quality string) (model.SourceDTO, error)
func (m *Machine) ReportProgress(ctx context.Context, episodeID string, positionSec float64) error
func (m *Machine) Ended(ctx context.Context) error
func (m *Machine) Next(ctx context.Context) (string, error)
```

`Prepare` 的编排固定为：

```go
pb, err := pool.ResolvePlayback(ctx, episodeID)   // 1 额度，或缓存命中 0
stream, err := pb.Select(preferQuality)           // 方案 F6：选流只委托 SDK
keyHex, _ := pool.Key(episodeID)                  // 只在内存，只喂 media.Input
in := media.Input{EpisodeID: episodeID, SourceURL: stream.URL, AudioURL: pb.AudioURL,
                  KeyHex: keyHex, Protected: pb.Protected, HLS: stream.HLS, Dash: stream.Dash}
switch decider.Decide(in, pb.ExpiresAt, now) {
case "direct":  src = model.SourceDTO{Mode: "direct", URL: stream.URL, MIME: "video/mp4", ExpiresAt: pb.ExpiresAt}
case "decrypted": out, _ := session.Start(ctx, in); url, _ := srv.URL(episodeID, out.Path)
                  src = model.SourceDTO{Mode: "local", URL: url, MIME: "video/mp4"}
}
```

## 4 `cmd/probe`（M0 冒烟工具）

```
go run ./cmd/probe [-data 目录] [-slots N] [-timeout 90s] search <关键词>
go run ./cmd/probe ............ list <seriesID>
go run ./cmd/probe ............ resolve <episodeID>
go run ./cmd/probe ............ status
```

每次调用后打印槽用量（`healthy/cooling/exhausted/rejected`、今日已用、非播放换档、每槽明细）。
播放摘要走 `model.PlaybackInfo`：只有清晰度、codec、是否加密、有效期与「内存是否持密钥」，
**字符串输出里没有任何密钥或密钥材料**。

## 5 与方案的偏差（逐条）

| # | 偏差 | 原因 |
|---|---|---|
| 1 | `SeriesDTO` 比 §5.1 多一个 `Platform int`，约定 `-1` = 只有列表数据、未知 | §7.7 的「这是书籍/有声书」拦截依赖 F13 的 `Platform==0`，DTO 无此字段就无从判定 |
| 2 | `HistoryDTO` 比 §5.1 多一个 `DurationSec int` | §5.3 的 `history` 表本来就有 `duration_sec` 列，DTO 不带会把该列写死为 0 |
| 3 | `sdkx` 目录方法返回 SDK 模型类型（`sdkmodel.*`），DTO 转换集中在 `sdkx/convert.go` | `sdkx` 是 SDK 唯一出口，上层不该再 import sdk；转换函数单列一处，T10 想挪到 bindings 只改调用点 |
| 4 | `store.Store` 多一个 `Recovered() string` | 「迁移失败→备份→重建」不能让数据静默消失，调用方要能拿到备份路径提示用户 |
| 5 | `store` 不提供 `"queue"`/`queue_item` 的读写方法 | 队列语义属 §5.4 / player 层；表已在 v1 schema 建好，T9 补方法即可（接口只增不改） |
| 6 | `devices.Pool` 除 `ResolvePlayback` 外还导出 `Do` / `Reset` / `Acquire` 等 | `Do` 给目录接口一个「也必须经过池」的入口（否则 §4.4 M1 的非播放换档无处记账）；`Reset` 是 §8.3 `Devices.Reset` 的落点。**发播放解析的入口仍然只有 `ResolvePlayback` 一个** |
| 7 | `NoteDeviceRejected` 只记拒绝次数 + 冷却，不直接 `ResetDevice` | 方案 §4.4 M1 写的是「SDK 自己换了设备，池要跟着记」；但本方按 §4.2 把 `DeviceResets` 关成 0，SDK 不会再自换，所以池要自己承担换档案：播放路径在 `ErrDeviceRejected` 时显式 `ResetDevice`（伪码原样），目录路径只记账并冷却，等 `Pool.Reset` 或冷却结束后再试 |
| 8 | `go.mod` 的 `go 1.26` 被 go 命令写成 `go 1.26.0` | Go 工具链的规范化行为，语义等价 |
| 9 | `playback_cache.series_id` 由 `devices` 写入时留空 | 本层只拿得到 `episodeID`；T9 刷新目录时可以补写（`CachePut` 已支持该列） |

## 6 验证状态（诚实口径）

真跑过（2026-11-05，本机）：

- `go build ./...`、`go vet ./...`、`gofmt -l .`；
- SQLite 冒烟：`store.Open` + 迁移 + 七表、`UpsertSeries`/`GetSeries`、
  `ReplaceEpisodes` 两次增量（新增 / 改名改集数 / 软删 / 新集标记）、`MarkRemoved`、
  History 读写清空、Cache 读写与过期清理、DeviceSlot 读写、关库重开数据仍在；
- 迁移失败路径：预置一个缺列的库 → `VACUUM INTO` 备份成功（12288 字节）→ 删表重建 → 七表可用；
- 设备池离线冒烟：选槽顺序（`last_used` 最久优先）、同槽单飞（槽占满时 `Acquire` 等到 ctx 超时）、
  `MarkExhausted` 后 `ErrQuotaExhausted`、记账落库、25 小时后额度窗口复原、
  `NoteDeviceRejected` 进冷却且 11 分钟后恢复；
- 以上冒烟程序是临时文件，验证后已删除，不在交付树里。

**没跑过**：任何真实网络请求（`search`/`list`/`resolve` 未对上游发过包，不消耗真实设备额度）、
ffmpeg 解密链路、Wails 构建。这些属 T8/M0 的实测项，不要当作已验证。
