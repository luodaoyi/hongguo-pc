<#
.SYNOPSIS
    scripts/verify-pe.ps1 —— 发布前 PE 自检（docs/wails3-assets-howto.md §7-⑤）

.DESCRIPTION
    校验 Windows 产物的三类硬指标，任何一项不满足都非零退出：

      1. Subsystem = WINDOWS_GUI（-H windowsgui 生效，双击不弹控制台）
      2. .rsrc 节存在，且含 RT_ICON / RT_GROUP_ICON / RT_MANIFEST / RT_VERSION
      3. VERSIONINFO 里能找到 info.json 写入的产品名与版本号（UTF-16 字符串）

    用法：pwsh -NoProfile -File scripts/verify-pe.ps1 [-Path bin\hongguo-pc.exe]

.EXAMPLE
    pwsh -NoProfile -File scripts/verify-pe.ps1
#>
[CmdletBinding()]
param(
    [string]$Path = 'bin/hongguo-pc.exe',
    # VERSIONINFO 里必须命中的字符串（与 build/windows/info.json 对应）
    [string[]]$MustContain = @('红果短剧', '0.1.0.0')
)

$ErrorActionPreference = 'Stop'
$bytes = [System.IO.File]::ReadAllBytes((Resolve-Path $Path))
$fail = 0

# ---- 1. Subsystem（PE+24+68） ----
$peOff = [BitConverter]::ToInt32($bytes, 0x3C)
$optOff = $peOff + 24
$subsystem = [BitConverter]::ToUInt16($bytes, $optOff + 68)
switch ($subsystem) {
    2 { Write-Host '==> Subsystem: WINDOWS_GUI（无控制台）' }
    3 { Write-Warning 'Subsystem: WINDOWS_CUI（会弹控制台！-H windowsgui 没生效）'; $fail++ }
    default { Write-Warning "Subsystem: $subsystem（非 GUI）"; $fail++ }
}

# ---- 2. 节表找 .rsrc，遍历资源目录第一层（Type 项） ----
$numSections = [BitConverter]::ToUInt16($bytes, $peOff + 6)
$sizeOpt = [BitConverter]::ToUInt16($bytes, $peOff + 20)
$secOff = $optOff + $sizeOpt

$rsrcRaw = -1
for ($i = 0; $i -lt $numSections; $i++) {
    $s = $secOff + $i * 40
    $name = [System.Text.Encoding]::ASCII.GetString($bytes, $s, 8).TrimEnd([char]0)
    if ($name -eq '.rsrc') { $rsrcRaw = [BitConverter]::ToUInt32($bytes, $s + 20) }
}
if ($rsrcRaw -lt 0) {
    Write-Warning '.rsrc 节不存在：无图标 / 无 manifest / 无版本信息'
    exit 1
}
Write-Host ("==> .rsrc 节存在（文件偏移 {0}）" -f $rsrcRaw)

# Win32 资源类型 ID：RT_ICON=3（2 是 RT_BITMAP，别写混）
$types = @{ 3 = 'RT_ICON'; 14 = 'RT_GROUP_ICON'; 16 = 'RT_VERSION'; 24 = 'RT_MANIFEST' }
$numTypes = [BitConverter]::ToUInt16($bytes, $rsrcRaw + 14)
$found = @()
for ($i = 0; $i -lt $numTypes; $i++) {
    $e = $rsrcRaw + 16 + $i * 8
    $nameOrId = [BitConverter]::ToUInt32($bytes, $e)
    # 高位=0 是整数 Type ID（名字项高位=1，只出现在第二层往下）
    if ((($nameOrId -shr 31) -eq 0) -and $types.ContainsKey([int]($nameOrId -band 0x7FFFFFFF))) {
        $found += $types[[int]($nameOrId -band 0x7FFFFFFF)]
    }
}
Write-Host ("==> 资源类型: {0}" -f ($found -join ', '))
foreach ($required in @('RT_ICON', 'RT_GROUP_ICON', 'RT_MANIFEST', 'RT_VERSION')) {
    if ($found -notcontains $required) { Write-Warning "缺少 $required"; $fail++ }
}

# ---- 3. VERSIONINFO 字符串（UTF-16LE 命中即认为 info.json 生效） ----
$tail = [System.Text.Encoding]::Unicode.GetString($bytes, $rsrcRaw, [Math]::Min(65536, $bytes.Length - $rsrcRaw))
foreach ($needle in $MustContain) {
    if ($tail.Contains($needle)) {
        Write-Host "==> VERSIONINFO 含 '$needle'"
    }
    else {
        Write-Warning "VERSIONINFO 未命中 '$needle'（info.json 没生效？）"
        $fail++
    }
}

if ($fail -gt 0) { throw "verify-pe: $fail 项不达标" }
Write-Host '==> verify-pe: 全部通过'
