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

  const cursorRef = useRef('');
  const hasMoreRef = useRef(true);
  const loadingRef = useRef(false);
  const seenRef = useRef<Set<string>>(new Set());
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
      cursorRef.current = page.cursor ?? '';
      hasMoreRef.current = Boolean(page.hasMore) && fresh.length > 0;
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

  const retry = () => {
    hasMoreRef.current = true;
    setHasMore(true);
    void loadMore();
  };

  return (
    <section className="page">
      <header className="page__header">
        <h1>书城</h1>
        <p className="page__hint">推荐内容来自上游接口，点击卡片可以查看剧集。</p>
      </header>

      {error ? (
        <div className="notice notice--error">
          <span>加载失败：{error}</span>
          <button type="button" onClick={retry}>
            重试
          </button>
        </div>
      ) : null}

      <div className="card-grid">
        {items.map((series) => (
          <SeriesCard
            key={series.id}
            series={series}
            onClick={(picked) => navigate(`/series/${picked.id}`)}
          />
        ))}
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
