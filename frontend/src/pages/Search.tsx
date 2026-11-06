import { useEffect, useRef, useState } from 'react';
import * as api from '../api';
import { errorMessage } from '../format';
import { navigate } from '../router';
import type { Series } from '../types';
import { SeriesCard } from '../components/SeriesCard';

/** 搜索：Library.Search(q, cursor)（方案 §8.1）。 */
export function SearchPage() {
  const [query, setQuery] = useState('');
  const [submitted, setSubmitted] = useState('');
  const [items, setItems] = useState<Series[]>([]);
  const [cursor, setCursor] = useState('');
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 瀑布流列数随容器宽度自适应（与书城同一套：220px 一列，2-8 列）。
  const gridRef = useRef<HTMLDivElement | null>(null);
  const [columns, setColumns] = useState(4);

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

  const run = async (text: string, nextCursor: string, append: boolean) => {
    setLoading(true);
    setError(null);
    try {
      const page = await api.search(text, nextCursor);
      const fresh = page.items ?? [];
      setItems((prev) => (append ? [...prev, ...fresh] : fresh));
      setCursor(page.cursor ?? '');
      setHasMore(Boolean(page.hasMore));
      setSubmitted(text);
    } catch (err) {
      setError(errorMessage(err));
      if (!append) {
        setItems([]);
        setHasMore(false);
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <section className="page">
      <header className="page__header">
        <h1>搜索</h1>
        <form
          className="search-form"
          onSubmit={(event) => {
            event.preventDefault();
            const text = query.trim();
            if (text !== '') void run(text, '', false);
          }}
        >
          <input
            className="search-form__input"
            type="search"
            placeholder="输入剧名关键词"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            autoFocus
          />
          <button className="button button--primary" type="submit" disabled={loading}>
            {loading ? '搜索中…' : '搜索'}
          </button>
        </form>
      </header>

      {error ? <div className="notice notice--error">搜索失败：{error}</div> : null}

      <div className="card-grid" ref={gridRef}>
        {/* 与书城同款最短列优先分发：直接平铺进 flex 容器会没有宽度约束，
            卡片被压成窄条（v0.1.9 实测搜索页塌陷）。高度估算与书城一致。 */}
        {(() => {
          const heights = new Array(columns).fill(0);
          const cols: Series[][] = Array.from({ length: columns }, () => []);
          for (const series of items) {
            let target = 0;
            for (let c = 1; c < columns; c += 1) {
              if (heights[c] < heights[target]) target = c;
            }
            cols[target].push(series);
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

      {submitted !== '' && items.length === 0 && !loading && !error ? (
        <p className="empty">没有找到「{submitted}」相关的剧集。</p>
      ) : null}

      {hasMore ? (
        <div className="load-more">
          <button
            className="button"
            type="button"
            disabled={loading}
            onClick={() => void run(submitted, cursor, true)}
          >
            加载更多
          </button>
        </div>
      ) : null}
    </section>
  );
}
