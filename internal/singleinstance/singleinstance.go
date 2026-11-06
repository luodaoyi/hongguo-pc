// Package singleinstance 保证桌面应用同时只有一个运行实例。
//
// 策略（Windows）：先按可执行文件名清理遗留实例（用户要求「多开时杀老」，
// 覆盖上次异常退出留下的僵尸进程），再抢命名互斥量作为唯一性裁决——
// mutex 由内核在进程退出时自动释放，杜绝僵尸锁。
// 其它平台：只做进程名清理（macOS/Linux 无需 mutex 也能靠 pgrep 语义工作）。
package singleinstance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Own 是应用可执行文件名（不含目录，小写），供跨平台进程匹配。
var own = strings.ToLower(filepath.Base(os.Args[0]))

// Acquire 清理同名旧实例并取得单实例锁。返回 release 函数（进程退出由
// defer 调用；Windows 上 mutex 随进程消亡，release 只是显式释放）。
// 清理失败不阻断启动（旧实例可能刚好退出），唯一性由 mutex 最终裁决。
func Acquire(appName string) (func(), error) {
	killStale(appName)
	return acquireOS(appName)
}

// killStale 结束除自己以外的所有同名进程（按可执行文件名匹配）。
// pid 类型随平台不同（Windows uint32 / POSIX int），用字符串比较规避。
func killStale(appName string) {
	self := fmt.Sprint(os.Getpid())
	for _, p := range listProcesses() {
		pid := fmt.Sprint(p.pid)
		if pid == self || p.pid <= 0 {
			continue
		}
		if !strings.EqualFold(p.name, own) && !strings.EqualFold(p.name, strings.ToLower(appName)) {
			continue
		}
		if err := killProcess(p.pid); err == nil {
			fmt.Fprintf(os.Stderr, "hongguo-pc: 已结束旧实例 pid=%d（单实例限制）\n", p.pid)
		}
	}
}
