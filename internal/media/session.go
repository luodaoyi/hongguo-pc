package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// defaultKeepLast 是同一会话保留的最近集数（方案 §3.4 建议 N=5，估算）。
	defaultKeepLast = 5
	// defaultMaxBytes 是临时目录总上限（方案 §3.4 建议 5 GB，估算）。
	defaultMaxBytes = int64(5 << 30)
	// readyTimeout 是「等 ffmpeg 写出首个分片」的上限；超时即判失败。
	readyTimeout = 10 * time.Second
	// stderrLimit 是每个进程保留的 stderr 字节数，够 20 行回溯即可。
	stderrLimit = 16 << 10
)

var (
	// ErrKeyRequired 表示加密集没有拿到内容密钥。
	ErrKeyRequired = errors.New("media: 加密集需要内容密钥")
	// ErrKilled 表示 ffmpeg 被本方主动终止（切集 / 退出），不是解密失败。
	ErrKilled = errors.New("media: ffmpeg 被主动终止")
	// ErrClosed 表示会话已关闭。
	ErrClosed = errors.New("media: 媒体会话已关闭")
)

// SessionConfig 是会话配置。FFmpegPath 由 DetectFFmpeg 提供；Dir 为空时用
// data/tmp/<sessionID>/；KeepLast / MaxBytes 为 0 时取默认 5 集 / 5 GB。
type SessionConfig struct {
	FFmpegPath string
	Dir        string
	KeepLast   int
	MaxBytes   int64
}

// Output 是一集的解密产物。SizeAt 读**当前**文件大小（文件仍在增长），
// 供 localsrv 或进度显示使用。
type Output struct {
	Path   string
	SizeAt func() int64
}

// Session 管理一个应用会话的 ffmpeg 子进程与临时文件。
//
// 并发模型：允许同时跑多个进程（例如预取下一集边看边解），每个 Start 一个
// 独立输出文件；Wait 等的是最近一次 Start 的进程，Kill 杀掉本会话全部进程。
// 临时文件按 LRU 保留最近 KeepLast 集，切集不删当前产物（方案 §3.4）。
type Session struct {
	ffmpeg   string
	dir      string
	keepLast int
	maxBytes int64
	seq      atomic.Uint64

	mu     sync.Mutex
	closed bool
	files  map[string]*tmpFile
	procs  map[*process]struct{}
	latest *process
}

// tmpFile 是临时文件治理表里的一条。
type tmpFile struct {
	episodeID string
	path      string
	size      int64
	lastUsed  time.Time
}

// NewSession 建会话目录并确认 ffmpeg 可执行。它不会清理别的会话目录，
// 遗留清理是显式的 Sweep（启动时调用一次）。
func NewSession(cfg SessionConfig) (*Session, error) {
	ff, err := resolveFFmpeg(cfg.FFmpegPath)
	if err != nil {
		return nil, err
	}
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		dir = filepath.Join("data", "tmp", newSessionID())
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("media: 无法确定临时目录: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("media: 创建临时目录失败: %w", err)
	}
	keep := cfg.KeepLast
	if keep <= 0 {
		keep = defaultKeepLast
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	return &Session{
		ffmpeg:   ff,
		dir:      absDir,
		keepLast: keep,
		maxBytes: maxBytes,
		files:    make(map[string]*tmpFile),
		procs:    make(map[*process]struct{}),
	}, nil
}

// Dir 返回会话临时目录的绝对路径。
func (s *Session) Dir() string { return s.dir }

// Start 拉起 ffmpeg 写渐进 fMP4，等首个分片落盘后返回。
//
// ctx 只约束「等首块」这一段：返回后进程生命周期由 Kill/Close 管理，
// 避免 player 的短命 context 把正在播放的解码进程杀掉。
func (s *Session) Start(ctx context.Context, in Input) (Output, error) {
	if strings.TrimSpace(in.SourceURL) == "" {
		return Output{}, errors.New("media: SourceURL 为空")
	}
	if in.Protected {
		if in.KeyHex == "" {
			return Output{}, fmt.Errorf("%w（episode=%s）", ErrKeyRequired, in.EpisodeID)
		}
		if err := validateKey(in.KeyHex); err != nil {
			return Output{}, err
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Output{}, ErrClosed
	}
	outPath := filepath.Join(s.dir, fmt.Sprintf("%s-%d-%d.mp4",
		safeName(in.EpisodeID), time.Now().UnixMilli(), s.seq.Add(1)))
	s.mu.Unlock()

	cmd := exec.Command(s.ffmpeg, ffmpegArgs(in, outPath)...)
	cmd.SysProcAttr = sysProcAttr()
	tail := newTailBuffer(stderrLimit)
	cmd.Stderr = tail
	cmd.Stdout = io.Discard
	p := &process{cmd: cmd, path: outPath, keyHex: in.KeyHex, stderr: tail, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return Output{}, fmt.Errorf("media: 启动 ffmpeg 失败: %w", err)
	}
	go p.run()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		p.kill()
		<-p.done
		return Output{}, ErrClosed
	}
	s.procs[p] = struct{}{}
	s.latest = p
	s.mu.Unlock()

	// 等 init 段（ftyp+moov）完整落盘：解析顶层 box，等 moov 的字节全部写齐。
	// 前端此时起播；后续 moof 由 Range 服务按已写前沿供给。
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	ready := false
	for !ready {
		if fi, statErr := os.Stat(outPath); statErr == nil && fi.Size() > 0 {
			if need := initSegmentSize(outPath); need > 0 && fi.Size() >= need {
				ready = true
				break
			}
		}
		select {
		case <-p.done:
			// 进程已结束：正常退出说明整个文件已写完，即便没扫到 moof 也可用。
			fi, statErr := os.Stat(outPath)
			if statErr != nil || fi.Size() == 0 {
				return Output{}, p.failure()
			}
			ready = true
		case <-ctx.Done():
			p.kill()
			return Output{}, fmt.Errorf("media: 等待首块时上下文结束: %w", ctx.Err())
		case <-deadline.C:
			p.kill()
			select {
			case <-p.done:
			case <-time.After(3 * time.Second):
			}
			detail := p.describe()
			if detail == nil {
				detail = errors.New("进程仍在运行且未产出数据")
			}
			return Output{}, fmt.Errorf("media: ffmpeg 在 %s 内未写出首个分片: %w", readyTimeout, detail)
		case <-ticker.C:
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Output{}, ErrClosed
	}
	s.files[outPath] = &tmpFile{episodeID: in.EpisodeID, path: outPath, lastUsed: time.Now()}
	s.retainLocked()
	s.mu.Unlock()

	return Output{
		Path: outPath,
		SizeAt: func() int64 {
			fi, statErr := os.Stat(outPath)
			if statErr != nil {
				return 0
			}
			return fi.Size()
		},
	}, nil
}

// Wait 等最近一次 Start 的 ffmpeg 退出。非 0 退出返回带 stderr 尾部（已脱敏）
// 的错误；被本方 Kill 的返回 ErrKilled。
func (s *Session) Wait() error {
	s.mu.Lock()
	p := s.latest
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	<-p.done
	s.mu.Lock()
	delete(s.procs, p)
	s.mu.Unlock()
	return p.failure()
}

// Kill 终止本会话全部 ffmpeg 进程（切集 / 退出播放）。输出文件保留到 LRU 淘汰。
func (s *Session) Kill() {
	s.mu.Lock()
	procs := make([]*process, 0, len(s.procs))
	for p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()
	for _, p := range procs {
		p.kill()
	}
}

// Close 停掉全部进程并删除本会话临时目录（方案 §3.4「退出时清空本会话目录」）。
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	dir := s.dir
	procs := make([]*process, 0, len(s.procs))
	for p := range s.procs {
		procs = append(procs, p)
	}
	s.procs = map[*process]struct{}{}
	s.mu.Unlock()

	for _, p := range procs {
		p.kill()
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("media: 清理临时目录失败: %w", err)
	}
	return nil
}

// Sweep 清空临时根目录下的全部遗留产物。**只在应用启动、尚未创建会话时调用一次**
// （方案 §3.4「启动时扫描并删除遗留的 tmp/*」）。root 不存在视为已完成。
func Sweep(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("media: 扫描临时目录失败: %w", err)
	}
	removed := 0
	var firstErr error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("media: 删除遗留产物 %s 失败: %w", e.Name(), err)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// retainLocked 执行 LRU 淘汰：文件数超过 KeepLast 或总量超过 MaxBytes 时，
// 从最久未用且不在运行中的产物开始删。
func (s *Session) retainLocked() {
	for p := range s.files {
		if _, err := os.Stat(p); err != nil {
			delete(s.files, p)
		}
	}
	active := make(map[string]bool, len(s.procs))
	for p := range s.procs {
		active[p.path] = true
	}
	var total int64
	for _, f := range s.files {
		if fi, err := os.Stat(f.path); err == nil {
			f.size = fi.Size()
			total += f.size
		}
	}
	for len(s.files) > s.keepLast || total > s.maxBytes {
		victim := s.oldestLocked(active)
		if victim == nil {
			return
		}
		if err := os.Remove(victim.path); err != nil && !os.IsNotExist(err) {
			// 文件被占用（例如播放器正读）：本轮不淘汰它，留到下轮再试。
			return
		}
		total -= victim.size
		delete(s.files, victim.path)
	}
}

// oldestLocked 挑最久未用的非运行中产物；没有返回 nil。
func (s *Session) oldestLocked(active map[string]bool) *tmpFile {
	var oldest *tmpFile
	for _, f := range s.files {
		if active[f.path] {
			continue
		}
		if oldest == nil || f.lastUsed.Before(oldest.lastUsed) {
			oldest = f
		}
	}
	return oldest
}

// process 是一个 ffmpeg 子进程及其 stderr 尾部。
type process struct {
	cmd    *exec.Cmd
	path   string
	keyHex string
	stderr *tailBuffer
	done   chan struct{}

	mu     sync.Mutex
	err    error
	killed bool
}

// run 等进程退出并记录结果；完成后关闭 done，供 Start/Wait/Close 观察。
func (p *process) run() {
	err := p.cmd.Wait()
	p.mu.Lock()
	switch {
	case p.killed:
		p.err = ErrKilled
	case err != nil:
		p.err = err
	}
	p.mu.Unlock()
	close(p.done)
}

func (p *process) kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// failure 等进程退出后汇总错误。
func (p *process) failure() error {
	<-p.done
	return p.describe()
}

// describe 读当前已知的退出错误与 stderr 尾部，不等待进程。
// 密钥与任意长 hex 串在返回前一律脱敏。
func (p *process) describe() error {
	p.mu.Lock()
	err := p.err
	p.mu.Unlock()
	tail := redact(strings.TrimSpace(p.stderr.Tail(20)), p.keyHex)
	switch {
	case errors.Is(err, ErrKilled):
		return ErrKilled
	case err == nil && tail == "":
		return nil
	case err == nil:
		return fmt.Errorf("media: ffmpeg 尚无退出状态; stderr 尾部: %s", tail)
	case tail == "":
		return fmt.Errorf("media: ffmpeg 退出: %w", err)
	default:
		return fmt.Errorf("media: ffmpeg 退出: %w; stderr 尾部: %s", err, tail)
	}
}

// tailBuffer 只保留最后 limit 字节的 stderr，避免大日志吃内存。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	if limit <= 0 {
		limit = stderrLimit
	}
	return &tailBuffer{limit: limit}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

// Tail 返回最后 n 行非空内容。
func (t *tailBuffer) Tail(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimRight(string(t.buf), "\r\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// initSegmentSize 解析文件头部的顶层 box，返回 ftyp+moov 写完所需的字节数。
// 返回 0 表示还没法判断（文件太短、box 不合法或 moov 出现在 mdat/moof 之后）。
//
// 只读 box 头，不随文件增长而变慢；空 moov（empty_moov）场景下 moov 很小，
// 看到它写齐就说明 init 段完整。
func initSegmentSize(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var offset int64
	var hdr [8]byte
	for i := 0; i < 8; i++ {
		if _, err := f.ReadAt(hdr[:], offset); err != nil {
			return 0
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:])
		if size < 8 {
			return 0
		}
		switch typ {
		case "moov":
			return offset + size
		case "moof", "mdat":
			// 正常封装里 moov 必在媒体数据之前。
			return 0
		}
		offset += size
		if offset > 16<<20 {
			return 0
		}
	}
	return 0
}

// validateKey 只校验长度与 hex 合法性；错误信息不含密钥值。
func validateKey(keyHex string) error {
	if len(keyHex) != 32 && len(keyHex) != 64 {
		return errors.New("media: 内容密钥须为 32 或 64 位 hex")
	}
	if _, err := hex.DecodeString(keyHex); err != nil {
		return errors.New("media: 内容密钥不是合法 hex")
	}
	return nil
}

// resolveFFmpeg 把配置里的 ffmpeg 路径规整成可启动的绝对路径。
func resolveFFmpeg(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", errors.New("media: FFmpegPath 为空（先用 DetectFFmpeg 探测）")
	}
	resolved, ok := resolveExecutable(p)
	if !ok {
		return "", fmt.Errorf("media: ffmpeg 不可执行: %s", p)
	}
	return resolved, nil
}

// safeName 把 episodeID 变成安全的文件名片段：只留字母数字与 -_。
// 输出文件名还会追加序号与扩展名，因此不会出现路径穿越。
func safeName(id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "episode"
	}
	return name
}

// newSessionID 生成 8 字节随机会话标识；随机源不可用时退化为时间戳。
func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return "sess-" + hex.EncodeToString(b[:])
}
