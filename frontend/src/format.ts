// 展示用格式化函数：中文文案集中一处，页面不各写一套。

export function statusText(status: string): string {
  switch (status) {
    case 'updating':
      return '更新中';
    case 'finished':
      return '已完结';
    case 'updated_today':
      return '今日更新';
    case 'stopped':
      return '已停更';
    default:
      return '状态未知';
  }
}

export function payTypeText(payType: string): string {
  switch (payType) {
    case 'free':
      return '免费';
    case 'vip':
      return '会员';
    case 'single':
      return '单集付费';
    default:
      return '付费类型未知';
  }
}

export function formatDuration(totalSeconds?: number): string {
  if (!totalSeconds || totalSeconds <= 0) return '';
  const seconds = Math.floor(totalSeconds);
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

export function formatClock(unixSeconds?: number): string {
  if (!unixSeconds || unixSeconds <= 0) return '—';
  const d = new Date(unixSeconds * 1000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 「刚才 / 3 小时前 / 2026-11-01」——历史列表用。 */
export function formatRelative(unixSeconds?: number): string {
  if (!unixSeconds || unixSeconds <= 0) return '—';
  const diff = Math.floor(Date.now() / 1000) - unixSeconds;
  if (diff < 60) return '刚刚';
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  if (diff < 86400 * 7) return `${Math.floor(diff / 86400)} 天前`;
  return formatClock(unixSeconds);
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  try {
    return JSON.stringify(err);
  } catch {
    return String(err);
  }
}
