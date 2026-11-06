//go:build windows

package singleinstance

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mutexName 是全局命名互斥量；Local\ 前缀限定当前会话，防多用户互杀。
const mutexName = `Local\hongguo-pc-singleinstance`

type procEntry struct {
	pid  uint32
	name string
}

// acquireOS 抢命名互斥量的所有权。Win32 语义：CreateMutex 对已存在的
// mutex 返回有效句柄 + ERROR_ALREADY_EXISTS（并非失败）；真正的所有权
// 要用 WaitForSingleObject(0) 判断——持有者死后等待立即成功。杀掉旧实例
// 后内核回收其句柄是异步的，带退避重试（约 5 秒）。
func acquireOS(appName string) (func(), error) {
	wname, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, err
	}
	const attempts = 10
	var handle windows.Handle
	for i := 0; i < attempts; i++ {
		h, err := windows.CreateMutex(nil, false, wname)
		if err != nil {
			// mutex 创建失败（权限等）不阻断启动：退化为无单实例保护。
			return func() {}, nil
		}
		handle = h
		// 0 超时试探所有权：WAIT_OBJECT_0 = 空闲；WAIT_ABANDONED = 原持有者
		// 被 TerminateProcess 强杀未释放——两种情况都表示本次拿到所有权。
		event, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr == nil && (event == windows.WAIT_OBJECT_0 || event == windows.WAIT_ABANDONED) {
			return func() {
				_ = windows.ReleaseMutex(handle)
				_ = windows.CloseHandle(handle)
			}, nil
		}
		_ = windows.CloseHandle(handle)
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("已有 hongguo-pc 实例在运行且无法结束（请手动退出后重试）")
}

// listProcesses 用 CreateToolhelp32Snapshot 枚举进程（纯 Go，无外部命令）。
func listProcesses() []procEntry {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []procEntry
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		out = append(out, procEntry{pid: entry.ProcessID, name: name})
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return out
}

// killProcess 结束指定 pid，并等进程真正退出（最多 5 秒）——TerminateProcess
// 是异步的，内核回收其句柄/互斥量稍有延迟，不等死透会让 acquireOS 空转重试。
func killProcess(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return err
	}
	_, _ = windows.WaitForSingleObject(h, 5000)
	return nil
}
