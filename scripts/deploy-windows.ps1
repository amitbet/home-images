#Requires -RunAsAdministrator
<#
Builds this checkout and installs it as the HomeImages Windows service.

Run scripts\deploy-windows.cmd (bypasses the execution policy for this run and
asks for administrator rights), or from an elevated PowerShell:

  git pull; .\scripts\deploy-windows.ps1

Requires Go (see go.mod). The TurboJPEG codec is packaged with 7-Zip when it is
available, otherwise the currently installed codec is reused, so an update never
silently drops JPEG acceleration. Existing config.json always wins.
#>
[CmdletBinding()]
param(
    [string]$Destination = 'C:\Program Files\HomeImages',
    [string]$DataDirectory = 'C:\ProgramData\HomeImages'
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$package = Join-Path $repo 'dist\package'
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'Go is not on PATH; install the version in go.mod' }

Push-Location $repo
try {
    $commit = 'local'
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $commit = (git rev-parse --short HEAD).Trim()
        if (git status --porcelain) { $commit += '-dirty' }
    }
    New-Item -ItemType Directory -Force -Path $package | Out-Null
    $binary = Join-Path $package 'home-images.exe'
    Remove-Item -LiteralPath $binary -Force -ErrorAction SilentlyContinue
    Write-Host "Building home-images $commit"
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags "-s -w -X main.version=$commit" -o $binary .
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally { Pop-Location }

$codec = Join-Path $package 'turbojpeg.dll'
$codecLicenses = Join-Path $package 'turbojpeg-licenses'
$sevenZip = @((Join-Path $env:ProgramFiles '7-Zip\7z.exe'), 'C:\ProgramData\chocolatey\tools\7z.exe') |
    Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if ($sevenZip -or (Get-Command 7z -ErrorAction SilentlyContinue)) {
    & (Join-Path $PSScriptRoot 'package-turbojpeg.ps1') -OutputDirectory $package
} elseif ((Test-Path -LiteralPath (Join-Path $Destination 'turbojpeg.dll')) -and
          (Test-Path -LiteralPath (Join-Path $Destination 'turbojpeg-licenses') -PathType Container)) {
    Write-Host 'Reusing the installed TurboJPEG codec (7-Zip not found)'
    Copy-Item -LiteralPath (Join-Path $Destination 'turbojpeg.dll') -Destination $codec -Force
    Remove-Item -LiteralPath $codecLicenses -Recurse -Force -ErrorAction SilentlyContinue
    Copy-Item -LiteralPath (Join-Path $Destination 'turbojpeg-licenses') -Destination $codecLicenses -Recurse
} else {
    Write-Warning 'No TurboJPEG codec (install 7-Zip to package it); JPEG decoding will use the slower built-in decoder'
}

& (Join-Path $repo 'install-windows.ps1') -BinaryPath $binary -Destination $Destination -DataDirectory $DataDirectory

$health = $null
for ($attempt = 0; $attempt -lt 30 -and -not $health; $attempt++) {
    try { $health = Invoke-RestMethod 'http://localhost:8372/health' -UseBasicParsing } catch { Start-Sleep -Seconds 2 }
}
if (-not $health) { throw "Service did not answer /health after install; see $DataDirectory\home-images.log" }
Get-Service HomeImages | Format-Table -AutoSize Name, Status
$health | Format-List
