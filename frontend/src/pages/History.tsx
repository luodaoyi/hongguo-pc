import { useCallback, useEffect, useState } from 'react';
import * as api from '../api';
import { errorMessage, formatDuration, formatRelative } from '../format';
import { navigate } from '../router';
import type { HistoryEntry, Series } from '../types';

/** 历史页：History.List，点击续播（方案 §8.1 + §6.5 续播）。 */
export function HistoryPage() {
  const [entries, setEntries] = useState<HistoryEntry[]>([]);
  const [seriesMap, setSeriesMap] = useState<Map<string, Series>>(new Map());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const list = await api.historyList();
      const safeList = (list ?? []).filter((entry) => entry.episodeId);
      setEntries(safeList);

      // HistoryDTO 没有剧名，补一次 GetSeries（0 额度），失败就退回显示 ID。
      const ids = [...new Set(safeList.map((entry) => entry.seriesId))].slice(0, 30);
      const details = await Promise.all(ids.map((id) => api.getSeries(id).catch(() => null)));
      const map = new Map<string, Series>();
      for (const detail of details) if (detail?.id) map.set(detail.id, detail);
      setSeriesMap(map);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const clear = async () => {
    setConfirming(false);
    try {
      await api.historyClear();
      setEntries([]);
      setSeriesMap(new Map());
    } catch (err) {
      setError(`清空失败：${errorMessage(err)}`);
    }
  };

  return (
    <section className="page">
      <header className="page__header page__header--row">
        <h1>继续观看</h1>
        <div className="page__actions">
          <button className="button" type="button" onClick={() => void load()} disabled={loading}>
            刷新
          </button>
          {confirming ? (
            <>
              <button className="button button--danger" type="button" onClick={() => void clear()}>
                确认清空
              </button>
              <button className="button button--ghost" type="button" onClick={() => setConfirming(false)}>
                取消
              </button>
            </>
          ) : (
            <button
              className="button"
              type="button"
              disabled={entries.length === 0}
              onClick={() => setConfirming(true)}
            >
              清空历史
            </button>
          )}
        </div>
      </header>

      {error ? <div className="notice notice--error">{error}</div> : null}
      {loading ? <p className="empty">加载中…</p> : null}
      {!loading && entries.length === 0 && !error ? (
        <p className="empty">还没有观看记录。去看点什么吧。</p>
      ) : null}

      <ul className="history-list">
        {entries.map((entry) => {
          const series = seriesMap.get(entry.seriesId);
          const percent =
            entry.durationSec && entry.durationSec > 0
              ? Math.min(100, Math.round((entry.positionSec / entry.durationSec) * 100))
              : null;
          return (
            <li key={entry.seriesId} className="history-item">
              <div className="history-item__cover">
                {series?.coverUrl ? <img src={series.coverUrl} alt="" /> : <span>剧</span>}
              </div>
              <div className="history-item__body">
                <div className="history-item__title">{series?.title ?? entry.seriesId}</div>
                <div className="history-item__meta">
                  <span>第 {entry.episodeNumber} 集</span>
                  <span>
                    {formatDuration(Math.floor(entry.positionSec))}
                    {entry.durationSec ? ` / ${formatDuration(entry.durationSec)}` : ''}
                  </span>
                  <span>{formatRelative(entry.updatedAt)}</span>
                  {entry.finished ? <span className="tag">已播完</span> : null}
                </div>
                {percent !== null ? (
                  <div className="progress">
                    <div className="progress__bar" style={{ width: `${percent}%` }} />
                  </div>
                ) : null}
              </div>
              <button
                className="button button--primary"
                type="button"
                onClick={() =>
                  navigate(
                    `/play/${entry.seriesId}/${entry.episodeId}?t=${Math.floor(entry.positionSec)}`,
                  )
                }
              >
                {entry.finished ? '重看' : '继续观看'}
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
