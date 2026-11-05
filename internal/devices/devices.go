// Package devices 是设备配额层（方案 §2.1 的 L1 层）。
//
// 一个设备槽 = 一份独立匿名设备档案 + 一个 sdkx.Client。池替上层分摊单台设备的
// 播放额度，并对每一次额度消耗记账。
//
// 硬规矩（方案 §2.2 I3）：**任何调用方不得绕过 Pool 直连 sdkx.Client**。
// 目录接口（Search / GetSeries / ListEpisodes / Seasons / SeriesRecommend）走
// Pool.Do；播放解析走 Pool.ResolvePlayback。裸调 sdkx.Client.ResolvePlayback
// 会让 QuotaUsed / LastUsed / CoolUntil 与真实档案脱节，评审直接打回。
//
// 单飞：同一个槽同一时刻只跑一个请求（Acquire 会等到槽空闲或 ctx 结束）。
package devices

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

// 默认参数。maxPerResolution 默认 2，与 SDK 默认 DeviceResets 对齐（方案 §4.3）。
const (
	defaultSlots            = 4
	defaultMaxPerResolution = 2
	defaultCoolDown         = 10 * time.Minute
	defaultQuotaWindow      = 24 * time.Hour
)

// Slot 是池内一个设备槽的句柄。
//
// ID 与 ProfilePath 是只读的常量；其余字段由 Pool 在内部锁内维护，
// 池外不得读写。客户端只在 Acquire 之后、Release 之前使用（见 Client）。
type Slot struct {
	ID          string
	ProfilePath string

	client *sdkx.Client
	// 以下字段由 Pool.mu 保护。
	state     string
	quotaUsed int
	quotaDay  int
	coolUntil time.Time
	lastUsed  time.Time
	rejects   int
	busy      bool
}

// Client 返回该槽的 SDK 客户端。
//
// 只允许在 Acquire 成功之后、Release 之前使用；回调结束后不要再持有它。
func (s *Slot) Client() *sdkx.Client { return s.client }

// Config 是建池参数。除 Dir 外都有默认值。
type Config struct {
	// Dir 是设备档案目录，每个槽写一份 <slotID>.json（形如 data/devices/d001.json）。
	Dir string
	// Slots 是槽数量，<=0 时取 4。
	Slots int
	// MaxPerResolution 是一次解析最多换几台设备，<=0 时取 2。
	MaxPerResolution int
	// CoolDown 是设备被拒后的冷却时长，<=0 时取 10 分钟（方案 §4.4）。
	CoolDown time.Duration
	// QuotaWindow 是额度窗口的保守长度，<=0 时取 24 小时（方案 §4.4 / 附录 A1）。
	QuotaWindow time.Duration
	// Store 可选：非空时把槽记账写入 device_slot 表，并支持跨进程恢复。
	Store store.Store
	// Now 可选：注入时钟（测试用），默认 time.Now。
	Now func() time.Time
	// Logf 可选：诊断日志，默认丢弃。**不得**在里面打印设备档案或密钥。
	Logf func(format string, args ...any)
	// NewClient 可选：注入客户端构造（测试用），默认 sdkx.New。
	NewClient func(profilePath string, onRejected func(error)) (*sdkx.Client, error)
	// Unlimited 开启后，播放路径在没有 healthy 槽时按需注册一台新匿名设备并
	// 立即用于本次解析（池无硬上限）。仅由显式设置项打开。
	Unlimited bool
}

// Pool 是设备池。并发安全。
type Pool struct {
	mu    sync.Mutex
	slots []*Slot
	// waitCh 在每次 Release 时关闭并换新，用来唤醒等待空闲槽的 Acquire。
	waitCh chan struct{}

	maxPerResolution int
	coolDown         time.Duration
	quotaWindow      time.Duration
	st               store.Store
	now              func() time.Time
	logfFn           func(string, ...any)
	keys             *Keyring
	nonPlaybackSwaps int

	// 按需扩槽的构造参数与并发单飞：多个请求同时缺槽时只新建一台。
	dir       string
	newClient func(profilePath string, onRejected func(error)) (*sdkx.Client, error)
	unlimited bool
	growing   bool
	growWait  chan struct{}
}

// NewPool 建池：确保目录存在、按 id 建槽、并从 device_slot 表恢复记账。
//
// 建池不发任何网络请求；匿名设备在首次读取时才注册。
func NewPool(cfg Config) (*Pool, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return nil, fmt.Errorf("devices: device directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("devices: create device directory %s: %w", dir, err)
	}
	slots := cfg.Slots
	if slots <= 0 {
		slots = defaultSlots
	}
	maxPer := cfg.MaxPerResolution
	if maxPer <= 0 {
		maxPer = defaultMaxPerResolution
	}
	coolDown := cfg.CoolDown
	if coolDown <= 0 {
		coolDown = defaultCoolDown
	}
	window := cfg.QuotaWindow
	if window <= 0 {
		window = defaultQuotaWindow
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	newClient := cfg.NewClient
	if newClient == nil {
		newClient = func(profilePath string, onRejected func(error)) (*sdkx.Client, error) {
			return sdkx.New(sdkx.Options{ProfilePath: profilePath, OnDeviceRejected: onRejected})
		}
	}

	p := &Pool{
		waitCh:           make(chan struct{}),
		maxPerResolution: maxPer,
		coolDown:         coolDown,
		quotaWindow:      window,
		st:               cfg.Store,
		now:              nowFn,
		logfFn:           logf,
		keys:             NewKeyring(),
		dir:              dir,
		newClient:        newClient,
		unlimited:        cfg.Unlimited,
	}
	for i := 0; i < slots; i++ {
		id := fmt.Sprintf("d%03d", i+1)
		profilePath := filepath.Join(dir, id+".json")
		s := &Slot{ID: id, ProfilePath: profilePath, state: model.SlotHealthy}
		client, err := newClient(profilePath, func(error) { p.NoteDeviceRejected(s) })
		if err != nil {
			return nil, fmt.Errorf("devices: create client for slot %s: %w", id, err)
		}
		s.client = client
		p.slots = append(p.slots, s)
	}
	p.restore()
	return p, nil
}

// growSlot 按需新建一个匿名设备槽并挂到池尾。构造不发网络请求（档案在首次
// 目录/播放调用时才注册）；并发时由 growing/growWait 单飞，多个缺槽请求共用
// 同一次新建结果，避免瞬间批量建档。返回的槽已标记 busy，调用方负责 Release。
func (p *Pool) growSlot(ctx context.Context) (*Slot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		p.mu.Lock()
		if !p.unlimited {
			p.mu.Unlock()
			return nil, ErrQuotaExhausted
		}
		if p.growing {
			wait := p.growWait
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
				// 等新建完成后重新走一遍常规选槽（可能已被别的请求占走）。
				return p.Acquire(ctx)
			}
		}
		p.growing = true
		p.growWait = make(chan struct{})
		id := fmt.Sprintf("d%03d", len(p.slots)+1)
		profilePath := filepath.Join(p.dir, id+".json")
		p.mu.Unlock()

		s := &Slot{ID: id, ProfilePath: profilePath, state: model.SlotHealthy}
		client, err := p.newClient(profilePath, func(error) { p.NoteDeviceRejected(s) })
		if err != nil {
			p.finishGrow()
			return nil, fmt.Errorf("devices: create client for on-demand slot %s: %w", id, err)
		}
		s.client = client
		s.busy = true

		p.mu.Lock()
		p.slots = append(p.slots, s)
		p.persistLocked(s)
		p.mu.Unlock()
		p.logf("devices: on-demand slot %s registered (pool size %d)", id, len(p.slots))
		p.finishGrow()
		return s, nil
	}
}

// finishGrow 结束一次扩槽并唤醒等待者。调用方不得持有 p.mu。
func (p *Pool) finishGrow() {
	p.mu.Lock()
	p.growing = false
	wait := p.growWait
	p.growWait = make(chan struct{})
	p.mu.Unlock()
	if wait != nil {
		close(wait)
	}
}

// Keys 返回本会话的密钥表。上层（player/media 接线）从这里取密钥串，
// 除此之外不要复制或缓存密钥（I2）。
func (p *Pool) Keys() *Keyring { return p.keys }

// Key 取一集的内容密钥（等价于 Keys().Get）。
func (p *Pool) Key(episodeID string) (string, bool) { return p.keys.Get(episodeID) }

// restore 从 device_slot 表恢复记账。只恢复计数与时间戳，
// 状态按「冷却是否结束、额度窗口是否还闭着」重新判定。
func (p *Pool) restore() {
	if p.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	records, err := p.st.DeviceSlots(ctx)
	if err != nil {
		p.logf("devices: restore slot accounting: %v", err)
		return
	}
	byID := make(map[string]store.DeviceSlotRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}
	now := p.now()
	for _, s := range p.slots {
		r, ok := byID[s.ID]
		if !ok {
			continue
		}
		s.quotaUsed = r.QuotaUsed
		s.quotaDay = r.QuotaDay
		s.lastUsed = unixTime(r.LastUsed)
		s.coolUntil = unixTime(r.CoolUntil)
		s.rejects = r.RejectCount
		switch r.State {
		case model.SlotExhausted:
			// 额度窗口还闭着就继续停用，否则按新窗口放回。
			if !p.newQuotaWindow(s, now) {
				s.state = model.SlotExhausted
			}
		case model.SlotCooling, model.SlotRejected:
			if now.Before(s.coolUntil) {
				s.state = r.State
			}
		}
	}
}

// Acquire 取一个可用槽：healthy、不 busy、last_used 最久（最久没用过的先用）。
//
// 没有可用槽时：若还有槽在忙则等它们释放（可被 ctx 取消），否则返回 ErrQuotaExhausted。
// 成功后必须配对 Release（ResolvePlayback 与 Do 内部已处理）。
func (p *Pool) Acquire(ctx context.Context) (*Slot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		p.mu.Lock()
		now := p.now()
		var best *Slot
		anyBusy := false
		for _, s := range p.slots {
			p.refreshLocked(s, now)
			if s.busy {
				anyBusy = true
				continue
			}
			if s.state != model.SlotHealthy {
				continue
			}
			if best == nil || s.lastUsed.Before(best.lastUsed) {
				best = s
			}
		}
		if best != nil {
			best.busy = true
			p.mu.Unlock()
			return best, nil
		}
		if !anyBusy {
			p.mu.Unlock()
			// 播放解析没有 healthy 槽：按需扩槽（Unlimited 开启），否则照旧报耗尽。
			if p.unlimited {
				return p.growSlot(ctx)
			}
			return nil, ErrQuotaExhausted
		}
		wait := p.waitCh
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// Release 归还槽。重复调用无副作用。
func (p *Pool) Release(s *Slot) {
	if s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !s.busy {
		return
	}
	s.busy = false
	p.broadcastLocked()
}

// AcquireAny 取一个能执行 0 额度目录调用的槽：优先 healthy，其次 cooling、
// exhausted、rejected。0 额度调用不消耗播放额度，非 healthy 槽的签名依然有效；
// 否则播放额度耗尽后连浏览、选集都被拦死，与界面承诺的口径矛盾。
// 没有任何空闲槽时与 Acquire 相同：等忙槽释放或返回 ErrQuotaExhausted。
func (p *Pool) AcquireAny(ctx context.Context) (*Slot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		p.mu.Lock()
		now := p.now()
		var best *Slot
		bestRank := 0
		haveBest := false
		anyBusy := false
		for _, s := range p.slots {
			p.refreshLocked(s, now)
			if s.busy {
				anyBusy = true
				continue
			}
			rank := slotReadRank(s.state)
			if !haveBest || rank < bestRank || (rank == bestRank && s.lastUsed.Before(best.lastUsed)) {
				best, bestRank, haveBest = s, rank, true
			}
		}
		if best != nil {
			best.busy = true
			p.mu.Unlock()
			return best, nil
		}
		if !anyBusy {
			p.mu.Unlock()
			return nil, ErrQuotaExhausted
		}
		wait := p.waitCh
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// slotReadRank 是只读调用的槽优先级：数值越小越优先。
func slotReadRank(state string) int {
	switch state {
	case model.SlotHealthy:
		return 0
	case model.SlotCooling:
		return 1
	case model.SlotExhausted:
		return 2
	default:
		return 3 // rejected：签名仍可用；调用被拒会走 Do 的换档路径
	}
}

// Commit 记一次成功的播放解析：该槽消耗 1 次额度，并且当场置为 exhausted。
//
// 前提来自方案 §4.1：服务端自 2026-10-29 起基本只给每台匿名设备返回一次播放模型，
// 即「1 次成功解析 = 1 台设备的全部额度」。额度既然在这一次成功里已经用掉，就不能
// 把槽放回 healthy——否则下次 Acquire 必然再选中它、必然返回 ErrAnonymousLimited，
// 再被 MarkExhausted 记一次，同一台设备被计两遍，Status().UsedToday 约为真实值的
// 两倍（T13 审查 D1）。额度窗口的复原见 refreshLocked / newQuotaWindow。
func (p *Pool) Commit(s *Slot) {
	if s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.quotaUsed++
	s.quotaDay = dayKey(now)
	s.lastUsed = now
	s.state = model.SlotExhausted
	s.coolUntil = time.Time{}
	p.persistLocked(s)
}

// MarkExhausted 记「该设备额度已用尽」：ErrAnonymousLimited 时调用（方案 §4.4），
// 以及「模型拿到了但密钥解不开」时（见 resolve.go）。
//
// 一台设备只记一次：成功路径走 Commit、失败路径走本方法，两条路互斥；
// Commit 之后槽已 exhausted，不会再被 Acquire 选中而重复记账。
//
// 额度窗口按「跨本地日 或 距上次使用满 24 小时」复原。**真实重置时点未实测**
// （方案附录 A1），24 小时是保守假设；实测结果回来只改这里一处。
func (p *Pool) MarkExhausted(s *Slot) {
	if s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.quotaUsed++
	s.quotaDay = dayKey(now)
	s.lastUsed = now
	s.state = model.SlotExhausted
	p.persistLocked(s)
}

// MarkCooling 让槽进入冷却，冷却结束前不参与 Acquire。
func (p *Pool) MarkCooling(s *Slot, d time.Duration) {
	if s == nil {
		return
	}
	if d <= 0 {
		d = p.coolDown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.state = model.SlotCooling
	s.coolUntil = now.Add(d)
	s.lastUsed = now
	p.persistLocked(s)
}

// markRecoveredLocked 把「换档成功」的槽立即恢复为可用。调用方必须持有 p.mu。
//
// 新档案意味着新额度，所以 quota_used 归零、冷却清空；rejects 作为诊断数据保留
// （§8.3 诊断页的「拒绝次数」），只有 Pool.Reset 这个显式操作才清零。
func (p *Pool) markRecoveredLocked(s *Slot, now time.Time) {
	s.state = model.SlotHealthy
	s.quotaUsed = 0
	s.quotaDay = dayKey(now)
	s.coolUntil = time.Time{}
}

// NoteDeviceRejected 记一次「设备被服务端拒绝」（方案 §4.4 M1 补充）。
//
// 它是 sdkx.Options.OnDeviceRejected 的落点：GetSeries / ListEpisodes / Search /
// Seasons / SeriesRecommend 返回 ErrDeviceRejected 时同样会走到这里，不只是播放接口。
// 这里只记拒绝次数并让槽进冷却；真正的换档案由三条路完成：ResolvePlayback 的播放
// 路径、Pool.Do 的目录路径（两者都在判到 ErrDeviceRejected 后调 ResetDevice），
// 以及 Pool.Reset（§8.3 的显式操作）。
func (p *Pool) NoteDeviceRejected(s *Slot) {
	if s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.rejects++
	s.state = model.SlotRejected
	if s.coolUntil.Before(now.Add(p.coolDown)) {
		s.coolUntil = now.Add(p.coolDown)
	}
	p.persistLocked(s)
}

// Reset 丢弃指定槽的匿名设备档案并注册新设备（方案 §8.3 的 Devices.Reset）。
func (p *Pool) Reset(ctx context.Context, slotID string) error {
	p.mu.Lock()
	var s *Slot
	for _, candidate := range p.slots {
		if candidate.ID == slotID {
			s = candidate
			break
		}
	}
	if s == nil {
		p.mu.Unlock()
		return fmt.Errorf("devices: slot %q not found", slotID)
	}
	if s.busy {
		p.mu.Unlock()
		return fmt.Errorf("devices: slot %q is busy", slotID)
	}
	s.busy = true
	p.mu.Unlock()
	defer p.Release(s)

	if err := s.client.ResetDevice(ctx); err != nil {
		return fmt.Errorf("devices: reset slot %s: %w", slotID, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.state = model.SlotHealthy
	s.quotaUsed = 0
	s.quotaDay = dayKey(now)
	s.coolUntil = time.Time{}
	s.rejects = 0
	p.persistLocked(s)
	return nil
}

// Status 返回槽状态快照（方案 §8.3 的 Devices.Status）。
func (p *Pool) Status() model.DevicesStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	today := dayKey(now)
	status := model.DevicesStatus{Slots: make([]model.SlotStatus, 0, len(p.slots))}
	for _, s := range p.slots {
		p.refreshLocked(s, now)
		switch s.state {
		case model.SlotHealthy:
			status.Healthy++
		case model.SlotCooling:
			status.Cooling++
		case model.SlotExhausted:
			status.Exhausted++
		case model.SlotRejected:
			status.Rejected++
		}
		if s.quotaDay == today {
			status.UsedToday += s.quotaUsed
		}
		status.Slots = append(status.Slots, model.SlotStatus{
			ID:        s.ID,
			State:     s.state,
			QuotaUsed: s.quotaUsed,
			QuotaDay:  s.quotaDay,
			CoolUntil: unixOrZero(s.coolUntil),
			LastUsed:  unixOrZero(s.lastUsed),
			Rejects:   s.rejects,
		})
	}
	status.NonPlaybackSwaps = p.nonPlaybackSwaps
	return status
}

// refreshLocked 在锁内做状态复原：
//   - cooling / rejected：冷却期满即可再用；
//   - exhausted：进入新额度窗口即可再用，并把 quota_used 归零；
//   - healthy：跨日后 quota_used 归零。
//
// rejected 槽在冷却期满后回到轮换，档案由「下一次被拒时立即换档」这条路径刷新：
// 播放路径与 Pool.Do 都会在判到 ErrDeviceRejected 后调 ResetDevice。换档本身失败时
// 只进冷却，下个窗口再试，不会无限重试同一次调用。
func (p *Pool) refreshLocked(s *Slot, now time.Time) {
	switch s.state {
	case model.SlotCooling, model.SlotRejected:
		if !s.coolUntil.IsZero() && !now.Before(s.coolUntil) {
			s.state = model.SlotHealthy
			s.coolUntil = time.Time{}
		}
	case model.SlotExhausted:
		// lastUsed 为空说明该槽还没真实消耗过额度（构造初值/记账异常），
		// 不按「新窗口」复原，避免刚标记的 exhausted 被立刻洗回 healthy。
		if !s.lastUsed.IsZero() && p.newQuotaWindow(s, now) {
			s.state = model.SlotHealthy
			s.quotaUsed = 0
			s.quotaDay = dayKey(now)
		}
	case model.SlotHealthy:
		if s.quotaDay != 0 && s.quotaDay != dayKey(now) {
			s.quotaUsed = 0
			s.quotaDay = dayKey(now)
		}
	}
}

// newQuotaWindow 判断该槽是否已进入新的额度窗口。
//
// 判据：跨本地日，或距上次使用满 QuotaWindow（默认 24h）。服务端真实重置时点
// 未实测（方案附录 A1），这是保守复原；实测后只改这一个函数。
func (p *Pool) newQuotaWindow(s *Slot, now time.Time) bool {
	if s.lastUsed.IsZero() {
		return true
	}
	if s.quotaDay != 0 && s.quotaDay != dayKey(now) {
		return true
	}
	return !now.Before(s.lastUsed.Add(p.quotaWindow))
}

// persistLocked 把槽记账写进 device_slot 表。失败只记日志，不影响主流程。
func (p *Pool) persistLocked(s *Slot) {
	if p.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec := store.DeviceSlotRecord{
		ID:          s.ID,
		ProfilePath: s.ProfilePath,
		State:       s.state,
		QuotaUsed:   s.quotaUsed,
		QuotaDay:    s.quotaDay,
		CoolUntil:   unixOrZero(s.coolUntil),
		LastUsed:    unixOrZero(s.lastUsed),
		RejectCount: s.rejects,
	}
	if err := p.st.DeviceSlotPut(ctx, rec); err != nil {
		p.logf("devices: persist slot %s: %v", s.ID, err)
	}
}

func (p *Pool) broadcastLocked() {
	close(p.waitCh)
	p.waitCh = make(chan struct{})
}

func (p *Pool) logf(format string, args ...any) { p.logfFn(format, args...) }

// dayKey 返回本地日期 YYYYMMDD，写入 device_slot.quota_day。
func dayKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// unixOrZero 把零值时间写成 0，避免 1 年 1 月的怪时间戳入库。
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// unixTime 把 unix 秒还原成时间；0 表示未设置。
func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
