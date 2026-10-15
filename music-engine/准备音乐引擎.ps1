param([string]$PythonExecutable = 'python', [string]$Repository = '')
$ErrorActionPreference = 'Stop'
$enginePython = Join-Path $PSScriptRoot 'venv\Scripts\python.exe'
$bundledPython = Join-Path $PSScriptRoot 'runtime\python.exe'
if (Test-Path -LiteralPath $bundledPython -PathType Leaf) { $enginePython = $bundledPython }
if (-not (Test-Path -LiteralPath $enginePython)) {
    & $PythonExecutable -m venv (Join-Path $PSScriptRoot 'venv')
    if ($LASTEXITCODE -ne 0) { throw 'Python 3.10 or newer is required.' }
}
if ($Repository) {
    & $enginePython -m pip install --disable-pip-version-check $Repository
} else {
    & $enginePython -m pip install --disable-pip-version-check 'musicdl==2.14.1'
}
if ($LASTEXITCODE -ne 0) { throw 'Music engine installation failed. Retry after checking your network.' }
& $enginePython -c 'from musicdl.modules.sources import MusicClientBuilder; import musicdl; print(musicdl.__version__, len(MusicClientBuilder.REGISTERED_MODULES))'
if ($LASTEXITCODE -ne 0) { throw 'Music engine import failed.' }
Write-Host 'Music engine is ready. Reopen the music page in ResourceLens.'
