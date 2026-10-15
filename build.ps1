param([string]$GoExecutable = '', [string]$NodeExecutable = '', [string]$OutputFile = (Join-Path $PSScriptRoot 'ResourceLens.exe'))
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
if (-not $GoExecutable) {
    $bundledGo = Join-Path $PSScriptRoot 'tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $bundledGo) { $GoExecutable = $bundledGo }
    elseif (Get-Command go -ErrorAction SilentlyContinue) { $GoExecutable = (Get-Command go).Source }
    else { throw 'Install Go 1.24 or newer, or pass -GoExecutable.' }
}
if (-not $NodeExecutable) {
    if (Get-Command node -ErrorAction SilentlyContinue) { $NodeExecutable = (Get-Command node).Source }
    elseif (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'video-engine\node.exe')) { $NodeExecutable = Join-Path $PSScriptRoot 'video-engine\node.exe' }
    else { throw 'Install Node.js 22.19 or newer, or pass -NodeExecutable.' }
}
$env:Path = (Split-Path -Parent $NodeExecutable) + ';' + $env:Path
$env:GOPATH = Join-Path $PSScriptRoot 'tools\gopath'
$env:GOCACHE = Join-Path $PSScriptRoot 'tools\gocache'
$env:CGO_ENABLED = '0'
foreach ($engineTool in @('yt-dlp.exe','ffmpeg.exe','ffprobe.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot ('video-engine\' + $engineTool)))) {
        throw "Missing video-engine\$engineTool. See video-engine\ORIGIN.md for the official distribution."
    }
}
$npmExecutable = Join-Path (Split-Path -Parent $NodeExecutable) 'npm.cmd'
if (-not (Test-Path -LiteralPath $npmExecutable)) { $npmExecutable = (Get-Command npm.cmd -ErrorAction Stop).Source }
& $npmExecutable ci --prefix frontend --ignore-scripts --no-audit --no-fund
if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed' }
& $npmExecutable test --prefix frontend
if ($LASTEXITCODE -ne 0) { throw 'Frontend checks failed' }
& $npmExecutable run build --prefix frontend
if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
& $GoExecutable test -tags 'desktop,production' ./app ./frontend
if ($LASTEXITCODE -ne 0) { throw 'Backend checks failed' }
$resourceCompiler = Join-Path $PSScriptRoot 'tools\bin\go-winres.exe'
if (-not (Test-Path -LiteralPath $resourceCompiler)) {
    $resourceCompiler = Join-Path $env:GOPATH 'bin\go-winres.exe'
    if (-not (Test-Path -LiteralPath $resourceCompiler)) {
        & $GoExecutable install github.com/tc-hib/go-winres@v0.3.3
        if ($LASTEXITCODE -ne 0) { throw 'Windows resource compiler installation failed' }
    }
}
$versionMatch = [regex]::Match([IO.File]::ReadAllText((Join-Path $PSScriptRoot 'app\features.go')), 'const appVersion = "(\d+\.\d+\.\d+)"')
if (-not $versionMatch.Success) { throw 'Cannot read application version.' }
$applicationVersion = $versionMatch.Groups[1].Value
$resourceFile = Join-Path $PSScriptRoot 'winres\winres.json'
$resources = Get-Content -LiteralPath $resourceFile -Raw | ConvertFrom-Json
$resources.RT_MANIFEST.'#1'.'0409'.identity.version = $applicationVersion + '.0'
$resources.RT_VERSION.'#1'.'0000'.fixed.file_version = $applicationVersion + '.0'
$resources.RT_VERSION.'#1'.'0000'.fixed.product_version = $applicationVersion + '.0'
$resources.RT_VERSION.'#1'.'0000'.info.'0409'.FileVersion = $applicationVersion
$resources.RT_VERSION.'#1'.'0000'.info.'0409'.ProductVersion = $applicationVersion
[IO.File]::WriteAllText($resourceFile, ($resources | ConvertTo-Json -Depth 20) + "`n", [Text.UTF8Encoding]::new($false))
& $resourceCompiler make --arch amd64 --out app/rsrc
if ($LASTEXITCODE -ne 0) { throw 'Windows resources build failed' }
& $GoExecutable build -tags 'desktop,production' -ldflags '-s -w -H windowsgui' -o $OutputFile ./app
if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
Write-Host "Built: $OutputFile"
