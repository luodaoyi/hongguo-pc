import { useCallback, useEffect, useState } from 'react';
import * as api from '../api';
import { errorMessage, payTypeText, statusText } from '../format';
import { navigate } from '../router';
import type { Episode, Series } from '../types';
import { EpisodeGrid } from '../components/EpisodeGrid';

/** 详情页：GetSeries + ListEpisodes + 「刷新剧集」（方案 §8.1/§7.3）。 */
export function SeriesDetailPage({ seriesId }: { seriesId: string }) {
  const [series, setSeries] = useState<Series | null>(null);
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshResult, setRefreshResult] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [detail, list] = await Promise.all([
        api.getSeries(seriesId),
        api.listEpisodes(seriesId),
      ]);
      setSeries(detail);
      setEpisodes(list ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }, [seriesId]);

  useEffect(() => {
    void load();
  }, [load]);

  const refresh = async () => {
    setRefreshing(true);
    setRefreshResult(null);
    try {
      const diff = await api.refreshEpisodes(seriesId);
      const list = await api.listEpisodes(seriesId);
      setEpisodes(list ?? []);
      setRefreshResult(
        `刷新完成：新增 ${diff.added} 集、更新 ${diff.updated} 集、下架 ${diff.removed} 集`,
      );
    } catch (err) {
      setRefreshResult(`刷新失败：${errorMessage(err)}`);
    } finally {
      setRefreshing(false);
    }
  };

  const firstPlayable = episodes.find((episode) => episode.playable);
  const unsupported = series?.platform === 0;

  return (
    <section className="page">
      <button className="button button--ghost" type="button" onClick={() => navigate('/')}>
        ← 返回书城
      </button>

      {error ? (
        <div className="notice notice--error">
          <span>加载失败：{error}</span>
          <button type="button" onClick={() => void load()}>
            重试
          </button>
        </div>
      ) : null}

      {loading && !series ? <p className="empty">加载中…</p> : null}

      {series ? (
        <>
          <div className="detail-head">
            <div className="detail-head__cover">
              {series.coverUrl ? (
                <img
                  src={series.coverUrl}
                  alt={series.title}
                  onError={(event) => {
                    event.currentTarget.style.display = 'none';
                  }}
                />
              ) : null}
            </div>
            <div className="detail-head__info">
              <h1>{series.title}</h1>
              <div className="tag-row">
                <span className="tag">{statusText(series.status)}</span>
                <span className="tag">{payTypeText(series.payType)}</span>
                <span className="tag">共 {series.episodeCount} 集</span>
                {series.rightText ? <span className="tag">{series.rightText}</span> : null}
              </div>
              {unsupported ? (
                <p className="notice notice--warn">
                  该内容是非视频类型（书籍 / 有声书），播放器不支持。
                </p>
              ) : null}
              <div className="detail-head__actions">
                <button
                  className="button button--primary"
                  type="button"
                  disabled={!firstPlayable || unsupported}
                  onClick={() => {
                    if (firstPlayable) navigate(`/play/${series.id}/${firstPlayable.id}`);
                  }}
                >
                  播放第一集
                </button>
                <button
                  className="button"
                  type="button"
                  onClick={() => void refresh()}
                  disabled={refreshing}
                >
                  {refreshing ? '刷新中…' : '刷新剧集'}
                </button>
              </div>
              {refreshResult ? <p className="page__hint">{refreshResult}</p> : null}
            </div>
          </div>

          <h2 className="section-title">
            选集 <span className="section-title__count">{episodes.length} 集</span>
          </h2>
          <div className="episode-grid--detail">
            <EpisodeGrid
              episodes={episodes}
              onPick={(episode) => navigate(`/play/${series.id}/${episode.id}`)}
              emptyText="本地还没有这部剧的剧集数据，点「刷新剧集」拉一次。"
            />
          </div>
        </>
      ) : null}
    </section>
  );
}
