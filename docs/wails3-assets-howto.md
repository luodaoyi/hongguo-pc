# Wails v3.1.0 Windows 资源操作手册（图标 / manifest / syso / 前端内嵌）

- 面向：工部主事（T2 修复：内嵌前端 + 图标 + 失败可见）
- 依据：本机实跑 `wails3`（PATH 中 `E:\data\golang\gopath\bin\wails3.exe`，`wails3 version` → `v3.1.0`）+ 模块缓存源码 `E:\data\golang\gopath\pkg\mod\github.com\wailsapp\wails\v3@v3.1.0`
- 取证时间：2026-11-05
- 写入范围：本文件（`docs/wails3-assets-howto.md`）。取证实验全部在 `%TEMP%` 完成，**未改动仓库任何源码、脚本或 build 目录**。

---

## 0. 六问结论速查

| # | 问题 | 结论 |
|---|---|---|
| 1 | 哪个命令生成 syso | **`wails3 generate syso`**（v3 里这个命令**仍然存在**，没改名）。`-manifest` 与 `-icon` **必填**，`-info` 可选，`-out` 可选（默认 `rsrc_windows_<arch>.syso`） |
| 2 | build/ 骨架最小必需文件 | **只做 syso 的话不需要 build/ 骨架**，只要 3 个输入文件：`icon.ico` + `wails.exe.manifest` +（可选）`info.json`。要走 `wails3 build` 才需要 `build/Taskfile.yml` 等 |
| 3 | syso 命名与放置 | 命名 `*_windows_<arch>.syso`；**必须放在 `main` 包所在目录**——本仓库是 **`cmd/app/`**，不是仓库根（wails 模板的 main 在根，所以官方 Taskfile 写的是根目录，照抄会失效） |
| 4 | **wails3 是否自动把 frontend/dist 嵌进 exe** | **不会。** 嵌入 100% 由**用户代码**的 `//go:embed all:frontend/dist` 完成；框架只提供 `AssetFileServerFS` 这个适配器。→ **我们自写 embed.FS 是必要的，R1 方向正确** |
| 5 | `-tags production embed` 如何合并 | wails 生产构建固定 `-tags production`，自定义 tag 通过 `wails3 build -tags xxx` 或 `EXTRA_TAGS=xxx` **逗号追加**成 `production,xxx`。但**embed 不需要 tag**（见 §5.2） |
| 6 | syso 是否自带 manifest | **不自带。** `-manifest` 是硬性必填（缺则报错退出）。但 wails 提供了标准 manifest 模板（asInvoker + perMonitorV2 DPI + Common-Controls v6），**照抄即可，不必自己写** |

---

## 1. Q1：生成 syso 的命令与最短可用序列

### 1.1 v2 → v3 命令对照（实测）

```
wails3 generate --help
  Available commands:
     build-assets           Generate build assets
     icons                  Generate icons
     syso                   Generate Windows .syso file     ← v3 仍有
     runtime / webview2bootstrapper / template / bindings / constants / .desktop / appimage
```

**结论**：v3 有 `wails3 generate syso`。同时新增了 `wails3 generate icons`（从 PNG 生成 .ico/.icns）与 `wails3 generate build-assets`（从 config.yml 生成 info.json/manifest 等）。

### 1.2 `generate syso` 完整参数（实测 `--help`）

```
wails3 generate syso - Generate Windows .syso file
Flags:
  -arch string      The target architecture
  -icon string      The icon file
  -info string      The info.json file
  -manifest string  The manifest file
  -out string       The output filename for the syso file
```

必填/可选由源码决定（`...\internal\commands\syso.go`）：

| 参数 | 必需性 | 源码行 | 缺失时的实测报错 |
|---|---|---|---|
| `-manifest` | **必填** | `syso.go:30-32` | `ERROR  manifest is required`，exit 1 |
| `-icon` | **必填** | `syso.go:33-35` | `ERROR  icon is required`，exit 1 |
| `-arch` | 必填（默认 `runtime.GOARCH`） | `syso.go:20-24`、`:37-45` | `ERROR  arch 'riscv64' not supported`，exit 1 |
| `-info` | 可选 | `syso.go:81`（`if options.Info != ""`） | 不给则**无 VERSIONINFO**（已实测，见 §3.3） |
| `-out` | 可选 | `syso.go:96-99` | 不给则默认 `rsrc_windows_<arch>.syso` |

支持架构仅三个（`syso.go:37-45`）：`amd64`、`arm64`、`386`。

### 1.3 `generate icons` 完整参数（实测 `--help`）

```
wails3 generate icons - Generate icons
Flags:
  -example                  Generate example icon file (appicon.png) in the current directory
  -input string             The input image file (default "build/appicon.png")
  -sizes string             The sizes to generate in .ico file (comma separated, 1-256 pixels) (default "256,128,64,48,32,16")
  -windowsfilename string   The output filename for the Windows icon (default "build/windows/icon.ico")
  -macfilename string       ... (default "build/darwin/icon.icns")
  -macassetdir / -iconcomposerinput ...   (macOS 专用，可忽略)
```

> 手上没有 PNG 时：`wails3 generate icons -example` 会在当前目录生成示例 `appicon.png`；或直接复制 wails 自带的 `...\internal\commands\build_assets\appicon.png`（31,495 字节）当占位图。

### 1.4 从零到有图标 exe 的最短命令序列（3 步，可复制）

```powershell
# 0) 目录准备（一次性）
New-Item -ItemType Directory -Force build\windows | Out-Null

# 1) PNG -> ICO（PNG 可从 wails 模块缓存复制占位，或自备 256x256 PNG）
Copy-Item "E:\data\golang\gopath\pkg\mod\github.com\wailsapp\wails\v3@v3.1.0\internal\commands\build_assets\appicon.png" build\appicon.png
wails3 generate icons -input build/appicon.png -windowsfilename build/windows/icon.ico

# 2) 写 manifest 与 info.json（内容见 §2.2 / §2.3，原样保存即可）

# 3) ICO + MANIFEST + INFO -> SYSO（关键：-out 指向 main 包目录 cmd/app/）
wails3 generate syso -arch amd64 `
  -icon build/windows/icon.ico `
  -manifest build/windows/wails.exe.manifest `
  -info build/windows/info.json `
  -out cmd/app/wails_windows_amd64.syso

# 4) 构建（syso 会被 Go 链接器自动带入，无需任何额外参数）
$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -trimpath -tags production -ldflags="-w -s -H windowsgui" -o bin/hongguo-pc.exe ./cmd/app
```

实测输出（`generate syso` 成功时**没有任何提示文字**，靠退出码判断）：

```
& wails3 generate syso -arch amd64 -icon <ico> -manifest <manifest> -out %TEMP%\...\wails_windows_amd64.syso
exit=0
Name                      Length
wails_windows_amd64.syso   23778
```

---

## 2. Q2：build/ 骨架与最小必需内容

### 2.1 两种路径，别混

| 路径 | 需要的文件 | 适用 |
|---|---|---|
| **A. 只要图标/manifest/版本信息**（推荐给 T2） | 只有 3 个输入：`icon.ico`、`wails.exe.manifest`、（可选）`info.json` + 一条命令 | 不引入 task 依赖、不改构建入口、CI 不动 |
| B. 全量接入 `wails3 build` | `build/Taskfile.yml`、`build/windows/Taskfile.yml`、`build/config.yml`、`build/appicon.png`、`build/windows/{icon.ico,wails.exe.manifest,info.json}`，并且要装 `task` | 后续想做 NSIS/MSIX 安装包时再考虑 |

**裁决建议：T2 走 A。** 理由：`wails3 build` 会把 `scripts/build.sh` / `build.ps1` 整条推倒重来，且引入 `task` 这个额外依赖；而图标/manifest 这件事，一条 `generate syso` 就够了。

### 2.2 最小 `build/windows/wails.exe.manifest`

来源：`...\internal\commands\updatable_build_assets\windows\wails.exe.manifest.tmpl:1-22`（`{{...}}` 是模板占位，替换成实际值即可）。

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly manifestVersion="1.0" xmlns="urn:schemas-microsoft-com:asm.v1" xmlns:asmv3="urn:schemas-microsoft-com:asm.v3">
    <assemblyIdentity type="win32" name="com.luodaoyi.hongguo-pc" version="0.1.0.0" processorArchitecture="*"/>
    <dependency>
        <dependentAssembly>
            <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
        </dependentAssembly>
    </dependency>
    <asmv3:application>
        <asmv3:windowsSettings>
            <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
            <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">permonitorv2,permonitor</dpiAwareness>
        </asmv3:windowsSettings>
    </asmv3:application>
    <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
        <security>
            <requestedPrivileges>
                <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
            </requestedPrivileges>
        </security>
    </trustInfo>
</assembly>
```

各段作用（对照模板行号）：

| 行 | 内容 | 作用 |
|---|---|---|
| `:3` | `<assemblyIdentity type="win32" name="…" version="…">` | 该二进制自身的程序集标识 |
| `:6` | `Microsoft.Windows.Common-Controls` v6.0.0.0 | 启用现代控件视觉样式（v5 会是 Win95 风） |
| `:11` | `dpiAware = true/pm` | Win7/8 的 DPI 回退声明 |
| `:12` | `dpiAwareness = permonitorv2,permonitor` | Win10+ 的 per-monitor v2 DPI，**显著减少高分屏模糊** |
| `:18` | `requestedExecutionLevel level="asInvoker"` | **不请求管理员权限**，避免每次启动弹 UAC |

### 2.3 最小 `build/windows/info.json`（决定属性页里的版本信息）

来源：`...\internal\commands\updatable_build_assets\windows\info.json.tmpl:1-17`。

```json
{
	"fixed": {
		"file_version": "0.1.0.0",
		"product_version": "0.1.0.0"
	},
	"info": {
		"0409": {
			"FileVersion": "0.1.0.0",
			"ProductVersion": "0.1.0.0",
			"CompanyName": "luodaoyi",
			"FileDescription": "红果短剧桌面播放器",
			"LegalCopyright": "Copyright © 2026 luodaoyi",
			"ProductName": "红果短剧",
			"Comments": "红果短剧桌面播放器"
		}
	}
}
```

> 版本号段数：**已实测**，`0.1.0.0`（四段）与 `0.1.0`（三段）**都能被接受**，`generate syso` 均 exit 0（产物分别 24,468 / 24,460 字节）。差别只是写进 VERSIONINFO 的字符串不同。**按 Windows 惯例写四段**（`a.b.c.d`），避免某些安装器/文件属性页对三段版本的解析差异。
>
> 本节的 manifest 与 info.json **已整段实测可用**：用这两个文件的内容原样跑 `generate syso`，exit 0，并把 syso 链接进 exe 后确认资源齐备（见 §3.3）。

### 2.4 若确实要走 `wails3 build`（路径 B）

`build/Taskfile.yml` 需 `includes` 平台 Taskfile；平台 Taskfile 依赖 common 的 `generate:icons`：

- `build_assets/windows/Taskfile.yml:3-4` → `includes: common: ../Taskfile.yml`
- `build_assets/Taskfile.tmpl.yml:193-204` → `generate:icons` 任务，`dir: build`，源 `appicon.png`，产物 `windows/icon.ico`
- `build_assets/config.yml:6-15` → `info:` 段（companyName/productName/productVersion/…），`wails3 generate build-assets` 用它生成 `info.json` 与 manifest

---

## 3. Q3：syso 的命名、放置与版本控制

### 3.1 命名规则

| 场景 | 名称 | 出处 |
|---|---|---|
| 不给 `-out` 的默认名 | `rsrc_windows_<arch>.syso` | `syso.go:96-99` |
| wails 官方 Taskfile 用名 | `wails_windows_<arch>.syso` | `build_assets/windows/Taskfile.yml:111` |
| 支持 arch | `amd64` / `arm64` / `386` | `syso.go:37-45` |

**命名里的 `_windows_<arch>` 不是随便写的**：Go 用文件名后缀施加隐式构建约束。带 `_windows_amd64` 的 syso 只在 windows/amd64 平台被链接；不带后缀的 syso 会在**所有平台**都尝试链接，交叉编译 Linux/macOS 时直接链接失败。

实测（`%TEMP%\syso-e2e`，三组对照）：

```
t1.exe  = main.go + rsrc_windows_amd64.syso
  Subsystem=WINDOWS_GUI
  .rsrc=有  类型: RT_ICON(6); RT_GROUP_ICON(1); RT_MANIFEST(1)
  size=1221120

t2.exe  = main.go（无 syso，对照组）
  .rsrc=无  => 无任何资源                        ← 与当前仓库 EXE 的状态完全一致

t3.exe  = main.go + myicon.syso（无 _windows_amd64 后缀）
  .rsrc=有  类型: RT_ICON(6); RT_GROUP_ICON(1); RT_MANIFEST(1)
```

两条可落地结论：
1. **syso 必须与 `main` 包源码在同一目录**，Go 才会链接它。t3 证明后缀并非「被链接的必要条件」，但跳过后缀会让其它平台构建失败 —— 所以**照规矩加后缀**。
2. `RT_ICON(6)` 的 6 个条目对应 `generate icons` 默认 `-sizes 256,128,64,48,32,16` 的 6 个尺寸。

### 3.2 放置位置：本仓库是 `cmd/app/`，不是仓库根（关键纠错）

wails 官方 Taskfile 写的是：

```yaml
# build_assets/windows/Taskfile.yml:107-113
  generate:syso:
    dir: build
    cmds:
      - wails3 generate syso -arch {{.ARCH}} -icon windows/icon.ico -manifest windows/wails.exe.manifest -info windows/info.json -out ../wails_windows_{{.ARCH}}.syso
```

`dir: build` + `-out ../` → 落在**项目根**。这能成立**只因为 wails 模板把 `main.go` 放在项目根**（见 `internal/templates/_common/main.go.tmpl`）。

**本仓库的 main 包在 `cmd/app/`（`cmd/app/main.go:9` `package main`）。照抄上面的 `-out` 会得到一个放在仓库根、永远不会被链接的 syso —— 图标不会出现，且没有任何报错。** 正确写法：

```powershell
wails3 generate syso -arch amd64 `
  -icon build/windows/icon.ico `
  -manifest build/windows/wails.exe.manifest `
  -info build/windows/info.json `
  -out cmd/app/wails_windows_amd64.syso
```

arm64 需另跑一次 `-arch arm64 -out cmd/app/wails_windows_arm64.syso`。

### 3.3 `-info` 与 VERSIONINFO

实测 t1 组没传 `-info`，结果资源里**只有** `RT_ICON / RT_GROUP_ICON / RT_MANIFEST`，**没有 `RT_VERSION`**。源码 `syso.go:81-94` 说明 VERSIONINFO 只在 `-info` 非空时才写入。

传了 `-info` 之后（用 §2.2 / §2.3 的文件内容原样），链接进 exe 实测：

```
$ go build -trimpath -ldflags="-w -s -H windowsgui" -o withinfo.exe .
build exit=0
Subsystem=WINDOWS_GUI
.rsrc 类型: RT_ICON(6); RT_GROUP_ICON(1); RT_VERSION(1); RT_MANIFEST(1)
  字符串 '红果短剧' 在资源中: True
  字符串 '0.1.0.0' 在资源中: True
  字符串 'luodaoyi' 在资源中: True
  字符串 'Copyright' 在资源中: True
```

→ 「属性页 → 详细信息」里要有版本号/公司名，就必须给 `-info`（配 `info.json`）。上面四个字符串命中证明 info.json 的值确实写进了 PE 资源，不是「传了但没生效」。

### 3.4 哪些该提交、哪些该忽略

| 文件 | 处理 | 理由 |
|---|---|---|
| `build/appicon.png` | **提交** | 图标的源文件 |
| `build/windows/icon.ico` | 二选一：提交，或忽略并每次由 PNG 生成 | 它是 `generate icons` 的产物；提交的好处是省掉 ImageMagick 式依赖 |
| `build/windows/wails.exe.manifest` | **提交** | 手写源文件 |
| `build/windows/info.json` | **提交** | 手写源文件 |
| `cmd/app/*.syso` | **忽略**（`.gitignore` 加 `*.syso`） | wails 自己也是用完即删：`build_assets/windows/Taskfile.yml:55-58` 在 `go build` 后执行 `powershell Remove-item *.syso` / `rm -f *.syso` |

> ⚠️ 权衡：syso 忽略后，**必须由构建脚本生成**，否则裸 `go build ./cmd/app`（不走脚本）的开发者拿到的是无图标 exe —— 这正是 T1 记录的「本地产物与发布产物不一致」的同类风险。要么让 `scripts/build.ps1` 与 `scripts/build.sh` 都先跑 `generate syso`，要么就把 syso 提交进仓库。**建议：忽略 + 两个构建脚本都生成**，与上游一致。

---

## 4. Q4（最关键）：wails3 不会自动内嵌 frontend/dist

### 4.1 裁决

**wails3 不负责嵌入。** 嵌入是**用户 `main` 包里的 `//go:embed` 干的活**。框架只提供一个把 `fs.FS` 适配成 `http.Handler` 的函数。

→ **工部自写 `embed.FS` 是必要的、方向正确的。** 不存在「wails3 已经嵌了，我们重复劳动」这种情况。

### 4.2 源码硬证据

**（1）模板里的嵌入代码属于用户工程，不属于框架**

```go
// ...\internal\templates\_common\main.go.tmpl:12-18
// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
//go:embed all:frontend/dist
var assets embed.FS
```

```go
// ...\internal\templates\_common\main.go.tmpl:43-45
Assets: application.AssetOptions{
    Handler: application.AssetFileServerFS(assets),
},
```

`main.go.tmpl` 是 `wails3 init` **生成到用户项目里的文件**，不是框架运行时代码。

**（2）框架侧只有「适配器」，没有「自动嵌入器」**

```go
// ...\pkg\application\application_options.go:203-205
type AssetOptions struct {
	...
	Handler http.Handler
```

```go
// ...\pkg\application\application_options.go:239-242
// AssetFileServerFS returns a http handler which serves the assets from the fs.FS.
func AssetFileServerFS(assets fs.FS) http.Handler {
```

`AssetOptions.Handler` 是 `http.Handler` —— 框架只调用它，不关心资源从哪来（磁盘 / embed.FS / 远程）。这正是我们当前 `main.go:177` 传 `http.FileServer(http.Dir(abs))` 能工作的原因。

**（3）`wails3 build` 在 Windows 上只有「编译 + 删 syso」，没有任何拷贝 dist 的步骤**

```yaml
# ...\internal\commands\build_assets\windows\Taskfile.yml:50-58
    cmds:
      - task: generate:syso
        vars:
          ARCH: '{{.ARCH}}'
      - '{{if eq .OBFUSCATED "true"}}garble {{.GARBLE_ARGS}} build{{else}}go build{{end}} {{.BUILD_FLAGS}} -o "{{.BIN_DIR}}/{{.APP_NAME}}.exe"'
      - cmd: powershell Remove-item *.syso
        platforms: [windows]
      - cmd: rm -f *.syso
        platforms: [linux, darwin]
```

**没有** `copy frontend/dist → bin/`，也**没有** `go:embed` 注入。`wails3 build` 只是把 task 编排成 `npm run build`（产出 dist）+ `go build`。

### 4.3 实跑观察（交叉印证）

| 观察 | 结果 | 含义 |
|---|---|---|
| T1 的 A/C2 实验 | exe 放在有 `frontend/dist` 的目录 → 起窗成功 | 资源是从**磁盘**读的 |
| T1 的 B/C1/D 实验 | exe 在无 dist 的目录 → `前端资源目录不存在` 退出 1 | 二进制里**没有**前端副本 |
| 本次 `t2.exe`（无 syso） | `.rsrc=无` | 与当前仓库产物状态一致，坐实无资源包 |

### 4.4 给工部的实现要点

```go
// 1) 必须导入 embed（哪怕没显式用到符号，也要 blank import 或直接声明 embed.FS）
import "embed"

// 2) all: 前缀不可省 —— 它会连 "_" 和 "." 开头的文件一起收进来
//    不加 all: 时，dist 下形如 .vite/、_plugin 的文件会被静默跳过
//go:embed all:frontend/dist
var distFS embed.FS

// 3) 交给框架的必须是 http.Handler
app := application.New(application.Options{
    ...
    Assets: application.AssetOptions{
        Handler: application.AssetFileServerFS(distFS),
    },
})
```

三点提醒：

1. **`all:` 前缀**：`//go:embed frontend/dist`（不带 `all:`）会跳过以 `_` / `.` 开头的文件。Vite 产物一般没有这类文件，但带 `all:` 更稳。
2. **`go:embed` 要求目标目录在构建时存在**。CI 里 `frontend/dist` 是 `npm run build` 产出的（`release.yml:116-122`），顺序没问题；但**裸 `go build` 在没跑前端构建时会直接编译失败**（`pattern frontend/dist: no matching files found`）。这一点与现状（dist 缺失时运行期才报错）完全不同 —— 是**编译期错误**，属于改善，但需要工部在 README/构建脚本里说清「必须先 build 前端」。
3. **别再同时保留磁盘读取分支**。`main.go:255-269` 的 `distHandler` 可以整段删掉；保留双路径会让「哪个生效」变成新的不确定性来源。

---

## 5. Q5：tags / ldflags 与 `-tags production,embed` 的关系

### 5.1 wails 生产构建实际用的 flags

```yaml
# ...\internal\commands\build_assets\windows\Taskfile.yml:60（生产分支，DEV 非 true）
BUILD_FLAGS: '… -tags production{{if eq .OBFUSCATED "true"}},wails_obfuscated{{end}}{{if .EXTRA_TAGS}},{{.EXTRA_TAGS}}{{end}} -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"'
```

即：**`-tags production` + `-trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"`**，与仓库现有 `scripts/build.sh:80-82`、`scripts/build.ps1:68` 的 `-s -w -H windowsgui` 基本一致（差别仅在 `-trimpath`/`-buildvcs`）。

### 5.2 自定义 tag 的注入方式

```powershell
wails3 build -tags embed          # → go build -tags production,embed
# 或
wails3 build EXTRA_TAGS=embed     # 同上
# 多个：-tags "embed,telemetry"    → -tags production,embed,telemetry
```

`wails3 build --help` 实测参数：

```
wails build - Build the project
  -garbleargs string
  -nocolour
  -obfuscated
  -tags string    Additional build tags to pass to the Go compiler (comma-separated)
```

### 5.3 但 embed 不需要 tag（建议别加）

`//go:embed` 是**语言特性**，受**导入 `embed` 包**约束，与 build tag 无关。加一个 `embed` tag 只会带来副作用：

- 必须同步改**三处**构建入口，否则产物不一致：`scripts/build.sh:82/98`、`scripts/build.ps1:68/78`、`.github/workflows/release.yml:133-146`；
- 忘记带 tag 就得到「无声的」无图标/无嵌入产物 —— 正是 T1 里「本地 bin 与 Release 产物不一致」的同类陷阱。

**建议**：直接无条件 `//go:embed all:frontend/dist`，不要引入 tag。若确实要做 dev/prod 双实现（dev 走 Vite 开发服务器），再用 `EXTRA_TAGS`，并**同时**更新 `scripts/build.sh`、`scripts/build.ps1` 与 `release.yml`。

---

## 6. Q6：syso 自带 manifest 吗

**不自带。** 三点证据：

1. `-manifest` 是硬性必填：`syso.go:30-32` `if options.Manifest == "" { return fmt.Errorf("manifest is required") }`。实测缺省即 `ERROR manifest is required`、exit 1。
2. manifest 内容来自**传入的文件**：`syso.go:69-79`
   ```go
   manifestData, err := os.ReadFile(options.Manifest)
   xmlData, err := winres.AppManifestFromXML(manifestData)
   rs.SetManifest(xmlData)
   ```
3. 框架内**没有**内置 manifest 常量可供回退 —— 只在 `updatable_build_assets/windows/wails.exe.manifest.tmpl` 提供**模板文件**。

**所以**：不必自己从零写 manifest（照抄 §2.2 的 22 行即可），但**必须**显式提供这个文件。内容已覆盖 `asInvoker`（不弹 UAC）+ `perMonitorV2` DPI + Common-Controls v6，正是我们需要的三项。

---

## 7. 给工部的执行清单（按序，可直接照做）

```powershell
# ① 建目录 + 准备图
New-Item -ItemType Directory -Force build\windows | Out-Null
Copy-Item "E:\data\golang\gopath\pkg\mod\github.com\wailsapp\wails\v3@v3.1.0\internal\commands\build_assets\appicon.png" build\appicon.png
wails3 generate icons -input build/appicon.png -windowsfilename build/windows/icon.ico

# ② 写入 build\windows\wails.exe.manifest（§2.2 全文）与 build\windows\info.json（§2.3 全文）

# ③ 生成 syso —— 输出到 main 包目录 cmd/app/，不是仓库根
wails3 generate syso -arch amd64 -icon build/windows/icon.ico `
  -manifest build/windows/wails.exe.manifest `
  -info build/windows/info.json `
  -out cmd/app/wails_windows_amd64.syso

# ④ .gitignore 追加
#    *.syso
#    并把 ③ 写进 scripts/build.ps1 与 scripts/build.sh（两个都要，否则本地/CI 不一致）

# ⑤ 构建后自检：必须看到 Subsystem=WINDOWS_GUI 且 .rsrc 含 RT_ICON/RT_GROUP_ICON/RT_MANIFEST
#    （解析方法见 docs/rootcause-exe-not-launch.md §4）

# ⑥ 前端内嵌（与 ③ 独立）：cmd/app/main.go 加
#    import "embed"
#    //go:embed all:frontend/dist
#    var distFS embed.FS
#    Assets: application.AssetOptions{Handler: application.AssetFileServerFS(distFS)}
#    同时删除 main.go:255-269 的 distHandler 与 :36 的 distDir 常量
```

---

## 8. 风险与未验证项

**已实测（本机可复现）**

- `wails3 version` = v3.1.0；`wails3 generate --help` 列出 `syso` / `icons` / `build-assets`。
- `generate syso` 成功路径（exit 0、23,778 字节产物）与缺 manifest / 缺 icon / 不支持 arch 三条失败路径的原文报错。
- syso 链接进 exe 后 `.rsrc` 含 `RT_ICON(6) RT_GROUP_ICON(1) RT_MANIFEST(1)`；无 syso 对照为 `.rsrc=无`；无 `_windows_amd64` 后缀的 syso 在 windows/amd64 上同样生效。
- `-info` 缺失 → 无 `RT_VERSION`；`-info` 存在 → 出现 `RT_VERSION(1)`，且 `info.json` 里的产品名/版本号/公司名/版权字符串在 PE 资源中命中（UTF-16）。
- §2.2 的 manifest 与 §2.3 的 info.json **原样**跑通 `generate syso`（exit 0），可复制直接用。
- 版本号四段 `0.1.0.0` 与三段 `0.1.0` **都**被接受（exit 0，产物 24,468 / 24,460 字节）。
- 所有源码行号已对照模块缓存文件核对。

**未验证 / 需工部实测确认**

1. `//go:embed all:frontend/dist` 在 dist 缺失时的确切编译错误文本 —— 未实测（本次未在仓库内跑构建，避免写入范围越界）。按 Go 语义应为编译期 `no matching files found`。
2. `wails3 build` 在本仓库（无 `build/` 骨架、main 在 `cmd/app/`）能否直接跑通 —— 未实测，**推断**需要补齐 build/Taskfile 与 main 位置约定，因此不建议 T2 走这条路径。
3. `wails3 generate icons` 的 PNG 输入尺寸/格式要求（是否必须 256×256 正方形）—— 未验证；用 wails 自带 `appicon.png` 作为输入可规避。
4. 图标在任务栏 / 标题栏 / 开始菜单的实际显示效果 —— 未截图核对；本次只确认 PE 资源存在。
