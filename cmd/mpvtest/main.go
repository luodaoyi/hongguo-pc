//go:build windows

// mpvtest 是 mpv 嵌入链路的最小验证程序（开发诊断用，不随应用分发）：
// 建一个普通 Win32 窗口 → wid 交给 libmpv（purego 加载 bin/ 旁的
// libmpv-2.dll）→ 播 bin/test.mp4，15 秒内打印 time-pos 前进即链路通。
// 用法：bin> mpvtest.exe（与 libmpv-2.dll、test.mp4 同目录）。
package main

import (
	"fmt"
	"time"
	"unsafe"

	"github.com/gen2brain/go-mpv"
	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procShowWindow      = user32.NewProc("ShowWindow")
	procGetMessageW     = user32.NewProc("GetMessageW")
	procGetModuleHandleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

type msg struct {
	Hwnd                      uintptr
	Message                   uint32
	WParam, LParam            uintptr
	Time                      uint32
	Pt                        struct{ X, Y int32 }
}

func main() {
	className, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("mpv wid test")
	inst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0x00CF0000, // WS_OVERLAPPEDWINDOW
		120, 120, 960, 540,
		0, 0, inst, 0,
	)
	if hwnd == 0 {
		fmt.Println("create window failed:", callErr)
		return
	}
	procShowWindow.Call(hwnd, 5) // SW_SHOW

	// 消息泵（后台）：不泵窗口就是白的且无响应。
	go func() {
		var mm msg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&mm)), 0, 0, 0)
			if r == 0 {
				return
			}
		}
	}()

	// wid 必须在 Initialize 前设（initialization-only）。
	m := mpv.New()
	widVal := fmt.Sprintf("%d", hwnd)
	if err := m.SetOptionString("wid", widVal); err != nil {
		fmt.Println("set wid:", err)
		return
	}
	if err := m.Initialize(); err != nil {
		fmt.Println("initialize:", err)
		return
	}
	ver, err := m.GetProperty("mpv-version", mpv.FormatString)
	fmt.Println("version:", ver, err)

	if err := m.Command([]string{"loadfile", "test.mp4"}); err != nil {
		fmt.Println("loadfile:", err)
		return
	}

	deadline := time.Now().Add(15 * time.Second)
	last := -1.0
	for time.Now().Before(deadline) {
		if v, err := m.GetProperty("time-pos", mpv.FormatDouble); err == nil {
			now := v.(float64)
			if int(now) != int(last) {
				fmt.Printf("time-pos=%.1f\n", now)
				last = now
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	m.TerminateDestroy()
	fmt.Println("mpvtest done")
}
