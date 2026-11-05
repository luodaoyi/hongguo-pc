package bindings

import (
	"context"
	"strings"

	"github.com/luodaoyi/hongguo-pc/internal/media"
)

// Media 是媒体环境服务（方案 §8.3 的 Media.DetectFFmpeg）。
//
// ffmpeg 不随包分发（方案 §3.6）：运行时探测，路径可由设置在设置页覆盖，
// 改动立即生效（每次探测都重新读设置）。
type Media struct {
	Core *Core
}

// DetectResult 是探测结果。License 留空表示未能从 -version 输出判断许可
// （ffmpeg 的 -version 首行不带许可信息，只有构建配置里才有 --enable-gpl）。
type DetectResult struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	OK      bool   `json:"ok"`
	License string `json:"license"`
}

// DetectFFmpeg 探测 ffmpeg：设置里的覆盖路径 → PATH → Windows 常见安装位置。
func (m *Media) DetectFFmpeg(ctx context.Context) DetectResult {
	override := m.Core.Settings.Get(KeyFFmpegPath)
	path, version, err := media.DetectFFmpeg(override)
	if err != nil {
		m.Core.logf("bindings: ffmpeg not found: %v", err)
		return DetectResult{Path: strings.TrimSpace(override), OK: false}
	}
	return DetectResult{Path: path, Version: version, OK: true, License: licenseHint(version)}
}

// licenseHint 从版本串里能看出来的只有构建来源，看不出来就留空。
func licenseHint(version string) string {
	if strings.Contains(strings.ToLower(version), "gpl") {
		return "GPL"
	}
	return ""
}
