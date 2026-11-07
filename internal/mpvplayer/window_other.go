//go:build !windows

// 非 Windows 平台的渲染窗占位：macOS/Linux 的 mpv 嵌入走各自原生路径
// （CI 上 linux/darwin 暂不分发 libmpv，Available=false 直接回退 <video>）。
package mpvplayer

import "fmt"

// AttachOwner 非 Windows 暂不支持嵌入渲染窗。
func (p *Player) AttachOwner(ownerPtr uintptr) (int64, error) {
	return 0, fmt.Errorf("mpv 渲染窗宿主暂未支持此平台")
}

// Move 非 Windows 无操作。
func (p *Player) Move(x, y, w, h int32) {}

// HideRenderWindow 非 Windows 无操作。
func (p *Player) HideRenderWindow() {}

// closeRenderWindow 非 Windows 无操作。
func (p *Player) closeRenderWindow() {}

// applyPending 非 Windows 无操作。
func (p *Player) applyPending() {}

// hideRenderWindowLocked 非 Windows 无操作（Stop 内持锁调用）。
func (p *Player) hideRenderWindowLocked() {}
