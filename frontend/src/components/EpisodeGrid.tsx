import { useEffect, useMemo, useRef, useState } from 'react';
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
 * 只挂载滚动视口附近的若干行，DOM 数量与总集数无关；宽度不足时退化单列。
 * 高度与宽度都靠 ResizeObserver 实测，父容器决定可视区大小（详情页固定高度、
 * 播放页抽屉撑满右侧）。列表展示前按 number 升序稳定排序（后端已排，这里兜底
 * 防御缓存/事件迟到等乱序来源）。
 */
export function EpisodeGrid({ episodes, currentEpisodeId, onPick, emptyText }: Props) {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [scrollTop, setScrollTop] = useState(0);

  const rowHeight = 64;
  const minColumnWidth = 108;
  const overscanRows = 2;

  const sorted = useMemo(
    () =>
      [...episodes].sort((a, b) =>
        a.number !== b.number ? a.number - b.number : a.id < b.id ? -1 : a.id > b.id ? 1 : 0,
      ),
    [episodes],
  );

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

  const columns = Math.max(1, Math.floor(viewport.width / minColumnWidth) || 1);
  const columnWidth = viewport.width > 0 ? viewport.width / columns : minColumnWidth;
  // 窄容器（播放页抽屉）：列宽不足 150px 时切换为单列紧凑条目，保证
  // 「第 N 集」+ 时长完整显示，不被缩略图挤压截断。
  const compact = viewport.width > 0 && columnWidth < 150;
  const columnWidthAdj = compact ? viewport.width : columnWidth;
  const totalRows = Math.ceil(sorted.length / columns);
  const firstRow = Math.max(0, Math.floor(scrollTop / rowHeight) - overscanRows);
  const lastRow = Math.min(
    totalRows,
    Math.ceil((scrollTop + (viewport.height || rowHeight * 6)) / rowHeight) + overscanRows,
  );

  const visible: Array<{ episode: Episode; index: number }> = [];
  for (let row = firstRow; row < lastRow; row += 1) {
    for (let col = 0; col < columns; col += 1) {
      const index = row * columns + col;
      const episode = sorted[index];
      if (!episode) break;
      visible.push({ episode, index });
    }
  }

  if (sorted.length === 0) {
    return <div className="episode-grid__empty">{emptyText ?? '暂无剧集'}</div>;
  }

  return (
    <div
      className={`episode-grid${compact ? ' episode-grid--compact' : ''}`}
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
                left: col * columnWidthAdj,
                top: row * rowHeight,
                width: Math.max(0, columnWidthAdj - 8),
                height: rowHeight - 8,
              }}
              disabled={!episode.playable}
              title={episode.playable ? episode.title ?? `第 ${episode.number} 集` : '该集不可播放'}
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
