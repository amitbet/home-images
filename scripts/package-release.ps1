[CmdletBinding()]
param([Parameter(Mandatory)][string]$OS, [Parameter(Mandatory)][string]$Architecture, [string]$Bundle = 'dist/package', [string]$Output = 'dist/release')
$ErrorActionPreference = 'Stop'
$bundlePath = (Resolve-Path -LiteralPath $Bundle).Path
New-Item -ItemType Directory -Force -Path $Output | Out-Null
$outputPath = (Resolve-Path -LiteralPath $Output).Path
Copy-Item README.md,LICENSE,THIRD-PARTY-NOTICES.md,config.example.json -Destination $bundlePath
if ($OS -eq 'windows') {
 Copy-Item install-windows.ps1,install.ps1 -Destination $bundlePath
 $required = @('home-images.exe','turbojpeg.dll','turbojpeg-licenses/LICENSE.md','install-windows.ps1')
} else {
 Copy-Item install-unix.sh,install.sh -Destination $bundlePath
 $codec = if ($OS -eq 'darwin') { 'libturbojpeg.dylib' } else { 'libturbojpeg.so' }
 $required = @('home-images',$codec,'turbojpeg-licenses/LICENSE.md','install-unix.sh')
}
foreach ($name in $required) { if (-not (Test-Path -LiteralPath (Join-Path $bundlePath $name))) { throw "Incomplete package: $name" } }
$temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
$stage = [IO.Path]::GetFullPath((Join-Path $temporaryRoot ('home-images-bundle-' + [Guid]::NewGuid().ToString('N'))))
if (-not $stage.StartsWith($temporaryRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid staging path' }
New-Item -ItemType Directory -Path $stage | Out-Null
try {
 # Do not ship the downloaded upstream installer, SDK, or extraction directory.
 $files = @('README.md','LICENSE','THIRD-PARTY-NOTICES.md','config.example.json')
 if ($OS -eq 'windows') { $files += @('home-images.exe','turbojpeg.dll','install.ps1','install-windows.ps1') }
 else { $files += @('home-images',$codec,'install.sh','install-unix.sh') }
 foreach ($file in $files) { Copy-Item -LiteralPath (Join-Path $bundlePath $file) -Destination $stage }
 Copy-Item -LiteralPath (Join-Path $bundlePath 'turbojpeg-licenses') -Destination $stage -Recurse
 Copy-Item -LiteralPath 'third-party-licenses' -Destination $stage -Recurse
 if ($OS -eq 'windows') {
  $archive = Join-Path $outputPath "home-images-$OS-$Architecture.zip"
  Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -Force
 } else {
  & chmod 755 (Join-Path $stage 'home-images') (Join-Path $stage $codec)
  if ($LASTEXITCODE -ne 0) { throw 'Bundle permissions failed' }
  $archive = Join-Path $outputPath "home-images-$OS-$Architecture.tar.gz"
  & tar -czf $archive -C $stage .
  if ($LASTEXITCODE -ne 0) { throw 'Bundle archive creation failed' }
 }
} finally {
 Remove-Item -LiteralPath $stage -Recurse -Force
}
Write-Output $archive
