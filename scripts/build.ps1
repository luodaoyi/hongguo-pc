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
if (Get-Command wails3 -ErrorAction SilentlyContinue) {
    Write-Host '==> bindings: wails3 generate bindings'
    wails3 generate bindings
    if ($LASTEXITCODE -ne 0) { Write-Warning 'wails3 generate bindings 失败，继续' }
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
