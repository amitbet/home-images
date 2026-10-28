# Exercise release download/checksum handling without installing a real service.
$ErrorActionPreference = 'Stop'
$onWindows = [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)
$onMacOS = [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)
$base = Join-Path ([IO.Path]::GetTempPath()) ('home-images-download-' + [Guid]::NewGuid().ToString('N'))
$fixture = Join-Path $base 'fixture'
$serve = Join-Path $base 'serve'
New-Item -ItemType Directory -Force -Path $fixture,$serve | Out-Null
$arch = if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -eq 'Arm64') { 'arm64' } else { 'amd64' }
$os = if ($onWindows) { 'windows' } elseif ($onMacOS) { 'darwin' } else { 'linux' }
$marker = Join-Path $base 'invoked.txt'
$oldMarker = $env:HOME_IMAGES_BOOTSTRAP_TEST_MARKER
$oldBase = $env:HOME_IMAGES_RELEASE_BASE_URL
$env:HOME_IMAGES_BOOTSTRAP_TEST_MARKER = $marker
if ($onWindows) {
 @'
param($BinaryPath,$Root,$Destination,$DataDirectory,$Port)
[IO.File]::WriteAllText($env:HOME_IMAGES_BOOTSTRAP_TEST_MARKER, $Root)
'@ | Set-Content (Join-Path $fixture 'install-windows.ps1')
 New-Item -ItemType Directory -Path (Join-Path $fixture 'turbojpeg-licenses') | Out-Null
 foreach ($file in @('home-images.exe','turbojpeg.dll','turbojpeg-licenses/LICENSE.md')) { 'fixture' | Set-Content (Join-Path $fixture $file) }
 $asset = "home-images-$os-$arch.zip"
 Compress-Archive -Path (Join-Path $fixture '*') -DestinationPath (Join-Path $serve $asset)
} else {
 "#!/usr/bin/env bash`nprintf '%s' `"`$*`" > `"`$HOME_IMAGES_BOOTSTRAP_TEST_MARKER`"`n" | Set-Content (Join-Path $fixture 'install-unix.sh')
 $asset = "home-images-$os-$arch.tar.gz"
 & tar -czf (Join-Path $serve $asset) -C $fixture .
 if ($LASTEXITCODE -ne 0) { throw 'Fixture archive failed' }
}
$archive = Join-Path $serve $asset
$manifest = Join-Path $serve 'SHA256SUMS'
((Get-FileHash $archive).Hash.ToLowerInvariant() + '  ' + $asset) | Set-Content $manifest
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$listener.Start()
$port = $listener.LocalEndpoint.Port
$listener.Stop()
$python = if ($onWindows) { (Get-Command python).Source } else { (Get-Command python3).Source }
# Run the server through a PowerShell job so native process startup/output is
# handled consistently on macOS as well as Windows and Linux.
$server = Start-Job -ScriptBlock {
 param($Interpreter,$Directory,$ListenPort)
 Write-Output "Starting fixture server: $Interpreter on $ListenPort in $Directory"
 & $Interpreter -u -m http.server $ListenPort --bind 127.0.0.1 --directory $Directory
} -ArgumentList $python,$serve,$port
$baseUrl = "http://127.0.0.1:$port"
function Run-Download([bool]$ShouldSucceed) {
 $success = $false
 if ($onWindows) {
  try { & ./install.ps1 -DownloadBaseUrl $baseUrl -Root 'photos with spaces'; $success = $true } catch { if ($ShouldSucceed) { throw } }
 } else {
  $env:HOME_IMAGES_RELEASE_BASE_URL = $baseUrl
  & bash ./install.sh --root 'photos with spaces'
  $success = $LASTEXITCODE -eq 0
 }
 if ($success -ne $ShouldSucceed) { throw "Download installer success=$success, expected=$ShouldSucceed" }
 if ((Test-Path $marker) -ne $ShouldSucceed) { throw 'Invalid download reached the bundled installer' }
 # Rejection tests intentionally return nonzero. Do not leak that expected
 # native exit code to GitHub's PowerShell wrapper after assertions pass.
 $global:LASTEXITCODE = 0
}
try {
 $ready = $false
 for ($attempt=0; $attempt -lt 30; $attempt++) {
  try { Invoke-WebRequest "$baseUrl/SHA256SUMS" -UseBasicParsing -TimeoutSec 1 | Out-Null; $ready=$true; break } catch { $lastRequestError = $_; Start-Sleep -Milliseconds 100 }
 }
 if (-not $ready) {
  Write-Host "Fixture server executable: $python; port: $port; state: $($server.State)"
  Write-Host "Last readiness request: $lastRequestError"
  Receive-Job $server -ErrorAction Continue | Out-Host
  if (-not $onWindows) { & curl --noproxy '*' --verbose --max-time 3 "$baseUrl/SHA256SUMS"; & pgrep -lf 'http.server' }
  throw 'Fixture HTTP server did not start'
 }
 Run-Download $true
 if ((Get-Content $marker -Raw) -notlike '*photos with spaces*') { throw 'Installer arguments were not forwarded' }
 Remove-Item -LiteralPath $marker
 [IO.File]::AppendAllText($archive,'corrupt')
 Run-Download $false
 'no checksum for this asset' | Set-Content $manifest
 Run-Download $false
 Write-Host 'Verified release download, argument forwarding, corrupt archive rejection and missing checksum rejection'
} finally {
 Stop-Job $server -ErrorAction SilentlyContinue
 Remove-Job $server -Force -ErrorAction SilentlyContinue
 $env:HOME_IMAGES_BOOTSTRAP_TEST_MARKER = $oldMarker
 $env:HOME_IMAGES_RELEASE_BASE_URL = $oldBase
}
