import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../api';
import {
  onEvent,
  type MpvEventPayload,
  type PlayerProgressPayload,
  type PreparingPayload,
  type QuotaChangedPayload,
} from '../events';
import { errorMessage, formatDuration } from '../format';
import { navigate } from '../router';
import type { Episode, PlayerError, PrepareResult, Series, Source, StreamInfo } from '../types';
import { EpisodeGrid } from '../components/EpisodeGrid';

/**
 * 连播交接区：Go 侧 `Player.Ended` 已经把下一集解析好时，源暂存在这里。
 *
 * 路由切到该集后 `prepareNow` 直接取用，**不再调一次 Prepare**——
 * 这正是「连播以 Go 侧为准」的落点（否则前后端各解析一次，解密会起两个 ffmpeg）。
 */
const preparedHandoff = new Map<string, PrepareResult>();

function qualityLabel(stream: StreamInfo): string {
  const definition = stream.definition?.trim();
  if (definition) return definition;
  const quality = stream.quality?.trim();
  if (quality) return quality;
  if (stream.height && stream.height > 0) return `${stream.height}P`;
  return '默认';
}

interface PlayerPageProps {
  seriesId: string;
  episodeId: string;
  /** 从历史页带来的续播秒数，0 表示从头播 */
  startAt: number;
}

/**
 * 播放页（方案 §8.2 的三分区 + §8.6 快捷键）。
 *
 * 播放源只来自 Player.Prepare 的 {mode,url,mime}：direct 与 local 一视同仁，
 * 直接赋给 video.src。换集走 Player.Prepare 再用新 url 换 src，不整页刷新。
 */
export function PlayerPage({ seriesId, episodeId, startAt }: PlayerPageProps) {
  const [series, setSeries] = useState<Series | null>(null);
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [source, setSource] = useState<Source | null>(null);
  const [qualities, setQualities] = useState<StreamInfo[]>([]);
  const [quality, setQuality] = useState('');
  const [phase, setPhase] = useState<'idle' | 'resolving' | 'ready' | 'error'>('idle');
  const [error, setError] = useState<PlayerError | null>(null);
  const [preparing, setPreparing] = useState<PreparingPayload | null>(null);
  const [quotaExhausted, setQuotaExhausted] = useState(false);
  const [quota, setQuota] = useState<QuotaChangedPayload | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(true);
  // 播放速度：会话内记忆，换集/连播交接在 loadedmetadata 里恢复（video 元素
  // 随 src 重建会重置 playbackRate）。档位最高 3 倍。
  const [playbackRate, setPlaybackRate] = useState(1);
  const playbackRateRef = useRef(1);
  // 后端可信时长（PrepareResult.durationSec，来自选集目录）：渐进 fMP4 的
  // 初始 moov 里时长是估计值（常偏小，写完才准），显示与 seek 上限以它校正。
  const [trustedDuration, setTrustedDuration] = useState(0);
  const [videoDuration, setVideoDuration] = useState(0);
  // 自绘控制条状态：原生 controls 的时长显示取 video.duration（moov 估计值）
  // 且 Chromium 锁定后不再更新，管不了——去掉 controls，自己画。
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  // mpv 内核路径（v0.1.12）：local/direct 源交给 libmpv 独立窗口播放，
  // Chromium <video> 对渐进 fMP4 音频包 fail-fast（坏包炸整条管线、连播
  // 误跳集），退为 mpv 不可用时的 fallback。null=尚未探测。
  const [mpvMode, setMpvMode] = useState<boolean | null>(null);
  // 最近一次 tick 的位置/时长：ended(eof) 时判断是否 premature（渐进文件
  // 还在写、mpv 提前读到尾）。sourceRef 供续播重载取当前地址；
  // currentTimeRef/mpvModeRef 供 reportPosition 等回调读即时值（state 闭包
  // 会过期）。
  const lastTickRef = useRef<{ pos: number; dur: number }>({ pos: 0, dur: 0 });
  const sourceRef = useRef<Source | null>(null);
  sourceRef.current = source;
  const currentTimeRef = useRef(0);
  const mpvModeRef = useRef(false);
  mpvModeRef.current = mpvMode === true;
  const mutedRef = useRef(false);
  // 自动下一集：唯一真值在 Go 设置项 autoNextEpisode（Settings.Get/Set），
  // localStorage 那份已删除（首辅裁断：两个开关必须合成一个）。
  const [autoNext, setAutoNext] = useState(true);
  const autoNextRef = useRef(true);
  const [backendState, setBackendState] = useState('');
  const [serverProgress, setServerProgress] = useState<PlayerProgressPayload | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const videoRef = useRef<HTMLVideoElement | null>(null);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const pendingSeekRef = useRef<number>(startAt > 0 ? startAt : 0);
  const preferQualityRef = useRef('');
  const lastReportRef = useRef<{ episodeId: string; position: number }>({
    episodeId: '',
    position: 0,
  });
  const episodeIdRef = useRef(episodeId);
  const seriesIdRef = useRef(seriesId);
  const switchingRef = useRef(false);
  const goNextRef = useRef<(reason: 'keyboard' | 'ended' | 'event') => void>(() => undefined);

  const currentEpisode = useMemo(
    () => episodes.find((episode) => episode.id === episodeId) ?? null,
    [episodes, episodeId],
  );

  const qualityOptions = useMemo(() => {
    const labels: string[] = [];
    for (const stream of qualities) {
      const label = qualityLabel(stream);
      if (label && !labels.includes(label)) labels.push(label);
    }
    return labels;
  }, [qualities]);

  /** 上报进度；force=false 时按 5 秒节流（任务书「timeupdate 5 秒节流」）。 */
  const reportPosition = useCallback((force: boolean) => {
    const id = episodeIdRef.current;
    if (!id) return;
    // mpv 模式没有 <video>：位置来自 mpv tick 的 currentTimeRef（首版在这
    // 里因 video 为 null 直接 return，播放进度永远不上报、续播失效）。
    const position = mpvModeRef.current
      ? currentTimeRef.current
      : (videoRef.current?.currentTime ?? NaN);
    if (!Number.isFinite(position) || position < 0) return;
    const last = lastReportRef.current;
    if (!force && last.episodeId === id && position - last.position < 5) return;
    lastReportRef.current = { episodeId: id, position };
    void api.reportProgress(id, position).catch((err) => {
      console.warn('[player] 进度上报失败：', errorMessage(err));
    });
  }, []);

  const prepareNow = useCallback(async (targetEpisodeId: string, preferQuality: string) => {
    setPhase('resolving');
    setError(null);
    setPreparing(null);
    // 换集重置自绘控制条：video 元素随 src 重建，旧时间/时长不能带过去。
    setCurrentTime(0);
    setVideoDuration(0);
    setPlaying(false);
    // 连播交接：Go 侧 Ended 已经解好这一集，直接用它的源，不再重复 Prepare。
    const handoff = preparedHandoff.get(targetEpisodeId);
    if (handoff?.source?.url) {
      preparedHandoff.delete(targetEpisodeId);
      setSource(handoff.source);
      setQualities(handoff.qualities ?? []);
      setPhase('ready');
      const resume = typeof handoff.resumeSec === 'number' ? handoff.resumeSec : 0;
      if (pendingSeekRef.current === 0 && resume > 0) pendingSeekRef.current = resume;
      setTrustedDuration(typeof handoff.durationSec === 'number' ? handoff.durationSec : 0);
      return;
    }
    try {
      const result = await api.prepare(targetEpisodeId, preferQuality);
      if (!result?.source?.url) {
        throw new Error('Player.Prepare 没有返回可播放地址（source.url 为空）');
      }
      setSource(result.source);
      setQualities(result.qualities ?? []);
      setPhase('ready');
      // 后端给的续播点：本次没有显式 ?t= 时采用（§6.5 续播）。
      const resume = typeof result.resumeSec === 'number' ? result.resumeSec : 0;
      if (pendingSeekRef.current === 0 && resume > 0) pendingSeekRef.current = resume;
      setTrustedDuration(typeof result.durationSec === 'number' ? result.durationSec : 0);
    } catch (err) {
      setPhase('error');
      setError({
        code: 'prepare_failed',
        message: errorMessage(err),
        retryable: true,
        episodeId: targetEpisodeId,
      });
    }
  }, []);

  const switchTo = useCallback(
    (episode: Episode) => {
      if (switchingRef.current || episode.id === episodeIdRef.current) return;
      switchingRef.current = true;
      window.setTimeout(() => {
        switchingRef.current = false;
      }, 1200);
      reportPosition(true);
      videoRef.current?.pause();
      pendingSeekRef.current = 0;
      setError(null);
      setPreparing(null);
      navigate(`/play/${seriesId}/${episode.id}`);
    },
    [reportPosition, seriesId],
  );

  const goNext = useCallback(
    (reason: 'keyboard' | 'ended' | 'event') => {
      if (episodes.length === 0) return;
      const index = episodes.findIndex((episode) => episode.id === episodeId);
      for (let i = Math.max(0, index + 1); i < episodes.length; i += 1) {
        if (episodes[i].playable) {
          switchTo(episodes[i]);
          return;
        }
      }
      setNotice(reason === 'ended' ? '已是最后一集，连播结束' : '已经是最后一集');
    },
    [episodes, episodeId, switchTo],
  );

  const goPrev = useCallback(() => {
    if (episodes.length === 0) return;
    const index = episodes.findIndex((episode) => episode.id === episodeId);
    for (let i = Math.min(episodes.length - 1, index - 1); i >= 0; i -= 1) {
      if (episodes[i].playable) {
        switchTo(episodes[i]);
        return;
      }
    }
    setNotice('已经是第一集');
  }, [episodes, episodeId, switchTo]);

  const togglePlay = useCallback(() => {
    if (mpvMode) {
      void api.mpvTogglePause().catch(() => {});
      return;
    }
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) {
      const promise = video.play();
      if (promise && typeof promise.catch === 'function') {
        promise.catch(() => {
          // 自动播放被拦或源尚未就绪，等用户再点一次
        });
      }
    } else {
      video.pause();
    }
  }, [mpvMode]);

  /** 展示/seek 共用的有效时长：元数据与后端可信时长取大（moov 估计值偏小）。 */
  const effectiveDuration = Math.max(videoDuration, trustedDuration);

  /** 点进度条跳转到指定秒；上限即有效时长。 */
  const seekTo = useCallback(
    (seconds: number) => {
      if (mpvMode) {
        if (!Number.isFinite(seconds) || seconds < 0) return;
        void api.mpvSeekTo(seconds).catch(() => {});
        setCurrentTime(seconds);
        reportPosition(true);
        return;
      }
      const video = videoRef.current;
      if (!video || !Number.isFinite(seconds) || seconds < 0) return;
      const duration = Math.max(
        Number.isFinite(video.duration) ? video.duration : 0,
        trustedDuration,
      );
      if (duration <= 0) return;
      video.currentTime = Math.min(seconds, duration);
      reportPosition(true);
    },
    [mpvMode, reportPosition, trustedDuration],
  );

  const toggleMute = useCallback(() => {
    if (mpvMode) {
      // mpv 模式：mute 属性翻转（音量不走 video 元素）。
      void (async () => {
        try {
          const wasMuted = mutedRef.current;
          await api.mpvSetVolume(wasMuted ? 100 : 0);
          mutedRef.current = !wasMuted;
        } catch (err) {
          console.warn('[player] mpv 静音切换失败：', errorMessage(err));
        }
      })();
      return;
    }
    const video = videoRef.current;
    if (video) video.muted = !video.muted;
  }, [mpvMode]);

  const seekBy = useCallback(
    (delta: number) => {
      if (mpvMode) {
        void api.mpvSeekTo(Math.max(0, currentTime + delta)).catch(() => {});
        reportPosition(true);
        return;
      }
      const video = videoRef.current;
      if (!video) return;
      // 渐进 fMP4 初始 moov 时长偏小：seek 上限取「元数据与后端可信时长」的较大者。
      const meta = Number.isFinite(video.duration) ? video.duration : 0;
      const duration = Math.max(meta, trustedDuration, video.currentTime + Math.abs(delta));
      video.currentTime = Math.min(Math.max(0, video.currentTime + delta), duration);
      reportPosition(true);
    },
    [mpvMode, currentTime, reportPosition, trustedDuration],
  );

  const toggleFullscreen = useCallback(() => {
    const element = stageRef.current;
    if (!element) return;
    if (document.fullscreenElement) {
      void document.exitFullscreen();
      return;
    }
    const promise = element.requestFullscreen();
    if (promise && typeof promise.catch === 'function') {
      promise.catch(() => setNotice('全屏失败：宿主不允许或已被拦截'));
    }
  }, []);

  const changeAutoNext = (checked: boolean) => {
    autoNextRef.current = checked;
    setAutoNext(checked);
    // 写入 Go 设置项：后端连播（Player.Ended 内部连播）读的就是这个值。
    void api.settingsSet(api.SettingKey.autoNextEpisode, checked ? 'true' : 'false').catch((err) => {
      setNotice(`自动连播设置保存失败：${errorMessage(err)}`);
    });
  };

  const changeQuality = async (next: string) => {
    if (next === quality) return;
    preferQualityRef.current = next;
    const previous = quality;
    setQuality(next);
    if (next === '') {
      await prepareNow(episodeId, '');
      return;
    }
    const resumeAt = videoRef.current?.currentTime ?? 0;
    try {
      const nextSource = await api.switchQuality(episodeId, next);
      if (!nextSource?.url) throw new Error('Player.SwitchQuality 没有返回地址');
      pendingSeekRef.current = resumeAt;
      setError(null);
      setSource(nextSource);
    } catch (err) {
      setQuality(previous);
      setError({
        code: 'quality_failed',
        message: `切换清晰度失败：${errorMessage(err)}`,
        retryable: true,
        episodeId,
      });
    }
  };

  /* ---------- 状态同步 ---------- */

  useEffect(() => {
    episodeIdRef.current = episodeId;
  }, [episodeId]);

  useEffect(() => {
    seriesIdRef.current = seriesId;
  }, [seriesId]);

  useEffect(() => {
    goNextRef.current = goNext;
  }, [goNext]);

  // handleEnded 经 ref 供 mpv 事件回调使用（定义在后，避免依赖序问题）。
  const handleEndedRef = useRef<() => void>(() => undefined);
  useEffect(() => {
    handleEndedRef.current = handleEnded;
  }, []);

  // 挂载时探测一次 mpv 可用性（dll 探测很快，且进程内不会变）。
  useEffect(() => {
    let alive = true;
    void api
      .mpvAvailable()
      .then((ok) => {
        if (alive) setMpvMode(ok);
      })
      .catch(() => {
        if (alive) setMpvMode(false);
      });
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    pendingSeekRef.current = startAt > 0 ? startAt : 0;
  }, [episodeId, startAt]);

  // 自动连播开关：从 Go 设置项读一次（缺省=开启，与 internal/player 的默认一致）。
  useEffect(() => {
    let cancelled = false;
    void api
      .settingsGet(api.SettingKey.autoNextEpisode)
      .then((value) => {
        if (cancelled) return;
        const enabled = value !== 'false' && value !== '0';
        autoNextRef.current = enabled;
        setAutoNext(enabled);
      })
      .catch((err) => console.warn('[player] 读取自动连播设置失败：', errorMessage(err)));
    return () => {
      cancelled = true;
    };
  }, []);

  /* ---------- 目录与播放准备 ---------- */

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const [detail, list] = await Promise.all([api.getSeries(seriesId), api.listEpisodes(seriesId)]);
        if (cancelled) return;
        setSeries(detail);
        setEpisodes(list ?? []);
      } catch (err) {
        if (cancelled) return;
        setNotice(`剧集列表加载失败：${errorMessage(err)}`);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [seriesId]);

  useEffect(() => {
    void prepareNow(episodeId, preferQualityRef.current);
  }, [episodeId, prepareNow]);

  /* ---------- video 元素 ---------- */

  useEffect(() => {
    const video = videoRef.current;
    if (!video || !source?.url) return;
    video.src = source.url;
    video.load();
    const promise = video.play();
    if (promise && typeof promise.catch === 'function') {
      promise.catch(() => {
        // 自动播放策略可能拦下；原生控件仍在，用户点一下即可
      });
    }
  }, [source]);

  useEffect(() => {
    const onChange = () => setIsFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  // 卸载时强制落一次进度（任务书：暂停/切集/卸载强制上报）
  useEffect(() => () => reportPosition(true), [reportPosition]);

  /* ---------- mpv 内核路径 ---------- */

  // 播放画面的视口矩形（物理像素）：渲染窗要对齐的 .player-stage 区域，
  // 排除顶部栏与选集抽屉。屏幕坐标由 Go 侧 ClientToScreen 换算（自动吸收
  // 标题栏高度——window.screenX 是外框原点，直接加会偏移一个标题栏）。
  const stageScreenRect = (): number[] => {
    const el = stageRef.current;
    if (!el) return [];
    // 渲染窗只盖画面区：底部控制条是 WebView 画的 DOM，被子窗口挡住就
    // 彻底不可见（WS_DISABLED 只穿透点击、不穿透显示）——高度扣掉它。
    const controls = el.querySelector('.player-controls');
    const controlsH = controls ? controls.getBoundingClientRect().height : 0;
    const r = el.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    return [
      r.left * dpr,
      r.top * dpr,
      r.width * dpr,
      Math.max(0, (r.height - controlsH) * dpr),
    ];
  };

  // 离开播放页/换集时停掉 mpv 并隐藏渲染窗（<video> 随组件走，mpv 是
  // 进程级的必须显式停；只停不隐藏会留黑窗盖住别的页面）。
  useEffect(() => {
    if (!mpvMode) return undefined;
    return () => {
      void api.mpvStop().catch(() => {});
      void api.mpvHide().catch(() => {});
    };
  }, [mpvMode]);

  // source 就绪 + mpv 可用：交给 mpv 播放（续播点经 seek 恢复，倍速同理）。
  // 回退纪律：只有 Play 本身失败（mpv 拒播/加载超时）才回退 <video>；
  // seek/倍速失败只警告——mpv 已在播，不能再中 Chromium 的坏包管线（首版
  // 误把「加载未完成时 seek 失败」当 mpv 不可用，正是双窗口+报错的根因）。
  useEffect(() => {
    if (mpvMode !== true || !source?.url || phase !== 'ready') return;
    let cancelled = false;
    void (async () => {
      try {
        await api.mpvPlay(source.url, series?.title ?? '红果短剧', stageScreenRect());
        if (cancelled) return;
        // Play 内部才建渲染窗，先于 Play 的首 Move 可能落在建窗前（Go 侧
        // pendingRect 已兜底，这里再强制补一次，双保险）。
        void api.mpvMove(stageScreenRect()).catch(() => {});
      } catch (err) {
        if (!cancelled) {
          console.warn('[player] mpv 起播失败，回退 <video>：', errorMessage(err));
          void api.mpvHide().catch(() => {});
          setMpvMode(false);
        }
        return;
      }
      const target = pendingSeekRef.current;
      if (target > 0) {
        try {
          await api.mpvSeekTo(target);
        } catch (err) {
          console.warn('[player] mpv 续播 seek 失败（忽略，从头播）：', errorMessage(err));
        }
        pendingSeekRef.current = 0;
      }
      if (playbackRateRef.current !== 1) {
        try {
          await api.mpvSetRate(playbackRateRef.current);
        } catch (err) {
          console.warn('[player] mpv 恢复倍速失败（忽略）：', errorMessage(err));
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [mpvMode, source?.url, phase, series?.title]);

  // 渲染窗几何跟随：主窗口移动（screenX/Y 变化，无 DOM 事件）+ 缩放
  // （resize/抽屉开合）。500ms 轮询矩形变化，变了才调 Move（幂等）。
  useEffect(() => {
    if (mpvMode !== true) return undefined;
    let last = '';
    const sync = () => {
      const rect = stageScreenRect();
      if (rect.length !== 4) return;
      const key = rect.map((n) => Math.round(n)).join(',');
      if (key === last) return;
      last = key;
      void api.mpvMove(rect).catch(() => {});
    };
    sync();
    const timer = window.setInterval(sync, 500);
    const ro = new ResizeObserver(sync);
    if (stageRef.current) ro.observe(stageRef.current);
    window.addEventListener('resize', sync);
    return () => {
      window.clearInterval(timer);
      ro.disconnect();
      window.removeEventListener('resize', sync);
    };
  }, [mpvMode]);

  // mpv 事件驱动：tick 更新进度/时长，play/pause 更新按钮，ended 按 reason 分流
  // （eof 才连播——这就是修「播一半跳下一集」的关键：解码失败不再伪装成播完）。
  useEffect(() => {
    if (mpvMode !== true) return undefined;
    return onEvent('player:mpv', (payload: MpvEventPayload) => {
      if (payload.event === 'tick') {
        setCurrentTime(payload.position ?? 0);
        currentTimeRef.current = payload.position ?? 0;
        const dur = payload.duration ?? 0;
        if (dur > 0) setVideoDuration((prev) => Math.max(prev, dur, trustedDuration));
        lastTickRef.current = { pos: payload.position ?? 0, dur };
      } else if (payload.event === 'play') {
        setPlaying(true);
      } else if (payload.event === 'pause') {
        setPlaying(false);
      } else if (payload.event === 'ended') {
        if (payload.reason === 'eof') {
          // 渐进文件竞态：转码还在写、mpv 已读到当前文件尾——此刻的「eof」
          // 是 premature，绝不能跳集。对照后端可信时长：位置差得远就重载续播
          // （此时写入多半已完成），真正播完（位置贴近时长）才走连播。
          const last = lastTickRef.current;
          const trusted = trustedDuration;
          const pos = last?.pos ?? 0;
          const shown = Math.max(last?.dur ?? 0, trusted);
          if (shown > 5 && pos < shown - 4 && sourceRef.current?.url) {
            setNotice('视频仍在后台写入，已自动续播');
            const url = sourceRef.current.url;
            void (async () => {
              try {
                await api.mpvPlay(url, '', []);
                await api.mpvSeekTo(Math.max(0, pos - 1));
              } catch (err) {
                console.warn('[player] 续播失败：', errorMessage(err));
              }
            })();
            return;
          }
          handleEndedRef.current();
        } else if (payload.reason === 'error') {
          setError({
            code: 'media_error',
            message: 'mpv 解码失败（已尽量容错；可重试或回退内置内核）',
            retryable: true,
            episodeId: episodeIdRef.current,
          });
        }
        // quit（用户关窗/Stop）：不跳集、不报错，静默回到页面。
      }
    });
  }, [mpvMode, trustedDuration]);

  /* ---------- 事件 ---------- */

  useEffect(() => {
    const offs = [
      onEvent('player:state', (payload) => {
        setBackendState(payload?.state ?? '');
        if (payload?.state === 'playing') {
          setPreparing(null);
          setError(null);
        }
        // 连播由 Go 侧在 Player.Ended 内完成（返回 prepared.source），
        // 前端不再因 player:state=ended 自己跳集——否则两边各 Prepare 一次。
      }),
      onEvent('player:error', (payload) => {
        setPreparing(null);
        setPhase('error');
        setError(payload ?? { code: 'unknown', message: '未知播放错误', retryable: false });
      }),
      onEvent('player:progress', (payload) => {
        if (payload?.episodeId === episodeIdRef.current) setServerProgress(payload);
      }),
      onEvent('episode:updated', (payload) => {
        if (payload?.seriesId !== seriesIdRef.current) return;
        void api
          .listEpisodes(payload.seriesId)
          .then((list) => setEpisodes(list ?? []))
          .catch((err) => console.warn('[player] 刷新剧集列表失败：', errorMessage(err)));
      }),
      onEvent('quota:changed', (payload) => {
        setQuota(payload ?? null);
        if ((payload?.healthy ?? 0) > 0) setQuotaExhausted(false);
      }),
      onEvent('quota:exhausted', () => setQuotaExhausted(true)),
      onEvent('media:preparing', (payload) => {
        if (payload?.episodeId && payload.episodeId !== episodeIdRef.current) return;
        // stage=ready 表示解密文件已可播，收起「解密中」提示。
        setPreparing(payload?.stage === 'ready' ? null : payload ?? null);
      }),
    ];
    return () => {
      for (const off of offs) off();
    };
  }, []);

  /* ---------- 快捷键 ---------- */

  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const tag = target?.tagName ?? '';
      const inField =
        tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target?.isContentEditable === true;
      if (inField) return;
      if ((tag === 'BUTTON' || tag === 'A') && (event.key === ' ' || event.key === 'Enter')) return;

      switch (event.key) {
        case ' ':
          event.preventDefault();
          togglePlay();
          break;
        case 'ArrowLeft':
          event.preventDefault();
          seekBy(-5);
          break;
        case 'ArrowRight':
          event.preventDefault();
          seekBy(5);
          break;
        case 'n':
        case 'N':
          event.preventDefault();
          goNext('keyboard');
          break;
        case 'p':
        case 'P':
          event.preventDefault();
          goPrev();
          break;
        case 's':
        case 'S':
          event.preventDefault();
          setDrawerOpen((open) => !open);
          break;
        case 'f':
        case 'F':
          event.preventDefault();
          toggleFullscreen();
          break;
        default:
          break;
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [goNext, goPrev, seekBy, toggleFullscreen, togglePlay]);

  /* ---------- 渲染 ---------- */

  const retry = () => {
    void prepareNow(episodeId, preferQualityRef.current);
  };

  const handleEnded = () => {
    const position = videoRef.current?.currentTime ?? 0;
    reportPosition(true);
    void (async () => {
      let result: Awaited<ReturnType<typeof api.reportEnded>> | null = null;
      try {
        result = await api.reportEnded(episodeIdRef.current, position);
      } catch (err) {
        console.warn('[player] Ended 上报失败：', errorMessage(err));
      }
      if (!autoNextRef.current) {
        setNotice('已播完（自动下一集已关闭）');
        return;
      }
      // 连播以 Go 侧为准：后端已在 Ended 内解析好下一集时，直接吃它的源。
      const prepared = result?.prepared;
      if (prepared?.episodeId && prepared.source?.url) {
        preparedHandoff.set(prepared.episodeId, prepared);
        pendingSeekRef.current = 0;
        setNotice(null);
        navigate(`/play/${seriesIdRef.current}/${prepared.episodeId}`);
        return;
      }
      if (result && (result.seriesFinished || !result.hasNext)) {
        setNotice('已是最后一集，连播结束');
        return;
      }
      // 后端没准备好（例如关闭了自动连播或解析失败）：退回前端按本地列表跳集。
      goNextRef.current('ended');
    })();
  };

  const handleVideoError = () => {
    const message = videoRef.current?.error?.message ?? '播放内核报错';
    setError({ code: 'media_error', message, retryable: true, episodeId });
    setPhase('error');
  };

  return (
    <div className="player-page">
      <header className="player-topbar">
        <button className="button button--ghost" type="button" onClick={() => navigate(`/series/${seriesId}`)}>
          ← 返回
        </button>
        <div className="player-topbar__title">
          <span className="player-topbar__series">{series?.title ?? '加载中…'}</span>
          {currentEpisode ? <span className="player-topbar__episode">第 {currentEpisode.number} 集</span> : null}
        </div>

        <label className="player-topbar__quality">
          清晰度
          <select
            value={quality}
            onChange={(event) => void changeQuality(event.target.value)}
            disabled={qualityOptions.length === 0}
          >
            <option value="">默认</option>
            {qualityOptions.map((label) => (
              <option key={label} value={label}>
                {label}
              </option>
            ))}
          </select>
        </label>

        <label className="player-topbar__quality">
          倍速
          <select
            value={playbackRate}
            onChange={(event) => {
              const rate = Number(event.target.value);
              setPlaybackRate(rate);
              playbackRateRef.current = rate;
              if (mpvMode) {
                void api.mpvSetRate(rate).catch(() => {});
                return;
              }
              const video = videoRef.current;
              if (video) video.playbackRate = rate;
            }}
          >
            {[0.5, 0.75, 1, 1.25, 1.5, 2, 2.5, 3].map((rate) => (
              <option key={rate} value={rate}>
                {rate}x
              </option>
            ))}
          </select>
        </label>

        <label className="toggle">
          <input type="checkbox" checked={autoNext} onChange={(event) => changeAutoNext(event.target.checked)} />
          自动下一集
        </label>

        <button className="button button--ghost" type="button" onClick={() => setDrawerOpen((open) => !open)}>
          {drawerOpen ? '收起选集' : '选集'}
        </button>
        <button className="button button--ghost" type="button" onClick={toggleFullscreen}>
          {isFullscreen ? '退出全屏' : '全屏'}
        </button>
      </header>

      {quotaExhausted ? (
        <div className="notice notice--warn">
          设备额度已耗尽：浏览、选集与设置仍可用，播放要等额度窗口恢复后再试。
        </div>
      ) : null}

      {error ? (
        <div className="notice notice--error">
          <span>
            播放出错（{error.code || 'unknown'}）：{error.message}
          </span>
          <button className="button" type="button" onClick={retry} disabled={quotaExhausted}>
            重试
          </button>
          {/* 加密方式不支持/内容不可用这类集级错误：重试通常无用，给一键跳过
              （连播会自动跳，手动点集失败时之前只能干看）。 */}
          {error.code === 'drm_unsupported' ||
          error.code === 'content_unavailable' ||
          error.code === 'anonymous_limited' ? (
            <button className="button" type="button" onClick={() => goNextRef.current('keyboard')}>
              跳过此集
            </button>
          ) : null}
        </div>
      ) : null}

      {notice ? (
        <div className="notice notice--info">
          <span>{notice}</span>
          <button className="button button--ghost" type="button" onClick={() => setNotice(null)}>
            知道了
          </button>
        </div>
      ) : null}

      <div className="player-body">
        <div className="player-stage" ref={stageRef}>
          {/* mpv 模式：画面在 mpv 独立窗口（WebView 之下塞 HWND 会被盖住），
              页内只保留控制条与选集；<video> 卸载避免 Chromium 再碰坏包。 */}
          {mpvMode !== true ? (
          <video
            ref={videoRef}
            className="player-video"
            playsInline
            preload="metadata"
            onTimeUpdate={() => {
              reportPosition(false);
              // 动态解析时长：渐进 fMP4 每次元数据修正都可能推大 duration，
              // timeupdate 里持续取 max，配合后端可信时长兜底估计值偏小。
              const video = videoRef.current;
              if (!video) return;
              setCurrentTime(video.currentTime);
              const meta = Number.isFinite(video.duration) ? video.duration : 0;
              const next = Math.max(meta, trustedDuration);
              setVideoDuration((prev) => (next > prev ? next : prev));
            }}
            onPlay={() => setPlaying(true)}
            onPause={() => {
              setPlaying(false);
              reportPosition(true);
            }}
            onEnded={handleEnded}
            onError={handleVideoError}
            onLoadedMetadata={() => {
              const video = videoRef.current;
              if (!video) return;
              // 换集/连播交接会重建 video 元素，playbackRate 被浏览器重置为 1：
              // 元数据就绪后恢复会话内选择的速度，实现「设 2 倍就一直 2 倍」。
              video.playbackRate = playbackRateRef.current;
              const target = pendingSeekRef.current;
              if (!target || target <= 0) return;
              // seek 上限取元数据与后端可信时长的较大者（渐进 moov 偏小）。
              const duration = Math.max(
                Number.isFinite(video.duration) ? video.duration : 0,
                trustedDuration,
                target,
              );
              video.currentTime = Math.min(target, duration);
              pendingSeekRef.current = 0;
            }}
            onDurationChange={() => {
              // 渐进 fMP4 的初始 moov 时长是估计值（常偏小）；后端
              // durationSec 来自选集目录，是可信值——只增不减取更大者，
              // 换集归零由 prepareNow 负责。
              const video = videoRef.current;
              if (!video) return;
              const meta = Number.isFinite(video.duration) ? video.duration : 0;
              setVideoDuration((prev) => Math.max(prev, meta, trustedDuration));
            }}
          />
          ) : null}
          {phase === 'resolving' ? <div className="player-overlay">正在解析播放地址…</div> : null}
          {videoDuration > 0 && phase === 'ready' ? (
            <div className="player-duration-badge">时长 {formatDuration(videoDuration)}</div>
          ) : null}
          {preparing ? (
            <div className="player-overlay">
              解密中…{preparing.stage ? `（${preparing.stage}）` : ''}
            </div>
          ) : null}
          {phase === 'ready' ? (
            <div className="player-controls">
              <button
                className="player-controls__btn"
                type="button"
                onClick={togglePlay}
                title="播放/暂停"
              >
                {playing ? '⏸' : '▶'}
              </button>
              <span className="player-controls__time">
                {formatDuration(currentTime)} / {formatDuration(effectiveDuration)}
              </span>
              <input
                className="player-controls__bar"
                type="range"
                min={0}
                max={effectiveDuration > 0 ? effectiveDuration : 1}
                step={0.1}
                value={Math.min(currentTime, effectiveDuration || currentTime)}
                onChange={(event) => seekTo(Number(event.target.value))}
                aria-label="播放进度"
              />
              <button
                className="player-controls__btn"
                type="button"
                onClick={toggleMute}
                title="静音"
              >
                🔊
              </button>
              <button
                className="player-controls__btn"
                type="button"
                onClick={toggleFullscreen}
                title="全屏"
              >
                {isFullscreen ? '⤡' : '⛶'}
              </button>
            </div>
          ) : null}
        </div>

        {drawerOpen ? (
          <aside className="player-drawer">
            <header className="player-drawer__header">
              选集
              <span className="player-drawer__count">{episodes.length} 集</span>
            </header>
            <EpisodeGrid
              episodes={episodes}
              currentEpisodeId={episodeId}
              onPick={switchTo}
              emptyText="剧集列表为空，去详情页刷新一次。"
            />
          </aside>
        ) : null}
      </div>

      <footer className="player-footer">
        <span>Space 播放/暂停 · ←/→ ±5 秒 · N/P 上下集 · S 选集 · F 全屏</span>
        <span className="mono">
          {source ? `${source.mode} · ${source.mime ?? ''}` : '无播放源'}
          {backendState ? ` · 状态 ${backendState}` : ''}
          {serverProgress ? ` · 后端进度 ${formatDuration(serverProgress.positionSec)}` : ''}
          {quota ? ` · 可用设备 ${quota.healthy}，今日已用 ${quota.usedToday}` : ''}
        </span>
      </footer>
    </div>
  );
}
