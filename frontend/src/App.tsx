import { useMemo } from 'react';
import { matchPattern, navigate, splitLocation, useLocation } from './router';
import { DiscoverPage } from './pages/Discover';
import { SearchPage } from './pages/Search';
import { SeriesDetailPage } from './pages/SeriesDetail';
import { PlayerPage } from './pages/Player';
import { HistoryPage } from './pages/History';
import { SettingsPage } from './pages/Settings';

const NAV_ITEMS = [
  { path: '/', label: '书城' },
  { path: '/search', label: '搜索' },
  { path: '/history', label: '历史' },
  { path: '/settings', label: '设置' },
];

export function App() {
  const location = useLocation();
  const { path, query } = useMemo(() => splitLocation(location), [location]);

  const playMatch = matchPattern(path, '/play/:seriesId/:episodeId');
  if (playMatch) {
    const startAtRaw = Number(query.get('t') ?? '0');
    const startAt = Number.isFinite(startAtRaw) && startAtRaw > 0 ? startAtRaw : 0;
    return (
      <PlayerPage
        key={playMatch.seriesId}
        seriesId={playMatch.seriesId}
        episodeId={playMatch.episodeId}
        startAt={startAt}
      />
    );
  }

  const seriesMatch = matchPattern(path, '/series/:id');

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="sidebar__brand">
          <span className="sidebar__mark">红</span>
          红果播放器
        </div>
        <nav className="sidebar__nav">
          {NAV_ITEMS.map((item) => (
            <button
              key={item.path}
              type="button"
              className={`sidebar__link${path === item.path ? ' sidebar__link--active' : ''}`}
              onClick={() => navigate(item.path)}
            >
              {item.label}
            </button>
          ))}
        </nav>
        <div className="sidebar__foot">
          本地播放
          <br />
          简体中文 · 浅色主题
        </div>
      </aside>
      <main className="shell__main">
        {seriesMatch ? (
          <SeriesDetailPage seriesId={seriesMatch.id} />
        ) : path === '/search' ? (
          <SearchPage />
        ) : path === '/history' ? (
          <HistoryPage />
        ) : path === '/settings' ? (
          <SettingsPage />
        ) : path === '/' ? (
          <DiscoverPage />
        ) : (
          <section className="page">
            <h1>页面不存在</h1>
            <p className="empty">路由 #{path} 没有对应页面。</p>
            <button className="button" type="button" onClick={() => navigate('/')}>
              回书城
            </button>
          </section>
        )}
      </main>
    </div>
  );
}
