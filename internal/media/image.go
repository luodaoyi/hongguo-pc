package media

// HEIC 封面转码：上游书城封面是 .heic 静态图（HEVC in ISO-BMFT），WebView2 的
// Chromium 内核不解码该格式，<img> 必然加载失败。本地用 ffmpeg 抽首帧转 JPEG
// 落盘；ffmpeg 缺失或转码失败由调用方回退原始字节，不阻断界面。
//
// 背景实测（2026-11-05）：书城/详情封面 URL 形如
// …fqnovelpic.com/…~tplv-…heic?x-expires=…（Content-Type image/heic），
// 改扩展名/模板会 403（签名覆盖路径），CDN 不做 Accept 协商——只能本地转。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IsHEIC 判断响应是否为 HEIC/HEIF 静态图：优先看 Content-Type，其次按
// ISO-BMFT 头嗅探（偏移 4 是 "ftyp"，紧随的 brand 标识具体格式）。
func IsHEIC(contentType string, data []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "heic") || strings.Contains(ct, "heif") {
		return true
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "heic", "heix", "heim", "heis", "mif1", "msf1":
			return true
		}
	}
	return false
}

// TranscodeHEIC 用 ffmpeg 把 srcPath 的 HEIC 首帧转成 JPEG 写到 dstPath。
//
// 只取第一帧（封面是单张静态图）；-q:v 3 的体积/画质对 200px 级展示足够。
// 失败时 dstPath 不会被写出（ffmpeg -y 先建临时目标失败即无产物），调用方按
// 「转码不可用」回退。
func TranscodeHEIC(ctx context.Context, ffmpegPath, srcPath, dstPath string) error {
	if strings.TrimSpace(ffmpegPath) == "" {
		return errors.New("media: ffmpeg 不可用，无法转码 HEIC 封面")
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, ffmpegPath,
		"-y", "-i", srcPath, "-map", "0:v:0", "-frames:v", "1", "-q:v", "3", dstPath)
	cmd.SysProcAttr = sysProcAttr() // GUI 进程必须隐藏子进程控制台，否则每张封面闪黑框
	cmd.Stderr = nil                // ffmpeg 的进度噪音只进管道，不落调用方日志
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 输出尾部通常带真正的失败原因（无流/不识别的 brand），截断防刷屏。
		msg := string(out)
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("media: HEIC 转码失败: %w: %s", err, msg)
	}
	fi, statErr := os.Stat(dstPath)
	if statErr != nil || fi.Size() == 0 {
		return fmt.Errorf("media: HEIC 转码无产物: %s", filepath.Base(dstPath))
	}
	return nil
}
