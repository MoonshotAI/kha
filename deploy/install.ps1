# Install the kha CLI: the latest stable release by default, -Version 0.X.Y
# (or KHA_VERSION) for a specific one. KHA_RELEASE_BASE_URL overrides the download site.
[CmdletBinding()]
param(
    [string]$InstallDir,
    [ValidateSet('amd64', 'arm64')]
    [string]$Architecture,
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$baseUrl = if ($env:KHA_RELEASE_BASE_URL) { $env:KHA_RELEASE_BASE_URL } else { 'https://ha-static.moonshot.cn/kha' }
if (-not $Version) { $Version = $env:KHA_VERSION }
if (-not $Version) {
    try {
        $Version = (Invoke-WebRequest "$baseUrl/stable.txt" -UseBasicParsing).Content.Trim()
    } catch {
        throw "Could not resolve the latest stable release from $baseUrl/stable.txt; pass -Version 0.X.Y (or set KHA_VERSION) to install a specific release."
    }
}
$release = 'v' + $Version.TrimStart('v')
if (-not $Architecture) {
    $nativeArch = $env:PROCESSOR_ARCHITEW6432
    if (-not $nativeArch) { $nativeArch = $env:PROCESSOR_ARCHITECTURE }
    $Architecture = switch ($nativeArch) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw "Unsupported architecture: $nativeArch" }
    }
}
if (-not $InstallDir) {
    $InstallDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'kha\bin'
}
$binary = "kha-windows-$Architecture.exe"
$base = "$baseUrl/$release"
$workdir = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $workdir | Out-Null
try {
    $download = Join-Path $workdir $binary
    Invoke-WebRequest "$base/$binary" -UseBasicParsing -OutFile $download
    $checksums = Join-Path $workdir 'checksums.txt'
    Invoke-WebRequest "$base/checksums.txt" -UseBasicParsing -OutFile $checksums
    $entry = @(Get-Content $checksums | Where-Object {
        ($_ -split '\s+')[1] -eq $binary
    })
    if ($entry.Count -ne 1) { throw 'Missing or duplicate checksum' }
    $expected = ($entry[0] -split '\s+')[0]
    if ((Get-FileHash $download -Algorithm SHA256).Hash -ne $expected) {
        throw 'SHA-256 mismatch; installation stopped'
    }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path (Resolve-Path $InstallDir).Path 'kha.exe'
    Copy-Item $download $target -Force
    & $target version
    if ($LASTEXITCODE -ne 0) { throw "kha version failed: $LASTEXITCODE" }
    Write-Output "Installed to $target"
    Write-Output "Add this directory to PATH: $InstallDir"
} finally {
    Remove-Item -Recurse -Force $workdir
}
