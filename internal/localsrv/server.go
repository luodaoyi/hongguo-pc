// Package localsrv 是 127.0.0.1 上的临时文件 Range 服务（方案 §3.4 / §3.8）。
//
// 它只端已解密的文件，不持有密钥，也不理解播放模型。安全约束：
// 只监听回环地址、系统分配端口、路径带 32 字节随机会话 token、token 不符直接 403
// 且不解释、不打印明文 URL、去掉 Server 头、Close 后撤销全部 token。
package localsrv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/media"
)

const mimeVideoMP4 = "video/mp4"

// ErrClosed 表示服务已关闭。
var ErrClosed = errors.New("localsrv: 服务已关闭")

// imgHostSuffixes 是封面代理允许回源的域名后缀（小写）。封面只来自上游图片
// CDN；白名单防 SSRF（拒绝内网/任意地址）。出现新图床时在这里补。
var imgHostSuffixes = []string{".fqnovelpic.com", ".byteimg.com"}

// Config 是本地服务的配置。Root 是临时文件根目录；非空时 URL 只接受该目录内的
// 文件，防止调用方传错路径把任意文件端出去。
type Config struct {
	Root string
	// ImgCacheDir 是封面转码产物的磁盘缓存目录（空 = 不做封面代理）。
	ImgCacheDir string
	// FFmpegPath 用于 HEIC 封面转 JPEG；空 = 不转码，原样回源（浏览器自行降级）。
	FFmpegPath string
	// Logf 可选诊断日志（只打 host 与结果，不打完整签名 URL）。
	Logf func(format string, args ...any)
}

// Server 是本地 Range 服务。
type Server struct {
	ln          net.Listener
	httpSrv     *http.Server
	token       string
	root        string
	imgCacheDir string
	ffmpegPath  string
	logf        func(format string, args ...any)
	imgClient   *http.Client
	imgMu       sync.Mutex // 同一封面的取源+转码互斥（小文件，全局锁足够）

	mu       sync.RWMutex
	episodes map[string]string // episodeID -> 绝对文件路径
	closed   bool
	serveErr error
}

// New 在 127.0.0.1:0 上起服务，token 每会话随机 32 字节（hex 编码后 64 字符）。
func New(cfg Config) (*Server, error) {
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("localsrv: 生成 token 失败: %w", err)
	}
	root := strings.TrimSpace(cfg.Root)
	if root != "" {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("localsrv: 无法解析临时根目录: %w", err)
		}
		root = absRoot
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	imgCacheDir := strings.TrimSpace(cfg.ImgCacheDir)
	if imgCacheDir != "" {
		if err := os.MkdirAll(imgCacheDir, 0o755); err != nil {
			// 缓存目录建不出来只降级为「不缓存、每次现取」，不阻断启动。
			logf("localsrv: 封面缓存目录不可用，封面代理退化为直取: %v", err)
			imgCacheDir = ""
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("localsrv: 监听回环地址失败: %w", err)
	}
	s := &Server{
		ln:          ln,
		token:       token,
		root:        root,
		imgCacheDir: imgCacheDir,
		ffmpegPath:  strings.TrimSpace(cfg.FFmpegPath),
		logf:        logf,
		imgClient:   &http.Client{Timeout: 20 * time.Second},
		episodes:    make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v/", s.serve)
	mux.HandleFunc("/img", s.serveImage)
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// 不打印访问日志：请求路径含 token，日志一旦落盘就是明文 URL。
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.serveErr = err
			s.mu.Unlock()
		}
	}()
	return s, nil
}

// URL 登记一集的文件并返回带 token 的本地地址。文件此时可以尚未出现
// （ffmpeg 还在写首字节），真正的存在性由请求时的 404 决定。
func (s *Server) URL(episodeID, filePath string) (string, error) {
	id := strings.TrimSpace(episodeID)
	if id == "" {
		return "", errors.New("localsrv: episodeID 为空")
	}
	if strings.TrimSpace(filePath) == "" {
		return "", errors.New("localsrv: filePath 为空")
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("localsrv: 无法解析文件路径: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	if s.root != "" {
		rel, err := filepath.Rel(s.root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New("localsrv: 文件不在临时根目录内，拒绝登记")
		}
	}
	s.episodes[id] = abs
	return fmt.Sprintf("http://%s/v/%s/%s.mp4", s.ln.Addr().String(), s.token, url.PathEscape(id)), nil
}

// Remove 撤销一集的地址；之后该集的请求按 404 处理。
func (s *Server) Remove(episodeID string) {
	s.mu.Lock()
	delete(s.episodes, episodeID)
	s.mu.Unlock()
}

// ImageURL 把需要本地转码的远端封面（HEIC）改写成带会话 token 的本机代理地址；
// 空串与非 .heic 的 URL（分集剧照是 jpeg）原样返回，浏览器直连 CDN。
// 注意按 path 后缀判断：签名 URL 的 query（x-expires/x-signature）会盖掉结尾。
// 服务已关闭时原样返回（界面拿到失效地址会走加载失败的兜底）。
func (s *Server) ImageURL(remote string) string {
	if !isHEICURL(remote) {
		return remote
	}
	s.mu.RLock()
	closed, addr, token := s.closed, s.ln.Addr().String(), s.token
	s.mu.RUnlock()
	if closed || token == "" {
		return remote
	}
	q := url.Values{}
	q.Set("u", remote)
	q.Set("t", token)
	return "http://" + addr + "/img?" + q.Encode()
}

// IsLocalProxyImg 判断 url 是否为本会话 /img 代理地址（同 host+port+token）。
// library 层在改写封面与落库前用它挡两件事：脏缓存里上一会话的死代理地址
// （token 已换，直接当无封面处理）、以及防止把本会话代理 URL 写回 store。
func (s *Server) IsLocalProxyImg(remote string) bool {
	if remote == "" {
		return false
	}
	s.mu.RLock()
	closed, addr, token := s.closed, s.ln.Addr().String(), s.token
	s.mu.RUnlock()
	if closed || token == "" {
		return false
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return false
	}
	return parsed.Host == addr && parsed.Path == "/img" && parsed.Query().Get("t") == token
}

// serveImage 是 HEIC 封面的本地代理：带 token 校验 + 图床白名单，命中磁盘缓存
// 直接回；否则回源下载、按嗅探结果转码 JPEG（ffmpeg 缺失/失败回退原始字节）。
// 错误分支只给状态码，不回显原因，与 /v/ 的纪律一致。
func (s *Server) serveImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.tokenOK(r.URL.Query().Get("t")) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	remote := strings.TrimSpace(r.URL.Query().Get("u"))
	parsed, err := url.Parse(remote)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !imgHostAllowed(parsed.Hostname()) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	sum := sha256.Sum256([]byte(remote))
	name := hex.EncodeToString(sum[:])
	jpeg := filepath.Join(s.imgCacheDir, name+".jpg")
	orig := filepath.Join(s.imgCacheDir, name+".orig")
	if s.imgCacheDir != "" {
		if fi, err := os.Stat(jpeg); err == nil && fi.Size() > 0 {
			s.serveImageFile(w, r, jpeg, "image/jpeg")
			return
		}
		if fi, err := os.Stat(orig); err == nil && fi.Size() > 0 {
			mt, _ := os.ReadFile(filepath.Join(s.imgCacheDir, name+".mime"))
			ct := strings.TrimSpace(string(mt))
			if ct == "" {
				ct = "application/octet-stream"
			}
			s.serveImageFile(w, r, orig, ct)
			return
		}
	}

	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	// 双重检查：等锁期间可能已被并发请求转好。
	if s.imgCacheDir != "" {
		if fi, err := os.Stat(jpeg); err == nil && fi.Size() > 0 {
			s.serveImageFile(w, r, jpeg, "image/jpeg")
			return
		}
	}

	data, ctype, err := s.fetchRemote(r.Context(), remote)
	if err != nil {
		s.logf("localsrv: 封面回源失败 %s: %v", parsed.Hostname(), err)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	if !media.IsHEIC(ctype, data) {
		// 非 HEIC（理论上不会走到：ImageURL 只改写 .heic）按原字节缓存回源。
		s.serveOrCacheRaw(w, r, s.imgCacheDir, name, ctype, data)
		return
	}

	if s.ffmpegPath == "" || s.imgCacheDir == "" {
		s.serveRaw(w, ctype, data)
		return
	}
	src := filepath.Join(s.imgCacheDir, name+".heic.tmp")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		s.serveRaw(w, ctype, data)
		return
	}
	if err := media.TranscodeHEIC(context.Background(), s.ffmpegPath, src, jpeg); err != nil {
		os.Remove(src)
		s.logf("localsrv: 封面转码失败 %s: %v", parsed.Hostname(), err)
		s.serveRaw(w, ctype, data)
		return
	}
	os.Remove(src)
	s.serveImageFile(w, r, jpeg, "image/jpeg")
}

// fetchRemote 下载远端封面，上限 4MB（封面实际 ~40KB，超限视为异常直接断）。
func (s *Server) fetchRemote(ctx context.Context, remote string) ([]byte, string, error) {
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, remote, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.imgClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// serveOrCacheRaw 把非 HEIC 的原始字节缓存为 <name>.orig（mime 记 sidecar）并回源。
func (s *Server) serveOrCacheRaw(w http.ResponseWriter, r *http.Request, dir, name, ctype string, data []byte) {
	if dir != "" {
		_ = os.WriteFile(filepath.Join(dir, name+".orig"), data, 0o644)
		_ = os.WriteFile(filepath.Join(dir, name+".mime"), []byte(ctype), 0o644)
	}
	s.serveRaw(w, ctype, data)
}

// serveRaw 用嗅探出的 Content-Type 回原始字节（HEIC 转码不可用时的兜底：
// 浏览器解不了会走 onError，界面已有首字/灰盒降级）。
func (s *Server) serveRaw(w http.ResponseWriter, ctype string, data []byte) {
	if strings.TrimSpace(ctype) == "" {
		ctype = http.DetectContentType(data)
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store")
	h.Del("Server")
	w.WriteHeader(http.StatusOK)
	if data != nil {
		_, _ = w.Write(data)
	}
}

// serveImageFile 回一张已缓存的图片文件。
func (s *Server) serveImageFile(w http.ResponseWriter, r *http.Request, path, ctype string) {
	fi, err := os.Stat(path)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	h.Set("Cache-Control", "public, max-age=604800")
	h.Del("Server")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

// imgHostAllowed 按后缀白名单校验回源主机（防 SSRF：拒绝内网/任意域名）。
func imgHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suffix := range imgHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// isHEICURL 判断远端地址的 path 是否以 .heic 结尾（带签名 query 的 URL 同样适用）。
func isHEICURL(remote string) bool {
	parsed, err := url.Parse(remote)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(parsed.Path), ".heic")
}

// Close 停止监听并撤销全部 token。
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.token = ""
	s.episodes = make(map[string]string)
	serveErr := s.serveErr
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		_ = s.httpSrv.Close()
		return err
	}
	return serveErr
}

// serve 校验 token 并分派到文件服务。所有失败分支只给状态码与无正文响应，
// 不回显原因（方案 §3.4）。
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] != "v" {
		s.notFound(w)
		return
	}
	if !s.tokenOK(parts[1]) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	name, err := url.PathUnescape(parts[2])
	if err != nil || !strings.HasSuffix(name, ".mp4") {
		s.notFound(w)
		return
	}
	episodeID := strings.TrimSuffix(name, ".mp4")
	s.mu.RLock()
	file, ok := s.episodes[episodeID]
	s.mu.RUnlock()
	if !ok {
		s.notFound(w)
		return
	}
	s.serveFile(w, r, file)
}

func (s *Server) notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusNotFound)
}

// tokenOK 用常量时间比较，避免长度/内容差异被计时侧信道利用。
func (s *Server) tokenOK(got string) bool {
	s.mu.RLock()
	want := s.token
	s.mu.RUnlock()
	if len(want) == 0 || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// serveFile 按「增长中文件」的语义供给：每次请求重新拿当前大小，
// 单段 Range、始终写 Content-Length（禁止 chunked）、越界 416 带最新长度。
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		s.notFound(w)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		s.notFound(w)
		return
	}
	size := fi.Size()
	mod := fi.ModTime()
	etag := fmt.Sprintf(`"%x-%x"`, size, mod.UnixNano())

	h := w.Header()
	h.Set("Content-Type", mimeVideoMP4)
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", etag)
	h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
	// 增长中的文件不能被缓存成最终版本。
	h.Set("Cache-Control", "no-store")
	h.Del("Server")

	start, length, status := int64(0), size, http.StatusOK
	spec := r.Header.Get("Range")
	if spec != "" && !strings.Contains(spec, ",") && ifRangeMatches(r.Header.Get("If-Range"), etag, mod) {
		rng, ok := parseRangeHeader(spec, size)
		if !ok {
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, length, status = rng.start, rng.length, http.StatusPartialContent
	}

	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if status == http.StatusPartialContent {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead || length == 0 {
		return
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return
	}
	// 文件只追加不截断，所以按已快照的长度读到底即可；浏览器若还想要更多，
	// 会带新的 Range 再来一次，届时 Content-Length 已是新前沿。
	_, _ = io.CopyN(w, f, length)
}

// ifRangeMatches 实现 If-Range：ETag 必须强匹配；日期型校验器要求文件不更新于该日期。
// 不匹配时调用方忽略 Range，回整文件 200。
func ifRangeMatches(ifRange, etag string, mod time.Time) bool {
	ir := strings.TrimSpace(ifRange)
	if ir == "" {
		return true
	}
	if strings.HasPrefix(ir, "W/") {
		return false
	}
	if strings.HasPrefix(ir, `"`) {
		return ir == etag
	}
	t, err := http.ParseTime(ir)
	if err != nil {
		return false
	}
	return !mod.Truncate(time.Second).After(t)
}

// newToken 生成 32 字节随机 token，hex 编码（64 字符）。
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
