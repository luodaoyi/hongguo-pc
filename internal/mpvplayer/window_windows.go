//go:build windows

// mpv 渲染窗口宿主（Windows）：主窗口的 **WS_CHILD 子窗口**，坐标相对
// 父客户区——父窗口移动/最小化/还原时子窗口由系统自动联动，无需任何
// 轮询跟随（v0.1.12 的 owned 顶层窗方案实测拖动后画面留在原地：owned
// 窗有独立屏幕坐标，前端感知不到主窗口移动，Go 轮询也漏帧）。
//
// 为什么子窗口能盖住 WebView：WebView2 是 windowed hosting（独立 HWND），
// 后创建的兄弟子窗口默认 z 序在上，且每次应用几何都用 HWND_TOP 再提顶。
//
// 为什么点击穿透：WS_DISABLED 子窗口不参与鼠标命中，点击落到下层
// WebView——控制条/进度条/抽屉恢复可交互（owned 方案把整个播放区挡死，
// 用户实测无法点击）。画面区内的点击暂不响应（mpv wid 模式不做输入）。
//
// 线程纪律：窗口的创建/几何操作全部 application.InvokeSync 到主线程——
// Win32 窗口消息进入创建线程的队列，service goroutine 没有消息泵，
// 跨线程同步消息会永久阻塞（首版整窗卡死的根因）。

package mpvplayer

import (
	"fmt"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

const (
	wsChild        = 0x40000000
	wsClipsiblings = 0x04000000
	wsDisabled     = 0x08000000
	swpShowWindow  = 0x0040
	swpHideWindow  = 0x0080
	swpNoActivate  = 0x0010
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procSetWindowPos    = user32.NewProc("SetWindowPos")
)

// createRenderWindow 在主线程建主窗口的子窗口（初始 0×0 隐藏，
// 位置由首次 applyPending 给出——建窗后立即应用 pendingRect）。
func createRenderWindow(parent windows.HWND) (windows.HWND, error) {
	var created windows.HWND
	var err error
	application.InvokeSync(func() {
		className, e := windows.UTF16PtrFromString("Static")
		if e != nil {
			err = e
			return
		}
		hwnd, _, callErr := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(className)),
			0, // 无标题
			uintptr(wsChild|wsClipsiblings|wsDisabled), // 不收鼠标：点击穿透到 WebView
			0, 0, 100, 100,
			uintptr(parent), 0, 0, 0,
		)
		if hwnd == 0 {
			err = fmt.Errorf("CreateWindowExW failed: %v", callErr)
			return
		}
		created = windows.HWND(hwnd)
		// 提到 WebView 之上（后创建默认在上，这里显式确保）。
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpShowWindow)
	})
	return created, err
}

// AttachOwner 让渲染窗成为主窗口的子窗口，返回 wid（mpv wid 属性值）。
// 渲染窗生命周期与 Player 相同（Close 时销毁）；换剧重入播放页复用同窗。
func (p *Player) AttachOwner(ownerPtr uintptr) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.owner {
		return 0, fmt.Errorf("libmpv 不可用")
	}
	if ownerPtr == 0 {
		return 0, fmt.Errorf("主窗口句柄为空")
	}
	p.ownerHwnd = ownerPtr
	if p.renderHwnd != 0 {
		return int64(p.renderHwnd), nil // 子窗口随父联动，无需换 parent
	}
	hwnd, err := createRenderWindow(windows.HWND(ownerPtr))
	if err != nil {
		return 0, err
	}
	p.renderHwnd = uintptr(hwnd)
	return int64(hwnd), nil
}

// Move 记录渲染窗目标矩形（子窗口客户区坐标 = 前端视口相对物理像素，
// 父动子随，无需换算）并立即应用。
func (p *Player) Move(x, y, w, h int32) {
	p.mu.Lock()
	p.pendingRect = [4]int32{x, y, w, h}
	hwnd := p.renderHwnd
	p.mu.Unlock()
	if hwnd == 0 || w <= 0 || h <= 0 {
		return
	}
	p.applyPending()
}

// applyPending 把 pendingRect 应用到子窗口。走主线程：SetWindowPos 的
// 同步消息由主线程消息泵立即处理；HWND_TOP 每次提顶，确保压住 WebView。
func (p *Player) applyPending() {
	p.mu.Lock()
	hwnd, r := p.renderHwnd, p.pendingRect
	p.mu.Unlock()
	if hwnd == 0 || r[2] <= 0 || r[3] <= 0 {
		return
	}
	application.InvokeSync(func() {
		procSetWindowPos.Call(uintptr(hwnd), 0, // HWND_TOP：兄弟置顶
			uintptr(int(r[0])), uintptr(int(r[1])),
			uintptr(int(r[2])), uintptr(int(r[3])),
			swpShowWindow,
		)
	})
}

// HideRenderWindow 隐藏渲染窗（离开播放页/回退 <video> 时防黑块残留）。
func (p *Player) HideRenderWindow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hideRenderWindowLocked()
}

// hideRenderWindowLocked 是 HideRenderWindow 的持锁版（Stop 内已持锁）。
func (p *Player) hideRenderWindowLocked() {
	if p.renderHwnd == 0 {
		return
	}
	hwnd := p.renderHwnd
	application.InvokeSync(func() {
		procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0,
			swpHideWindow|swpNoActivate)
	})
}

// closeRenderWindow 销毁渲染窗（Close 内持锁调用；进程退出路径）。
func (p *Player) closeRenderWindow() {
	if p.renderHwnd != 0 {
		procDestroyWindow.Call(uintptr(p.renderHwnd))
		p.renderHwnd = 0
	}
}
