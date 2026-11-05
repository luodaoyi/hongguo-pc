package model

// 短剧发布状态的可选值，与 SDK 的 model.SeriesStatus.String() 一致。
// 方案 §5.2 的 TS 联合类型漏了 updated_today，前端补上即可（不改后端取值）。
const (
	StatusUpdating     = "updating"
	StatusFinished     = "finished"
	StatusUpdatedToday = "updated_today"
	StatusStopped      = "stopped"
	StatusUnknown      = "unknown"
)

// 付费类型的可选值，与 SDK 的 model.PayType.String() 一致。
const (
	PayTypeFree    = "free"
	PayTypeVip     = "vip"
	PayTypeSingle  = "single"
	PayTypeUnknown = "unknown"
)

// 数据库里 status / pay_type 存的是服务端枚举整数（方案 §5.3 的 INT 列），
// DTO 里是字符串，两个方向都在这里换算，避免 store 层散落魔法数字。

// SeriesStatusCode 把状态字符串换成入库用的枚举整数。
func SeriesStatusCode(status string) int {
	switch status {
	case StatusUpdating:
		return 0
	case StatusFinished:
		return 1
	case StatusUpdatedToday:
		return 3
	case StatusStopped:
		return 4
	default:
		return -1
	}
}

// SeriesStatusName 把入库的枚举整数换回状态字符串。
func SeriesStatusName(code int) string {
	switch code {
	case 0:
		return StatusUpdating
	case 1:
		return StatusFinished
	case 3:
		return StatusUpdatedToday
	case 4:
		return StatusStopped
	default:
		return StatusUnknown
	}
}

// PayTypeCode 把付费类型字符串换成入库用的枚举整数。
func PayTypeCode(payType string) int {
	switch payType {
	case PayTypeFree:
		return 0
	case PayTypeVip:
		return 1
	case PayTypeSingle:
		return 2
	default:
		return -1
	}
}

// PayTypeName 把入库的枚举整数换回付费类型字符串。
func PayTypeName(code int) string {
	switch code {
	case 0:
		return PayTypeFree
	case 1:
		return PayTypeVip
	case 2:
		return PayTypeSingle
	default:
		return PayTypeUnknown
	}
}
