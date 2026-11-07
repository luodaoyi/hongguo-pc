// Package mpvplayer 用 libmpv（自写 purego 最小绑定，无 CGO、惰性加载）替代
// WebView2 的 <video> 内核播放本地媒体。
//
// 为什么换内核：Chromium 的 MP4 demuxer 对渐进 fMP4 的音频包容错是
// fail-fast——单个坏包（实测 size=139 的 audio packet）直接中止整条解码
// 管线（PIPELINE_ERROR_DECODE），前端 onEnded/onError 被触发、连播误跳集。
// mpv 的 demuxer 遇坏包跳过继续播，且 demuxer-max-bytes-bytes 可配大缓冲，
// 渐进分片边写边播的等待行为也更可控。
//
// 窗口模型：mpv wid 指向主窗口的 WS_CHILD 子窗口——父动子随、点击穿透
// 到 WebView（owned 顶层窗方案实测拖动不跟、挡死控制条，已弃）。
// 播放/暂停/进度/音量经 mpv 属性读写；EndFile 事件回调上层决定连播。
//
// dll 缺失（libmpv-2.dll 不在 exe 旁）时 Available() 为 false，前端回退
// <video> 路径——不阻断播放功能。
package mpvplayer

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// callbacks 是上层（bindings/Player）注册的事件出口；mpv 事件线程回调里
// 只做非阻塞投递，业务逻辑（连播解析、进度落盘）由既有 Go 侧处理。
type callbacks struct {
	onEnded func(reason string) // eof | error | quit
	onTick  func(positionSec, durationSec float64)
	onPlay  func()
	onPause func()
}

// Player 是单例 mpv 实例：一个应用同时只播一个视频，复用实例省去
// 反复初始化的开销（mpv Initialize 约几十毫秒）。
type Player struct {
	mu         sync.Mutex
	mpv        *nativeClient
	owner      bool // dll 是否加载成功（Available 的内核依据）
	cb         callbacks
	ticker     *time.Ticker
	renderHwnd windows.HWND // Windows 渲染窗（wid），Close 时销毁
	lastURL    string

	// 渲染窗几何：子窗口坐标相对父客户区（= 前端视口相对物理像素），
	// 父动子随、无需换算与轮询。窗口未创建时先暂存，AttachOwner 建窗后
	// 立即应用（Play 前到达的首 Move 不丢失）。
	pendingRect [4]int32
	ownerHwnd   uintptr
}

var (
	single     *Player
	singleOnce sync.Once
)

// Get 返回进程级单例。
func Get() *Player {
	singleOnce.Do(func() {
		single = &Player{}
		single.owner = probe()
	})
	return single
}

// probe 真实加载 libmpv（结果缓存于 native 包内），非文件存在性猜测。
func probe() bool { return nativeAvailable() }

// Available 报告本机能否走 mpv 路径（dll 是否就位）。
func Available() bool { return Get().owner }

// SetCallbacks 注册事件出口（bind 一次，换集不重装）。
func (p *Player) SetCallbacks(onEnded func(reason string), onTick func(pos, dur float64), onPlay, onPause func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cb = callbacks{onEnded: onEnded, onTick: onTick, onPlay: onPlay, onPause: onPause}
}

// Play 用 mpv 打开 url（localsrv /v/ 或 direct CDN）。
// ownerPtr 是 Wails 主窗口句柄（首次调用时据此建渲染窗并设 wid）；
// wid 设定必须在首次 loadfile 之前（mpv 语义：加载后再设 wid 会重启 vo）。
// loadfile 同步等待 file-loaded（最多 10 秒）才返回——前端拿到返回后
// 立即 seek/设倍速都是安全的（首版不等导致 seek 落空被误判失败回退）。
func (p *Player) Play(url, title string, ownerPtr uintptr) error {
	p.mu.Lock()
	if !p.owner {
		p.mu.Unlock()
		return fmt.Errorf("libmpv 不可用：exe 旁缺少 libmpv-2.dll")
	}
	if p.mpv == nil {
		// 渲染窗必须先于实例存在：**wid 是 initialization-only 选项**，
		// Initialize 后 SetPropertyString 会被 mpv 拒绝（首版「无法显示
		// mpv 窗口」的根因）——先建窗，把 wid 传进 initInstance。
		if p.renderHwnd == 0 {
			p.mu.Unlock()
			if _, err := p.AttachOwner(ownerPtr); err != nil {
				return err
			}
			p.mu.Lock()
		}
		if err := p.initInstance(fmt.Sprintf("%d", int64(p.renderHwnd))); err != nil {
			p.mu.Unlock()
			return err
		}
	}
	p.lastURL = url
	if title != "" {
		_ = p.mpv.SetPropertyString("force-media-title", title)
	}
	if err := p.mpv.Command([]string{"loadfile", url}); err != nil {
		p.mu.Unlock()
		return err
	}
	// 起观察泵（EndFile 后上一泵已退役），应用几何（子窗口随父自动跟随）。
	p.startObserveLocked()
	p.mu.Unlock()
	p.applyPending()

	// 等文件加载完成（事件泵在后台跑，这里轮询核心属性）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		m := p.mpv
		p.mu.Unlock()
		if m == nil {
			return fmt.Errorf("mpv 已关闭")
		}
		if v, err := m.GetProperty("filename", FormatString); err == nil && v.(string) != "" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mpv 加载文件超时（10s）")
}

// initInstance 初始化（或重置后再次初始化）mpv 实例与属性。
// wid 非空时在 Initialize 前设置（initialization-only），渲染进宿主窗口；
// wid 为空时 force-window 自建窗口（独立窗口模式）。
func (p *Player) initInstance(wid string) error {
	m, err := newNativeClient()
	if err != nil {
		return err
	}
	// 事件必开：EndFile/时间更新走事件线程，不开收不到。
	if err := m.SetOptionString("terminal", "no"); err != nil {
		return fmt.Errorf("mpv option terminal: %w", err)
	}
	// 大缓冲：渐进 fMP4 边写边播，给 demuxer 足量前向缓存（128MB），
	// 网络抖动/磁盘慢时宁可多等也不断流。
	_ = m.SetOptionString("demuxer-max-bytes", "128MiB")
	_ = m.SetOptionString("demuxer-readahead-secs", "60")
	// 播完触发 EndFile(eof)，不驻留。
	_ = m.SetOptionString("keep-open", "no")
	if wid != "" {
		// 必须在 Initialize 前设置；窗口由宿主（owned 弹窗）提供。
		_ = m.SetOptionString("wid", wid)
		_ = m.SetOptionString("force-window", "no")
	} else {
		_ = m.SetOptionString("force-window", "immediate")
		_ = m.SetOptionString("title", "红果短剧")
	}
	if err := m.Initialize(); err != nil {
		return fmt.Errorf("mpv initialize: %w", err)
	}
	p.mpv = m
	return nil
}

// startObserveLocked 起事件泵与属性轮询（调用方已持锁；泵内部自行取锁）。
func (p *Player) startObserveLocked() {
	if p.ticker != nil {
		p.ticker.Stop()
	}
	p.startObserve()
}

// startObserve 起事件泵与属性轮询。mpv 事件线程不能阻塞；时间/时长经
// 500ms 轮询属性拿（比 observe-property 事件在 purego 下更稳）。
func (p *Player) startObserve() {
	if p.ticker != nil {
		p.ticker.Stop()
	}
	p.ticker = time.NewTicker(500 * time.Millisecond)
	lastPaused := false // 泵周期内的暂停态（变化沿才发事件）
	go func(t *time.Ticker) {
		for range t.C {
			p.mu.Lock()
			m := p.mpv
			cb := p.cb
			if m == nil {
				p.mu.Unlock()
				return
			}
			// 事件先排空：EndFile 在属性读取前处理，跳集判定不被延迟。
			for {
				ev := m.WaitEvent(0)
				if ev.ID == eventNone {
					break
				}
				if ev.ID == eventEnd {
					p.mu.Unlock()
					if cb.onEnded != nil {
						cb.onEnded(classifyEndReason(ev.EndReason))
					}
					return // EndFile 后本泵退役；下次 Play 重新起
				}
			}
			pos, dur := 0.0, 0.0
			if v, err := m.GetProperty("time-pos", FormatDouble); err == nil {
				pos, _ = v.(float64)
			}
			if v, err := m.GetProperty("duration", FormatDouble); err == nil {
				dur, _ = v.(float64)
			}
			paused := false
			if v, err := m.GetProperty("pause", FormatFlag); err == nil {
				paused, _ = v.(bool)
			}
			p.mu.Unlock()
			if cb.onTick != nil {
				cb.onTick(pos, dur)
			}
			// 仅在暂停态变化时发 play/pause：无条件每 tick 发会把前端
			// 播放按钮打回「未播放」（首版实测按钮状态闪烁）。
			if paused != lastPaused {
				lastPaused = paused
				if paused {
					if cb.onPause != nil {
						cb.onPause()
					}
				} else {
					if cb.onPlay != nil {
						cb.onPlay()
					}
				}
			}
		}
	}(p.ticker)
}

// TogglePause 翻转暂停态。
func (p *Player) TogglePause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mpv == nil {
		return fmt.Errorf("mpv 未初始化")
	}
	return p.mpv.Command([]string{"cycle", "pause"})
}

// SeekTo 绝对跳转（秒）。
func (p *Player) SeekTo(sec float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mpv == nil {
		return fmt.Errorf("mpv 未初始化")
	}
	return p.mpv.Command([]string{"seek", fmt.Sprintf("%.2f", sec), "absolute"})
}

// SetRate 设置倍速（0.5-3.0，与前端档位一致）。
func (p *Player) SetRate(rate float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mpv == nil {
		return fmt.Errorf("mpv 未初始化")
	}
	return p.mpv.SetPropertyString("speed", fmt.Sprintf("%.2f", rate))
}

// SetVolume 设置音量（0-100）。
func (p *Player) SetVolume(v int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mpv == nil {
		return fmt.Errorf("mpv 未初始化")
	}
	return p.mpv.SetPropertyString("volume", fmt.Sprintf("%d", v))
}

// Stop 停止播放（触发 EndFile(stop)，上层按 quit 处理不跳集）。
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// 渲染窗一并隐藏：离开播放页/回退 <video> 时黑窗不残留。
	// EndFile(stop) 会让事件泵退役，前端收到 quit 静默处理。
	p.hideRenderWindowLocked()
	if p.mpv == nil {
		return
	}
	_ = p.mpv.Command([]string{"stop"})
}

// Position 当前播放位置与总时长（秒）。
func (p *Player) Position() (float64, float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mpv == nil {
		return 0, 0
	}
	pos, dur := 0.0, 0.0
	if v, err := p.mpv.GetProperty("time-pos", FormatDouble); err == nil {
		pos, _ = v.(float64)
	}
	if v, err := p.mpv.GetProperty("duration", FormatDouble); err == nil {
		dur, _ = v.(float64)
	}
	return pos, dur
}

// Close 退出时销毁实例（main cleanup 调；重复调用安全）。
func (p *Player) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ticker != nil {
		p.ticker.Stop()
		p.ticker = nil
	}
	p.closeRenderWindow()
	if p.mpv != nil {
		p.mpv.TerminateDestroy()
		p.mpv = nil
	}
}
