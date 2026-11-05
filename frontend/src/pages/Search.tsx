import { useState } from 'react';
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

      <div className="card-grid">
        {items.map((series) => (
          <SeriesCard
            key={series.id}
            series={series}
            onClick={(picked) => navigate(`/series/${picked.id}`)}
          />
        ))}
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
