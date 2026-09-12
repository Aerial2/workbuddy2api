<#
run.ps1 — 本机直接运行 WorkBuddy2API（按需编译 → 后台启动 → 打印入口）

用法：
  .\run.ps1                # 源码有改动才重新编译，然后后台启动（已在跑则先停旧进程）
  .\run.ps1 -Rebuild       # 强制重新编译
  .\run.ps1 -Foreground    # 前台运行：日志直接打在终端，Ctrl+C 退出

停止：.\stop.ps1
#>
[CmdletBinding()]
param(
    [switch]$Rebuild,
    [switch]$Foreground
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$exe = Join-Path $PSScriptRoot 'wb2api.exe'
$cfg = Join-Path $PSScriptRoot 'config.json'

if (-not (Test-Path $cfg)) {
    throw "找不到 $cfg（首次使用请先 cp config.example.json config.json 并设置 api_key）"
}

# 1. 编译：缺 exe / 强制 / 有 .go 比 exe 新
$needBuild = $Rebuild -or (-not (Test-Path $exe))
if (-not $needBuild) {
    $exeTime = (Get-Item $exe).LastWriteTimeUtc
    $newer = Get-ChildItem -Path cmd, internal -Recurse -Filter *.go |
        Where-Object { $_.LastWriteTimeUtc -gt $exeTime } | Select-Object -First 1
    if ($newer) {
        Write-Host "[*] 源码已变更（$($newer.Name)），重新编译…" -ForegroundColor Cyan
        $needBuild = $true
    }
}
if ($needBuild) {
    Write-Host '[*] 编译 wb2api…' -ForegroundColor Cyan
    go build -o $exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'go build ./cmd/server 失败' }
    Write-Host '[+] 编译完成' -ForegroundColor Green
}

# 2. 停掉已在跑的实例（否则端口会冲突）
$old = Get-Process wb2api -ErrorAction SilentlyContinue
if ($old) {
    Write-Host "[*] 停止旧进程 PID=$(($old.Id) -join ',')…" -ForegroundColor Yellow
    $old | Stop-Process -Force
    Start-Sleep -Milliseconds 800
}

# 3. 启动
if ($Foreground) {
    Write-Host '[+] 前台运行中（Ctrl+C 退出）' -ForegroundColor Green
    & $exe -config $cfg
    exit $LASTEXITCODE
}

$out = Join-Path $PSScriptRoot 'server.out.log'
$err = Join-Path $PSScriptRoot 'server.err.log'
Start-Process -FilePath $exe -ArgumentList '-config', $cfg -WorkingDirectory $PSScriptRoot `
    -RedirectStandardOutput $out -RedirectStandardError $err -WindowStyle Hidden
Start-Sleep -Seconds 2

# 4. 读配置 → 探活 → 打印入口
$conf = (Get-Content $cfg -Raw -Encoding utf8) | ConvertFrom-Json
$port = ($conf.listen -split ':')[-1]
$key  = $conf.api_key

Write-Host ''
Write-Host "  管理台  : http://localhost:$port/admin/" -ForegroundColor Cyan
if ($key) {
    Write-Host "  API Key : $key"
} else {
    Write-Host '  API Key : (未设置，不鉴权)'
}
Write-Host '  日志    : server.out.log / server.err.log'
Write-Host '  停止    : .\stop.ps1'
Write-Host ''

try {
    $st = Invoke-RestMethod -Uri "http://127.0.0.1:$port/status" -TimeoutSec 5 `
        -Headers @{ Authorization = "Bearer $key" }
    if ($st.healthy -gt 0) {
        Write-Host "[+] 运行中：健康 $($st.healthy)/$($st.total) 个账号" -ForegroundColor Green
    } else {
        Write-Host "[!] 服务已起，但暂无可用账号（$($st.total) 个）：请在管理台点「+ 添加账号」" -ForegroundColor Yellow
    }
} catch {
    Write-Host "[x] 探活失败：$($_.Exception.Message)" -ForegroundColor Red
    Write-Host "    请查看 server.err.log（常见原因：端口被占用 / config.json 非法）" -ForegroundColor Red
}
