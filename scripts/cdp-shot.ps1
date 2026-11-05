<#
.SYNOPSIS
    scripts/cdp-shot.ps1 —— 开发诊断：对运行中页面的 WebView/浏览器 CDP 截图。
    用法：pwsh -NoProfile -File scripts/cdp-shot.ps1 -Port 9334 -UrlFilter 8977 -Out shot.png
#>
[CmdletBinding()]
param(
    [int]$Port = 9334,
    [Parameter(Mandatory)][string]$UrlFilter,
    [Parameter(Mandatory)][string]$Out
)
$ErrorActionPreference = 'Stop'
$targets = Invoke-RestMethod "http://127.0.0.1:$Port/json/list" -TimeoutSec 5
$page = $targets | Where-Object { $_.type -eq 'page' -and $_.url -like "*$UrlFilter*" } | Select-Object -First 1
if (-not $page) { throw 'no matching page target' }
$ws = New-Object System.Net.WebSockets.ClientWebSocket
$ct = [System.Threading.CancellationToken]::None
$ws.ConnectAsync([Uri]$page.webSocketDebuggerUrl, $ct).Wait()

function Invoke-Cdp($id, $method, $params) {
    $msg = @{ id = $id; method = $method }
    if ($params) { $msg.params = $params }
    $bytes = [System.Text.Encoding]::UTF8.GetBytes(($msg | ConvertTo-Json -Depth 6 -Compress))
    $ws.SendAsync([ArraySegment[byte]]::new($bytes), [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $ct).Wait()
    $buf = New-Object byte[] (32MB)
    while ($true) {
        $r = $ws.ReceiveAsync([ArraySegment[byte]]::new($buf), $ct).Result
        $text = [System.Text.Encoding]::UTF8.GetString($buf, 0, $r.Count)
        $json = $text | ConvertFrom-Json
        if ($json.id -eq $id) { return $json }
    }
}

$result = Invoke-Cdp 2 'Page.captureScreenshot' @{ format = 'png' }
if (-not $result.result.data) { throw ('no screenshot data: ' + ($result | ConvertTo-Json -Depth 4).Substring(0, 300)) }
[IO.File]::WriteAllBytes((Join-Path (Get-Location) $Out), [Convert]::FromBase64String($result.result.data))
$ws.Dispose()
Write-Host ("saved: {0} ({1} bytes)" -f $Out, (Get-Item $Out).Length)
