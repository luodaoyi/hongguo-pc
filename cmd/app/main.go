// Command app 是桌面应用入口（方案 §9.4 / §9.1）。
//
// 装配：SQLite → 设备池 → 目录层 → 媒体管道（media + localsrv）→ 播放器状态机
// → Wails v3 服务层 → 窗口。
//
// 两条启动纪律：
//   - 前端 dist 缺失时给可读错误并退出，不 panic（资源用 http.Dir 提供，不做 embed）；
//   - ffmpeg 缺失不阻止启动：只有加密集播不了，界面会提示去设置页指定路径（方案 §13.3）。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/luodaoyi/hongguo-pc/internal/bindings"
	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/library"
	"github.com/luodaoyi/hongguo-pc/internal/localsrv"
	"github.com/luodaoyi/hongguo-pc/internal/media"
	"github.com/luodaoyi/hongguo-pc/internal/player"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

// distDir 是前端构建产物目录（相对工作目录，与 wails3 build 的约定一致）。
const distDir = "frontend/dist"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hongguo-pc:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := log.New(os.Stderr, "hongguo-pc: ", log.LstdFlags)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }

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

	// 3. 设备池。
	pool, err := devices.NewPool(devices.Config{
		Dir:   filepath.Join(dataDir, "devices"),
		Store: st,
		Logf:  logf,
	})
	if err != nil {
		return fmt.Errorf("创建设备池失败: %w", err)
	}

	// 4. 设置。
	settings, err := bindings.NewSettingsStore(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return err
	}

	// 5. 本地 Range 服务（与进程同生命周期）。
	srv, err := localsrv.New(localsrv.Config{Root: tmpRoot})
	if err != nil {
		return fmt.Errorf("启动本地播放服务失败: %w", err)
	}

	// 6. 媒体会话：ffmpeg 缺失不阻断启动，只是加密集播不了。
	ffmpegPath, ffmpegVersion, ffmpegErr := media.DetectFFmpeg(settings.Get(bindings.KeyFFmpegPath))
	if ffmpegErr != nil {
		logf("未找到 ffmpeg，加密集将无法播放（可在设置页指定路径）: %v", ffmpegErr)
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

	// 7. 目录层（0 额度，全部经设备池）。
	lib, err := library.New(library.Options{
		Pool:                pool,
		Store:               st,
		Logf:                logf,
		OnEpisodesRefreshed: bindings.EmitEpisodeUpdated,
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
		Pool:     pool,
		Store:    st,
		Catalog:  lib,
		Pipeline: pipeline,
		AutoNext: func() bool { return settings.Bool(bindings.KeyAutoNext, true) },
		Emit:     bindings.Emit,
		Logf:     logf,
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

	// 10. 前端资源：dist 缺失时给可读错误，不做 embed（方案 §11 验收口径）。
	assets, err := distHandler(distDir)
	if err != nil {
		return err
	}

	serviceOptions := application.ServiceOptions{MarshalError: bindings.MarshalError}
	app := application.New(application.Options{
		Name:        "hongguo-pc",
		Description: "红果短剧桌面播放器",
		Assets:      application.AssetOptions{Handler: assets},
		Services: []application.Service{
			application.NewServiceWithOptions(&bindings.Library{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Player{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.History{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Devices{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Settings{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Media{Core: core}, serviceOptions),
			application.NewServiceWithOptions(&bindings.Diag{Core: core}, serviceOptions),
		},
	})

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

// distHandler 用 http.Dir 提供前端静态资源。
//
// dist 不存在时返回可读错误而不是 panic：这既让 `go build ./cmd/app` 不依赖前端产物，
// 也让第一次跑的人在终端看到「先构建前端」的提示。
func distHandler(dir string) (http.Handler, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析前端目录 %s 失败: %w", dir, err)
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("前端资源目录不存在: %s\n请先在 frontend 目录执行 `npm ci && npm run build` 生成 dist（或用 wails3 build 打包）", abs)
	}
	if _, indexErr := os.Stat(filepath.Join(abs, "index.html")); indexErr != nil {
		return nil, fmt.Errorf("前端资源不完整: %s 缺少 index.html\n请重新执行 `npm run build`", abs)
	}
	files := http.FileServer(http.Dir(abs))
	return files, nil
}

// newSessionID 给本次运行的临时目录取个短名（进程内唯一）。
func newSessionID() string {
	return fmt.Sprintf("sess-%d", time.Now().UnixNano())
}
