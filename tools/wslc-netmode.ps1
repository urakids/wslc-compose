<#
.SYNOPSIS
    wslc のネットワークモードを nat / internal で切り替える。

.DESCRIPTION
    wslc の設定ファイル (settings.yaml) の session.networkingMode を書き換え、セッションを再起動して反映する。

      nat      : 通信が Windows の NAT を通る。セキュリティソフトの SSL/TLS 検査の影響を受けない。
                 host.wslc.internal は使えない。
      internal : wslc の既定 (consomme)。host.wslc.internal が使える。

    現在のモードと切り替え先を表示し、確認したうえで切り替える。
    引数を省略すると、現在と反対のモードへの切り替えを提案する。

.EXAMPLE
    .\wslc-netmode.ps1
    .\wslc-netmode.ps1 nat
    .\wslc-netmode.ps1 internal -Yes
#>
param(
    [ValidateSet('nat', 'internal')]
    [string]$Mode,
    # 確認せずに切り替える
    [switch]$Yes
)

$ErrorActionPreference = 'Stop'

function Get-SettingsPath {
    # wslc info の出力から設定ファイルのパスを取得し、取れなければ既定の場所を使う
    $info = (& wslc info 2>$null) -join "`n"
    $m = [regex]::Match($info, '[A-Za-z]:\\[^\r\n]*settings\.yaml')
    if ($m.Success) { return $m.Value }
    return Join-Path $env:LOCALAPPDATA 'wslc\settings.yaml'
}

# session ブロック内の有効な networkingMode 行の位置を返す (なければ -1)
function Find-ModeLine([string[]]$lines) {
    $inSession = $false
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $l = $lines[$i]
        if ($l -match '^session:\s*(#.*)?$') { $inSession = $true; continue }
        if ($inSession -and $l -match '^\S' -and $l -notmatch '^#') { $inSession = $false }
        if ($inSession -and $l -match '^\s+networkingMode:') { return $i }
    }
    return -1
}

function Get-CurrentMode([string[]]$lines) {
    $i = Find-ModeLine $lines
    if ($i -lt 0) { return 'internal' }
    $v = ($lines[$i] -replace '^\s+networkingMode:\s*', '' -replace '\s*#.*$', '').Trim().Trim('"', "'")
    if ($v -eq 'nat') { return 'nat' }
    if ($v -in 'consomme', 'default', '') { return 'internal' }
    return $v
}

$path = Get-SettingsPath
if (-not (Test-Path $path)) {
    Write-Error "設定ファイルが見つかりません: $path"
}

$raw = [IO.File]::ReadAllText($path)
$nl = if ($raw.Contains("`r`n")) { "`r`n" } else { "`n" }
$lines = [System.Collections.Generic.List[string]]($raw -split '\r?\n')
$current = Get-CurrentMode $lines

$descriptions = @{
    'nat'      = 'Windows の NAT 経由。SSL/TLS 検査を受けない。host.wslc.internal は使えない'
    'internal' = 'wslc の既定 (consomme)。host.wslc.internal が使える。SSL/TLS 検査を受ける'
}

Write-Host "現在のネットワークモード: $current"
if ($descriptions.ContainsKey($current)) { Write-Host "  $($descriptions[$current])" }

if (-not $Mode) {
    $Mode = if ($current -eq 'nat') { 'internal' } else { 'nat' }
}
if ($current -eq $Mode) {
    Write-Host "既に $Mode モードです。変更はありません。"
    exit 0
}

Write-Host ""
Write-Host "切り替え先: $Mode"
Write-Host "  $($descriptions[$Mode])"

# 切り替えにはセッションの再起動が必要で、動作中のコンテナは停止する
$running = @(& wslc ps --format json 2>$null | Where-Object { $_.Trim() } | ForEach-Object { ($_ | ConvertFrom-Json).Names })
if ($running.Count -gt 0) {
    Write-Host ""
    Write-Host "切り替えるとセッションが再起動され、次のコンテナが停止します:"
    $running | ForEach-Object { Write-Host "  $_" }
}

if (-not $Yes) {
    Write-Host ""
    $ans = Read-Host "$Mode モードに切り替えますか? (y/N)"
    if ($ans -notmatch '^(y|yes)$') {
        Write-Host "切り替えを中止しました。"
        exit 0
    }
}

# 設定ファイルを書き換える (書き換え前にバックアップを残す)
$idx = Find-ModeLine $lines
if ($Mode -eq 'nat') {
    if ($idx -ge 0) {
        $lines[$idx] = '  networkingMode: nat'
    } else {
        $s = $lines.FindIndex([Predicate[string]] { param($l) $l -match '^session:\s*(#.*)?$' })
        if ($s -ge 0) {
            $lines.Insert($s + 1, '  networkingMode: nat')
        } else {
            $lines.Add('session:')
            $lines.Add('  networkingMode: nat')
        }
    }
} else {
    $lines.RemoveAt($idx)
}

Copy-Item $path "$path.bak" -Force
[IO.File]::WriteAllText($path, ($lines -join $nl), (New-Object System.Text.UTF8Encoding $false))

# セッションを再起動して反映する
& wslc system session terminate 2>&1 | Out-Null
Write-Host ""
Write-Host "$Mode モードに切り替えました。(変更前の設定: $path.bak)"
if ($running.Count -gt 0) {
    Write-Host "停止したコンテナは必要に応じて起動し直してください (wslc-compose up -d など)。"
}
