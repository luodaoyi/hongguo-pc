// Package localsrv 是 127.0.0.1 上的临时文件 Range 服务（方案 §3.4 / §3.8）。
//
// 它只端已解密的文件，不持有密钥，也不理解播放模型。安全约束：
// 只监听回环地址、系统分配端口、路径带 32 字节随机会话 token、token 不符直接 403
// 且不解释、不打印明文 URL、去掉 Server 头、Close 后撤销全部 token。
package localsrv

import (
	"context"
	"crypto/rand"
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
)

const mimeVideoMP4 = "video/mp4"

// ErrClosed 表示服务已关闭。
var ErrClosed = errors.New("localsrv: 服务已关闭")

// Config 是本地服务的配置。Root 是临时文件根目录；非空时 URL 只接受该目录内的
// 文件，防止调用方传错路径把任意文件端出去。
type Config struct {
	Root string
}

// Server 是本地 Range 服务。
type Server struct {
	ln      net.Listener
	httpSrv *http.Server
	token   string
	root    string

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("localsrv: 监听回环地址失败: %w", err)
	}
	s := &Server{
		ln:       ln,
		token:    token,
		root:     root,
		episodes: make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v/", s.serve)
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
