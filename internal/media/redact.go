package media

import (
	"regexp"
	"strings"
)

// hexRunPattern 兜底：任何 32 位以上的 hex 串（密钥、spade 材料）都抹掉，
// 防止 ffmpeg stderr 或上游错误把密钥片段带进日志。
var hexRunPattern = regexp.MustCompile(`[0-9a-fA-F]{32,}`)

// redact 把已知密钥与任意长 hex 串替换成 <redacted>。只用于错误信息与日志，
// 不得用于写回磁盘的内容。
func redact(s, keyHex string) string {
	if s == "" {
		return s
	}
	if keyHex != "" {
		s = strings.ReplaceAll(s, keyHex, "<redacted>")
		s = strings.ReplaceAll(s, strings.ToLower(keyHex), "<redacted>")
		s = strings.ReplaceAll(s, strings.ToUpper(keyHex), "<redacted>")
	}
	return hexRunPattern.ReplaceAllString(s, "<redacted>")
}
