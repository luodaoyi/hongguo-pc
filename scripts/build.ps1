<#
.SYNOPSIS
    scripts/build.ps1 —— Windows 本地构建入口（等价于 scripts/build.sh）

.DESCRIPTION
    CI 一律用 bash（Git Bash 在所有 runner 上都可用）。
    这个脚本只是方便 Windows 开发者在本机复现同一套步骤。
    若仓库里已有 wails3 生成的 build/ 资产，优先用 `wails3 build`。

.EXAMPLE
    pwsh -File scripts/build.ps1
    pwsh -File scripts/build.ps1 -GOOS windows -GOARCH arm64
#>
[CmdletBinding()]
param(
    [string]$GOOS = "",
    [string]$GOARCH = "",
    [switch]$App,
    [switch]$SkipFrontend,
    [switch]$AllowNoFrontend
)

$ErrorActionPreference = 'Stop'

$AppName = if ($env:APP_NAME) { $env:APP_NAME } else { 'hongguo-pc' }
$Entry   = if ($env:ENTRY)    { $env:ENTRY }    else { './cmd/app' }
$OutDir  = if ($env:OUT_DIR)  { $env:OUT_DIR }  else { 'bin' }

if (-not (Test-Path 'go.mod')) { throw 'not in repository root (go.mod missing)' }
if (-not (Test-Path $Entry))   { throw "entry package not found: $Entry（应用入口尚未落地）" }

if (-not $GOOS)   { $GOOS = (& go env GOOS).Trim() }
if (-not $GOARCH) { $GOARCH = (& go env GOARCH).Trim() }

# 1) 前端
if (Test-Path 'frontend/package.json') {
    if ($SkipFrontend -and (Test-Path 'frontend/dist')) {
        Write-Host '==> frontend: skip'
    }
    else {
        Write-Host '==> frontend: install + build'
        Push-Location frontend
        try {
            if (Test-Path 'package-lock.json') { npm ci } else { npm install }
            if ($LASTEXITCODE -ne 0) { throw 'npm install/ci failed' }
            npm run build
            if ($LASTEXITCODE -ne 0) { throw 'npm run build failed' }
        }
        finally { Pop-Location }
    }
}
elseif ($AllowNoFrontend) {
    Write-Host '==> frontend: package.json 缺失，按 -AllowNoFrontend 继续'
}
else {
    throw 'frontend/package.json 缺失（脚手架期可加 -AllowNoFrontend 临时跳过）'
}

# 2) 绑定（有 wails3 就交给它，失败不静默，前端构建会暴露问题）
# `-ts -d frontend/bindings ./cmd/app` 与 scripts/build.sh 保持一致：缺 -ts 会把
# 入库的 .ts 绑定重写成 .js，缺 ./cmd/app 只扫仓库根、出不来绑定。
if (Get-Command wails3 -ErrorAction SilentlyContinue) {
    Write-Host '==> bindings: wails3 generate bindings -ts ./cmd/app'
    wails3 generate bindings -ts -d frontend/bindings ./cmd/app
    if ($LASTEXITCODE -ne 0) { Write-Warning 'wails3 generate bindings 失败，继续' }
}

# 2.5) Windows 资源（图标 / manifest / 版本信息）→ syso
# 与 scripts/build.sh 的 2.5 步一致：syso 落在 main 包目录（cmd/app/），Go 链接器
# 按 _windows_<arch> 后缀自动带入；产物 gitignore 但保留，裸 go build 也带图标。
if ($GOOS -eq 'windows') {
    if ($GOARCH -notin @('amd64', 'arm64', '386')) {
        throw "windows/$GOARCH 无法生成 syso（wails3 仅支持 amd64/arm64/386）"
    }
    if (Get-Command wails3 -ErrorAction SilentlyContinue) {
        foreach ($f in @('build/windows/icon.ico', 'build/windows/wails.exe.manifest', 'build/windows/info.json')) {
            if (-not (Test-Path $f)) { throw "missing build asset: $f（源资产应入库）" }
        }
        $syso = Join-Path $Entry "wails_windows_$GOARCH.syso"
        Write-Host "==> syso: wails3 generate syso -arch $GOARCH"
        & wails3 generate syso -arch $GOARCH `
            -icon build/windows/icon.ico `
            -manifest build/windows/wails.exe.manifest `
            -info build/windows/info.json `
            -out $syso
        if ($LASTEXITCODE -ne 0) { throw 'wails3 generate syso failed' }
        if (-not (Test-Path $syso)) { throw "syso not produced: $syso" }
    }
    else {
        Write-Warning '本机无 wails3，跳过 syso：产物可运行但没有图标/版本信息'
    }
}

# 3) go build
$ext = if ($GOOS -eq 'windows') { '.exe' } else { '' }
$ldflags = if ($GOOS -eq 'windows') { '-s -w -H windowsgui' } else { '-s -w' }
$cgo = if ($env:CGO_ENABLED) { $env:CGO_ENABLED } elseif ($GOOS -eq 'windows') { '0' } else { '1' }

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$binPath = Join-Path $OutDir "$AppName$ext"

Write-Host "==> go build: GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$cgo -> $binPath"
$env:CGO_ENABLED = $cgo
$env:GOOS = $GOOS
$env:GOARCH = $GOARCH
& go build -trimpath -tags production -ldflags $ldflags -o $binPath $Entry
if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

Write-Host "==> done: $binPath"
