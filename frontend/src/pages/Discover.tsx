import { useCallback, useEffect, useRef, useState } from 'react';
import * as api from '../api';
import { errorMessage } from '../format';
import { navigate } from '../router';
import type { Series } from '../types';
import { SeriesCard } from '../components/SeriesCard';

/** 书城：Library.Discover 无限流卡片（方案 §8.1）。 */
export function DiscoverPage() {
  const [items, setItems] = useState<Series[]>([]);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [columns, setColumns] = useState(4);
  const gridRef = useRef<HTMLDivElement | null>(null);

  const cursorRef = useRef('');
  const hasMoreRef = useRef(true);
  const loadingRef = useRef(false);
  const seenRef = useRef<Set<string>>(new Set());
  // 上游推荐流耗尽后的回卷计数：连续两页拉不到新内容才真正停（防对静态流空转刷请求）。
  const emptyWrapsRef = useRef(0);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  const loadMore = useCallback(async () => {
    if (loadingRef.current || !hasMoreRef.current) return;
    loadingRef.current = true;
    setLoading(true);
    try {
      const page = await api.discover('', cursorRef.current);
      const fresh = (page.items ?? []).filter((series) => !seenRef.current.has(series.id));
      for (const series of fresh) seenRef.current.add(series.id);
      setItems((prev) => [...prev, ...fresh]);
      if (page.hasMore) {
        cursorRef.current = page.cursor ?? '';
        hasMoreRef.current = true;
        emptyWrapsRef.current = 0;
      } else {
        // 上游游标耗尽：回到第一页续拉（推荐流轮换会出现新内容），瀑布流无限续。
        cursorRef.current = '';
        emptyWrapsRef.current = fresh.length > 0 ? 0 : emptyWrapsRef.current + 1;
        hasMoreRef.current = emptyWrapsRef.current < 2;
      }
      setError(null);
    } catch (err) {
      hasMoreRef.current = false;
      setError(errorMessage(err));
    } finally {
      loadingRef.current = false;
      setLoading(false);
      setHasMore(hasMoreRef.current);
    }
  }, []);

  useEffect(() => {
    void loadMore();
  }, [loadMore]);

  // 瀑布流列数随容器宽度自适应（220px 一列，2-8 列）。
  useEffect(() => {
    const el = gridRef.current;
    if (!el) return undefined;
    const update = () => {
      const k = Math.max(2, Math.min(8, Math.floor(el.clientWidth / 220) || 2));
      setColumns((prev) => (prev === k ? prev : k));
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const element = sentinelRef.current;
    if (!element) return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) void loadMore();
      },
      { rootMargin: '320px' },
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, [loadMore]);

  // 空列补拉：最短列分发下若视觉失衡（最高列与最矮列的高度差超过一张
  // 卡，页面下缘即出现成片空白）就自动补一页。判据用高度差而非张数差：
  // 张数相同的列因简介有无，视觉高度仍可能差出一张卡（v0.1.8 实测空格）。
  // items 少于「每列至少一张」也补拉——页矮无滚动条时触底 observer 永远
  // 不触发，没有这个门小批量场景会死锁。触底（hasMore=false）时也允许：
  // 重置回卷计数再拉一次——推荐流轮换会给出新内容；仍拉不到新卡则下次
  // 不再触发（items 不变 → effect 不重跑），不会死循环。
  useEffect(() => {
    if (loading || items.length === 0) return;
    // 与渲染层同一套高度估算，保证判据与视觉一致。
    const heights = new Array(columns).fill(0);
    for (const series of items) {
      let target = 0;
      for (let c = 1; c < columns; c += 1) {
        if (heights[c] < heights[target]) target = c;
      }
      heights[target] += series.description ? 110 : 92;
    }
    const min = Math.min(...heights);
    const max = Math.max(...heights);
    if (max - min < 110) return;
    if (!hasMore) {
      if (emptyWrapsRef.current >= 2) return;
      emptyWrapsRef.current += 1;
      hasMoreRef.current = true;
      setHasMore(true);
    }
    void loadMore();
  }, [items, columns, loading, hasMore, loadMore]);

  const retry = () => {
    hasMoreRef.current = true;
    setHasMore(true);
    void loadMore();
  };

  // 刷新推荐：清空去重表与游标，从第一页重新拉。上游按请求返回推荐流，
  // 重拉即得到新的一批推进剧集。
  const refresh = useCallback(async () => {
    if (loadingRef.current) return;
    seenRef.current = new Set();
    cursorRef.current = '';
    emptyWrapsRef.current = 0;
    hasMoreRef.current = true;
    setItems([]);
    setHasMore(true);
    setError(null);
    await loadMore();
  }, [loadMore]);

  return (
    <section className="page">
      <header className="page__header page__header--row">
        <div>
          <h1>书城</h1>
          <p className="page__hint">推荐内容来自上游接口，点击卡片可以查看剧集。</p>
        </div>
        <button className="button" type="button" onClick={() => void refresh()} disabled={loading}>
          刷新推荐
        </button>
      </header>

      {error ? (
        <div className="notice notice--error">
          <span>加载失败：{error}</span>
          <button type="button" onClick={retry}>
            重试
          </button>
        </div>
      ) : null}

      <div className="card-grid" ref={gridRef}>
        {/* 最短列优先分发：比取模均分更平衡，条目数不是列数整数倍时
            多出的卡片平摊到各列，不会出现右侧整列空白。 */}
        {(() => {
          const heights = new Array(columns).fill(0);
          const cols: Series[][] = Array.from({ length: columns }, () => []);
          for (const series of items) {
            let target = 0;
            for (let c = 1; c < columns; c += 1) {
              if (heights[c] < heights[target]) target = c;
            }
            cols[target].push(series);
            // 高度近似：封面按列宽 4/3 比例 + 文本三行，卡片高度差异主要
            // 来自简介有无，用「有无简介」分档即可保证分发表平衡。
            heights[target] += series.description ? 110 : 92;
          }
          return cols.map((col, ci) => (
            <div className="card-col" key={ci}>
              {col.map((series) => (
                <SeriesCard
                  key={series.id}
                  series={series}
                  onClick={(picked) => navigate(`/series/${picked.id}`)}
                />
              ))}
            </div>
          ));
        })()}
      </div>

      {items.length === 0 && !loading && !error ? (
        <p className="empty">没有拿到推荐内容。上游接口可能临时不可用。</p>
      ) : null}

      <div ref={sentinelRef} className="sentinel">
        {loading ? '加载中…' : hasMore ? '' : items.length > 0 ? '没有更多了' : ''}
      </div>
    </section>
  );
}
