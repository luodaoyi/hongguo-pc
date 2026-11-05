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

  // 空态会提前 return 导致 scroller 卸载/重建：observer 依赖 sorted 存在性
  // 重挂，否则新 scroller 永远测不到尺寸，viewport 停在初始小值、网格退化单列。
  useEffect(() => {
    const element = scrollerRef.current;
    if (!element) return undefined;
    const measure = () => {
      setViewport({ width: element.clientWidth, height: element.clientHeight });
    };
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      setViewport({ width: rect.width, height: rect.height });
    });
    observer.observe(element);
    measure();
    return () => observer.disconnect();
  }, [sorted.length > 0]);

  // 可用宽度要给纵向滚动条留位：clientWidth 不含滚动条，但部分平台
  // overlay 滚动条会盖住内容右缘，统一预留，避免最后一列贴边截半。
  const usableWidth = Math.max(0, viewport.width - 14);
  // 只在总宽放不下两列（<2×minColumnWidth）时才退化单列紧凑条目——
  // 多列时列数按 minColumnWidth 均分，列窄靠 ellipsis 截断是正常形态；
  // v0.1.2 的回归就是拿「单列列宽 <150」误判成紧凑模式导致横向溢出。
  const columns = Math.max(1, Math.floor(usableWidth / minColumnWidth) || 1);
  const compact = usableWidth > 0 && usableWidth < minColumnWidth * 2;
  const columnWidth = usableWidth > 0 ? Math.floor(usableWidth / columns) : minColumnWidth;
  const columnWidthAdj = columnWidth;
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
                // 单元格自身只占列宽的一部分，右缘固定留 8px 呼吸空隙；
                // 配合整数化列宽，最后一列永远不贴容器边。
                width: Math.max(0, columnWidthAdj - 10),
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
                <span className="episode-cell__number">第{episode.number}集</span>
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
