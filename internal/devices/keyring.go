package devices

import "sync"

// Keyring 是本会话的内容密钥表（方案 §2.2 I2）。
//
// 只存进程内存：key = episodeID，value = 32 位小写 hex 内容密钥。进程退出即失，
// 不落库、不打日志、不进事件载荷、不进 DTO。方案 §5.3 的 playback_cache.has_key
// 列只是这张表在某一时刻「有没有值」的布尔快照。
//
// 之所以不能省掉这份内存表：SDK 的 Playback.KeyMaterial 带 json:"-"，从 SQLite
// 读回来的模型不含密钥材料，无法再推导密钥。所以进程重启后，加密集即使命中缓存
// 也必须重解析（1 台设备，方案 §4.5）。
type Keyring struct {
	mu   sync.RWMutex
	keys map[string]string
}

// NewKeyring 建一个空的密钥表。
func NewKeyring() *Keyring {
	return &Keyring{keys: make(map[string]string, 8)}
}

// Put 记下一集的内容密钥。
func (k *Keyring) Put(episodeID, hex string) {
	if k == nil || episodeID == "" || hex == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[episodeID] = hex
}

// Get 取一集的内容密钥。返回值只允许交给媒体层（internal/media 的密钥串参数）。
func (k *Keyring) Get(episodeID string) (string, bool) {
	if k == nil {
		return "", false
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	hex, ok := k.keys[episodeID]
	return hex, ok
}

// Has 报告本会话是否持有该集的密钥。
func (k *Keyring) Has(episodeID string) bool {
	_, ok := k.Get(episodeID)
	return ok
}

// Drop 丢掉一集的密钥（切集或密钥失效时主动清内存）。
func (k *Keyring) Drop(episodeID string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, episodeID)
}

// Len 返回当前持有的密钥数量（诊断用，不暴露内容）。
func (k *Keyring) Len() int {
	if k == nil {
		return 0
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.keys)
}
