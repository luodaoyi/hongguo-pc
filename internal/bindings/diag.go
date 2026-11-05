package bindings

import "context"

// Diag 是诊断服务（方案 §8.3 的 Diag.RecentErrors、§9.6）。
//
// 只显示内存环形缓冲（最近 50 条归一后的错误码与文案），不读磁盘日志，
// 也不含任何密钥材料或带签名的地址。
type Diag struct {
	Core *Core
}

// RecentErrors 返回最近 50 条错误（时间正序，最近的在后）。
func (d *Diag) RecentErrors(ctx context.Context) []ErrorEntry {
	return d.Core.Errors.list()
}
