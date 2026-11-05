//go:build !windows

package media

import "syscall"

// sysProcAttr 在非 Windows 平台不需要额外属性。
func sysProcAttr() *syscall.SysProcAttr { return nil }
