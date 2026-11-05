package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DetectFFmpeg 按 override → PATH → Windows 常见安装路径的顺序探测 ffmpeg
// （方案 §3.6：不随包分发，运行时探测）。返回可执行文件绝对路径与
// `ffmpeg -version` 的首行版本串。
func DetectFFmpeg(override string) (path, version string, err error) {
	var candidates []string
	if o := strings.TrimSpace(override); o != "" {
		candidates = append(candidates, o)
	}
	if p, lookErr := exec.LookPath("ffmpeg"); lookErr == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, commonFFmpegPaths()...)

	var tried []string
	for _, c := range candidates {
		resolved, ok := resolveExecutable(c)
		if !ok {
			tried = append(tried, c+"（不存在或不可执行）")
			continue
		}
		v, verr := ffmpegVersion(resolved)
		if verr != nil {
			tried = append(tried, c+"（"+verr.Error()+"）")
			continue
		}
		return resolved, v, nil
	}
	if len(tried) == 0 {
		return "", "", errors.New("media: 未找到 ffmpeg，请在设置里指定路径或安装后加入 PATH")
	}
	return "", "", fmt.Errorf("media: 未找到可用的 ffmpeg；已尝试: %s", strings.Join(tried, "; "))
}

// commonFFmpegPaths 是 Windows 上的常见安装位置（brief 指定的两条 + Program Files）。
func commonFFmpegPaths() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var paths []string
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		paths = append(paths, filepath.Join(la, "Microsoft", "WinGet", "Links", "ffmpeg.exe"))
	}
	paths = append(paths, `C:\ffmpeg\bin\ffmpeg.exe`)
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		paths = append(paths, filepath.Join(pf, "ffmpeg", "bin", "ffmpeg.exe"))
	}
	return paths
}

// resolveExecutable 把候选路径规整成可启动的绝对路径；裸名字走 PATH。
func resolveExecutable(c string) (string, bool) {
	if strings.ContainsAny(c, `/\`) {
		fi, err := os.Stat(c)
		if err != nil || fi.IsDir() {
			return "", false
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			return "", false
		}
		return abs, true
	}
	if p, err := exec.LookPath(c); err == nil {
		return p, true
	}
	return "", false
}

// ffmpegVersion 跑 `ffmpeg -version` 并取首个非空行。
func ffmpegVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-version")
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("执行 -version 失败: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", errors.New("-version 输出为空")
}

// ffmpegArgs 按方案 §3.8 拼参数。**绝不加 +faststart**：与分片输出互斥
// （§3.8 硬约束 ①）。
//
// 密钥只在 in.Protected 且 KeyHex 非空时出现；未加密集整段省略（§3.8）。
func ffmpegArgs(in Input, outPath string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	if in.Protected && in.KeyHex != "" {
		args = append(args, "-decryption_key", in.KeyHex)
	}
	args = append(args, "-i", in.SourceURL)
	if in.AudioURL != "" {
		args = append(args, "-i", in.AudioURL, "-map", "0:v:0", "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0")
	}
	args = append(args,
		"-c", "copy",
		"-movflags", "frag_keyframe+empty_moov",
		"-f", "mp4",
		outPath,
	)
	return args
}

// CommandLine 返回可写进日志的命令行：密钥替换为 <redacted>。
// 诊断包与错误上报只允许用这个形态，不得自行拼接 argv。
func CommandLine(in Input, outPath string) string {
	safe := in
	if safe.KeyHex != "" {
		safe.KeyHex = "<redacted>"
	}
	return strings.Join(ffmpegArgs(safe, outPath), " ")
}
