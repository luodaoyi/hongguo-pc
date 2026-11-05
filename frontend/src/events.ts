// 事件适配层：前端所有对 Go 事件的订阅都从这里出去。
//
// 运行时用 @wailsio/runtime 的 Events（方案 §9.4），**静态导入**以便构建期就发现缺包；
// `frontend/bindings/events/index.ts` 是生成物，负责把事件数据构造器注册进运行时，
// 这里 import 一次即可（当前后端未注册自定义事件类型，所以载荷类型仍是本文件手写，
// 与 internal/bindings/core.go 的事件名和 map 载荷一一对应）。
//
// 页面只依赖 onEvent / initEvents 两个函数；换成别的运行时（测试用事件总线）只需改本文件。

import { Events } from '@wailsio/runtime';
import '../bindings/events/index.js';

export type EventName =
  | 'player:state'
  | 'player:error'
  | 'player:progress'
  | 'episode:updated'
  | 'quota:changed'
  | 'quota:exhausted'
  | 'media:preparing'
  | 'devices:changed';

export interface PlayerStatePayload {
  state: string;
  episodeId?: string;
}

export interface PlayerErrorPayload {
  code: string;
  message: string;
  retryable: boolean;
  episodeId?: string;
}

export interface PlayerProgressPayload {
  episodeId: string;
  positionSec: number;
  durationSec: number;
}

export interface EpisodeUpdatedPayload {
  seriesId: string;
  added: number;
  updated: number;
  removed: number;
}

export interface QuotaChangedPayload {
  healthy: number;
  usedToday: number;
}

export interface PreparingPayload {
  episodeId: string;
  mode?: string;
  stage?: string;
}

export interface DevicesChangedPayload {
  slots?: Array<{ id: string; state: string }>;
}

export interface EventPayloads {
  'player:state': PlayerStatePayload;
  'player:error': PlayerErrorPayload;
  'player:progress': PlayerProgressPayload;
  'episode:updated': EpisodeUpdatedPayload;
  'quota:changed': QuotaChangedPayload;
  'quota:exhausted': Record<string, never>;
  'media:preparing': PreparingPayload;
  'devices:changed': DevicesChangedPayload;
}

export type EventStatus = 'idle' | 'connected' | 'unavailable';

const KNOWN_EVENTS: EventName[] = [
  'player:state',
  'player:error',
  'player:progress',
  'episode:updated',
  'quota:changed',
  'quota:exhausted',
  'media:preparing',
  'devices:changed',
];

type AnyHandler = (payload: any) => void;

const handlers = new Map<string, Set<AnyHandler>>();
let status: EventStatus = 'idle';
let initPromise: Promise<boolean> | null = null;

function dispatch(name: string, payload: unknown): void {
  const set = handlers.get(name);
  if (!set) return;
  for (const handler of set) {
    try {
      handler(payload);
    } catch (err) {
      console.error('[events] 处理事件出错：', name, err);
    }
  }
}

function connect(): Promise<boolean> {
  try {
    for (const name of KNOWN_EVENTS) {
      try {
        // Events.On 的回调是 WailsEvent：业务载荷在 .data 里。
        Events.On(name, (event: { data?: unknown }) => dispatch(name, event?.data));
      } catch (err) {
        console.warn('[events] 订阅失败：', name, err);
      }
    }
    status = 'connected';
    return Promise.resolve(true);
  } catch (err) {
    status = 'unavailable';
    console.warn('[events] 事件运行时不可用（非 Wails 宿主？）：', err);
    return Promise.resolve(false);
  }
}

/** 应用启动时调一次；非 Wails 宿主下返回 false 也不影响页面浏览。 */
export function initEvents(): Promise<boolean> {
  if (!initPromise) initPromise = connect();
  return initPromise;
}

export function getEventStatus(): EventStatus {
  return status;
}

/** 订阅事件，返回取消订阅函数。初始化前后调用都安全（先登记，连上后统一分发）。 */
export function onEvent<K extends EventName>(
  name: K,
  handler: (payload: EventPayloads[K]) => void,
): () => void {
  let set = handlers.get(name);
  if (!set) {
    set = new Set();
    handlers.set(name, set);
  }
  const wrapped: AnyHandler = (payload) => handler(payload as EventPayloads[K]);
  set.add(wrapped);
  return () => {
    set!.delete(wrapped);
  };
}
