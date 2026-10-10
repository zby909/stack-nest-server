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
$previousTestURL = $env:STACKNEST_TEST_DATABASE_URL
$previousAuthTestURL = $env:STACKNEST_TEST_AUTH_DATABASE_URL
if (-not (Get-Command $Atlas -ErrorAction SilentlyContinue)) { throw '找不到 Atlas，请通过 -Atlas 提供程序路径。' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw '测试端口正在使用，未启动或修改任何数据库。' }
New-Item -ItemType Directory -Path $testWorkRoot | Out-Null
Push-Location -LiteralPath $testServerRoot
try {
    & (Join-Path $PostgresBin 'initdb.exe') -D $testDataPath -U stacknest --auth=trust --encoding=UTF8 --no-locale
    if ($LASTEXITCODE -ne 0) { throw '初始化测试库失败。' }
    # 子进程输出写入文件，避免 Windows 在外层重定向日志时等到 PostgreSQL 退出才返回。
    $launchOutput = Join-Path $testWorkRoot 'launch.out'
    $launchError = Join-Path $testWorkRoot 'launch.err'
    $databaseLog = Join-Path $testWorkRoot 'postgres.log'
    $pgArguments = @('-D', "`"$testDataPath`"", '-l', "`"$databaseLog`"", '-o', "`"-h 127.0.0.1 -p $Port`"", '-w', '-t', '30', 'start')
    $launcher = Start-Process -FilePath (Join-Path $PostgresBin 'pg_ctl.exe') -ArgumentList $pgArguments -WindowStyle Hidden -PassThru -RedirectStandardOutput $launchOutput -RedirectStandardError $launchError
    if (-not $launcher.WaitForExit(45000)) {
        Stop-Process -Id $launcher.Id -Force
        $testStarted = Test-Path -LiteralPath (Join-Path $testDataPath 'postmaster.pid')
        throw '启动测试库超时。'
    }
    $testStarted = Test-Path -LiteralPath (Join-Path $testDataPath 'postmaster.pid')
    Get-Content -LiteralPath $launchOutput, $launchError
    if ($launcher.ExitCode -ne 0 -or -not $testStarted) { throw '启动测试库失败。' }
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
    if ($testStarted -and (Test-Path -LiteralPath (Join-Path $testDataPath 'postmaster.pid'))) {
        & (Join-Path $PostgresBin 'pg_ctl.exe') -D $testDataPath -w -m fast stop
        if ($LASTEXITCODE -ne 0) { throw '测试数据库未停止，保留测试目录。' }
    }
    [Environment]::SetEnvironmentVariable('STACKNEST_TEST_DATABASE_URL', $previousTestURL, 'Process')
    [Environment]::SetEnvironmentVariable('STACKNEST_TEST_AUTH_DATABASE_URL', $previousAuthTestURL, 'Process')
    Pop-Location
    $testResolved = (Resolve-Path -LiteralPath $testWorkRoot).Path
    $testAllowedRoot = Join-Path $testServerRoot '.local-dev'
    if (-not $testResolved.StartsWith($testAllowedRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $testResolved) -notmatch '^integration-[0-9a-f]{32}$') { throw '拒绝清理范围外目录。' }
    if (Test-Path -LiteralPath (Join-Path $testDataPath 'postmaster.pid')) { throw '数据库可能仍在运行，保留目录。' }
    Remove-Item -LiteralPath $testResolved -Recurse -Force
}
exit $testCode
