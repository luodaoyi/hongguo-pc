import { useEffect, useState } from 'react';
import * as api from '../api';
import { errorMessage, formatClock } from '../format';
import { getEventStatus } from '../events';
import type { DevicesStatus, FFmpegInfo } from '../types';

const FFMPEG_KEY = 'ffmpegPath';

/** 设置页（最简）：ffmpeg 路径、设备池状态、清空数据（方案 §8.1）。 */
export function SettingsPage() {
  const [ffmpeg, setFfmpeg] = useState<FFmpegInfo | null>(null);
  const [ffmpegPath, setFfmpegPath] = useState('');
  const [detecting, setDetecting] = useState(false);
  const [ffmpegError, setFfmpegError] = useState<string | null>(null);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);

  const [devices, setDevices] = useState<DevicesStatus | null>(null);
  const [devicesError, setDevicesError] = useState<string | null>(null);

  const [clearConfirm, setClearConfirm] = useState(false);
  const [clearMessage, setClearMessage] = useState<string | null>(null);

  const detect = async () => {
    setDetecting(true);
    setFfmpegError(null);
    try {
      const info = await api.detectFFmpeg();
      setFfmpeg(info);
      if (info?.path && ffmpegPath === '') setFfmpegPath(info.path);
    } catch (err) {
      setFfmpegError(errorMessage(err));
      setFfmpeg(null);
    } finally {
      setDetecting(false);
    }
  };

  const loadDevices = async () => {
    setDevicesError(null);
    try {
      setDevices(await api.devicesStatus());
    } catch (err) {
      setDevicesError(errorMessage(err));
    }
  };

  useEffect(() => {
    void detect();
    void loadDevices();
    void api
      .settingsGet(FFMPEG_KEY)
      .then((value) => {
        if (typeof value === 'string' && value !== '') setFfmpegPath(value);
      })
      .catch(() => {
        // Settings.Get 未实现时不影响其它设置项，路径仍可手填并显示探测结果。
      });
    // 只跑一次：以上三个调用都是只读状态拉取（仓库未配 eslint，此处靠约定）。
  }, []);

  const saveFFmpegPath = async () => {
    setSaveMessage(null);
    try {
      await api.settingsSet(FFMPEG_KEY, ffmpegPath.trim());
      setSaveMessage('已保存。重新探测一次可确认 ffmpeg 是否可用。');
      await detect();
    } catch (err) {
      setSaveMessage(`保存失败：${errorMessage(err)}`);
    }
  };

  const clearData = async () => {
    setClearConfirm(false);
    try {
      await api.historyClear();
      setClearMessage('观看历史已清空。');
    } catch (err) {
      setClearMessage(`清空失败：${errorMessage(err)}`);
    }
  };

  return (
    <section className="page">
      <header className="page__header">
        <h1>设置</h1>
      </header>

      <section className="panel">
        <h2 className="panel__title">ffmpeg（解密转封装用，不随包分发）</h2>
        <p className="panel__hint">
          未检测到时，加密分集无法本地播种；直链分集不受影响。可手填可执行文件路径。
        </p>
        <div className="field">
          <label className="field__label" htmlFor="ffmpeg-path">
            路径
          </label>
          <input
            id="ffmpeg-path"
            className="field__input"
            type="text"
            placeholder={detecting ? '探测中…' : '例如 C:\\ffmpeg\\bin\\ffmpeg.exe'}
            value={ffmpegPath}
            onChange={(event) => setFfmpegPath(event.target.value)}
          />
          <button className="button" type="button" onClick={() => void saveFFmpegPath()}>
            保存
          </button>
          <button className="button button--ghost" type="button" onClick={() => void detect()} disabled={detecting}>
            {detecting ? '探测中…' : '重新探测'}
          </button>
        </div>
        {saveMessage ? <p className="panel__hint">{saveMessage}</p> : null}
        {ffmpegError ? <p className="notice notice--error">探测失败：{ffmpegError}</p> : null}
        {ffmpeg ? (
          <ul className="kv">
            <li>
              <span>状态</span>
              <span>{ffmpeg.ok ? '可用' : '不可用'}</span>
            </li>
            <li>
              <span>版本</span>
              <span>{ffmpeg.version || '未知'}</span>
            </li>
            <li>
              <span>许可</span>
              <span>{ffmpeg.license || '未知'}</span>
            </li>
            <li>
              <span>探测路径</span>
              <span className="mono">{ffmpeg.path || '未找到'}</span>
            </li>
          </ul>
        ) : null}
      </section>

      <section className="panel">
        <h2 className="panel__title">设备池</h2>
        <p className="panel__hint">
          每台匿名设备档案基本只给一次播放模型；池状态只读展示，重置设备属破坏性操作，本期未开放。
        </p>
        <div className="panel__actions">
          <button className="button" type="button" onClick={() => void loadDevices()}>
            刷新状态
          </button>
        </div>
        {devicesError ? <p className="notice notice--error">{devicesError}</p> : null}
        {devices ? (
          <>
            <ul className="kv">
              <li>
                <span>可用</span>
                <span>{devices.healthy}</span>
              </li>
              <li>
                <span>冷却中</span>
                <span>{devices.cooling}</span>
              </li>
              <li>
                <span>额度耗尽</span>
                <span>{devices.exhausted}</span>
              </li>
              <li>
                <span>被拒绝</span>
                <span>{devices.rejected}</span>
              </li>
              <li>
                <span>今日已用</span>
                <span>{devices.usedToday}</span>
              </li>
              <li>
                <span>非播放换档</span>
                <span>{devices.nonPlaybackSwaps}</span>
              </li>
            </ul>
            {devices.slots && devices.slots.length > 0 ? (
              <table className="table">
                <thead>
                  <tr>
                    <th>槽</th>
                    <th>状态</th>
                    <th>今日用量</th>
                    <th>最近使用</th>
                  </tr>
                </thead>
                <tbody>
                  {devices.slots.map((slot) => (
                    <tr key={slot.id}>
                      <td className="mono">{slot.id}</td>
                      <td>{slot.state}</td>
                      <td>{slot.quotaUsed}</td>
                      <td>{formatClock(slot.lastUsed)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : null}
          </>
        ) : null}
      </section>

      <section className="panel">
        <h2 className="panel__title">数据</h2>
        <p className="panel__hint">
          清空只删观看历史；播放缓存与设备档案的清理接口后端尚未暴露（见 README Roadmap）。
        </p>
        <div className="panel__actions">
          {clearConfirm ? (
            <>
              <button className="button button--danger" type="button" onClick={() => void clearData()}>
                确认清空观看历史
              </button>
              <button className="button button--ghost" type="button" onClick={() => setClearConfirm(false)}>
                取消
              </button>
            </>
          ) : (
            <button className="button" type="button" onClick={() => setClearConfirm(true)}>
              清空观看历史
            </button>
          )}
        </div>
        {clearMessage ? <p className="panel__hint">{clearMessage}</p> : null}
      </section>

      <section className="panel">
        <h2 className="panel__title">关于与合规</h2>
        <ul className="plain-list">
          <li>本工具只做本机播放，不注册账号、不伪造付费凭证、不破解、不去水印、不批量抓取。</li>
          <li>设备池的做法是用多份本地匿名设备档案分摊单台设备额度；是否违反上游 ToS 由使用者自行判断。</li>
          <li>仅限个人学习研究使用；本项目不含任何内容密钥。</li>
          <li>
            事件通道：
            {getEventStatus() === 'connected'
              ? '已连接'
              : getEventStatus() === 'unavailable'
                ? '不可用（非 Wails 宿主或运行时缺失）'
                : '未初始化'}
          </li>
        </ul>
      </section>
    </section>
  );
}
