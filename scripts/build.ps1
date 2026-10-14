param([string]$GoCommand = 'go')
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
Push-Location $projectRoot
$savedBuildEnv = @{CGO_ENABLED=$env:CGO_ENABLED; GOOS=$env:GOOS; GOARCH=$env:GOARCH}
try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $releaseName = 'campus-network-go-windows-amd64'
    $releaseDir = Join-Path $projectRoot "dist\$releaseName"
    New-Item -ItemType Directory -Path $releaseDir -Force | Out-Null
    & $GoCommand build -trimpath -ldflags '-s -w' -o (Join-Path $releaseDir 'campus-network.exe') ./cmd/campus-net
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
    Copy-Item -LiteralPath (Join-Path $releaseDir 'campus-network.exe') -Destination (Join-Path $projectRoot 'campus-network.exe') -Force
    foreach ($file in @('config.example.toml','README.md','LICENSE','THIRD_PARTY_NOTICES.md','clash-campus-direct.yaml')) {
        Copy-Item -LiteralPath $file -Destination (Join-Path $releaseDir $file) -Force
    }
    Copy-Item -LiteralPath 'LICENSES' -Destination $releaseDir -Recurse -Force
    $zip = Join-Path $projectRoot "dist\$releaseName.zip"
    $packageItems = @('campus-network.exe','config.example.toml','README.md','LICENSE','THIRD_PARTY_NOTICES.md','clash-campus-direct.yaml','LICENSES') | ForEach-Object { Join-Path $releaseDir $_ }
    Compress-Archive -LiteralPath $packageItems -DestinationPath $zip -Force
    $hashes = @((Get-FileHash -LiteralPath (Join-Path $releaseDir 'campus-network.exe') -Algorithm SHA256), (Get-FileHash -LiteralPath $zip -Algorithm SHA256))
    $hashes | ForEach-Object { "$($_.Hash.ToLower())  $(Split-Path $_.Path -Leaf)" } | Set-Content -LiteralPath (Join-Path $projectRoot 'dist\SHA256SUMS.txt') -Encoding ascii
    $hashes | Select-Object Path,Hash
} finally {
    $env:CGO_ENABLED=$savedBuildEnv.CGO_ENABLED
    $env:GOOS=$savedBuildEnv.GOOS
    $env:GOARCH=$savedBuildEnv.GOARCH
    Pop-Location
}
