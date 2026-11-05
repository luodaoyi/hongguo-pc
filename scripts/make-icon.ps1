<#
.SYNOPSIS
    scripts/make-icon.ps1 —— 生成应用图标源图与 Windows 图标

.DESCRIPTION
    图标是本仓库自绘的二进制资产，来源必须可复现，因此由本脚本生成，而不是外部下载：

      1. 用 System.Drawing 画 512x512 的源图 -> build/appicon.png（红橙渐变底 + 白色播放三角）
      2. 调 wails3 generate icons（pin v3.1.0）把源图切成多尺寸 -> build/windows/icon.ico
         至少包含 16/32/48/256（默认还会出 64/128）

    产物入库，构建脚本直接使用 build/windows/icon.ico；需要改图标时重跑本脚本即可。
    退出码只在「源图与 ICO 都真的生成且 ICO 含要求尺寸」时为 0，不吞错。

.EXAMPLE
    pwsh -NoProfile -File scripts/make-icon.ps1
#>
[CmdletBinding()]
param(
    # 源图尺寸（正方形）。ICO 里仍会按 -Sizes 切出小尺寸。
    [int]$SourceSize = 512,
    # ICO 内含尺寸，逗号分隔
    [string]$Sizes = '256,128,64,48,32,16'
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path 'go.mod')) { throw 'not in repository root (go.mod missing)' }

$pngPath = Join-Path (Get-Location) 'build/appicon.png'
$icoPath = Join-Path (Get-Location) 'build/windows/icon.ico'

New-Item -ItemType Directory -Force -Path (Split-Path $pngPath) | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path $icoPath) | Out-Null

# ---------------------------------------------------------------- 1. 画源图
Add-Type -AssemblyName System.Drawing

$bmp = New-Object System.Drawing.Bitmap($SourceSize, $SourceSize)
try {
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    try {
        $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $g.Clear([System.Drawing.Color]::Transparent)

        # 圆底：左上暖橙 -> 右下深红
        $margin = [int]($SourceSize * 0.015)
        $radius = $SourceSize - 2 * $margin
        $rect = New-Object System.Drawing.Rectangle($margin, $margin, $radius, $radius)
        $brush = New-Object System.Drawing.Drawing2D.LinearGradientBrush(
            (New-Object System.Drawing.Point($margin, $margin)),
            (New-Object System.Drawing.Point(($SourceSize - $margin), ($SourceSize - $margin))),
            [System.Drawing.Color]::FromArgb(255, 255, 122, 66),
            [System.Drawing.Color]::FromArgb(255, 205, 28, 40))
        try { $g.FillEllipse($brush, $rect) } finally { $brush.Dispose() }

        # 内白描边，让图标在深色/浅色背景上都立得住
        $ringPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(46, 255, 255, 255), [float]($SourceSize * 0.02))
        try {
            $inset = $margin + [int]($SourceSize * 0.055)
            $g.DrawEllipse($ringPen, $inset, $inset, $SourceSize - 2 * $inset, $SourceSize - 2 * $inset)
        }
        finally { $ringPen.Dispose() }

        # 播放三角（光学居中：整体右移一点，视觉重心才在圆心上）
        $cx = $SourceSize * 0.5
        $cy = $SourceSize * 0.5
        $triW = $SourceSize * 0.30
        $triH = $SourceSize * 0.40
        $points = @(
            (New-Object System.Drawing.PointF([float]($cx - $triW * 0.42), [float]($cy - $triH / 2))),
            (New-Object System.Drawing.PointF([float]($cx - $triW * 0.42), [float]($cy + $triH / 2))),
            (New-Object System.Drawing.PointF([float]($cx + $triW * 0.72), [float]$cy))
        )
        $white = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::White)
        try { $g.FillPolygon($white, [System.Drawing.PointF[]]$points) } finally { $white.Dispose() }
    }
    finally { $g.Dispose() }

    $bmp.Save($pngPath, [System.Drawing.Imaging.ImageFormat]::Png)
}
finally { $bmp.Dispose() }

if (-not (Test-Path $pngPath)) { throw "源图未生成: $pngPath" }
Write-Host "==> 源图: $pngPath ($((Get-Item $pngPath).Length) bytes)"

# ---------------------------------------------------------------- 2. 生成 ICO
$wails3 = Get-Command wails3 -ErrorAction SilentlyContinue
if (-not $wails3) { throw 'wails3 不在 PATH；ICO 由 wails3 generate icons 生成，请先 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0' }

# wails3 generate icons 会顺带写 macOS 的 icns；给一个临时目标，避免因缺 build/darwin 目录而报错，
# 同时用显式判据确认 ICO 真的产出（而不是靠忽略它的退出码）。
$macTmp = Join-Path ([System.IO.Path]::GetTempPath()) ("hg-icns-" + [guid]::NewGuid().ToString('N') + '.icns')

Remove-Item -Force $icoPath -ErrorAction SilentlyContinue
& wails3 generate icons -input $pngPath -sizes $Sizes -windowsfilename $icoPath -macfilename $macTmp 2>&1 | ForEach-Object { Write-Host "    $_" }
$genExit = $LASTEXITCODE
Remove-Item -Force $macTmp -ErrorAction SilentlyContinue

if (-not (Test-Path $icoPath)) {
    throw "generate icons 未产出 ICO（exit=$genExit）: $icoPath"
}

# ---------------------------------------------------------------- 3. 校验 ICO 内容
$bytes = [System.IO.File]::ReadAllBytes($icoPath)
if ($bytes.Length -lt 6) { throw "ICO 文件过短: $($bytes.Length) bytes" }
$reserved = [BitConverter]::ToUInt16($bytes, 0)
$type = [BitConverter]::ToUInt16($bytes, 2)
$count = [BitConverter]::ToUInt16($bytes, 4)
if ($reserved -ne 0 -or $type -ne 1 -or $count -eq 0) {
    throw ("ICO 头不合法: reserved=$reserved type=$type count=$count")
}

$have = @()
for ($i = 0; $i -lt $count; $i++) {
    $off = 6 + $i * 16
    $w = if ($bytes[$off] -eq 0) { 256 } else { [int]$bytes[$off] }
    $have += $w
}
Write-Host "==> ICO: $icoPath ($($bytes.Length) bytes, $count 个尺寸: $($have -join '/'))"

$required = @(16, 32, 48, 256)
$missing = $required | Where-Object { $have -notcontains $_ }
if ($missing) { throw "ICO 缺少必需尺寸: $($missing -join ', ')（实际: $($have -join '/')）" }

Write-Host '==> done: 图标生成完成'
