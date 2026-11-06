package localsrv

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

// IsLocalProxyImg 是封面代理污染防御的裁决点（v0.1.6 实测事故）：
// GetSeries 曾把带 token 的代理 URL 落库，重启后 token 换代、缓存全灰。
// 本会话的代理地址必须被识别（library 拿它挡改写与落库），
// 上一会话的死地址必须不被识别为可改写对象（返回空串走降级）。
func TestIsLocalProxyImg(t *testing.T) {
	s, err := New(Config{Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	// ImageURL 的输出必须是 IsLocalProxyImg=true（本会话代理地址）。
	img := s.ImageURL("https://p3-reading-sign.fqnovelpic.com/novel-pic/x~tplv:400:0.heic?x-expires=1")
	if img == "" {
		t.Fatal("ImageURL returned empty for .heic url")
	}
	if !s.IsLocalProxyImg(img) {
		t.Fatalf("IsLocalProxyImg(%s) = false, want true", img)
	}

	// 同 host:port 但 token 不符（上一会话）必须 false。
	u, _ := url.Parse(img)
	old := *u
	q := old.Query()
	q.Set("t", "0000000000000000000000000000000000000000000000000000000000000000")
	old.RawQuery = q.Encode()
	if s.IsLocalProxyImg(old.String()) {
		t.Fatal("IsLocalProxyImg = true for stale token, want false")
	}

	// 非代理地址一律 false。
	for _, x := range []string{
		"",
		"https://p3-reading-sign.fqnovelpic.com/a.jpg",
		"http://127.0.0.1:1/img?t=x&u=y",
	} {
		if s.IsLocalProxyImg(x) {
			t.Fatalf("IsLocalProxyImg(%q) = true, want false", x)
		}
	}
}

// ImageURL 收到死代理地址（任何 /img 形态）时不得再改写一遍：
// 防御兜底在 library 层，这里只保证普通 heic 直达改写、非 heic 原样返回。
func TestImageURLPassthrough(t *testing.T) {
	s, err := New(Config{Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	jpeg := "https://p3-reading-sign.fqnovelpic.com/a.jpeg?x-signature=zz"
	if got := s.ImageURL(jpeg); got != jpeg {
		t.Fatalf("ImageURL(jpeg) = %q, want passthrough", got)
	}
}

// serveImage 对本会话 ImageURL 生成的地址必须回 200 + JPEG（缓存命中路径），
// 对 token 不符的地址回 403——重启后死代理 URL 正是命中这条。
func TestServeImageTokenGate(t *testing.T) {
	s, err := New(Config{Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	img := s.ImageURL("https://p3-reading-sign.fqnovelpic.com/x.heic")
	u, _ := url.Parse(img)

	req := httptest.NewRequest("GET", img, nil)
	rec := httptest.NewRecorder()
	s.serveImage(rec, req)
	// 有效 token 走到回源：200（含缓存/兜底）或 502（离线回源失败）都算过闸，
	// 唯独不能是 403/404——那是 token/参数裁决层的拒绝码。
	if rec.Code == 403 || rec.Code == 404 {
		t.Fatalf("valid token: code=%d, want not-403/404", rec.Code)
	}

	stale := *u
	q := stale.Query()
	q.Set("t", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	stale.RawQuery = q.Encode()
	req2 := httptest.NewRequest("GET", stale.String(), nil)
	rec2 := httptest.NewRecorder()
	s.serveImage(rec2, req2)
	if rec2.Code != 403 {
		t.Fatalf("stale token: code=%d, want 403", rec2.Code)
	}
}
