# Runs on disposable CI hosts. Refuses to replace an existing HomeImages service.
$ErrorActionPreference = 'Stop'
$base = Join-Path ([IO.Path]::GetTempPath()) ('home-images-installer-' + [Guid]::NewGuid().ToString('N'))
$root = Join-Path $base 'photos'
$data = Join-Path $base 'data'
$destination = Join-Path $base 'program/home-images'
$config = if ($IsWindows) { Join-Path $data 'config.json' } else { Join-Path $base 'config.json' }
$bundle = Join-Path $base 'bundle'
$suffix = if ($IsWindows) { '.exe' } else { '' }
$codec = if ($IsWindows) { 'turbojpeg.dll' } elseif ($IsMacOS) { 'libturbojpeg.dylib' } else { 'libturbojpeg.so' }
$binary = Join-Path $destination "home-images$suffix"
New-Item -ItemType Directory -Force -Path $root | Out-Null
New-Item -ItemType Directory -Path $bundle | Out-Null
$arch = if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -eq 'Arm64') { 'arm64' } else { 'amd64' }
$platform = if ($IsWindows) { 'windows' } elseif ($IsMacOS) { 'darwin' } else { 'linux' }
$extension = if ($IsWindows) { 'zip' } else { 'tar.gz' }
$archive = (Resolve-Path "dist/release/home-images-$platform-$arch.$extension").Path
if ($IsWindows) { Expand-Archive -LiteralPath $archive -DestinationPath $bundle -Force }
else { & tar -xzf $archive -C $bundle; if ($LASTEXITCODE -ne 0) { throw 'Release extraction failed' } }
Copy-Item testdata/orientation6.jpg (Join-Path $root 'photo.jpg')
$probe = Join-Path $base 'probe.json'
@{root=$root; data_dir=$data; host='127.0.0.1'; port=18379; scan_interval_seconds=30; max_pixels=80000000} | ConvertTo-Json | Set-Content $probe
$probeBinary = Join-Path $bundle "home-images$suffix"
if ($IsWindows) { & $probeBinary --config $probe --service status 2>$null }
else { & sudo $probeBinary --config $probe --service status 2>$null }
if ($LASTEXITCODE -eq 0) { throw 'Installer smoke test requires a host without HomeImages installed' }
function Native([string[]]$Arguments) {
 if ($IsWindows) { & $script:binary @Arguments } else { & sudo $script:binary @Arguments }
 if ($LASTEXITCODE -ne 0) { throw "Service command failed: $Arguments" }
}
function Install-Bundle {
 if ($IsWindows) {
  & ./install.ps1 -PackagePath $archive -Root $root -Destination $destination -DataDirectory $data -Port 18379
 } else {
  & sudo bash (Join-Path $bundle 'install-unix.sh') --root $root --prefix $destination --config $config --data-dir $data --port 18379 --host 127.0.0.1
  if ($LASTEXITCODE -ne 0) { throw 'Unix installer failed' }
 }
}
function Wait-Healthy {
 for ($attempt=0; $attempt -lt 60; $attempt++) {
  try {
   $health = Invoke-RestMethod http://127.0.0.1:18379/health -TimeoutSec 2
   if ($health.images -eq 1 -and $health.jpeg_decoder -eq 'turbojpeg') { return }
  } catch {}
  Start-Sleep -Seconds 1
 }
 throw 'Installer did not start a healthy, natively accelerated service'
}
try {
 Install-Bundle
 # The Windows installer stores its config beneath DataDirectory.
 if ($IsWindows) { $config = Join-Path $data 'config.json' }
 Wait-Healthy
 $photo = Invoke-RestMethod 'http://127.0.0.1:18379/api/next?mode=sequential'
 Invoke-RestMethod ('http://127.0.0.1:18379/api/curation/' + $photo.id) -Method Post -Headers @{'X-Home-Images'='1'} -ContentType 'application/json' -Body '{"favorite":true,"title":"Installer preservation test"}' | Out-Null
 $configHash = (Get-FileHash $config).Hash
 # Reinstall the same package to exercise program/codec replacement and backups.
 Install-Bundle
 Wait-Healthy
 if ((Get-FileHash $config).Hash -ne $configHash) { throw 'Upgrade changed the existing configuration' }
 $annotation = Invoke-RestMethod ('http://127.0.0.1:18379/api/curation/' + $photo.id)
 if (-not $annotation.favorite -or $annotation.title -ne 'Installer preservation test') { throw 'Upgrade lost user annotations' }
 if ((Get-FileHash (Join-Path $destination $codec)).Hash -ne (Get-FileHash (Join-Path $bundle $codec)).Hash) { throw 'Installed codec differs from bundled codec' }
 $image = Invoke-WebRequest ('http://127.0.0.1:18379' + $photo.image_url)
 if ($image.Headers['Content-Type'] -notlike '*image/jpeg*') { throw 'Installed service failed image rendering' }
 Native @('--config',$config,'--service','stop')
 Native @('--config',$config,'--service','start')
 Wait-Healthy
 Write-Host 'Install/upgrade/native rendering/restart/config and annotation preservation passed'
} finally {
 if (Test-Path -LiteralPath $binary) {
  if ($IsWindows) {
   & $binary --config $config --service stop 2>$null
   & $binary --config $config --service uninstall 2>$null
   Get-NetFirewallRule -Name HomeImages-LAN -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  } else {
   & sudo $binary --config $config --service stop 2>$null
   & sudo $binary --config $config --service uninstall 2>$null
  }
 }
}
