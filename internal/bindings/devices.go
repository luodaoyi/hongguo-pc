package bindings

import (
	"context"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// Devices 是设备池服务（方案 §8.3 的 Devices.Status/Reset）。
//
// Status 是只读快照；Reset 是显式换档操作（前端应二次确认）。
type Devices struct {
	Core *Core
}

// OKResult 是 {ok} 形状的返回。
type OKResult struct {
	OK bool `json:"ok"`
}

// Status 返回槽状态快照：healthy/cooling/exhausted/usedToday/nonPlaybackSwaps。
func (d *Devices) Status(ctx context.Context) model.DevicesStatus {
	return d.Core.Pool.Status()
}

// Reset 丢弃指定槽的匿名设备档案并注册新设备。
func (d *Devices) Reset(ctx context.Context, slotID string) (OKResult, error) {
	if err := d.Core.Pool.Reset(ctx, slotID); err != nil {
		d.Core.record(err)
		return OKResult{}, err
	}
	emitEvent(EventDevicesChanged, map[string]any{"slots": d.Core.Pool.Status().Slots})
	emitEvent(EventQuotaChanged, quotaPayload(d.Core))
	return OKResult{OK: true}, nil
}

// quotaPayload 拼 quota:changed 的载荷（方案 §8.4）。
func quotaPayload(core *Core) map[string]any {
	status := core.Pool.Status()
	return map[string]any{"healthy": status.Healthy, "usedToday": status.UsedToday}
}
