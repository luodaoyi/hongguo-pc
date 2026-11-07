// mpv 播放 API 的平台无关定义：格式常量、事件类型与 EndFile 语义映射。
// 平台差异（Windows purego 绑定 / 非 Windows 不可用桩）在 mpvnative_*.go
// 与 window_*.go；本文件无 build tag，两边共用。

package mpvplayer

// mpv_format 常量（mpv/client.h）。
const (
	FormatFlag   = 3
	FormatDouble = 5
	FormatString = 6

	eventNone = 0
	eventEnd  = 7
)

// mpvEvent 是绑定层的事件（EndReason 仅 EventEnd 时有效）。
type mpvEvent struct {
	ID        int32
	EndReason int32
}

// classifyEndReason 把 mpv EndFile reason 映射为应用层语义。
func classifyEndReason(reason int32) string {
	switch reason {
	case 0: // MPV_END_FILE_REASON_EOF
		return "eof"
	case 2, 3: // STOP / QUIT
		return "quit"
	default: // ERROR / REDIRECT 及未知值
		return "error"
	}
}
