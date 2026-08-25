$ErrorActionPreference = "Stop"

if (-not $env:SSS_URL) { throw "Set SSS_URL" }
if (-not $env:SSS_PASSWORD) { throw "Set SSS_PASSWORD" }

$User = if ($env:SSS_USER) { $env:SSS_USER } else { "sss" }
$Auth = "${User}:$($env:SSS_PASSWORD)"
$Base = $env:SSS_URL.TrimEnd("/")
$Temp = Join-Path ([System.IO.Path]::GetTempPath()) ("sss-smoke-" + [guid]::NewGuid())

New-Item -ItemType Directory -Path $Temp | Out-Null

try {
    $Alpha = Join-Path $Temp "alpha.txt"
    $Out = Join-Path $Temp "alpha.out"
    Set-Content -NoNewline -Path $Alpha -Value ("alpha-" + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds())

    & curl.exe -fsS -u $Auth "$Base/v1/info" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "info request failed" }

    $Code = (& curl.exe -fsS -u $Auth -F "file=@$Alpha" -F "note=windows smoke" "$Base/s").Trim()
    if ($LASTEXITCODE -ne 0) { throw "send failed" }

    if ($Code -notmatch '^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$') {
        throw "invalid code: $Code"
    }

    & curl.exe -fsS -u $Auth -o $Out "$Base/r/$Code"
    if ($LASTEXITCODE -ne 0) { throw "receive failed" }

    $A = [System.IO.File]::ReadAllBytes($Alpha)
    $B = [System.IO.File]::ReadAllBytes($Out)
    if (-not [System.Linq.Enumerable]::SequenceEqual($A, $B)) {
        throw "round-trip bytes differ"
    }

    Write-Output "PASS: $Code"
}
finally {
    Remove-Item -Recurse -Force $Temp -ErrorAction SilentlyContinue
}
