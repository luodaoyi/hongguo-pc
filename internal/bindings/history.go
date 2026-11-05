package bindings

import (
	"context"

	"github.com/luodaoyi/hongguo-pc/internal/model"
)

// History 是断点服务（方案 §8.3 的 History.List/Get/Clear）。
//
// 断点由 player 层按 series_id 写入（每 5 秒节流 + 暂停/切集/退出强制落盘），
// 本服务只读与清空。
type History struct {
	Core *Core
}

// List 返回最近的断点（按 updated_at 倒序）。limit<=0 时取 50。
func (h *History) List(ctx context.Context, limit int) ([]model.HistoryDTO, error) {
	items, err := h.Core.Store.HistoryList(ctx, limit)
	if err != nil {
		h.Core.record(err)
		return nil, err
	}
	return items, nil
}

// Get 返回一部剧的断点；没有则返回 nil。
func (h *History) Get(ctx context.Context, seriesID string) (*model.HistoryDTO, error) {
	item, err := h.Core.Store.HistoryGet(ctx, seriesID)
	if err != nil {
		h.Core.record(err)
		return nil, err
	}
	return item, nil
}

// Clear 清空全部断点。
func (h *History) Clear(ctx context.Context) error {
	if err := h.Core.Store.HistoryClear(ctx); err != nil {
		h.Core.record(err)
		return err
	}
	return nil
}
