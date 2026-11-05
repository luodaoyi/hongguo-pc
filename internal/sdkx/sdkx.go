// Package sdkx 是 openhongguo SDK 的唯一出口（方案 §2.1 的 L0 层）。
//
// 职责：构造 sdk.Client、归一错误码、脱敏日志、重试策略。上层（internal/devices、
// internal/library、internal/player）**不得**直接 import github.com/beilunyang/openhongguo/sdk，
// 也不得裸调 ResolvePlayback：任何额度消耗必须经 internal/devices.Pool 记账（方案 §2.2 I3）。
//
// 唯一例外是 SDK 的数据类型（sdk/model）：Playback / Stream 需要原样传给媒体层，
// 本包不为它们再造一层副本。类型别名见 Playback 等（media.go）。
//
// 关键决定（方案 §4.2）：DeviceResets 恒为 0，关闭 SDK 的自动换设备。SDK 一旦
// 越过设备池自行换档，池里的 QuotaUsed / LastUsed / CoolUntil 就与真实档案脱节。
// 换设备只由 internal/devices 显式调用 Client.ResetDevice 触发。
package sdkx

import (
	"errors"
	"fmt"
	"strings"

	"github.com/beilunyang/openhongguo/adapters"
	"github.com/beilunyang/openhongguo/sdk"
)

// DefaultRetryAttempts 是单个请求最多执行的次数（方案 §4.2 取值 2）。
// SDK 内部对限流 / 传输错误 / 5xx 会重发同一已签名请求；退避上限 10 秒（F16）。
const DefaultRetryAttempts = 2

// Options 是构造一个设备客户端所需的全部参数。
//
// 每个设备槽用独立的 Options.ProfilePath 建一个 Client：一份设备档案 + 一个 client。
type Options struct {
	// ProfilePath 是该槽的设备档案文件路径（形如 data/devices/d001.json），必填。
	ProfilePath string
	// RetryAttempts 缺省（0）时取 DefaultRetryAttempts。
	RetryAttempts int
	// APIOrigin / RegisterOrigin / DistributionOrigin 留空时用 SDK 的默认生产主机。
	APIOrigin          string
	RegisterOrigin     string
	DistributionOrigin string
	// Logger 接收 SDK 的脱敏诊断行（内容只有 HTTP 方法与脱敏 URL）。
	Logger sdk.Logger
	// OnDeviceRejected 在任一 SDK 调用返回 ErrDeviceRejected 时回调（方案 §4.4 M1
	// 补充：该错误在任何接口都会导致设备档案失效）。设备池用它记「拒绝次数」。
	// 回调在调用方 goroutine 上同步执行，不得阻塞。
	OnDeviceRejected func(err error)
}

// Client 包住一个 sdk.Client，对外只暴露已归一的方法。
//
// Client 可并发使用（与 SDK 的约定一致）；同槽单飞由设备池保证。
type Client struct {
	sdk         *sdk.Client
	profilePath string
	onRejected  func(error)
}

// New 按 opts 创建一个设备客户端。创建时不发请求：匿名设备在首次读取时才注册。
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.ProfilePath) == "" {
		return nil, fmt.Errorf("%w: profile path is required", ErrInvalidOptions)
	}
	attempts := opts.RetryAttempts
	if attempts <= 0 {
		attempts = DefaultRetryAttempts
	}
	inner, err := adapters.NewClient(opts.ProfilePath, sdk.Options{
		// Transport / ProfileStore 留空：adapters 会填 httptransport 与
		// filestore(profilePath)，即一份独立的设备档案文件。
		Logger:             opts.Logger,
		APIOrigin:          opts.APIOrigin,
		RegisterOrigin:     opts.RegisterOrigin,
		DistributionOrigin: opts.DistributionOrigin,
		RetryAttempts:      attempts,
		// 方案 §4.2：绝不交给 SDK 自动换设备。
		DeviceResets: sdk.Int(0),
	})
	if err != nil {
		return nil, err
	}
	return &Client{sdk: inner, profilePath: opts.ProfilePath, onRejected: opts.OnDeviceRejected}, nil
}

// ProfilePath 返回该客户端的设备档案路径，设备池据此记账与排障。
func (c *Client) ProfilePath() string { return c.profilePath }

// noteDeviceRejected 在调用失败时按需触发池的记账钩子，并原样返回 err。
func (c *Client) noteDeviceRejected(err error) error {
	if err != nil && errors.Is(err, ErrDeviceRejected) && c.onRejected != nil {
		c.onRejected(err)
	}
	return err
}
