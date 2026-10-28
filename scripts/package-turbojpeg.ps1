# Package the optional Windows codec without installing it system-wide.
[CmdletBinding()]
param([string]$OutputDirectory = '', [ValidateSet('amd64','arm64')][string]$Architecture = 'amd64')
$ErrorActionPreference = 'Stop'
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot '..\dist' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$version = '3.2.0'
if ($Architecture -eq 'arm64') {
 $packageArch = 'arm64'
 $expected = '269FC323BFCF6E1AD45138AF808F7BD912447E9282DB54635C72F6A889075BBA'
} else {
 $packageArch = 'x64'
 $expected = '662761D8BA8DAE04AEC74023EBAECEB856C2B56B9B59CFD180759D26300DDA42'
}
$installer = Join-Path $OutputDirectory "turbojpeg-installer-$Architecture.exe"
if (-not (Test-Path -LiteralPath $installer)) {
 Invoke-WebRequest "https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/$version/libjpeg-turbo-$version-vc-$packageArch.exe" -OutFile $installer
}
if ((Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash -ne $expected) { throw 'TurboJPEG download checksum mismatch' }
$candidates = @((Join-Path $env:ProgramFiles '7-Zip\7z.exe'), 'C:\ProgramData\chocolatey\tools\7z.exe')
$sevenZip = $candidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $sevenZip) { $command = Get-Command 7z -ErrorAction SilentlyContinue; if ($command) { $sevenZip = $command.Source } }
if (-not $sevenZip) { throw '7-Zip is required to extract the pinned optional codec package' }
$extracted = Join-Path $OutputDirectory "turbojpeg-package-$Architecture"
& $sevenZip x $installer "-o$extracted" -y | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'TurboJPEG extraction failed' }
Copy-Item -LiteralPath (Join-Path $extracted 'bin\turbojpeg.dll') -Destination $OutputDirectory -Force
$licenseDir = Join-Path $OutputDirectory 'turbojpeg-licenses'
New-Item -ItemType Directory -Force -Path $licenseDir | Out-Null
foreach ($name in @('LICENSE.md','README.ijg','README.md')) {
 Copy-Item -LiteralPath (Join-Path $extracted "doc\$name") -Destination $licenseDir -Force
}
Write-Output (Join-Path $OutputDirectory 'turbojpeg.dll')
