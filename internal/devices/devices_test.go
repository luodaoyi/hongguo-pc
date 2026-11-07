package devices

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

func TestUnlimitedGrowWhenAllExhausted(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const n = 16
	p, err := NewPool(Config{
		Dir: dir, Store: st, Slots: n, Unlimited: true,
		NewClient: func(profilePath string, onRejected func(error)) (*sdkx.Client, error) {
			return &sdkx.Client{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 全部槽置为 exhausted 且带真实 lastUsed（复现「看剧一天后全烧完」的状态）。
	// quotaDay 必须取当天：写死日期后隔天跑，dayKey 已翻页、额度自动重置，
	// growSlot 不会再扩槽（2026-11-07 实测踩坑）。
	p.mu.Lock()
	now := p.now()
	today := now.Year()*10000 + int(now.Month())*100 + now.Day()
	for _, s := range p.slots {
		s.state = "exhausted"
		s.lastUsed = now.Add(-time.Hour)
		s.quotaUsed = 1
		s.quotaDay = today
	}
	p.mu.Unlock()
	s, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("全耗尽 + Unlimited 应自动扩槽，实际 err=%v", err)
	}
	if s.ID != "d017" {
		t.Fatalf("期望新槽 d017，实际 %s", s.ID)
	}
	p.Release(s)
	if got := len(p.Status().Slots); got != n+1 {
		t.Fatalf("期望池大小 %d，实际 %d", n+1, got)
	}
}
