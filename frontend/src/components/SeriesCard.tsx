import type { Series } from '../types';
import { statusText } from '../format';

interface Props {
  series: Series;
  onClick: (series: Series) => void;
}

/** 书城/搜索的封面卡片。封面缺失或加载失败时退化为标题首字块，避免破图。 */
export function SeriesCard({ series, onClick }: Props) {
  const count = series.episodeCount ?? 0;
  return (
    <button className="series-card" type="button" onClick={() => onClick(series)}>
      <div className="series-card__cover">
        {series.coverUrl ? (
          <img
            src={series.coverUrl}
            alt={series.title}
            loading="lazy"
            onError={(event) => {
              event.currentTarget.style.display = 'none';
            }}
          />
        ) : null}
        <span className="series-card__fallback">{series.title.slice(0, 1) || '剧'}</span>
        {count > 0 ? <span className="series-card__badge">{count} 集</span> : null}
      </div>
      <div className="series-card__title" title={series.title}>
        {series.title}
      </div>
      <div className="series-card__meta">{statusText(series.status)}</div>
    </button>
  );
}
