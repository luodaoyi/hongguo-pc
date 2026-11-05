package player

import (
	"context"

	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
)

// Pipeline 把播放模型变成可播地址（方案 §3.2 的 A / B1 两条通道）。
//
// 具体实现见 mediapipeline.go：内部走 media.Decider 判道，
// direct 直接给 CDN 直链，decrypted 走 media.Session + localsrv.URL。
type Pipeline interface {
	// Render 返回一个可交给 <video src> 的地址。
	Render(ctx context.Context, req RenderRequest) (RenderResult, error)
	// Close 关闭本会话持有的 ffmpeg 进程与本地服务。
	Close() error
}

// RenderRequest 是管道输入。
//
// KeyHex 的硬红线（方案 §2.2 I2）：只来自 devices.Pool.Key(episodeID)，
// 只允许写进 media.Input.KeyHex 并进 ffmpeg 命令行；不得写日志、进事件、进 DTO。
type RenderRequest struct {
	EpisodeID string
	Playback  *sdkx.Playback
	Stream    sdkx.Stream
	KeyHex    string
	Quality   string
}

// RenderResult 是管道输出。
type RenderResult struct {
	Source model.SourceDTO
	// Mode 取 "direct" 或 "local"（对应 media.Mode 的两个值）。
	Mode string
}
