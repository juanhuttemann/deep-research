# deep-research installer - Windows (native PowerShell).
# Downloads the latest release binary from GitHub into %LOCALAPPDATA%\deep-research
# and adds that directory to the user PATH.
#
# The live terminal UI is unix-only; on Windows the CLI degrades to headless
# output (one log line per event, then the report). Everything else works.
$ErrorActionPreference = 'Stop'

$repo = 'juanhuttemann/deep-research'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$dir = if ($env:DEEP_RESEARCH_BIN_DIR) { $env:DEEP_RESEARCH_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'deep-research' }
$asset = "deep-research_windows_$arch.zip"
$url = "https://github.com/$repo/releases/latest/download/$asset"
$sumUrl = "https://github.com/$repo/releases/latest/download/deep-research_checksums.txt"

New-Item -ItemType Directory -Force -Path $dir | Out-Null
$zip = Join-Path $env:TEMP $asset
Invoke-WebRequest -Uri $url -OutFile $zip

# A release without a matching checksum is not safe to install.
$sumPath = Join-Path $env:TEMP 'deep-research_checksums.txt'
Invoke-WebRequest -Uri $sumUrl -OutFile $sumPath
$lines = @(Get-Content $sumPath | Where-Object { ($_ -split '\s+')[-1] -eq $asset })
if ($lines.Count -ne 1) { throw "expected exactly one checksum for $asset" }
$want = (($lines[0] -split '\s+')[0]).ToLower()
if ($want -notmatch '^[0-9a-f]{64}$') { throw "invalid checksum for $asset" }
$got = (Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLower()
if ($got -ne $want) { throw "checksum mismatch for $asset" }

Expand-Archive -Path $zip -DestinationPath $dir -Force
Remove-Item $zip
Remove-Item $sumPath

# Check the persisted User PATH (not just this session's) so a directory
# already configured but missing from the current session is not appended
# twice.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ((($userPath -split ';') -notcontains $dir) -and ($dir -ne '')) {
    [Environment]::SetEnvironmentVariable('Path', $userPath + ';' + $dir, 'User')
}
if (($env:Path -split ';') -notcontains $dir) {
    $env:Path += ';' + $dir
}

Write-Host "installed $dir\deep-research.exe"
Write-Host "next: deep-research init, then put your API key in .env"
