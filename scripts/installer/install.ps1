# sss client installer for Windows — extracted from an sss handoff, run in place.
#
#   curl.exe -fsS -u sss "https://YOUR-RELAY/r/CODE?format=tar" -o $env:TEMP\sss.tar
#   tar.exe -xf $env:TEMP\sss.tar -C $env:TEMP
#   powershell -ExecutionPolicy Bypass -File $env:TEMP\sss-install\install.ps1
#
# Installs the right binary for this machine, creates the sssend/ssrecv copies
# (Windows has no symlinks without elevation), puts them on PATH, and points the
# client at the relay.
$ErrorActionPreference = 'Stop'

$Url     = if ($env:SSS_URL) { $env:SSS_URL } else { '__SSS_URL__' }
$Version = '1.0.0'
$Src     = Split-Path -Parent $MyInvocation.MyCommand.Path

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
	'AMD64' { 'amd64' }
	'ARM64' { 'arm64' }
	default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$bin = Join-Path $Src "sss-$Version-windows-$arch.exe"
if (-not (Test-Path $bin)) { throw "no binary for windows/$arch in this handoff" }

# Verify against the checksums shipped alongside.
$sums = Join-Path $Src 'SHA256SUMS.txt'
if (Test-Path $sums) {
	$want = (Select-String -Path $sums -Pattern "sss-$Version-windows-$arch.exe" |
		Select-Object -First 1).Line -split '\s+' | Select-Object -First 1
	$got = (Get-FileHash -Algorithm SHA256 $bin).Hash.ToLower()
	if ($want -and $got -ne $want.ToLower()) { throw "checksum mismatch for $bin" }
}

# Per-user install: no elevation needed, and it survives without admin rights.
$dest = Join-Path $env:LOCALAPPDATA 'Programs\sss'
New-Item -ItemType Directory -Force -Path $dest | Out-Null

# The binary dispatches on the name it was invoked with, so these are copies,
# not wrappers.
Copy-Item $bin (Join-Path $dest 'sss.exe')    -Force
Copy-Item $bin (Join-Path $dest 'sssend.exe') -Force
Copy-Item $bin (Join-Path $dest 'ssrecv.exe') -Force

# Add to the user PATH once, and to this session so the next line works.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$dest*") {
	[Environment]::SetEnvironmentVariable('Path', "$userPath;$dest", 'User')
	Write-Host "added $dest to your user PATH (restart terminals to pick it up)"
}
$env:Path = "$env:Path;$dest"

& (Join-Path $dest 'sss.exe') configure --url $Url

Write-Host "installed to $dest"
Write-Host "next: `$env:SSS_PASSWORD='...'; sss doctor"
