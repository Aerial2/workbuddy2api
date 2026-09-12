<#
login.ps1 — Windows / PowerShell 版 OAuth 登录（等价于 login.sh）

用法：
  .\login.ps1              # 取授权链接 -> 浏览器登录 -> 落盘 auths/workbuddy-<uid>.json -> 重启服务
  .\login.ps1 -NoRestart   # 只落盘，不重启服务
#>
[CmdletBinding()]
param(
    [switch]$NoRestart
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }
Set-Location $root

$loginBin = Join-Path $root "login.exe"

# 1. login 工具不存在则编译
if (-not (Test-Path $loginBin)) {
    Write-Host "[*] login.exe 不存在，正在编译..."
    go build -o $loginBin ./cmd/login
    if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/login 失败" }
}

$authDir = Join-Path $root "auths"
New-Item -ItemType Directory -Force -Path $authDir | Out-Null

# 2. 取授权链接
Write-Host "[*] 正在获取授权链接..."
$authURL = (& $loginBin url | Select-Object -First 1)
if (-not $authURL) { throw "获取授权链接失败" }

Write-Host ""
Write-Host "[*] 请在浏览器中打开以下链接完成登录（已尝试自动打开）："
Write-Host "    $authURL"
Write-Host ""
try { Start-Process $authURL } catch { }

Read-Host "完成登录后按回车继续"

# 3. 轮询换取 token
Write-Host "[*] 正在获取 token..."
$raw = (& $loginBin poll | Select-Object -First 1)
if (-not $raw) { throw "获取 token 失败：请确认浏览器已完成登录后重试" }

$res = $raw | ConvertFrom-Json
if (-not $res.uid) { throw "未获取到 uid，登录可能尚未完成" }

# 4. 落盘 auth 文件（字段与 internal/auth 读取格式一致）
$expiresAt = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + [int64]$res.expires_in
$auth = [ordered]@{
    account = [ordered]@{
        uid          = $res.uid
        enterpriseId = $res.enterprise_id
        nickname     = $res.nickname
    }
    auth    = [ordered]@{
        accessToken  = $res.access_token
        refreshToken = $res.refresh_token
        expiresAt    = $expiresAt
        domain       = $res.domain
    }
}
$file = Join-Path $authDir "workbuddy-$($res.uid).json"
$json = $auth | ConvertTo-Json -Depth 5
[System.IO.File]::WriteAllText($file, $json, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "[+] 已保存: $file"

# 5. 重启服务加载新账号
if (-not $NoRestart) {
    $proc = Get-Process wb2api -ErrorAction SilentlyContinue
    if ($proc) {
        Write-Host "[*] 正在重启 wb2api..."
        $proc | Stop-Process -Force
        Start-Sleep -Seconds 1
        $out = Join-Path $root "server.out.log"
        $err = Join-Path $root "server.err.log"
        Start-Process -FilePath (Join-Path $root "wb2api.exe") -ArgumentList "-config", "config.json" -WorkingDirectory $root -RedirectStandardOutput $out -RedirectStandardError $err -WindowStyle Hidden
        Start-Sleep -Seconds 2
        $key = (Get-Content (Join-Path $root "config.json") -Raw | ConvertFrom-Json).api_key
        $st = curl.exe -s "http://127.0.0.1:7863/status" -H "Authorization: Bearer $key"
        Write-Host "[*] status: $st"
    }
    else {
        Write-Host "[i] wb2api 未运行，auth 文件将在下次启动时自动加载"
    }
}

Write-Host ""
Write-Host "[+] 登录完成"
Write-Host "    uid      : $($res.uid)"
Write-Host "    nickname : $($res.nickname)"
