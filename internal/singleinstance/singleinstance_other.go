//go:build !windows

package singleinstance

import (
	"os/exec"
	"strconv"
	"strings"
)

type procEntry struct {
	pid  int
	name string
}

// acquireOS 非 Windows 平台无 mutex 封装，靠进程名清理后的天然结果工作；
// 仍有并发窗口时以「最后一次启动者为准」，不做更重的锁。
func acquireOS(appName string) (func(), error) {
	return func() {}, nil
}

// listProcesses 用 pgrep 枚举同名进程（macOS/Linux）。
func listProcesses() []procEntry {
	out, err := exec.Command("pgrep", "-l", "-f", own).Output()
	if err != nil {
		return nil
	}
	var result []procEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		result = append(result, procEntry{pid: pid, name: fields[1]})
	}
	return result
}

// killProcess 结束指定 pid。
func killProcess(pid int) error {
	return exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
}
