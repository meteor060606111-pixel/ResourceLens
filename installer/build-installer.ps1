param(
    [string]$OutputDirectory = (Join-Path (Split-Path -Parent $PSScriptRoot) 'release'),
    [string]$Compiler = (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\installer\inno-6.7.3\ISCC.exe')
)
$ErrorActionPreference = 'Stop'
$installerProject = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path -LiteralPath $Compiler -PathType Leaf)) { throw 'Inno Setup 6.7.3 compiler not found; pass -Compiler with the ISCC.exe path.' }
$installerApp = Get-Item -LiteralPath (Join-Path $installerProject 'ResourceLens.exe')
$versionMatch = [regex]::Match([IO.File]::ReadAllText((Join-Path $installerProject 'app\features.go')), 'const appVersion = "(\d+\.\d+\.\d+)"')
if (-not $versionMatch.Success) { throw 'Cannot read application version.' }
$installerVersion = $versionMatch.Groups[1].Value
if ($installerApp.VersionInfo.ProductVersion -ne $installerVersion) { throw ('Application version does not match source: ' + $installerVersion) }
foreach ($installerTool in @('ffmpeg.exe','ffprobe.exe','yt-dlp.exe','node.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $installerProject ('video-engine\' + $installerTool)) -PathType Leaf)) { throw ('Missing bundled tool: ' + $installerTool) }
}
$installerMusicPython = Join-Path $installerProject 'music-engine\runtime\python.exe'
if (-not (Test-Path -LiteralPath $installerMusicPython -PathType Leaf)) { throw 'Build the portable music runtime with music-engine/build_runtime.py first.' }
$installerMusicProbe = & $installerMusicPython -B -X utf8 -c 'import json,musicdl; from musicdl.modules.sources import MusicClientBuilder; print(json.dumps(dict(version=musicdl.__version__,sources=len(MusicClientBuilder.REGISTERED_MODULES))))'
if ($LASTEXITCODE -ne 0) { throw 'Bundled music engine cannot start.' }
$installerMusicStatus = $installerMusicProbe | ConvertFrom-Json
if ($installerMusicStatus.version -ne '2.14.1' -or $installerMusicStatus.sources -ne 57) { throw 'Unexpected bundled music engine version or registry.' }
$installerBootstrap = Join-Path $PSScriptRoot 'deps\MicrosoftEdgeWebview2Setup.exe'
$installerSignature = Get-AuthenticodeSignature -LiteralPath $installerBootstrap
if ($installerSignature.Status -ne 'Valid' -or $installerSignature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') { throw 'WebView2 bootstrapper must have a valid Microsoft signature.' }
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$installerOutput = (Resolve-Path -LiteralPath $OutputDirectory).Path
& $Compiler ('/DAppVersion=' + $installerVersion) ('/DReleaseOutput=' + $installerOutput) (Join-Path $PSScriptRoot 'ResourceLens.iss')
if ($LASTEXITCODE -ne 0) { throw ('Installer compiler failed: ' + $LASTEXITCODE) }
$installerExe = Join-Path $installerOutput ('ResourceLens-' + $installerVersion + '-Setup.exe')
if (-not (Test-Path -LiteralPath $installerExe -PathType Leaf)) { throw 'Installer output not found.' }
$installerManifest = [pscustomobject]@{
    version=$installerVersion
    file=$installerExe
    bytes=(Get-Item -LiteralPath $installerExe).Length
    sha256=(Get-FileHash -LiteralPath $installerExe -Algorithm SHA256).Hash.ToLower()
    applicationSha256=(Get-FileHash -LiteralPath $installerApp.FullName -Algorithm SHA256).Hash.ToLower()
    bootstrapperSha256=(Get-FileHash -LiteralPath $installerBootstrap -Algorithm SHA256).Hash.ToLower()
    musicdlVersion=$installerMusicStatus.version
    musicSources=$installerMusicStatus.sources
    pythonSha256=(Get-FileHash -LiteralPath $installerMusicPython -Algorithm SHA256).Hash.ToLower()
    compilerVersion='6.7.3'
    compilerSha256=(Get-FileHash -LiteralPath $Compiler -Algorithm SHA256).Hash.ToLower()
    desktopShortcut='拾流 ResourceLens.lnk'
}
$installerManifest | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $installerOutput ('安装程序校验-v' + $installerVersion + '.json')) -Encoding utf8
$installerManifest | ConvertTo-Json -Depth 3
