package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdkmodel "github.com/beilunyang/openhongguo/sdk/model"

	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

var (
	// ErrQuotaExhausted 表示池里没有 healthy 槽可用了（方案 §4.6 的第一条降级）。
	// 上层文案：「今日可播放集数已用尽（匿名额度限制），明天恢复」。
	ErrQuotaExhausted = errors.New("devices: no healthy device slot available")

	// ErrRateLimitedLong 表示服务端要求等待超过 10 秒（方案 F16 / §4.4）：
	// 不排队，直接把降级交给上层。
	ErrRateLimitedLong = errors.New("devices: rate limited with a long waiting window")
)

// maxInlineWait 是原地等待重试的上限。超过就放弃（方案 F16：单次等待 >10s 直接返回错）。
const maxInlineWait = 10 * time.Second

// ResolvePlayback 解析一集的播放模型，按方案 §4.4 的伪码逐条实现：
//
//  1. 缓存命中且未过期，且（本会话持内存密钥 或 该模型未加密）→ 0 额度返回；
//  2. 最多尝试 MaxPerResolution 台设备：
//     ErrAnonymousLimited → 标记该槽额度耗尽，换下一台；
//     ErrDeviceRejected   → ResetDevice 换档案后冷却该槽，换下一台；
//     ErrRateLimited      → 等待 RetryAfter（>10s 直接返回 ErrRateLimitedLong）；
//     err==nil 或 (ErrDRMProtected 且 pb!=nil) → 成功：记账（该槽随即转 exhausted，
//     因为「1 次成功解析 = 1 台设备的全部额度」，方案 §4.1）+ 写缓存；
//  3. 走完仍失败 → ErrQuotaExhausted。
//
// 加密集的密钥在成功路径上本地解包并写进 Keyring（I2），不返回、不落库。
// 若模型拿到了但密钥解不开，同样要按一台设备记账（这次解析已真实消耗额度），
// 然后返回 (pb, *sdkx.KeyUnavailableError)，上层按方案 §6.3 提示
// 「该集加密方式暂不支持」并跳过。
func (p *Pool) ResolvePlayback(ctx context.Context, episodeID string) (*sdkmodel.Playback, error) {
	if strings.TrimSpace(episodeID) == "" {
		return nil, fmt.Errorf("devices: episode id is required")
	}
	if pb, ok := p.cached(ctx, episodeID); ok {
		return pb, nil
	}

	keyRetries := 0 // 密钥材料解不开时的换设备重试计数（每集最多一次）
	for attempt := 1; ; attempt++ {
		// Unlimited 模式：尝试次数不设上限，烧一台造一台，直到成功或 ctx 取消；
		// 上限只约束单次解析内对存量槽的重试（防对冷却中的池空转）。
		if attempt > p.maxPerResolution && !p.unlimited {
			return nil, ErrQuotaExhausted
		}
		if attempt > 1 {
			p.logf("devices: playback attempt %d for %s", attempt, episodeID)
		}
		s, err := p.Acquire(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, ErrQuotaExhausted
		}
		pb, callErr := p.resolveOn(ctx, s, episodeID)
		switch {
		case errors.Is(callErr, sdkx.ErrAnonymousLimited):
			// 该设备额度用尽：记账后换下一台；Unlimited 模式下 Acquire 会
			// 自动注册新设备（方案 §4.1 的无限版）。
			p.MarkExhausted(s)
			continue

		case errors.Is(callErr, sdkx.ErrDeviceRejected):
			// 设备被拒：换档案 + 冷却，换下一台（方案 §4.4 / F17）。
			if resetErr := s.client.ResetDevice(ctx); resetErr != nil {
				p.MarkCooling(s, p.coolDown)
				if p.unlimited {
					continue // 换档失败也继续造新设备
				}
				return nil, fmt.Errorf("devices: reset device for slot %s: %w", s.ID, resetErr)
			}
			p.MarkCooling(s, p.coolDown)
			continue

		case errors.Is(callErr, sdkx.ErrRateLimited):
			wait := sdkx.RetryAfter(callErr, 5*time.Second)
			if wait > maxInlineWait {
				return nil, ErrRateLimitedLong
			}
			p.logf("devices: rate limited, waiting %s before retry %d", wait, attempt)
			if sleepErr := sleepContext(ctx, wait); sleepErr != nil {
				return nil, sleepErr
			}
			continue

		case isKeyUnavailable(callErr):
			// 模型拿到了、密钥解不开。这台设备的额度已经真实消耗掉（服务端只回一次
			// 模型，方案 §4.1），必须记账，否则 UsedToday 会少算一台（T13 审查 D2）。
			// 换一台设备重试一次：不同设备的签名/参数可能让服务端下发不同形态的
			// 密钥材料（v0.1.14 实测某集 spade 材料解析失败）；仍失败才交上层跳过。
			p.MarkExhausted(s)
			if keyRetries < 1 {
				keyRetries++
				p.logf("devices: key unavailable for %s on %s; retrying on a fresh device", episodeID, s.ID)
				continue
			}
			return pb, callErr

		case callErr == nil, errors.Is(callErr, sdkx.ErrDRMProtected) && pb != nil:
			// 加密集也是成功（F3：err != nil 不得丢弃模型）。
			p.Commit(s)
			p.cachePut(ctx, episodeID, pb)
			return pb, nil

		default:
			return pb, callErr
		}
	}
}

// Do 在池内取一个槽执行一次目录类调用（0 额度，方案 §4.5）。只用 healthy 槽。
func (p *Pool) Do(ctx context.Context, name string, fn func(ctx context.Context, c *sdkx.Client) error) error {
	return p.do(ctx, name, fn, p.Acquire)
}

// DoReadOnly 与 Do 相同，但允许使用非 healthy 的槽（优先级 healthy > cooling >
// exhausted > rejected）：0 额度调用不消耗播放额度，播放额度耗尽后浏览与选集
// 仍应可用——否则「额度耗尽只影响播放」的界面承诺就是空话。
func (p *Pool) DoReadOnly(ctx context.Context, name string, fn func(ctx context.Context, c *sdkx.Client) error) error {
	return p.do(ctx, name, fn, p.AcquireAny)
}

// do 是目录调用的公共实现：acquire 决定选槽策略；回调返回 ErrDeviceRejected 时
// 除 sdkx 钩子记下的拒绝次数外，这里再记一次「非播放换档」计数（方案 §4.4 的
// M1 补充：非播放入口同样会烧设备），并立刻对该槽执行 ResetDevice 换档案，
// 成功后恢复为 healthy（T13 审查 D3）。换档失败只让槽进冷却，本次调用仍返回
// 原来的 ErrDeviceRejected。
func (p *Pool) do(ctx context.Context, name string, fn func(ctx context.Context, c *sdkx.Client) error, acquire func(ctx context.Context) (*Slot, error)) error {
	if fn == nil {
		return fmt.Errorf("devices: nil catalog callback")
	}
	s, err := acquire(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrQuotaExhausted
	}
	defer p.Release(s)

	callErr := fn(ctx, s.client)
	if errors.Is(callErr, sdkx.ErrDeviceRejected) {
		p.mu.Lock()
		p.nonPlaybackSwaps++
		p.mu.Unlock()
		// 目录接口被拒时同样要换档案（T13 审查 D3）。只记次数、等冷却期满的话，
		// 这个槽会拿同一份被拒档案按冷却周期反复失败；四槽全中时用户只能进设置页
		// 手动 Reset。此刻槽仍由本次调用占用（busy），换档天然单飞；换档失败则进
		// 冷却，下个窗口再试，不会在本次调用里重试。
		if resetErr := s.client.ResetDevice(ctx); resetErr != nil {
			p.MarkCooling(s, p.coolDown)
			p.logf("devices: %s rejected on slot %s; re-register failed: %v", name, s.ID, resetErr)
		} else {
			p.mu.Lock()
			p.markRecoveredLocked(s, p.now())
			p.persistLocked(s)
			p.mu.Unlock()
			p.logf("devices: %s rejected on slot %s; device re-registered", name, s.ID)
		}
	}
	if callErr != nil && name != "" {
		code, _, _ := sdkx.Classify(callErr)
		p.logf("devices: %s failed on slot %s: %s", name, s.ID, code)
	}
	return callErr
}

// resolveOn 在指定槽上解析，负责归还槽，并在成功后把内容密钥写进 Keyring。
func (p *Pool) resolveOn(ctx context.Context, s *Slot, episodeID string) (*sdkmodel.Playback, error) {
	defer p.Release(s)

	pb, err := s.client.ResolvePlayback(ctx, episodeID)
	if pb == nil || !errors.Is(err, sdkx.ErrDRMProtected) {
		return pb, err
	}
	hex, keyErr := s.client.ContentKey(pb)
	if keyErr != nil {
		return pb, &sdkx.KeyUnavailableError{EpisodeID: episodeID, Err: keyErr}
	}
	p.keys.Put(episodeID, hex)
	return pb, err
}

// cached 读缓存并按方案 §4.4 判断能否 0 额度返回。
//
// 判据是「未过期」+「该模型未加密，或本会话确实持有它的内存密钥」。
// 进程重启后 Keyring 为空，加密集即使命中缓存也要重解析（方案 §4.5 / I2）。
func (p *Pool) cached(ctx context.Context, episodeID string) (*sdkmodel.Playback, bool) {
	if p.st == nil {
		return nil, false
	}
	entry, err := p.st.CacheGet(ctx, episodeID)
	if err != nil {
		p.logf("devices: read playback cache %s: %v", episodeID, err)
		return nil, false
	}
	if entry == nil {
		return nil, false
	}
	var pb sdkmodel.Playback
	if err := json.Unmarshal(entry.Payload, &pb); err != nil {
		p.logf("devices: decode playback cache %s: %v", episodeID, err)
		return nil, false
	}
	if expired(pb.ExpiresAt, p.now()) {
		// 地址过期即删，不留陈旧 URL（方案 §7.5）。
		if delErr := p.st.CacheDelete(ctx, episodeID); delErr != nil {
			p.logf("devices: drop expired playback cache %s: %v", episodeID, delErr)
		}
		return nil, false
	}
	hasKey := entry.HasKey && p.keys.Has(episodeID)
	if hasKey || !pb.Protected {
		return &pb, true
	}
	return nil, false
}

// cachePut 写缓存。payload 是 Playback 的 JSON，**不含密钥**（KeyMaterial 带 json:"-"）；
// 这里再验一次，防止上游哪天把密钥塞进可序列化字段（I2 的最后一道闸）。
func (p *Pool) cachePut(ctx context.Context, episodeID string, pb *sdkmodel.Playback) {
	if p.st == nil || pb == nil {
		return
	}
	payload, err := json.Marshal(pb)
	if err != nil {
		p.logf("devices: encode playback cache %s: %v", episodeID, err)
		return
	}
	if pb.KeyMaterial != "" && bytes.Contains(payload, []byte(pb.KeyMaterial)) {
		p.logf("devices: refusing to cache playback %s: key material would be persisted", episodeID)
		return
	}
	entry := store.CacheEntry{
		EpisodeID:  episodeID,
		SeriesID:   "", // 本层只知道分集；上层刷新目录时按需补 series_id。
		Payload:    payload,
		ResolvedAt: p.now().Unix(),
		ExpiresAt:  pb.ExpiresAt,
		HasKey:     p.keys.Has(episodeID),
	}
	if err := p.st.CachePut(ctx, entry); err != nil {
		p.logf("devices: write playback cache %s: %v", episodeID, err)
	}
}

// isKeyUnavailable 报告 err 是否是「模型有、密钥解不开」。
func isKeyUnavailable(err error) bool {
	var target *sdkx.KeyUnavailableError
	return errors.As(err, &target)
}

// expired 是方案 F15 的过期判据：ExpiresAt > 0 且距现在不足 1 分钟。
// ExpiresAt 为 0 表示服务端没给有效期，交由上层按「解析后 15 分钟」兜底（§7.5）。
func expired(expiresAt int64, now time.Time) bool {
	if expiresAt <= 0 {
		return false
	}
	return now.Unix() > expiresAt-60
}

// sleepContext 等待 d，或等到 ctx 结束。
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
