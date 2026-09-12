<#
stop.ps1 — 停止本机运行的 WorkBuddy2API

用法：
  .\stop.ps1
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'SilentlyContinue'
Set-Location $PSScriptRoot

$procs = Get-Process wb2api
if (-not $procs) {
    Write-Host 'wb2api 未在运行' -ForegroundColor Yellow
    exit 0
}

$ids = ($procs.Id) -join ', '
$procs | Stop-Process -Force
Write-Host "[+] 已停止 wb2api（PID=$ids）" -ForegroundColor Green
