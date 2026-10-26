$ErrorActionPreference = 'Stop'
function Native([string[]]$Arguments) {
    if ($IsWindows) { & $script:binary @Arguments }
    else { & sudo $script:binary @Arguments }
    if ($LASTEXITCODE -ne 0) { throw "Daemon command failed: $Arguments" }
}
$base = Join-Path ([IO.Path]::GetTempPath()) ('home-images-daemon-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $base | Out-Null
$root = Join-Path $base 'photos'
New-Item -ItemType Directory -Path $root | Out-Null
Copy-Item testdata/orientation6.jpg (Join-Path $root 'photo.jpg')
$script:binary = Join-Path $base ('home-images' + $(if ($IsWindows) { '.exe' } else { '' }))
go build -o $script:binary .
if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
if ($IsWindows -and $env:HOMEIMAGES_TEST_TURBO_DLL) {
    Copy-Item -LiteralPath $env:HOMEIMAGES_TEST_TURBO_DLL -Destination (Join-Path $base 'turbojpeg.dll')
}

$config = Join-Path $base 'config.json'
@{root=$root; data_dir=(Join-Path $base 'data'); host='127.0.0.1'; port=18379; scan_interval_seconds=30; max_pixels=80000000} | ConvertTo-Json | Set-Content $config
$argsBase = @('--config', $config)
$installed = $false
try {
    $installArgs = $argsBase + @('--service','install')
    if ($IsWindows) { $installArgs += @('--user', 'LocalSystem') }
    Native $installArgs
    $installed = $true
    Native ($argsBase + @('--service','start'))
    $ready = $false
    for ($i=0; $i -lt 30; $i++) {
        try { $health=Invoke-RestMethod http://127.0.0.1:18379/health; if ($health.images -eq 1) { $ready=$true;break } } catch {}
        Start-Sleep -Seconds 1
    }
    if (-not $ready) { throw 'Daemon did not index the fixture' }
    if ($IsWindows -and $env:HOMEIMAGES_TEST_TURBO_DLL -and $health.jpeg_decoder -ne 'turbojpeg') { throw 'Native JPEG decoder was not loaded' }
    $photo = Invoke-RestMethod 'http://127.0.0.1:18379/api/next?mode=sequential'
    $image = Invoke-WebRequest ('http://127.0.0.1:18379' + $photo.image_url)
    if ($image.StatusCode -ne 200 -or $image.Headers['Content-Type'] -notlike '*image/jpeg*') { throw 'Daemon image delivery failed' }
    Native ($argsBase + @('--service','stop'))
    Native ($argsBase + @('--service','start'))
    $ready = $false
    for ($i=0; $i -lt 30; $i++) {
        try { $health=Invoke-RestMethod http://127.0.0.1:18379/health; if ($health.images -eq 1) { $ready=$true;break } } catch {}
        Start-Sleep -Seconds 1
    }
    if (-not $ready) { throw 'Daemon did not recover after restart' }
    Write-Host 'Native service install/start/image delivery/stop/start passed'
} finally {
    if ($installed) {
        Native ($argsBase + @('--service','stop'))
        Native ($argsBase + @('--service','uninstall'))
    }
}
