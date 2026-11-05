//go:build windows

package media

import "syscall"

// sysProcAttr 在 Windows 上隐藏 ffmpeg 的控制台窗口，避免 GUI 进程反复闪黑框。
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
