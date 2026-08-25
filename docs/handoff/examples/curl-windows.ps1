$ErrorActionPreference = "Stop"

if (-not $env:SSS_URL) {
    throw "Set SSS_URL, for example https://drop.example.com"
}
if (-not $env:SSS_PASSWORD) {
    throw "Set SSS_PASSWORD or adapt the examples to use _netrc"
}

$SssUser = if ($env:SSS_USER) { $env:SSS_USER } else { "sss" }
$Auth = "${SssUser}:$($env:SSS_PASSWORD)"
$Base = $env:SSS_URL.TrimEnd("/")

function Send-SssFile {
    param([Parameter(Mandatory=$true)][string]$Path)

    & curl.exe -fsS `
        -u $Auth `
        -F "file=@$Path" `
        "$Base/s"

    if ($LASTEXITCODE -ne 0) {
        throw "curl send failed with exit code $LASTEXITCODE"
    }
}

function Send-SssFiles {
    param(
        [Parameter(Mandatory=$true)][string]$First,
        [Parameter(Mandatory=$true)][string]$Second,
        [string]$Note = "Review these results.",
        [int]$TtlMinutes = 120
    )

    & curl.exe -fsS `
        -u $Auth `
        -F "file=@$First" `
        -F "file=@$Second" `
        -F "note=$Note" `
        -F "ttl=$TtlMinutes" `
        "$Base/s"

    if ($LASTEXITCODE -ne 0) {
        throw "curl send failed with exit code $LASTEXITCODE"
    }
}

function Receive-Sss {
    param(
        [Parameter(Mandatory=$true)][string]$Code,
        [string]$Output
    )

    if ($Output) {
        & curl.exe -fS -u $Auth -o $Output "$Base/r/$Code"
    } else {
        & curl.exe -fS -u $Auth -OJ "$Base/r/$Code"
    }

    if ($LASTEXITCODE -ne 0) {
        throw "curl receive failed with exit code $LASTEXITCODE"
    }
}

function Get-SssMetadata {
    param([Parameter(Mandatory=$true)][string]$Code)

    & curl.exe -fsS `
        -u $Auth `
        -H "Accept: application/json" `
        "$Base/v1/transfers/$Code"

    if ($LASTEXITCODE -ne 0) {
        throw "curl inspect failed with exit code $LASTEXITCODE"
    }
}
