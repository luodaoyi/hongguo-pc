// Command app 是桌面应用入口（方案 §9.4 / §9.1）。
//
// 装配：SQLite → 设备池 → 目录层 → 媒体管道（media + localsrv）→ 播放器状态机
// → Wails v3 服务层 → 窗口。
//
// 两条启动纪律：
//   - 前端产物用 go:embed 随二进制分发（发布包只有一个 exe，双击即可用，
//     与 CWD 无关；背景见 docs/rootcause-exe-not-launch.md）；
//   - ffmpeg 缺失不阻止启动：只有加密集播不了，界面会提示去设置页指定路径（方案 §13.3）。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/luodaoyi/hongguo-pc/frontend"
	"github.com/luodaoyi/hongguo-pc/internal/bindings"
	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/library"
	"github.com/luodaoyi/hongguo-pc/internal/localsrv"
	"github.com/luodaoyi/hongguo-pc/internal/media"
	"github.com/luodaoyi/hongguo-pc/internal/mpvplayer"
	"github.com/luodaoyi/hongguo-pc/internal/player"
	"github.com/luodaoyi/hongguo-pc/internal/singleinstance"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

func main() {
	if err := run(); err != nil {
		reportStartupError(err)
		os.Exit(1)
	}
}

// reportStartupError 把启动失败同时送到 stderr 与数据目录的 startup-error.log。
//
// 发布版以 -H windowsgui 链接，没有控制台承接 stderr——用户双击失败时看到的
// 是「毫无反应」（docs/rootcause-exe-not-launch.md §1.3）。落一份日志到数据
// 目录，让「打不开」至少有处可查。日志写不进去也不掩盖原始错误：stderr 仍打。
func reportStartupError(err error) {
	fmt.Fprintln(os.Stderr, "hongguo-pc:", err)
	if base, dirErr := resolveDataDir(); dirErr == nil {
		if mkErr := os.MkdirAll(base, 0o755); mkErr == nil {
			path := filepath.Join(base, "startup-error.log")
			f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if openErr == nil {
				fmt.Fprintf(f, "%s 启动失败: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
				f.Close()
				fmt.Fprintln(os.Stderr, "hongguo-pc: 错误详情已写入", path)
			}
		}
	}
}

func run() error {
	logger := log.New(os.Stderr, "hongguo-pc: ", log.LstdFlags)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }

	// 0. 单实例：先杀同名旧实例（含异常退出残留），再抢互斥量裁决唯一性。
	// 旧实例持有 SQLite 与设备池，双开必然互相踩写，必须先清场。
	release, err := singleinstance.Acquire("hongguo-pc")
	if err != nil {
		return fmt.Errorf("单实例检查失败: %w", err)
	}
	defer release()

	// 1. 数据目录：%LOCALAPPDATA%\hongguo-pc（方案 §13.1）。
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录 %s 失败: %w", dataDir, err)
	}
	tmpRoot := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return fmt.Errorf("创建临时目录 %s 失败: %w", tmpRoot, err)
	}
	// 启动扫遗留临时文件（方案 §3.4 / §9.7）：上次异常退出的半成品在这里清掉。
	if removed, sweepErr := media.Sweep(tmpRoot); sweepErr != nil {
		logf("清理遗留临时文件失败: %v", sweepErr)
	} else if removed > 0 {
		logf("已清理上次会话的临时文件 %d 个", removed)
	}

	// 2. 持久层。
	st, err := store.Open(filepath.Join(dataDir, "app.db"))
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer st.Close()
	if recovered := st.Recovered(); recovered != "" {
		logf("数据库迁移失败，已备份并重建；备份在 %s", recovered)
	}

	// 3. 设置（先于设备池：槽数从设置读，改动重启生效）。
	settings, err := bindings.NewSettingsStore(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return err
	}

	// 4. 设备池：每槽是一份匿名设备档案，单槽日额度约 1 次播放模型；
	// 槽数可在设置页调整（1-16，默认 4），重启生效。
	slots := settings.Int(bindings.KeyDeviceSlots, 4)
	if slots < 1 {
		slots = 1
	}
	if slots > 16 {
		slots = 16
	}
	if slots != 4 {
		logf("设备槽数 %d（默认 4，设置页可调，重启生效）", slots)
	}
	unlimited := settings.Bool(bindings.KeyUnlimitedDevices, false)
	if unlimited {
		logf("设备池按需无限扩槽已开启：无 healthy 槽时自动注册新匿名设备（重启生效的设置项）")
	}
	pool, err := devices.NewPool(devices.Config{
		Dir:       filepath.Join(dataDir, "devices"),
		Store:     st,
		Logf:      logf,
		Slots:     slots,
		Unlimited: unlimited,
	})
	if err != nil {
		return fmt.Errorf("创建设备池失败: %w", err)
	}

	// 5. ffmpeg 探测：加密集播放与 HEIC 封面转码都用它；缺失不阻断启动。
	ffmpegPath, ffmpegVersion, ffmpegErr := media.DetectFFmpeg(settings.Get(bindings.KeyFFmpegPath))
	if ffmpegErr != nil {
		logf("未找到 ffmpeg，加密集与 HEIC 封面转码将不可用（可在设置页指定路径）: %v", ffmpegErr)
	} else {
		logf("ffmpeg: %s (%s)", ffmpegPath, ffmpegVersion)
	}
	var session *media.Session
	if ffmpegErr == nil {
		session, err = media.NewSession(media.SessionConfig{
			FFmpegPath: ffmpegPath,
			Dir:        filepath.Join(tmpRoot, newSessionID()),
		})
		if err != nil {
			logf("创建媒体会话失败，加密集将无法播放: %v", err)
			session = nil
		}
	}

	// 6. 本地服务（Range 供播 + HEIC 封面代理），与进程同生命周期。
	srv, err := localsrv.New(localsrv.Config{
		Root:        tmpRoot,
		ImgCacheDir: filepath.Join(dataDir, "cache", "img"),
		FFmpegPath:  ffmpegPath,
		Logf:        logf,
	})
	if err != nil {
		return fmt.Errorf("启动本地播放服务失败: %w", err)
	}

	// 7. 目录层（0 额度，全部经设备池）；封面经本地代理转码（HEIC→JPEG）。
	lib, err := library.New(library.Options{
		Pool:                pool,
		Store:               st,
		Logf:                logf,
		OnEpisodesRefreshed: bindings.EmitEpisodeUpdated,
		CoverProxy:          srv.ImageURL,
		CoverProxyIsLocal:   srv.IsLocalProxyImg,
	})
	if err != nil {
		return err
	}

	// 8. 播放管道 + 状态机。
	pipeline, err := player.NewMediaPipeline(player.MediaPipelineConfig{
		Session: session,
		Server:  srv,
		Logf:    logf,
		Emit:    bindings.Emit,
	})
	if err != nil {
		return err
	}
	machine, err := player.New(player.Config{
		Pool:            pool,
		Store:           st,
		Catalog:         lib,
		Pipeline:        pipeline,
		AutoNext:        func() bool { return settings.Bool(bindings.KeyAutoNext, true) },
		PrefetchEnabled: func() bool { return settings.Bool(bindings.KeyPrefetchEnabled, true) },
		Emit:            bindings.Emit,
		Logf:            logf,
	})
	if err != nil {
		return err
	}

	// 9. 服务层。
	core, err := bindings.NewCore(bindings.CoreOptions{
		Pool:     pool,
		Store:    st,
		Library:  lib,
		Machine:  machine,
		Settings: settings,
		DataDir:  dataDir,
		Logf:     logf,
	})
	if err != nil {
		return err
	}

	// 10. 前端资源：go:embed 随二进制分发，双击 exe 即可用，与 CWD 无关。
	// （v0.1.0 按 CWD 读 frontend/dist，发布包又只带 exe，导致所有正常双击
	// 场景都起不来——docs/rootcause-exe-not-launch.md 的主因。）

	serviceOptions := application.ServiceOptions{MarshalError: bindings.MarshalError}
	app := application.New(application.Options{
		Name:        "hongguo-pc",
		Description: "红果短剧桌面播放器",
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(frontend.Dist)},
		Services: []application.Service{
			application.NewServiceWithOptions(&bindings.Library{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Player{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.History{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Devices{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Settings{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Media{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Diag{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.MPV{Core: core}, serviceOptions),
		},
	})

	// 12. mpv 内核（v0.1.12 起 local 源默认走 mpv，<video> 为 fallback）：
	// Chromium 对渐进 fMP4 音频包 fail-fast（PIPELINE_ERROR_DECODE），单个坏包
	// 炸整条管线、连播误跳集；mpv 遇坏包跳过。EndFile 转前端事件决定连播。
	// SetCallbacks 与 defer Close 在此注册；主窗口句柄注入在窗口创建后（见下）。
	if mpvplayer.Available() {
		logf("mpv 内核可用（libmpv）：local 源将走 mpv 播放")
		mpvplayer.Get().SetCallbacks(
			// eof=自然播完→转 player:mpv 事件由前端跳集；quit=用户关窗；error=解码失败。
			func(reason string) {
				bindings.Emit("player:mpv", map[string]any{"event": "ended", "reason": reason})
			},
			func(pos, dur float64) {
				bindings.Emit("player:mpv", map[string]any{"event": "tick", "position": pos, "duration": dur})
			},
			func() {
				bindings.Emit("player:mpv", map[string]any{"event": "play"})
			},
			func() {
				bindings.Emit("player:mpv", map[string]any{"event": "pause"})
			},
		)
		defer mpvplayer.Get().Close()
	} else {
		logf("mpv 不可用（exe 旁无 libmpv-2.dll）：继续用内置 <video> 播放")
	}

	// 11. 主窗口：1280×720，最小 960×540（方案 §8.5）。
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "红果短剧",
		Width:            1280,
		Height:           720,
		MinWidth:         960,
		MinHeight:        540,
		BackgroundColour: application.NewRGB(18, 18, 18),
	})

	// mpv 渲染窗的 owner：窗口在 Run 内才真正创建，轮询拿原生句柄注入
	// bindings。拿不到就保持 0——MPV.Available 为 false，前端回退 <video>。
	if mpvplayer.Available() {
		go func() {
			for i := 0; i < 200; i++ {
				if hw := mainWindow.NativeWindow(); hw != nil {
					bindings.MainWindowHandle = uintptr(hw)
					logf("mpv 渲染窗 owner 已注入（主窗口句柄就绪）")
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			logf("mpv 主窗口句柄注入超时：本次运行 mpv 不可用（回退 <video>）")
		}()
	}

	// 12. 退出：落盘断点 → 关媒体会话（杀 ffmpeg、删会话目录）→ 关本地服务 → 关库。
	//
	// 两个触发点，同一个 cleanup（sync.Once 保证只跑一次）：
	//   - app.OnShutdown：走 App.Quit / destroy 的路径（macOS、Linux、显式 Quit）；
	//   - WindowClosing：**Windows 关闭最后一个窗口时 Wails v3.1.0 不走 cleanup()**，
	//     PostQuitMessage 直接结束消息循环，OnShutdown 任务不会执行（实测：关窗后
	//     会话临时目录残留）。这里在窗口关闭事件上补一次，保证 ffmpeg 与临时文件不泄漏。
	var shutdownOnce sync.Once
	cleanup := func() {
		shutdownOnce.Do(func() {
			logf("正在退出：落盘断点并清理临时文件")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var errs []error
			if err := machine.Close(shutdownCtx); err != nil {
				errs = append(errs, err)
			}
			if session != nil {
				if err := session.Close(); err != nil {
					errs = append(errs, err)
				}
			}
			if err := srv.Close(); err != nil {
				errs = append(errs, err)
			}
			if err := st.Close(); err != nil {
				errs = append(errs, err)
			}
			if err := errors.Join(errs...); err != nil {
				logf("退出清理出现错误: %v", err)
			}
		})
	}
	app.OnShutdown(cleanup)
	mainWindow.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) { cleanup() })

	return app.Run()
}

// resolveDataDir 返回数据目录：优先 %LOCALAPPDATA%\hongguo-pc，回退到用户配置目录。
func resolveDataDir() (string, error) {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, "hongguo-pc"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法确定数据目录（LOCALAPPDATA 为空且取不到用户配置目录）: %w", err)
	}
	return filepath.Join(base, "hongguo-pc"), nil
}

// newSessionID 给本次运行的临时目录取个短名（进程内唯一）。
func newSessionID() string {
	return fmt.Sprintf("sess-%d", time.Now().UnixNano())
}
