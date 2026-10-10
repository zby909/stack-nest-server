param(
    [switch]$Integration,
    [string]$PostgresBin = '',
    [string]$Atlas = 'atlas'
)

$ErrorActionPreference = 'Stop'
$checkRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

# Invoke-Check 遇到外部检查失败立即停止，避免把部分成功误报成通过。
function Invoke-Check([scriptblock]$Command, [string]$Label) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$Label failed (exit $LASTEXITCODE)." }
}

if ($Integration -and -not $PostgresBin) {
    $postgresInit = Get-Command initdb.exe -ErrorAction SilentlyContinue
    if (-not $postgresInit) { throw '请用 -PostgresBin 指定 PostgreSQL bin 目录。' }
    $PostgresBin = Split-Path -Parent $postgresInit.Source
}
Push-Location -LiteralPath $checkRoot
try {
    Invoke-Check { go vet ./... } 'Go vet'
    if ($Integration) {
        Invoke-Check { & (Join-Path $PSScriptRoot 'test-integration.ps1') -PostgresBin $PostgresBin -Atlas $Atlas } 'Go integration tests'
    } else {
        Write-Host '未启用临时 PostgreSQL 集成环境；完整验证请增加 -Integration。'
        Invoke-Check { go test -count=1 ./... } 'Go tests'
    }
    Invoke-Check { python -m unittest discover -s deploy -p 'test_*.py' } 'Deployment tool tests'
    Invoke-Check { go build ./... } 'Go build'
    Write-Host '本次选定的检查全部通过。'
} finally {
    Pop-Location
}
