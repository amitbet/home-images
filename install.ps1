[CmdletBinding()]
param(
 [string]$Version = 'latest',
 [string]$Root = 'D:\Photos',
 [string]$Destination = 'C:\Program Files\HomeImages',
 [string]$DataDirectory = 'C:\ProgramData\HomeImages',
 [int]$Port = 8372,
 [string]$PackagePath = '',
 [string]$DownloadBaseUrl = ''
)
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^[A-Za-z0-9._-]+$') { throw 'Invalid release version' }
$architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
switch ($architecture) { 'X64' { $arch = 'amd64' }; 'Arm64' { $arch = 'arm64' }; default { throw "Unsupported architecture: $architecture" } }
$asset = "home-images-windows-$arch.zip"
$temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$work = [IO.Path]::GetFullPath((Join-Path $temporaryRoot ('home-images-install-' + [Guid]::NewGuid().ToString('N'))))
if (-not $work.StartsWith($temporaryRoot + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid temporary path' }
New-Item -ItemType Directory -Path $work | Out-Null
try {
 if ($PackagePath) {
  $archive = (Resolve-Path -LiteralPath $PackagePath).Path
 } else {
  if (-not $DownloadBaseUrl) {
   $DownloadBaseUrl = "https://github.com/amitbet/home-images/releases/download/$Version"
   if ($Version -eq 'latest') { $DownloadBaseUrl = 'https://github.com/amitbet/home-images/releases/latest/download' }
  }
  $archive = Join-Path $work $asset
  Invoke-WebRequest "$DownloadBaseUrl/$asset" -OutFile $archive -UseBasicParsing
  $manifestPath = Join-Path $work 'SHA256SUMS'
  Invoke-WebRequest "$DownloadBaseUrl/SHA256SUMS" -OutFile $manifestPath -UseBasicParsing
  $manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8
  $matchesForAsset = @($manifest -split "`n" | Where-Object { $_.Trim() -match ('^[a-fA-F0-9]{64}\s+' + [regex]::Escape($asset) + '$') })
  if ($matchesForAsset.Count -ne 1) { throw 'Release checksum is missing or ambiguous' }
  $expected = ($matchesForAsset[0].Trim() -split '\s+')[0]
  if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expected) { throw 'Release checksum mismatch' }
 }
 $package = Join-Path $work 'package'
 Expand-Archive -LiteralPath $archive -DestinationPath $package
 foreach ($required in @('home-images.exe','turbojpeg.dll','turbojpeg-licenses\LICENSE.md','install-windows.ps1')) {
  if (-not (Test-Path -LiteralPath (Join-Path $package $required))) { throw "Incomplete release package: $required" }
 }
 & (Join-Path $package 'install-windows.ps1') -BinaryPath (Join-Path $package 'home-images.exe') -Root $Root -Destination $Destination -DataDirectory $DataDirectory -Port $Port
} finally {
 # This is the checked, uniquely generated temporary directory, never an install/data path.
 Remove-Item -LiteralPath $work -Recurse -Force
}
