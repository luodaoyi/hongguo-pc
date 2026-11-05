# 根因报告：编译产物 EXE 双击无反应、无图标

- 仓库：`D:\sources\repos-new\gh\ai\ai-app\hongguo-pc`
- 版本：分支 `main`，HEAD `833418f`，tag `v0.1.0`
- 环境：Windows 11 专业工作站版 build 26200；go1.26.2；Wails v3.1.0；WebView2 Runtime 156.0.4314.58
- 取证时间：2026-11-05 14:53–14:56（+08:00）
- 取证方式：只读 + 实测启动；**未修改任何源码、未改配置、未提交、未发布**
- 写入范围：本文件（`docs/rootcause-exe-not-launch.md`）一个文件

---

## 0. 结论摘要

1. **主因（已实测复现）**：程序启动时按**相对当前工作目录**解析前端资源目录 `frontend/dist`（`cmd/app/main.go:36`、`cmd/app/main.go:256`），而该目录**从未随发布包分发**（`scripts/package.sh:42-52` 只收 `bin/*.exe`；`frontend/dist/` 被 `.gitignore:18` 忽略）。找不到即 `return err`（`cmd/app/main.go:260-263`），`main()` 打到 stderr 后 `os.Exit(1)`（`cmd/app/main.go:39-42`）；发布版链接了 `-H windowsgui`（`scripts/build.sh:82`、`scripts/build.ps1:68`），**没有控制台承接 stderr**，于是用户看到的是「双击毫无反应」。
2. **次因（已实测）**：两份 EXE **都没有 `.rsrc` 节**，即无 `RT_ICON` / `RT_GROUP_ICON` / `RT_MANIFEST` / `VERSIONINFO`。这是「连个 icon 都没有」的直接原因：资源管理器只能显示系统默认空白图标。成因是构建走裸 `go build`，没有 wails 生成的 `.syso`（仓库无 `build/` 目录，无 `*.syso` / `*.ico` / `*.manifest`）。
3. **排除项（已实测）**：WebView2 Runtime 缺失、缺 manifest 阻止起窗、缺 `WebView2Loader.dll` —— 三项**都不是**本次打不开的原因。同一份用户下载的 EXE 放进有 `frontend/dist` 的目录即可正常起窗（实测窗口标题「红果短剧」）。

一句话：**二进制是好的，坏的是「资源怎么找、资源怎么发」和「图标根本不存在」。**

---

## 1. 实测启动矩阵

统一方法：`Start-Process -PassThru -RedirectStandardError err.txt -RedirectStandardOutput out.txt`，等待 5 秒后检查进程存活与 `MainWindowTitle`。

| # | 被测二进制 | 工作目录 CWD | 结果 | 退出码 | 关键输出 |
|---|---|---|---|---|---|
| A | `bin\hongguo-pc.exe`（本地构建） | 仓库根（有 `frontend/dist`） | **存活，起窗成功** | 运行中被强杀 | `MainWindowTitle=[红果短剧]`，`Responding=True`，`[WebView2] Environment created successfully` |
| B | `bin\hongguo-pc.exe` 副本 | `%TEMP%\hgtest`（目录内只有 exe） | 5 秒内退出 | **1** | `前端资源目录不存在: C:\Users\...\Temp\hgtest\frontend\dist` |
| C1 | **Release 解压出的 exe**（用户实际下载的那份） | `%TEMP%\hgrel_clean`（目录内只有 exe） | 5 秒内退出 | **1** | `前端资源目录不存在: C:\Users\...\Temp\hgrel_clean\frontend\dist` |
| C2 | **同一份 Release exe** | 仓库根（有 `frontend/dist`） | **存活，起窗成功** | 运行中被强杀 | `MainWindowTitle=[红果短剧]`，`Responding=True` |
| C3 | Release exe，**不重定向**（等价双击） | `%TEMP%\hgrel_clean2` | 5 秒内退出 | **1** | 无控制台、无对话框、目录内未产生任何日志文件 |
| D | `bin\hongguo-pc.exe`（模拟双击 bin 目录里的 exe） | `bin\` | 5 秒内退出 | **1** | `前端资源目录不存在: D:\...\hongguo-pc\bin\frontend\dist` |

### 1.1 A 的完整 stderr（成功场景基线）

```
hongguo-pc: 2026/11/05 14:53:12 已清理上次会话的临时文件 1 个
hongguo-pc: 2026/11/05 14:53:12 ffmpeg: C:\Users\tb16p\AppData\Local\Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-9.0.2-full_build\bin\ffmpeg.exe (ffmpeg version 9.0.2-full_build-www.gyan.dev ...)
Nov  5 14:53:12.743 INF Build Info: Wails=v3.1.0 Compiler=go1.26.2 GOAMD64=v1 vcs=git vcs.modified=true -buildmode=exe -compiler=gc CGO_ENABLED=0 GOARCH=amd64 GOOS=windows
Nov  5 14:53:12.744 INF Platform Info: Go-WebView2Loader=true WebView2=156.0.4314.58 ID=25H2 Name=Windows 10 Pro for Workstations Version=2009 (Build: 26200) Branding=Windows 11 专业工作站版
Nov  5 14:53:12.744 INF AssetServer Info: middleware=true handler=true
2026/11/05 14:53:12 [WebView2] Environment created successfully
```

> 注意 `Go-WebView2Loader=true`：WebView2 的 loader 是 Go 实现、编译进二进制，**不需要外置 `WebView2Loader.dll`**。

### 1.2 B / C1 / D 的完整 stderr（失败场景，三者同构）

```
hongguo-pc: 2026/11/05 14:54:38 已清理上次会话的临时文件 1 个
hongguo-pc: 2026/11/05 14:54:38 ffmpeg: C:\Users\tb16p\...\ffmpeg.exe (ffmpeg version 9.0.2-full_build-www.gyan.dev ...)
hongguo-pc: 前端资源目录不存在: C:\Users\tb16p\AppData\Local\Temp\hgrel_clean\frontend\dist
请先在 frontend 目录执行 `npm ci && npm run build` 生成 dist（或用 wails3 build 打包）
```

三例的错误路径分别落到 `...\Temp\hgtest\frontend\dist`、`...\Temp\hgrel_clean\frontend\dist`、`...\hongguo-pc\bin\frontend\dist` —— **路径随 CWD 漂移**，这是「相对工作目录」的直接实证。

### 1.3 失败的临界点

DB、设备池、本地服务、ffmpeg 探测**全部成功**（stderr 已打出 ffmpeg 版本），失败发生在第 10 步「前端资源」（`cmd/app/main.go:167-171`）。也就是说：除了前端资源这一步，其余启动链路在本机是通的。

---

## 2. 代码链路（行号锚定）

| 位置 | 内容 | 作用 |
|---|---|---|
| `cmd/app/main.go:35-36` | `// distDir 是前端构建产物目录（相对工作目录…）` / `const distDir = "frontend/dist"` | 硬编码相对路径，注释自己写明了「相对工作目录」 |
| `cmd/app/main.go:255-256` | `func distHandler(dir string)` / `abs, err := filepath.Abs(dir)` | `filepath.Abs` 以**进程 CWD** 为基准展开 |
| `cmd/app/main.go:260-263` | `os.Stat(abs)` 失败 → `return nil, fmt.Errorf("前端资源目录不存在: %s\n请先在 frontend 目录执行 …")` | 目录缺失即返回错误 |
| `cmd/app/main.go:264-266` | 再检查 `index.html`，缺失同样报错 | 半残目录也会拒启 |
| `cmd/app/main.go:167-171` | `assets, err := distHandler(distDir); if err != nil { return err }` | 该错误发生在新窗口创建（`:190`）**之前**，所以连窗口都不会出现 |
| `cmd/app/main.go:38-43` | `main()` → `fmt.Fprintln(os.Stderr, "hongguo-pc:", err)` → `os.Exit(1)` | 错误只进 stderr |
| `cmd/app/main.go:267` | `http.FileServer(http.Dir(abs))` | 前端资源用文件服务器而非 `embed`，所以必须外部目录存在 |
| `scripts/build.sh:82` | `windows) EXT=".exe"; LDFLAGS="-s -w -H windowsgui"` | CI 走这条：Windows 产物无控制台 |
| `scripts/build.ps1:68` | `$ldflags = if ($GOOS -eq 'windows') { '-s -w -H windowsgui' } …` | 本地脚本同样无控制台 |
| `scripts/package.sh:42-52` | windows 分支：`7z a -tzip "$ARTIFACT" "./$OUT_DIR"/*.exe` / `zip -r … ./*.exe` / `Compress-Archive -Path '$OUT_DIR/*.exe'` | **只把 exe 打进包** |
| `.gitignore:18` | `frontend/dist/` | dist 不入库 |
| `.github/workflows/release.yml:116-122` | `Build frontend` → `npm run build` → `test -d dist` | CI **确实**构建了 dist |
| `.github/workflows/release.yml:124-147` | `Build` → 无 `build/Taskfile.yml` → `bash scripts/build.sh` | 走裸 Go 路径，不生成 syso |
| `.github/workflows/release.yml:149-155` | `Package` → `bash scripts/package.sh` | 此步把 dist 丢掉 |
| `README.md:34-43` | 「运行依赖」表：WebView2 / GTK4 / macOS / ffmpeg | **完全没有提「需要 frontend/dist」** |

链路一句话：CI 建了 dist（`release.yml:116-122`）→ 打包只取 exe（`package.sh:42-52`）→ 用户解压只得到 exe → 运行期按 CWD 找 dist（`main.go:36`、`main.go:256`）→ 找不到即退出 1（`main.go:260-263`）→ 无控制台看不见（`build.sh:82`）。

---

## 3. 发布包核对（GitHub Release v0.1.0）

来源：`GET https://api.github.com/repos/luodaoyi/hongguo-pc/releases/tags/v0.1.0`（HTTP 200，release id `434851708`，`published_at: 2026-11-05T06:26:56Z`）。

| 资产 | size（字节） | digest sha256（前 16 位） |
|---|---|---|
| `hongguo-pc_windows_amd64.zip` | 6,788,809 | `961810c6f6821547` |
| `hongguo-pc_windows_arm64.zip` | 6,182,590 | `e13688e5a089240c` |
| `hongguo-pc_linux_amd64.tar.gz` | 7,030,952 | `c3eb8a8801d784e8` |
| `hongguo-pc_linux_arm64.tar.gz` | 6,496,033 | `0fd084ca02540e54` |
| `hongguo-pc_macos_amd64.zip` | 6,664,908 | `96d1b6c00ec96d13` |
| `hongguo-pc_macos_arm64.zip` | 6,292,565 | `6c7d9265dd7363ea` |
| `SHA256SUMS.txt` | 580 | `8e4c0f2791014ae7` |

### 3.1 实际下载并解压 `hongguo-pc_windows_amd64.zip` 的内容清单

```
下载: 6,788,809 字节
本地复算 SHA256 = 961810C6F6821547F87E543A1980474763C62170E2A33E83EEF0497F507DC98A  （与 API digest 一致）
zip 条目:
  hongguo-pc.exe   17217536（未压缩）  6788647（压缩后）
```

**包内只有 1 个文件：`hongguo-pc.exe`。** 无 `frontend/dist`、无 `.ico`、无 `WebView2Loader.dll`、无任何说明文件。

### 3.2 用户解压双击后的实际结果

见第 1 节 C1 / C3：**进程退出码 1，不出现窗口，不弹任何对话框，不留任何日志**。用户视角就是「点了没反应」。同时因为 EXE 无图标资源（第 4 节），文件夹里显示的是系统默认空白图标 —— 与用户原话「连个 icon 都没有」吻合。

---

## 4. PE 资源核对

方法：PowerShell 解析 PE 头（COSF/可选头 Subsystem，`PE+24+68`）与节表，定位 `.rsrc` 并遍历资源目录第一层（`IMAGE_RESOURCE_DIRECTORY`）的 Type 项。

| 项 | 本地 `bin\hongguo-pc.exe` | Release `hongguo-pc.exe` |
|---|---|---|
| 文件大小 | 25,512,960 | 17,217,536 |
| Machine | 0x8664（x64） | 0x8664（x64） |
| OptMagic | 0x020B（PE32+） | 0x020B（PE32+） |
| **Subsystem** | **WINDOWS_CUI**（无 `-H windowsgui`） | **WINDOWS_GUI** |
| 节表 | `.text .rdata .data .pdata .xdata /4 /19 /32 /46 /65 /78 /95 /112 .idata .reloc .symtab` | `.text .rdata .data .pdata .xdata .idata .reloc .symtab` |
| **`.rsrc` 节** | **无** | **无** |
| **RT_ICON / RT_GROUP_ICON** | **无** | **无** |
| **RT_MANIFEST** | **无** | **无** |
| **VERSIONINFO（RT_VERSION）** | **无** | **无** |

结论：**「无」+ 上表证据**。两份产物都是零资源 PE，所以：

- 资源管理器 / 任务栏 / 快捷方式**取不到图标**，显示系统默认空白图标 → 「连个 icon 都没有」；
- 无 `RT_MANIFEST` → 无 `requestedExecutionLevel`、无 `Microsoft.Windows.Common-Controls` v6、无 `dpiAware` 声明；
- 无 `RT_VERSION` → 属性页「详细信息」里没有版本号/公司/产品名；
- 无图标时 wails 只能用内置默认值 —— 但**这不阻止起窗**（C2 已实测）。

### 4.1 为什么是零资源

`go build` 默认不产生资源，Windows 资源要靠 `.syso`（Go 链接器会把同包目录下的 `*.syso` 一起链接）。wails v3 的 syso 生成是 **CLI 步骤**，不在 `pkg/application` 里自动发生：

- `E:\data\golang\gopath\pkg\mod\github.com\wailsapp\wails\v3@v3.1.0\internal\commands\manifest_pipeline.go:1337`
  → `GenerateSyso(&SysoOptions{Manifest: .../wails.exe.manifest, Info: .../info.json, Icon: .../icon.ico, Out: .../xxx.syso, Arch: s.TargetArch})`
- wails 官方示例每个都带资源：`...\v3@v3.1.0\examples\build\assets\icon.ico`、`...\examples\build\assets\wails.exe.manifest` 等（模块缓存实测存在）。

本仓库实测：

```
*.syso     : 无
*.ico      : 无
*.manifest : 无
*.rc       : 无
build/     : 不存在
git ls-files 中 syso/build/*.ico/manifest/(wails 相关) : 仅 frontend/bindings 下的两个 wails ts/d.ts 文件
```

且 `release.yml:133` 的条件 `if [ -f build/Taskfile.yml ] && command -v task` 在本仓库**不成立**（无 `build/`），永远走 `scripts/build.sh` 的裸 `go build` 分支 → 永远不会生成 syso。这与 `README.md:65`「若仓库后续接入 wails3 的标准骨架…」的自述一致：**骨架没接入**。

---

## 5. WebView2 Runtime 核对

```
FOUND   HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}
        pv   = 156.0.4314.58
        name = Microsoft Edge WebView2 Runtime
MISSING HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}
MISSING HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}

卸载表：Microsoft Edge WebView2 Runtime  156.0.4314.58
系统  ：Microsoft Windows 11 专业工作站版 build 26200
```

**判定：本机已安装 WebView2 Runtime（156.0.4314.58），不是本次打不开的原因。**

三重佐证：注册表 `pv` 存在；实验 A/C2 的 stderr 打出 `[WebView2] Environment created successfully` 与 `Platform Info: … WebView2=156.0.4314.58`；同一台机器上 A/C2 确实起了窗。

---

## 6. 两问裁决

### (a) 主因是否就是「frontend/dist 未随包分发 + 相对 CWD + windowsgui 吞掉错误」

**是，三个环节缺一不可，且全部实测坐实：**

| 环节 | 证据 |
|---|---|
| dist 未随包分发 | zip 只有 1 个条目 `hongguo-pc.exe`（第 3.1 节）；`scripts/package.sh:42-52` 只收 `*.exe`；`.gitignore:18` 忽略 `frontend/dist/` |
| 相对 CWD 解析 | `cmd/app/main.go:36` + `:256`；B/C1/D 三例错误路径随 CWD 漂移 |
| 缺失即硬退 | `cmd/app/main.go:260-263`、`:167-171`、`:39-42`，退出码实测 = 1 |
| 错误不可见 | `scripts/build.sh:82`、`scripts/build.ps1:68` 的 `-H windowsgui`；C3 实测无控制台、无对话框、无日志 |

补充一点必须说明的**边界**：`frontend/dist` 存在、CWD 正确时，程序完全正常（A/C2）。所以这不是「打包漏了个文件」这么轻的问题 —— 它是**几十 MB 的前端产物被设计成运行期从外部目录读取，但发布流程从来没有把它交付出去**。即便用户手工把 dist 补到 exe 旁边，也仍然要问「哪个目录算 CWD」：双击 EXE 时 CWD 是 EXE 所在目录，双击快捷方式时可能是别的目录。

### (b) 缺 manifest / 缺 wails 生成的 syso 是否也会阻止起窗

**不会阻止起窗。** 理由与证据：

1. **直接实测**：C2 用的就是那份**零资源、无 manifest、无图标**的 Release exe，放在有 dist 的目录后**成功起窗**（`MainWindowTitle=[红果短剧]`，`Responding=True`，`[WebView2] Environment created successfully`）。这是最硬的证据 —— 无需推测。
2. **WebView2Loader.dll 不需要外置**：A/C2 的 stderr 明确 `Go-WebView2Loader=true`；对应源码 `...\v3@v3.1.0\pkg\application\application_windows.go:460`（`args = append(args, "Go-WebView2Loader", webviewloader.UsingGoWebview2Loader)`）与 `:481`；实现位于 `...\v3@v3.1.0\internal\webview2\webviewloader\`。包内不含该 DLL 也不影响启动。
3. **manifest 不是 WebView2 的启动前提**：manifest 声明的是 UAC 级别、Common-Controls 版本、DPI 感知，属于进程**外观/兼容性**属性，不是加载 WebView2 的条件。缺失的实际代价是：高 DPI 下可能模糊、系统控件可能退化为旧视觉样式、无法声明 `requestedExecutionLevel`。

**因此归类**：缺 syso/图标/manifest 是**次因（外观与体验缺陷）**，与「打不开」是两个独立缺陷，不能合并成一句根因，也**不能**用「补 icon」来掩盖启动失败。

---

## 7. 根因排序表

| 级别 | 结论 | 关键证据 | 影响 |
|---|---|---|---|
| **主因** | 前端资源 `frontend/dist` 未随发布包分发，且运行期按相对 CWD 解析，缺失即 `exit 1`；`windowsgui` 让错误完全不可见 | zip 仅含 `hongguo-pc.exe`；`main.go:36/256/260-263/167-171/39-42`；`package.sh:42-52`；`build.sh:82`；实验 B/C1/C3/D 全部 `ExitCode=1`，C2 起窗成功 | EXE 在所有「正常双击」场景下都打不开，且不告知原因 |
| **次因 1** | 产物零 PE 资源（无 RT_ICON / RT_GROUP_ICON / RT_VERSION / RT_MANIFEST）：构建走裸 `go build`，未走 `wails3 GenerateSyso` | 两份 exe 均无 `.rsrc` 节（第 4 节）；仓库无 `build/`、无 `*.syso/*.ico/*.manifest`；`manifest_pipeline.go:1337`；`release.yml:133` 条件不成立 | 「连个 icon 都没有」的直接原因；无版本信息；DPI/控件样式未声明 |
| **次因 2**（需 Lead 裁断，见第 8 节） | 本地 `bin\hongguo-pc.exe` 与 Release exe **子系统不一致**：CUI vs GUI，体积 25.5MB vs 17.2MB | PE 头 `Subsystem` 实测；`build.ps1:68`／`build.sh:82` 都写的是 `windowsgui`，说明本地 bin 不是这两个脚本的产物 | 「用户看到的是控制台报错还是静默失败」取决于拿的是哪份产物，取证结论容易被误读 |
| **排除项** | WebView2 Runtime 缺失 | HKLM `pv=156.0.4314.58`；`Environment created successfully` | 与本故障无关 |
| **排除项** | 缺 manifest / 缺 syso 阻止起窗 | C2 实测零资源 exe 起窗成功 | 与本故障无关 |
| **排除项** | 缺外置 `WebView2Loader.dll` | `Go-WebView2Loader=true`；`application_windows.go:460/481` | 与本故障无关 |
| **排除项** | 数据库 / 设备池 / 本地服务 / ffmpeg 探测 / Wails 绑定层故障 | 失败前 stderr 已完成这些步骤并打出 ffmpeg 版本；A/C2 全链路起窗成功 | 与本故障无关 |
| **排除项** | 平台不兼容（架构/OS 版本） | x64 PE32+；Windows 11 build 26200 上起窗成功 | 与本故障无关 |

---

## 8. 需要 Lead 裁断的一处证据矛盾

`bin\hongguo-pc.exe` 的 PE `Subsystem` 是 **WINDOWS_CUI**，但 `scripts/build.ps1:68` 与 `scripts/build.sh:82` 都写死了 `-H windowsgui`（应产出 GUI 子系统）。同时体积 25,512,960 明显大于 Release 的 17,217,536 —— `-s -w` 会 strip 掉符号表（两份都还留着 `.symtab`，但本地那份没有任何 `-ldflags` 生效的痕迹：CUI + 更大体积）。

**推断（未验证）**：本地 `bin\hongguo-pc.exe` 是某次**未经 `scripts/build.ps1`** 的直接 `go build`（例如 IDE/手动命令）留下的旧产物，不是脚本产物，也不等于 Release 那份。

这条不确定性的实际影响：如果复现时用的是本地 CUI 产物，双击会**弹控制台并打印中文错误**；如果用 Release 的 GUI 产物，则**完全静默**。用户原话是「根本打不开，连个 icon 都没有」，更接近 Release 那份的行为。**建议 Lead 在对外结论里明确「以 Release 的 `hongguo-pc.exe` 为准」，不要把本地 bin 的行为混进来。**

---

## 9. 修复方向（仅列方向，本次未实施）

按缺陷独立性分三组，**不要合并成一次改动**：

1. **启动可用性（对应主因）**：前端产物改为随二进制分发（`go:embed`）或随包分发并把资源路径锚定到 EXE 自身目录（而不是 CWD）；并在无控制台时提供可见的失败反馈（MessageBox / 日志文件 / 降级窗口）。
2. **打包完整性**：`scripts/package.sh` 的 windows 分支要么把 dist 打进包，要么在 embed 后不再需要 dist；CI 需加一条「解压后从干净目录启动」的冒烟校验，否则同一缺陷会复发。
3. **产物外观（对应次因 1）**：接入 wails3 骨架（`build/windows/icon.ico` + `wails.exe.manifest`）或用 `rsrc`/`GenerateSyso` 生成 `.syso`；并在发布流程里保留 `-H windowsgui`（需要先做好第 1 组的可见错误反馈，否则错误依旧被吞）。

---

## 10. 复现命令清单

```powershell
# 1. 仓库根目录启动（应成功起窗，窗口标题「红果短剧」）
Start-Process -FilePath .\bin\hongguo-pc.exe -WorkingDirectory (Get-Location) -PassThru `
  -RedirectStandardError $env:TEMP\e.txt -RedirectStandardOutput $env:TEMP\o.txt

# 2. 干净目录启动（应 ExitCode=1，stderr 报「前端资源目录不存在: <CWD>\frontend\dist」）
$d = "$env:TEMP\hgtest"; New-Item -ItemType Directory -Force $d | Out-Null
Copy-Item .\bin\hongguo-pc.exe $d
$p = Start-Process -FilePath "$d\hongguo-pc.exe" -WorkingDirectory $d -PassThru `
  -RedirectStandardError "$d\err.txt" -RedirectStandardOutput "$d\out.txt"
Start-Sleep 5; if ($p.HasExited) { "ExitCode=$($p.ExitCode)" } else { "仍在运行" }
Get-Content "$d\err.txt"

# 3. Release 产物核对
Invoke-WebRequest "https://github.com/luodaoyi/hongguo-pc/releases/download/v0.1.0/hongguo-pc_windows_amd64.zip" -OutFile "$env:TEMP\rel.zip"
Expand-Archive "$env:TEMP\rel.zip" -DestinationPath "$env:TEMP\rel" -Force
Get-ChildItem "$env:TEMP\rel" -Recurse        # 只有 hongguo-pc.exe

# 4. PE 子系统与资源（无 .rsrc 节即无 icon / manifest / 版本信息）
#    见本报告第 4 节脚本：读 0x3C 处 e_lfanew，Subsystem 在 PE+24+68，节表在 PE+24+SizeOfOptionalHeader

# 5. WebView2 Runtime
Get-ItemProperty "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" | Select-Object pv,name
```

---

## 11. 风险与未验证项

**已实测、可复现**（第 1／3／4／5 节全部）：A、B、C1、C2、C3、D 六个启动实验；Release zip 下载与条目清单、SHA256 复算；两份 EXE 的 PE Subsystem 与节表/资源类型；WebView2 注册表 `pv`；仓库资源文件清单；`.gitignore` 与 `git ls-files` 核对。

**未验证 / 推断**：

1. **用户实际的操作路径未验证**：不知道用户解压到哪个目录、是否双击 `bin\` 里的本地产物、是否用了快捷方式。本报告按「下载 zip → 解压 → 双击 exe」（C1/C3）这一最可能路径复现，并用 A/C2/D 覆盖了其它 CWD 情况。
2. **Linux / macOS 包是否同样中招：未验证**（未在对应平台实测）。按 `scripts/package.sh:35`（`tar -czf "$ARTIFACT" -C "$OUT_DIR" .`）与 `:61-65`（只打 `.app`）看，包里同样只有二进制、没有 `frontend/dist`，**推断**同样会因缺 dist 退出；这是推断，不是实测结论。
3. **本地 bin exe 的真实来源：未验证**（第 8 节，仅推断为未经 `build.ps1` 的直接 `go build`）。
4. **缺 manifest 的运行时副作用（DPI 模糊、控件旧样式）未实测**：本机没有做高 DPI / 视觉样式对比截图，只确认「不阻止起窗」。
5. **图标缺失在运行期的具体表现未实测**：只确认 PE 无 `RT_ICON`／`RT_GROUP_ICON`；任务栏与窗口标题栏运行时是否由 wails 内置默认图填充，未做截图核对。
6. **副作用声明**：为了让 EXE 真实跑到失败点，测试过程在 `%LOCALAPPDATA%\hongguo-pc\`（数据库、tmp 目录）产生了运行数据，并在 `%TEMP%` 下留了 `hgtest`／`hgrel`／`hgrel_clean`／`hgrel_clean2` 等取证目录。**源码与仓库状态未改动**（`git status` 仅剩既有的未跟踪 `.serena/` 与本报告文件）。
