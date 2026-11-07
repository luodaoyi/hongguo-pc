//go:build windows

// libmpv C API 的 purego 最小绑定（无 CGO）。
//
// 为什么不用 github.com/gen2brain/go-mpv：它在 package init() 阶段强制
// LoadLibrary，找不到 dll 直接 panic——进程在 Available() 兜底之前就死了
// （v0.1.12 CI 冒烟干净目录实测 exit=2）；其 CGO 分支在 linux/darwin 又要
// 系统 mpv 头文件。本文件只绑定应用用到的 9 个函数，全部 **lazy 加载**：
// 首次 New 才 load，失败返回 error，应用优雅回退 <video>。
//
// mpv API 是 UTF-8；字符串经 LocalAlloc 分配（调用期间地址稳定）。

package mpvplayer

import (
	"fmt"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

// syscallN 是 purego 的原生调用入口（var 便于在非 windows 文件桩掉）。
var syscallN = purego.SyscallN

func float64Bits(f float64) uint64 { return math.Float64bits(f) }

var (
	libMu     sync.Mutex
	libLoaded bool
	libErr    error

	procCreate    uintptr
	procInit      uintptr
	procTerminate uintptr
	procSetOption uintptr
	procSetProp   uintptr
	procGetProp   uintptr
	procCommand   uintptr
	procWaitEvent uintptr
	procFree      uintptr
)

// loadLib 惰性加载 libmpv 并解析全部符号（进程内只做一次）。
func loadLib() error {
	libMu.Lock()
	defer libMu.Unlock()
	if libLoaded {
		return libErr
	}
	libLoaded = true
	h, err := windows.LoadLibrary("libmpv-2.dll")
	if err != nil {
		// 兼容旧命名分发。
		h, err = windows.LoadLibrary("libmpv.dll")
	}
	if err != nil {
		libErr = fmt.Errorf("找不到 libmpv-2.dll: %w", err)
		return libErr
	}
	base := uintptr(h)
	must := func(name string) uintptr {
		p, e := windows.GetProcAddress(h, name)
		if e != nil {
			libErr = fmt.Errorf("libmpv 缺少导出 %s: %w", name, e)
		}
		return p
	}
	procCreate = must("mpv_create")
	procInit = must("mpv_initialize")
	procTerminate = must("mpv_terminate_destroy")
	procSetOption = must("mpv_set_option_string")
	procSetProp = must("mpv_set_property_string")
	procGetProp = must("mpv_get_property")
	procCommand = must("mpv_command")
	procWaitEvent = must("mpv_wait_event")
	procFree = must("mpv_free")
	if libErr != nil || base == 0 {
		return libErr
	}
	return nil
}

// nativeAvailable 报告 libmpv 是否可加载（探测即真实加载，结果缓存）。
func nativeAvailable() bool { return loadLib() == nil }

// cMpvEvent 是 C struct mpv_event 的 64 位布局
// { int event_id; int error; uint64 reply_userdata; void *data; }。
type cMpvEvent struct {
	EventID  int32
	Error    int32
	Userdata uint64
	Data     uintptr
}

// cStr 分配 UTF-8 C 字符串（含结尾 0），返回指针。
func cStr(s string) uintptr {
	b := append([]byte(s), 0)
	p, err := windows.LocalAlloc(0, uint32(len(b)))
	if err != nil {
		return 0
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)), b)
	return uintptr(unsafe.Pointer(p))
}

func freeC(p uintptr) {
	if p != 0 {
		_, _ = windows.LocalFree(windows.Handle(p))
	}
}

func cStringAt(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Pointer(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

// nativeClient 是一个 mpv 实例（mpv_create 句柄）。
type nativeClient struct {
	handle uintptr
}

// newNativeClient 创建 mpv 实例（惰性加载 dll；失败返回 error 不 panic）。
func newNativeClient() (*nativeClient, error) {
	if err := loadLib(); err != nil {
		return nil, err
	}
	h, _, _ := syscallN(procCreate)
	if h == 0 {
		return nil, fmt.Errorf("mpv_create 返回空句柄")
	}
	return &nativeClient{handle: h}, nil
}

func (c *nativeClient) SetOptionString(name, value string) error {
	np, vp := cStr(name), cStr(value)
	defer freeC(np)
	defer freeC(vp)
	r, _, _ := syscallN(procSetOption, c.handle, np, vp)
	if int32(r) < 0 {
		return fmt.Errorf("mpv_set_option_string(%s): %d", name, int32(r))
	}
	return nil
}

func (c *nativeClient) SetPropertyString(name, value string) error {
	np, vp := cStr(name), cStr(value)
	defer freeC(np)
	defer freeC(vp)
	r, _, _ := syscallN(procSetProp, c.handle, np, vp)
	if int32(r) < 0 {
		return fmt.Errorf("mpv_set_property_string(%s): %d", name, int32(r))
	}
	return nil
}

// GetProperty 按 format 读取属性。FormatString 返回 string（内部 mpv_free）。
func (c *nativeClient) GetProperty(name string, format int) (any, error) {
	np := cStr(name)
	defer freeC(np)
	switch format {
	case FormatDouble:
		var f float64
		r, _, _ := syscallN(procGetProp, c.handle, np, uintptr(format), uintptr(unsafe.Pointer(&f)))
		if int32(r) < 0 {
			return nil, fmt.Errorf("mpv_get_property(%s): %d", name, int32(r))
		}
		return f, nil
	case FormatFlag:
		var i int32
		r, _, _ := syscallN(procGetProp, c.handle, np, uintptr(format), uintptr(unsafe.Pointer(&i)))
		if int32(r) < 0 {
			return nil, fmt.Errorf("mpv_get_property(%s): %d", name, int32(r))
		}
		return i != 0, nil
	case FormatString:
		var p uintptr
		r, _, _ := syscallN(procGetProp, c.handle, np, uintptr(format), uintptr(unsafe.Pointer(&p)))
		if int32(r) < 0 {
			return nil, fmt.Errorf("mpv_get_property(%s): %d", name, int32(r))
		}
		s := cStringAt(p)
		_, _, _ = syscallN(procFree, p)
		return s, nil
	default:
		return nil, fmt.Errorf("mpv_get_property: 不支持的 format %d", format)
	}
}

// Command 执行 mpv 命令（char** argv，NULL 结尾）。
func (c *nativeClient) Command(args []string) error {
	n := len(args)
	arrP, err := windows.LocalAlloc(0, uint32((n+1)*int(unsafe.Sizeof(uintptr(0)))))
	if err != nil {
		return err
	}
	arr := unsafe.Slice((*uintptr)(unsafe.Pointer(arrP)), n+1)
	ptrs := make([]uintptr, n)
	for i, a := range args {
		ptrs[i] = cStr(a)
		arr[i] = ptrs[i]
	}
	arr[n] = 0
	r, _, _ := syscallN(procCommand, c.handle, arrP)
	for _, p := range ptrs {
		freeC(p)
	}
	_, _ = windows.LocalFree(windows.Handle(arrP))
	if int32(r) < 0 {
		return fmt.Errorf("mpv_command(%v): %d", args[0], int32(r))
	}
	return nil
}

// WaitEvent 等待事件（timeout 秒，0=非阻塞轮询）。
func (c *nativeClient) WaitEvent(timeout float64) mpvEvent {
	r, _, _ := syscallN(procWaitEvent, c.handle, uintptr(float64Bits(timeout)))
	if r == 0 {
		return mpvEvent{ID: eventNone}
	}
	ev := (*cMpvEvent)(unsafe.Pointer(r))
	out := mpvEvent{ID: ev.EventID}
	if ev.EventID == eventEnd && ev.Data != 0 {
		out.EndReason = *(*int32)(unsafe.Pointer(ev.Data))
	}
	return out
}

// TerminateDestroy 销毁实例并释放（mpv_terminate_destroy 后句柄失效）。
func (c *nativeClient) TerminateDestroy() {
	_, _, _ = syscallN(procTerminate, c.handle)
	c.handle = 0
}

func (c *nativeClient) Initialize() error {
	r, _, _ := syscallN(procInit, c.handle)
	if int32(r) < 0 {
		return fmt.Errorf("mpv_initialize: %d", int32(r))
	}
	return nil
}
