// Package media 是解密播放的主路径（方案 §3.2 / §3.8）：策略判定 + ffmpeg 子进程
// + 临时文件生命周期。
//
// 两条硬边界（contracts.md §1）：
//   - 不 import openhongguo/sdk 或 internal/sdkx：本包只认 URL、codec 与 32/64 位
//     hex 密钥，播放模型到 Input 的转换在 internal/player 完成。
//   - 密钥只允许存在于内存与 ffmpeg 子进程 argv 中（I2）：绝不写日志、写文件、
//     进错误信息或事件的载荷。
package media

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy 是方案 §3.2 的单常量开关：采样后只改这一处。
type Policy struct {
	// DirectFastPath 为真时，未加密且编码受支持的流先试 CDN 直链（A 通道）。
	DirectFastPath bool
	// DirectSkipCodecs 是实测 WebView2 不支持的编码集合，命中即走 B1。
	DirectSkipCodecs map[string]bool
}

// DefaultPolicy 返回出厂默认：直链快速通道开，尚无实测跳过编码。
func DefaultPolicy() Policy {
	return Policy{DirectFastPath: true, DirectSkipCodecs: map[string]bool{}}
}

// Current 是运行时生效的策略；T9 装配时用它构造 Decider。
var Current = DefaultPolicy()

// Mode 是播放通道。"direct" = CDN 直链（A）；"decrypted" = 本地解密（B1）。
type Mode string

const (
	ModeDirect    Mode = "direct"
	ModeDecrypted Mode = "decrypted"
)

// MIMEVideoMP4 是两条通道共用的 MIME（方案 F20：容器是单文件 MP4）。
const MIMEVideoMP4 = "video/mp4"

// Input 是媒体层的唯一输入：只有 URL 与密钥串，不含任何 SDK 类型。
//
// 字段与 contracts.md §3.1 一致；Codec 是补充字段——codec 白名单判定离不开它，
// 契约签名当时漏列（已在交付报告里说明）。
type Input struct {
	EpisodeID string
	SourceURL string // Stream.URL
	AudioURL  string // pb.AudioURL，空表示单文件 MP4
	KeyHex    string // 32/64 位 hex；未加密为空。只来自 devices.Keyring
	Protected bool   // pb.Protected
	HLS       bool   // Stream.HLS
	Dash      bool   // Stream.Dash
	Codec     string // Stream.Codec，用于白名单判定
}

// String 让 %v 打印 Input 时不泄露密钥与签名地址。
func (in Input) String() string {
	key := "<none>"
	if in.KeyHex != "" {
		key = "<redacted>"
	}
	return fmt.Sprintf("media.Input{episode=%s protected=%v hls=%v dash=%v codec=%s audio=%v key=%s src=<redacted>}",
		in.EpisodeID, in.Protected, in.HLS, in.Dash, in.Codec, in.AudioURL != "", key)
}

// Decision 是一次通道判定结果。
type Decision struct {
	Mode Mode
	// Fallback 为真表示：直链失败后可用同一个播放模型改走 B1（§3.2 运行时兜底，
	// 不额外耗额度）。仅在 Mode==ModeDirect 时为真。
	Fallback bool
	// NeedsResolve 为真表示地址已过过期门（ExpiresAt>0 且剩余不足 1 分钟），
	// 上层应重新解析（代价 1 台设备，§4.5）。M6 的节流由 player 层负责。
	NeedsResolve bool
}

// Decider 按 Policy 判定通道。零值 Decider{Policy: Policy{}} 恒走 B1（安全侧）。
type Decider struct {
	Policy Policy
}

// NewDecider 用给定策略构造判定器；p.DirectSkipCodecs 为 nil 时补空表。
func NewDecider(p Policy) Decider {
	if p.DirectSkipCodecs == nil {
		p.DirectSkipCodecs = map[string]bool{}
	}
	return Decider{Policy: p}
}

// ErrExpiredAddress 表示播放地址已到过期门内，必须重新解析才能继续使用（§4.5）。
var ErrExpiredAddress = errors.New("media: 播放地址已临期，需要重新解析")

// expiredMargin 是方案 F15 的过期门：剩余有效期不足 1 分钟即视为过期。
const expiredMargin = 60 * time.Second

// Expired 判断地址是否进入过期门。ExpiresAt<=0 表示服务端没给有效期，永不过期。
func Expired(expiresAt int64, now time.Time) bool {
	if expiresAt <= 0 {
		return false
	}
	return now.Add(expiredMargin).Unix() > expiresAt
}

// ExpiredAt 是过期门的错误形态：返回 nil 或 ErrExpiredAddress。
func ExpiredAt(expiresAt int64, now time.Time) error {
	if Expired(expiresAt, now) {
		return ErrExpiredAddress
	}
	return nil
}

// Decide 按方案 §3.2 的顺序判定：过期门 → Protected → Dash/HLS → AudioURL →
// codec 白名单 → DirectFastPath。任一条件命中走 B1，全不命中才走 A。
//
// HLS 与 Dash 同列：真实 HLS 无法直接喂给 WebView2 的 <video>，且 B2 本期不实现，
// 故一律落到 B1（偏差已报）。
func (d Decider) Decide(in Input, expiresAt int64, now time.Time) Decision {
	dec := Decision{Mode: ModeDecrypted}
	if Expired(expiresAt, now) {
		dec.NeedsResolve = true
	}
	switch {
	case in.Protected:
		// 加密集只能解密播放。
	case in.Dash || in.HLS:
		// DASH / HLS 只能由 ffmpeg 拉流解密后本地重封装。
	case in.AudioURL != "":
		// 独立音轨必须由 ffmpeg 合并。
	case !d.CodecOK(in.Codec):
		// 编码不在白名单，直链在 WebView2 里大概率放不出来。
	case !d.Policy.DirectFastPath:
		// 采样判定要求关闭快速通道。
	default:
		dec.Mode = ModeDirect
		dec.Fallback = true
	}
	return dec
}

// CodecOK 判定 codec 是否值得试直链：h264/avc1 与 aac/mp4a 为真；
// hevc/h265/vp9/av1 及未知编码一律为假（等 M0 实测 WebView2 系统解码器）。
func (d Decider) CodecOK(codec string) bool {
	c := normalizeCodec(codec)
	if c == "" {
		return false
	}
	if d.Policy.DirectSkipCodecs[c] {
		return false
	}
	switch {
	case c == "h264", c == "avc1", strings.HasPrefix(c, "avc1."):
		return true
	case c == "aac", c == "mp4a", strings.HasPrefix(c, "mp4a."):
		return true
	default:
		return false
	}
}

// normalizeCodec 统一大小写并截掉空格后的备注（如 "h264 main"）。
func normalizeCodec(codec string) string {
	c := strings.ToLower(strings.TrimSpace(codec))
	if i := strings.IndexAny(c, " \t"); i >= 0 {
		c = c[:i]
	}
	return c
}
