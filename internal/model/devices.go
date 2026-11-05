package model

// 设备槽状态（方案 §4.3）。字符串而非枚举整数，因为 device_slot.state 存的就是
// 这些字面量（方案 §5.3 未约束为 INT）。
const (
	SlotHealthy   = "healthy"
	SlotCooling   = "cooling"
	SlotExhausted = "exhausted"
	SlotRejected  = "rejected"
)

// SlotStatus 是单个设备槽的只读快照，对应 device_slot 表的一行。
type SlotStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	QuotaUsed int    `json:"quotaUsed"`
	// QuotaDay 是 QuotaUsed 所属的本地日期（YYYYMMDD）。
	QuotaDay int `json:"quotaDay,omitempty"`
	// CoolUntil / LastUsed 是 unix 秒。
	CoolUntil int64 `json:"coolUntil,omitempty"`
	LastUsed  int64 `json:"lastUsed,omitempty"`
	Rejects   int   `json:"rejects"`
}

// DevicesStatus 是 Devices.Status 的返回（方案 §8.3）。
//
// NonPlaybackSwaps 单列「非播放换档」计数：GetSeries / ListEpisodes / Search /
// Seasons / SeriesRecommend 都可能因 ErrDeviceRejected 换档（方案 §4.4 M1 补充）。
type DevicesStatus struct {
	Healthy          int          `json:"healthy"`
	Cooling          int          `json:"cooling"`
	Exhausted        int          `json:"exhausted"`
	Rejected         int          `json:"rejected"`
	UsedToday        int          `json:"usedToday"`
	NonPlaybackSwaps int          `json:"nonPlaybackSwaps"`
	Slots            []SlotStatus `json:"slots"`
}
