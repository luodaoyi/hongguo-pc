package bindings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 设置项的键（方案 §8.3 Settings.Get/Set）。值是字符串，布尔按 "true"/"false" 存。
const (
	KeyQuality           = "quality"           // 默认清晰度（空串=最高）
	KeyAutoNext          = "autoNextEpisode"   // 自动下一集（默认 true）
	KeyFFmpegPath        = "ffmpegPath"        // ffmpeg 路径覆盖（空=自动探测）
	KeyAlwaysOnTop       = "alwaysOnTop"       // 置顶（默认 false）
	KeyContentProtection = "contentProtection" // 防截屏（默认 false，方案 §8.5）
	KeyFirstRunDone      = "firstRunDone"      // 首启引导是否走完（方案 §13.3）
)

// SettingsStore 是本地设置存储：一个 JSON 文件，键值都是字符串。
//
// 为什么不放 SQLite：设置项少、改动频繁、丢失成本低；坏了直接删文件重置。
// 文件路径由 cmd/app 给到 data 目录下。
type SettingsStore struct {
	path   string
	mu     sync.Mutex
	values map[string]string
}

// NewSettingsStore 读入设置文件；文件不存在视为空设置。
func NewSettingsStore(path string) (*SettingsStore, error) {
	if path == "" {
		return nil, fmt.Errorf("bindings: settings path is required")
	}
	s := &SettingsStore{path: path, values: make(map[string]string, 8)}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("bindings: create settings directory: %w", err)
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &s.values); err != nil {
				return nil, fmt.Errorf("bindings: parse settings %s: %w", path, err)
			}
		}
	case os.IsNotExist(err):
		// 首次运行：空设置。
	default:
		return nil, fmt.Errorf("bindings: read settings %s: %w", path, err)
	}
	if s.values == nil {
		s.values = make(map[string]string, 8)
	}
	return s, nil
}

// Get 读一个设置项；不存在返回空串。
func (s *SettingsStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

// Set 写一个设置项并落盘。
func (s *SettingsStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return s.saveLocked()
}

// All 返回全部设置项的副本。
func (s *SettingsStore) All() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

// Bool 读一个布尔设置项，缺失或无法解析时取 def。
func (s *SettingsStore) Bool(key string, def bool) bool {
	v := s.Get(key)
	switch v {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return def
	}
}

// saveLocked 把设置写回文件（调用方持锁）。
func (s *SettingsStore) saveLocked() error {
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return fmt.Errorf("bindings: encode settings: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("bindings: write settings %s: %w", s.path, err)
	}
	return nil
}

// Settings 是设置服务（方案 §8.3 的 Settings.Get/Set）。
type Settings struct {
	Core *Core
}

// Get 读一个设置项。
func (s *Settings) Get(ctx context.Context, key string) (string, error) {
	return s.Core.Settings.Get(key), nil
}

// Set 写一个设置项。ffmpegPath 立即生效：Media.DetectFFmpeg 每次都读最新值。
func (s *Settings) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("bindings: settings key is required")
	}
	if err := s.Core.Settings.Set(key, value); err != nil {
		s.Core.record(err)
		return err
	}
	return nil
}

// All 返回全部设置项，供设置页一次性渲染。
func (s *Settings) All(ctx context.Context) (map[string]string, error) {
	return s.Core.Settings.All(), nil
}
