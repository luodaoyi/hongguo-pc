// MPV 是 mpv 播放内核的服务层（方案外延：v0.1.12 起 local 源默认走 mpv，
// WebView2 <video> 因 Chromium 对渐进 fMP4 音频包 fail-fast 致整管线中止、
// 连播误跳集，退居 mpv 不可用时的 fallback）。
//
// 前端流程：Prepare 拿到 source.url 后调 MPV.Available 探测——可用则
// MPV.Play(url)（mpv 自建窗口播放，进度/暂停/倍速经本服务属性往返），
// 不可用则按老路径把 url 交给 <video>。EndFile 事件在 Go 侧转成
// player:mpv 事件（eof=自然播完、quit=用户关窗/停止、error=解码失败），
// 前端按 eof 才跳下一集。
package bindings

import (
	"github.com/luodaoyi/hongguo-pc/internal/mpvplayer"
)

// MainWindowHandle 是 Wails 主窗口的原生句柄（main 装配时注入；不注入则
// mpv 拒播——渲染窗没有 owner 会漂成独立顶层窗）。
var MainWindowHandle uintptr

type MPV struct {
	Core *Core
}

// Available 报告本机 mpv 内核是否可用（exe 旁有 libmpv-2.dll 且主窗口句柄已注入）。
func (m *MPV) Available() bool {
	return mpvplayer.Available() && MainWindowHandle != 0
}

// Play 让 mpv 播放指定 URL（localsrv /v/ 或 CDN 直链）。
// rect 是前端量好的播放画面屏幕矩形（x,y,w,h）：渲染窗对齐它，避开顶部栏
// 与选集抽屉。title 用于媒体标题。
func (m *MPV) Play(url, title string, rect []float64) error {
	if len(rect) == 4 {
		mpvplayer.Get().Move(int32(rect[0]), int32(rect[1]), int32(rect[2]), int32(rect[3]))
	}
	return mpvplayer.Get().Play(url, title, MainWindowHandle)
}

// Move 挪渲染窗（主窗口 resize/拖动时前端 ResizeObserver 持续跟）。
func (m *MPV) Move(rect []float64) {
	if len(rect) == 4 {
		mpvplayer.Get().Move(int32(rect[0]), int32(rect[1]), int32(rect[2]), int32(rect[3]))
	}
}

// Hide 隐藏渲染窗（回退 <video> 或离开播放页时防黑窗残留）。
func (m *MPV) Hide() {
	mpvplayer.Get().HideRenderWindow()
}

// TogglePause 翻转播放/暂停。
func (m *MPV) TogglePause() error {
	return mpvplayer.Get().TogglePause()
}

// SeekTo 绝对跳转（秒）。
func (m *MPV) SeekTo(sec float64) error {
	return mpvplayer.Get().SeekTo(sec)
}

// SetRate 设置倍速。
func (m *MPV) SetRate(rate float64) error {
	return mpvplayer.Get().SetRate(rate)
}

// SetVolume 设置音量（0-100）。
func (m *MPV) SetVolume(v int) error {
	return mpvplayer.Get().SetVolume(v)
}

// Stop 停止并关闭 mpv 播放窗口（换集/退出页面用）。
func (m *MPV) Stop() {
	mpvplayer.Get().Stop()
}

// Position 当前 {position, duration}（秒；mpv 未起时返回 0）。
func (m *MPV) Position() map[string]float64 {
	pos, dur := mpvplayer.Get().Position()
	return map[string]float64{"position": pos, "duration": dur}
}
