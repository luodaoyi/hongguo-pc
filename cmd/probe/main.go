// Command probe 是 M0 阶段的最小冒烟工具（方案 §11 M0 验收：连续解析 3 集并打印槽用量）。
//
// 用法：
//
//	go run ./cmd/probe search <关键词>
//	go run ./cmd/probe list <seriesID>
//	go run ./cmd/probe resolve <episodeID>
//	go run ./cmd/probe status
//
// 可选参数（写在子命令前面）：-data <目录>（默认 data，放设备档案与 SQLite）、
// -slots <槽数>（默认 4）、-timeout <时长>（默认 90s）。
//
// 打印纪律（方案 §2.2 I2）：**绝不输出内容密钥或密钥材料**。播放摘要走
// model.PlaybackInfo，那里没有密钥字段，只有「本会话是否持有密钥」的布尔。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/luodaoyi/hongguo-pc/internal/devices"
	"github.com/luodaoyi/hongguo-pc/internal/model"
	"github.com/luodaoyi/hongguo-pc/internal/sdkx"
	"github.com/luodaoyi/hongguo-pc/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "用法: probe [-data 目录] [-slots N] [-timeout 时长] <search|list|resolve|status> [参数]")
		fs.PrintDefaults()
	}
	dataDir := fs.String("data", "data", "数据目录（设备档案与 SQLite）")
	slotCount := fs.Int("slots", 4, "设备槽数量")
	timeout := fs.Duration("timeout", 90*time.Second, "单次调用超时")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return errors.New("缺少子命令：search | list | resolve | status")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	st, err := store.Open(filepath.Join(*dataDir, "hongguo.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if rec := st.Recovered(); rec != "" {
		fmt.Fprintf(os.Stderr, "警告: schema 迁移失败，已重建空库；备份在 %s\n", rec)
	}

	pool, err := devices.NewPool(devices.Config{
		Dir:   filepath.Join(*dataDir, "devices"),
		Slots: *slotCount,
		Store: st,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "log: "+format+"\n", args...)
		},
	})
	if err != nil {
		return err
	}

	switch args[0] {
	case "search":
		return runSearch(ctx, pool, args[1:])
	case "list":
		return runList(ctx, pool, args[1:])
	case "resolve":
		return runResolve(ctx, pool, args[1:])
	case "status":
		printSlots(pool.Status())
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

// runSearch 打印搜索结果；0 额度调用，但可能触发非播放换档。
func runSearch(ctx context.Context, pool *devices.Pool, args []string) error {
	if len(args) == 0 {
		return errors.New("用法: search <关键词>")
	}
	query := strings.Join(args, " ")
	var items []model.SeriesDTO
	err := pool.Do(ctx, "search", func(ctx context.Context, c *sdkx.Client) error {
		summaries, _, _, err := c.Search(ctx, query, sdkx.SearchOptions{})
		if err != nil {
			return err
		}
		items = sdkx.ToSeriesDTOs(summaries, time.Now().Unix())
		return nil
	})
	printSlots(pool.Status())
	if err != nil {
		return describe(err)
	}
	fmt.Printf("搜索结果 %d 条（关键词 %q）\n", len(items), query)
	for _, it := range items {
		fmt.Printf("  %-24s %-36s 集数=%d\n", it.ID, it.Title, it.EpisodeCount)
	}
	return nil
}

// runList 打印一部剧的分集列表。
func runList(ctx context.Context, pool *devices.Pool, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: list <seriesID>")
	}
	seriesID := args[0]
	var (
		series   model.SeriesDTO
		episodes []model.EpisodeDTO
	)
	err := pool.Do(ctx, "list", func(ctx context.Context, c *sdkx.Client) error {
		detail, err := c.GetSeries(ctx, seriesID)
		if err != nil {
			return err
		}
		series = sdkx.ToSeriesDetailDTO(detail, time.Now().Unix())
		eps, err := c.ListEpisodes(ctx, seriesID)
		if err != nil {
			return err
		}
		episodes = sdkx.ToEpisodeDTOs(eps)
		return nil
	})
	printSlots(pool.Status())
	if err != nil {
		return describe(err)
	}
	fmt.Printf("%s  %s\n", series.ID, series.Title)
	fmt.Printf("  状态=%s 付费=%s 平台=%d 集数=%d 多季=%v 季号=%d 合集=%s\n",
		series.Status, series.PayType, series.Platform, series.EpisodeCount,
		series.IsMultiSeason, series.SeasonIndex, emptyDash(series.AlbumID))
	if series.Platform == 0 {
		fmt.Println("  注意: platform=0 表示书 / 有声书等非视频内容，播放器不支持（方案 F13）")
	}
	fmt.Printf("  分集 %d 条：\n", len(episodes))
	for _, ep := range episodes {
		fmt.Printf("    第 %-4d 集  %-24s %4d 秒  可播=%-5v 新集=%-5v  %s\n",
			ep.Number, emptyDash(ep.ID), ep.DurationSec, ep.Playable, ep.IsNew, ep.Title)
	}
	return nil
}

// runResolve 解析一集播放模型并打印摘要 + 槽用量。这是 M0 的「连续解析 3 集」入口。
func runResolve(ctx context.Context, pool *devices.Pool, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: resolve <episodeID>")
	}
	episodeID := args[0]
	pb, err := pool.ResolvePlayback(ctx, episodeID)
	status := pool.Status()
	printSlots(status)
	if err != nil && !errors.Is(err, sdkx.ErrDRMProtected) {
		return describe(err)
	}
	info := sdkx.ToPlaybackInfo(pb, pool.Keys().Has(episodeID))
	fmt.Printf("播放模型 %s\n", info.EpisodeID)
	fmt.Printf("  加密=%v 时长=%d 秒 地址有效期=%s 独立音轨=%v 内存持密钥=%v 清晰度=%d 条\n",
		info.Protected, info.DurationSec, unixOrDash(info.ExpiresAt), info.HasAudio,
		info.KeyInMemory, len(info.Streams))
	for _, s := range info.Streams {
		fmt.Printf("    %-8s %-8s %-8s %dx%d %6d kbps  hls=%-5v dash=%-5v 加密=%v %s\n",
			emptyDash(s.Definition), emptyDash(s.Quality), emptyDash(s.Codec),
			s.Width, s.Height, s.Bitrate/1000, s.HLS, s.Dash, s.Protected, s.Encryption)
	}
	if errors.Is(err, sdkx.ErrDRMProtected) {
		fmt.Println("  说明: ErrDRMProtected 是加密集的正常返回（方案 F3），模型照常可用")
	}
	return nil
}

// printSlots 打印设备槽用量（方案 §11 M0：解析后必须能看到槽消耗）。
func printSlots(st model.DevicesStatus) {
	fmt.Printf("设备槽: healthy=%d cooling=%d exhausted=%d rejected=%d 今日已用=%d 非播放换档=%d\n",
		st.Healthy, st.Cooling, st.Exhausted, st.Rejected, st.UsedToday, st.NonPlaybackSwaps)
	for _, s := range st.Slots {
		fmt.Printf("  %s %-9s 用量=%-2d 额度日=%-8s 拒绝=%d 上次使用=%s\n",
			s.ID, s.State, s.QuotaUsed, zeroDash(s.QuotaDay),
			s.Rejects, unixOrDash(s.LastUsed))
	}
}

// describe 把错误翻译成「机器码 + 用户文案 + 原始错误」，不打印任何密钥材料。
func describe(err error) error {
	code, msg, retryable := sdkx.Classify(err)
	return fmt.Errorf("[%s] %s（可重试=%v）原始错误: %v", code, msg, retryable, err)
}

func unixOrDash(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

func zeroDash(v int) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}

func emptyDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
