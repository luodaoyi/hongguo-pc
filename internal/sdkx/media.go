package sdkx

import (
	"context"
	"fmt"

	sdkmodel "github.com/beilunyang/openhongguo/sdk/model"

	"github.com/beilunyang/openhongguo/sdk"
)

// Playback / Stream 是 SDK 数据类型的别名。上层（媒体层、设备池）只 import sdkx，
// 不必知道 SDK 的包路径；媒体层拿到的仍是原始模型，Stream.URL 可通。
type (
	// Playback 是 SDK 的播放模型。KeyMaterial 带 json:"-"，永不跨 JSON 边界。
	Playback = sdkmodel.Playback
	// Stream 是播放模型的一条清晰度。
	Stream = sdkmodel.Stream
)

// ResolvePlayback 解析一集的播放模型。
//
// **只有 internal/devices.Pool 可以调用它**：每次调用消耗一台匿名设备的一次额度
// （方案 §4.5 / I3）。加密集返回 (pb, ErrDRMProtected) 双值，调用方不得丢弃模型（F3）。
//
// 传 `sdk.PlaybackOptions{}` 空选项：SDK 的该类型是空结构体占位（F7）。
func (c *Client) ResolvePlayback(ctx context.Context, episodeID string) (*sdkmodel.Playback, error) {
	pb, err := c.sdk.ResolvePlayback(ctx, episodeID, sdk.PlaybackOptions{})
	return pb, c.noteDeviceRejected(err)
}

// ResetDevice 丢弃当前匿名设备并注册一台新设备（覆盖该槽的档案文件）。
//
// 设备池在 ErrDeviceRejected 时显式调用它；SDK 的自动换设备已关闭（§4.2）。
func (c *Client) ResetDevice(ctx context.Context) error {
	return c.noteDeviceRejected(c.sdk.ResetDevice(ctx))
}

// ContentKey 在本地把播放模型的密钥材料解包成内容密钥，返回 32 位小写 hex。
//
// 硬红线（I2）：返回值只允许写进进程内的 Keyring，绝不允许落库、写日志、
// 进事件载荷或子进程命令行（子进程命令行见方案 §3.7 的 C′ 风险）。
// 纯本地计算，不发请求（F4/F5）。
func (c *Client) ContentKey(pb *sdkmodel.Playback) (string, error) {
	key, err := sdk.ContentKey(pb)
	if err != nil {
		return "", err
	}
	if key.Hex == "" {
		return "", fmt.Errorf("%w: derived key is empty", ErrDRMProtected)
	}
	return key.Hex, nil
}
