param(
    [string]$OutputDirectory = (Join-Path (Split-Path -Parent $PSScriptRoot) 'release'),
    [string]$Compiler = (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\installer\inno-6.7.3\ISCC.exe')
)
$ErrorActionPreference = 'Stop'
$installerProject = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path -LiteralPath $Compiler -PathType Leaf)) { throw 'Inno Setup 6.7.3 compiler not found; pass -Compiler with the ISCC.exe path.' }
$installerApp = Get-Item -LiteralPath (Join-Path $installerProject 'ResourceLens.exe')
if ($installerApp.VersionInfo.ProductVersion -ne '1.2.0') { throw 'Installer script expects ResourceLens 1.2.0.' }
foreach ($installerTool in @('ffmpeg.exe','ffprobe.exe','yt-dlp.exe','node.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $installerProject ('video-engine\' + $installerTool)) -PathType Leaf)) { throw ('Missing bundled tool: ' + $installerTool) }
}
$installerBootstrap = Join-Path $PSScriptRoot 'deps\MicrosoftEdgeWebview2Setup.exe'
$installerSignature = Get-AuthenticodeSignature -LiteralPath $installerBootstrap
if ($installerSignature.Status -ne 'Valid' -or $installerSignature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') { throw 'WebView2 bootstrapper must have a valid Microsoft signature.' }
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$installerOutput = (Resolve-Path -LiteralPath $OutputDirectory).Path
& $Compiler ('/DReleaseOutput=' + $installerOutput) (Join-Path $PSScriptRoot 'ResourceLens.iss')
if ($LASTEXITCODE -ne 0) { throw ('Installer compiler failed: ' + $LASTEXITCODE) }
$installerExe = Join-Path $installerOutput '拾流-1.2.0-安装程序.exe'
if (-not (Test-Path -LiteralPath $installerExe -PathType Leaf)) { throw 'Installer output not found.' }
$installerManifest = [pscustomobject]@{
    version='1.2.0'
    file=$installerExe
    bytes=(Get-Item -LiteralPath $installerExe).Length
    sha256=(Get-FileHash -LiteralPath $installerExe -Algorithm SHA256).Hash.ToLower()
    applicationSha256=(Get-FileHash -LiteralPath $installerApp.FullName -Algorithm SHA256).Hash.ToLower()
    bootstrapperSha256=(Get-FileHash -LiteralPath $installerBootstrap -Algorithm SHA256).Hash.ToLower()
    compilerVersion='6.7.3'
    compilerSha256=(Get-FileHash -LiteralPath $Compiler -Algorithm SHA256).Hash.ToLower()
    desktopShortcut='拾流 ResourceLens.lnk'
}
$installerManifest | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $installerOutput '安装程序校验-v1.2.0.json') -Encoding utf8
$installerManifest | ConvertTo-Json -Depth 3
