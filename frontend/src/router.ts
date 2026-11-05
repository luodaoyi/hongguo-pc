// 极简 hash 路由。
//
// 为什么不用 react-router：本期只有六个页面，且 Wails 的 Assets handler 是否对
// 深链接回退 index.html 由 T9 决定；hash 路由不依赖任何服务端回退，最省事。
// 路由形态：`#/series/abc`、`#/play/abc/e1`、`#/search`。

import { useEffect, useState } from 'react';

export function currentLocation(): string {
  const raw = window.location.hash.replace(/^#/, '');
  return raw === '' ? '/' : raw;
}

export function navigate(to: string): void {
  const next = to.startsWith('#') ? to : `#${to}`;
  if (`#${currentLocation()}` === next) return;
  window.location.hash = next;
}

export function replaceQuery(to: string): void {
  // 用于换剧集时保持页面不新增历史栈（播放页同一路由）
  window.location.replace(`#${to}`);
}

export function useLocation(): string {
  const [location, setLocation] = useState(currentLocation);
  useEffect(() => {
    const onChange = () => setLocation(currentLocation());
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);
  return location;
}

/** 拆成 path 与 query（hash 路由的 query 走 `?` 之后）。 */
export function splitLocation(location: string): { path: string; query: URLSearchParams } {
  const index = location.indexOf('?');
  if (index < 0) return { path: location, query: new URLSearchParams() };
  return {
    path: location.slice(0, index),
    query: new URLSearchParams(location.slice(index + 1)),
  };
}

/** 模式匹配：`/series/:id` 匹配 `/series/abc`，返回 {id: 'abc'}；不匹配返回 null。 */
export function matchPattern(
  path: string,
  pattern: string,
): Record<string, string> | null {
  const pathParts = path.split('/').filter(Boolean);
  const patternParts = pattern.split('/').filter(Boolean);
  if (pathParts.length !== patternParts.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < patternParts.length; i += 1) {
    const patternPart = patternParts[i];
    const pathPart = pathParts[i];
    if (patternPart.startsWith(':')) {
      params[patternPart.slice(1)] = decodeURIComponent(pathPart);
    } else if (patternPart !== pathPart) {
      return null;
    }
  }
  return params;
}
