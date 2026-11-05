# hongguo-pc

红果短剧的第三方桌面播放器（非官方），Golang + Wails v3。

- 上游 SDK：[`github.com/beilunyang/openhongguo`](https://github.com/beilunyang/openhongguo)（匿名设备读取，Apache-2.0）
- 桌面底座：[Wails v3](https://v3.wails.io/)（Go 1.26+，Windows 走 WebView2）
- 本仓库只做「客户端 + 播放器」这一层：目录浏览、设备池、播放与本地化播放链路。

> 本项目**不是**红果官方产品，与字节跳动 / 番茄 / 红果无任何关联。

## 当前状态

仓库仍在建设中，下面按「已落地代码」和「未落地」分开写，避免误解。

### 已有代码

| 目录 | 内容 |
|---|---|
| `internal/sdkx` | SDK 适配层：构造客户端、错误归一、目录读取、播放模型取用（唯一允许碰 `openhongguo` 的地方） |
| `internal/devices` | 本地匿名设备池：多档案、单飞重置、冷却与额度记账 |
| `internal/store` | SQLite 存储（`modernc.org/sqlite`，纯 Go 实现，不需要 CGO） |
| `internal/model` | 跨层 DTO 与枚举 |
| `cmd/probe` | 调研用探测 CLI（**不是**用户入口，只用于验证上游行为） |

### 尚未落地

- `cmd/app`：应用主入口（Wails v3 Application/Window 组装）——建设中，CI 首跑前必须先有它。
- `frontend/`：Vite + TypeScript + React 界面——建设中。
- 播放链路：ffmpeg 解密、本地 HTTP Range 服务、`<video>` 播放、字幕与进度。
- 分发形态：NSIS 安装包、代码签名、自动更新（本期不做）。

完整的架构、风险与决策依据见 [`docs/hongguo-player-plan.md`](docs/hongguo-player-plan.md)。

## 运行依赖

程序本身不打包媒体工具链，下列依赖需要用户自备或由系统提供。

| 平台 | 依赖 | 说明 |
|---|---|---|
| Windows 10/11 | **WebView2 Runtime**（Evergreen） | Windows 11 通常已自带；缺失时按 [Microsoft 官方文档](https://learn.microsoft.com/microsoft-edge/webview2/) 安装。程序不静默安装运行时。 |
| Linux | **GTK4 + WebKitGTK 6.0** 运行时 | Debian/Ubuntu：`libgtk-4-1 libwebkitgtk-6.0-4`。本项目基于 Wails v3.1，默认就是 GTK4/WebKitGTK 6.0 路径，**不提供**旧的 GTK3/WebKit2GTK 4.1 构建（v3.1 已移除 `-tags gtk3`）。发行二进制在 Ubuntu 24.04 上构建，更老的发行版未验证。 |
| macOS | macOS 11+ | 见下方「macOS 未签名说明」。 |
| 所有平台 | **ffmpeg**（必需，自备） | 用于解密 `cenc-aes-ctr` 分集与本地转封装。仓库**不附带**、不下载、也不再分发 ffmpeg。用户本机自行安装的 ffmpeg 若是 GPLv3 构建，**该二进制不可由本项目再分发**；本项目只调用用户已安装的可执行文件。 |

## 构建

### 本地

前置：Go 1.26+、Node.js 24+；可选 `wails3`（`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`）与 `task`。

```powershell
# Windows（PowerShell）
pwsh -File scripts/build.ps1                 # 产物 bin/hongguo-pc.exe
pwsh -File scripts/build.ps1 -GOOS windows -GOARCH arm64
```

```bash
# Linux / macOS
bash scripts/build.sh                        # 产物 bin/hongguo-pc
GOOS=darwin GOARCH=arm64 bash scripts/build.sh --app   # macOS 额外产出 .app
bash scripts/package.sh                      # 打包到 dist/
```

`Taskfile.yml` 只是入口包装（`task build` / `task package`），逻辑都在 `scripts/` 下，避免两处维护。
若仓库后续接入 wails3 的标准骨架（`build/Taskfile.yml`），构建会自动优先走 `wails3 build` / `wails3 package`，本仓库的脚本退为兜底路径。

### CI / 发布

推一个 `v*` 标签即触发 `.github/workflows/release.yml`：

```
git tag v0.1.0 && git push origin v0.1.0
```

流水线结构：

1. 六个构建作业（linux amd64/arm64、windows amd64/arm64、macOS arm64/amd64）并行构建并 `upload-artifact`；
2. 一个发布作业汇总全部产物，生成 `SHA256SUMS.txt`，调一次 `softprops/action-gh-release` 上传。

「六个作业直接各自上传同一个 Release」会互相踩（并发创建同一 Release 会 422），所以采用上面的单点上传结构。

产物命名：

| 目标 | 文件 |
|---|---|
| linux/amd64 | `hongguo-pc_linux_amd64.tar.gz` |
| linux/arm64 | `hongguo-pc_linux_arm64.tar.gz` |
| windows/amd64 | `hongguo-pc_windows_amd64.zip` |
| windows/arm64 | `hongguo-pc_windows_arm64.zip` |
| macOS arm64 | `hongguo-pc_macos_arm64.zip`（含 `.app`） |
| macOS amd64 | `hongguo-pc_macos_amd64.zip`（含 `.app`） |

runner 标签：`ubuntu-24.04`、`ubuntu-24.04-arm`、`windows-latest`、`windows-11-arm`、`macos-26`。
macOS 13 的镜像已不存在，macOS 14 的镜像自 2026-11-02 起从 GitHub Actions 退役，两者都已不可用。

## macOS 未签名说明

本项目的 macOS 发行包**没有代码签名，也没有经过 Apple 公证**。

事实与影响：

- macOS 的 Gatekeeper 对未签名/未公证的应用会在首次打开时阻止运行，用户看到的是「无法验证开发者」一类的提示。
- 这属于预期行为，不是构建失败。
- 是否信任并运行一个未签名应用，由用户自行判断；本项目不提供、也不建议任何绕过系统安全机制的步骤。
- Apple 官方说明：<https://support.apple.com/guide/mac-help/mh40616/mac>

若要正规分发，正确做法是购买 Apple Developer ID 证书，对 `.app` 签名并提交公证（notarization）。本期未做。

## 已知限制

- 上游 SDK 未发布 1.0，次版本可能不兼容；本项目锁版本（`v0.2.0`），所有上游调用都收在 `internal/sdkx`。
- 匿名设备额度有限：每台设备大致只能换取一次播放模型。本项目用多份本地匿名设备档案分摊，具体表现随上游策略变化。
- 解密链路在 Windows 上没有上游实测记录（上游项目自述 Windows 仅交叉编译），首次真实播放需要本项目自己验证。
- 发行包不含 ffmpeg，首次运行会做环境自检并提示用户指定路径。

## 免责声明

本仓库仅供**个人学习与技术研究**使用。

- 本方案**以多份本地匿名设备档案分摊单台设备的播放额度**。
- 明确**不做**：不注册账号、不伪造付费凭证、不破解内容、不去水印、不批量抓取与再分发、不实现 DRM 规避。
- **该做法是否违反上游 ToS，由使用者自行判断并承担后果。**
- 本项目不提供任何内容，也不对内容的合法性、可用性作任何承诺；请在研究完成后及时删除相关内容。
- 请勿将本项目用于商业用途，或用于侵犯他人合法权益、违反当地法律法规的场景。

### 关于内容密钥

本项目**不含任何内容密钥**：密钥由上游 SDK 在本地从服务端下发的播放模型即时推导（`sdk.ContentKey`），只在进程内存中短暂存活，不写入磁盘、不写入日志、不发送给前端；仓库与发行包中不存在任何密钥材料。解密所需的 `ffmpeg` 由用户本机自带。

## 许可证

本项目以 [MIT 许可证](LICENSE) 发布。

上游 SDK `github.com/beilunyang/openhongguo` 使用 Apache License 2.0，作为独立依赖被引用，与本项目的 MIT 许可不冲突。本项目**不包含**上游源码，也不代表上游作者的立场或背书。
