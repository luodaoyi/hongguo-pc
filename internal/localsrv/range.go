package localsrv

import (
	"strconv"
	"strings"
)

// byteRange 是一段已解析的字节区间。
type byteRange struct {
	start  int64
	length int64
}

// parseRangeHeader 解析单段 Range（`bytes=a-b` / `bytes=a-` / `bytes=-n`）。
//
// 返回 ok=false 表示不可满足或格式非法，调用方按 416 + `Content-Range: bytes */<最新长度>`
// 处理。多段 Range（含逗号）由调用方直接忽略并回整文件 200，不进本函数。
func parseRangeHeader(spec string, size int64) (byteRange, bool) {
	const prefix = "bytes="
	s := strings.TrimSpace(spec)
	if len(s) <= len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return byteRange{}, false
	}
	s = strings.TrimSpace(s[len(prefix):])
	dash := strings.IndexByte(s, '-')
	if dash < 0 {
		return byteRange{}, false
	}
	first := strings.TrimSpace(s[:dash])
	last := strings.TrimSpace(s[dash+1:])

	// 后缀形式 bytes=-n：最后 n 字节。
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 || size <= 0 {
			return byteRange{}, false
		}
		if n > size {
			n = size
		}
		return byteRange{start: size - n, length: n}, true
	}

	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return byteRange{}, false
	}
	if last == "" {
		return byteRange{start: start, length: size - start}, true
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start {
		return byteRange{}, false
	}
	if end >= size {
		end = size - 1
	}
	return byteRange{start: start, length: end - start + 1}, true
}
