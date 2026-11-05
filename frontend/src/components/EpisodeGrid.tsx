import { useEffect, useRef, useState } from 'react';
import type { Episode } from '../types';
import { formatDuration } from '../format';

interface Props {
  episodes: Episode[];
  currentEpisodeId?: string;
  onPick: (episode: Episode) => void;
  /** 测试与调试用的稳定钩子，便于在无键盘环境定位组件 */
  emptyText?: string;
}

/**
 * 剧集网格（窗口化渲染，方案 §7.1 与任务书「>120 条窗口化」）。
 *
 * 只挂载滚动视口附近的若干行，DOM 数量与总集数无关；宽度不足一列时退化为两列。
 * 高度与宽度都靠 ResizeObserver 实测，父容器决定可视区大小（详情页固定高度、
 * 播放页抽屉撑满右侧）。
 */
export function EpisodeGrid({ episodes, currentEpisodeId, onPick, emptyText }: Props) {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [scrollTop, setScrollTop] = useState(0);

  const rowHeight = 64;
  const minColumnWidth = 108;
  const overscanRows = 2;

  useEffect(() => {
    const element = scrollerRef.current;
    if (!element) return undefined;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      setViewport({ width: rect.width, height: rect.height });
    });
    observer.observe(element);
    setViewport({ width: element.clientWidth, height: element.clientHeight });
    return () => observer.disconnect();
  }, []);

  const columns = Math.max(2, Math.floor(viewport.width / minColumnWidth) || 2);
  const columnWidth = viewport.width > 0 ? viewport.width / columns : minColumnWidth;
  const totalRows = Math.ceil(episodes.length / columns);
  const firstRow = Math.max(0, Math.floor(scrollTop / rowHeight) - overscanRows);
  const lastRow = Math.min(
    totalRows,
    Math.ceil((scrollTop + (viewport.height || rowHeight * 6)) / rowHeight) + overscanRows,
  );

  const visible: Array<{ episode: Episode; index: number }> = [];
  for (let row = firstRow; row < lastRow; row += 1) {
    for (let col = 0; col < columns; col += 1) {
      const index = row * columns + col;
      const episode = episodes[index];
      if (!episode) break;
      visible.push({ episode, index });
    }
  }

  if (episodes.length === 0) {
    return <div className="episode-grid__empty">{emptyText ?? '暂无剧集'}</div>;
  }

  return (
    <div
      className="episode-grid"
      ref={scrollerRef}
      onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
    >
      <div style={{ height: totalRows * rowHeight, position: 'relative' }}>
        {visible.map(({ episode, index }) => {
          const row = Math.floor(index / columns);
          const col = index % columns;
          const isCurrent = episode.id === currentEpisodeId;
          return (
            <button
              key={episode.id}
              type="button"
              className={`episode-cell${isCurrent ? ' episode-cell--current' : ''}${
                episode.playable ? '' : ' episode-cell--disabled'
              }`}
              style={{
                position: 'absolute',
                left: col * columnWidth,
                top: row * rowHeight,
                width: Math.max(0, columnWidth - 8),
                height: rowHeight - 8,
              }}
              disabled={!episode.playable}
              title={episode.playable ? episode.title ?? '' : '该集不可播放'}
              onClick={(event) => {
                event.currentTarget.blur();
                onPick(episode);
              }}
            >
              {episode.coverUrl ? (
                <img
                  className="episode-cell__thumb"
                  src={episode.coverUrl}
                  alt=""
                  loading="lazy"
                  onError={(event) => {
                    event.currentTarget.style.display = 'none';
                  }}
                />
              ) : null}
              <span className="episode-cell__text">
                <span className="episode-cell__number">第 {episode.number} 集</span>
                <span className="episode-cell__meta">
                  {episode.isNew ? <em className="episode-cell__new">新</em> : null}
                  {formatDuration(episode.durationSec)}
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}
