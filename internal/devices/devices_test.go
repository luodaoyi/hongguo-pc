package devices

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

func TestUnlimitedGrowSlot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := NewPool(Config{
		Dir: dir, Store: st, Slots: 1, Unlimited: true,
		NewClient: func(profilePath string, onRejected func(error)) (*sdkx.Client, error) {
			return &sdkx.Client{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 把唯一槽标成 exhausted，模拟额度用尽。
	p.mu.Lock()
	p.slots[0].state = "exhausted"
	p.mu.Unlock()
	s, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("期望自动扩槽，实际 err=%v", err)
	}
	if s.ID != "d002" {
		t.Fatalf("期望新槽 d002，实际 %s", s.ID)
	}
	p.Release(s)
	if len(p.Status().Slots) != 2 {
		t.Fatal("期望池大小 2")
	}
}

func TestCappedPoolNoGrow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := NewPool(Config{
		Dir: dir, Store: st, Slots: 1, Unlimited: false,
		NewClient: func(profilePath string, onRejected func(error)) (*sdkx.Client, error) {
			return &sdkx.Client{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.slots[0].state = "exhausted"
	p.mu.Unlock()
	if _, err := p.Acquire(context.Background()); err != ErrQuotaExhausted {
		t.Fatalf("未开启 Unlimited 时应报额度耗尽，实际 %v", err)
	}
}
