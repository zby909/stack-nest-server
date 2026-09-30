param(
    [Parameter(Mandatory = $true)][string]$PostgresBin,
    [int]$Port = 15439,
    [string]$Atlas = 'atlas'
)

$ErrorActionPreference = 'Stop'
$testServerRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$testWorkRoot = Join-Path $testServerRoot ('.local-dev\integration-' + [guid]::NewGuid().ToString('N'))
$testDataPath = Join-Path $testWorkRoot 'pgdata'
$testStarted = $false
$testCode = 1
if (-not (Get-Command $Atlas -ErrorAction SilentlyContinue)) { throw '找不到 Atlas，请通过 -Atlas 提供程序路径。' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw '测试端口正在使用，未启动或修改任何数据库。' }
New-Item -ItemType Directory -Path $testWorkRoot | Out-Null
Push-Location -LiteralPath $testServerRoot
try {
    & (Join-Path $PostgresBin 'initdb.exe') -D $testDataPath -U stacknest --auth=trust --encoding=UTF8 --no-locale
    if ($LASTEXITCODE -ne 0) { throw '初始化测试库失败。' }
    & (Join-Path $PostgresBin 'pg_ctl.exe') -D $testDataPath -l (Join-Path $testWorkRoot 'postgres.log') -o "-h 127.0.0.1 -p $Port" -w start
    if ($LASTEXITCODE -ne 0) { throw '启动测试库失败。' }
    $testStarted = $true
    foreach ($testName in @('stacknest_test', 'stacknest_auth_test')) {
        & (Join-Path $PostgresBin 'createdb.exe') -h 127.0.0.1 -p $Port -U stacknest $testName
        if ($LASTEXITCODE -ne 0) { throw '创建测试数据库失败。' }
        & $Atlas migrate apply --url "postgres://stacknest@127.0.0.1:$Port/$testName`?sslmode=disable" --dir file://migrations
        if ($LASTEXITCODE -ne 0) { throw '应用测试迁移失败。' }
    }
    $env:STACKNEST_TEST_DATABASE_URL = "postgres://stacknest@127.0.0.1:$Port/stacknest_test?sslmode=disable"
    $env:STACKNEST_TEST_AUTH_DATABASE_URL = "postgres://stacknest@127.0.0.1:$Port/stacknest_auth_test?sslmode=disable"
    go test -count=1 ./...
    $testCode = $LASTEXITCODE
    if ($testCode -ne 0) { throw '集成测试未通过。' }
} finally {
    if ($testStarted) {
        & (Join-Path $PostgresBin 'pg_ctl.exe') -D $testDataPath -w -m fast stop
        if ($LASTEXITCODE -ne 0) { throw '测试数据库未停止，保留测试目录。' }
    }
    Remove-Item Env:STACKNEST_TEST_DATABASE_URL,Env:STACKNEST_TEST_AUTH_DATABASE_URL -ErrorAction SilentlyContinue
    Pop-Location
    $testResolved = (Resolve-Path -LiteralPath $testWorkRoot).Path
    $testAllowedRoot = Join-Path $testServerRoot '.local-dev'
    if (-not $testResolved.StartsWith($testAllowedRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $testResolved) -notmatch '^integration-[0-9a-f]{32}$') { throw '拒绝清理范围外目录。' }
    if (Test-Path -LiteralPath (Join-Path $testDataPath 'postmaster.pid')) { throw '数据库可能仍在运行，保留目录。' }
    Remove-Item -LiteralPath $testResolved -Recurse -Force
}
exit $testCode
