#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [string]$BinaryPath = '',
    [string]$Destination = 'C:\Program Files\HomeImages',
    [string]$DataDirectory = 'C:\ProgramData\HomeImages',
    [string]$Root = 'D:\Photos',
    [int]$Port = 8372
)
$ErrorActionPreference = 'Stop'
if (-not $BinaryPath) {
    $BinaryPath = Join-Path $PSScriptRoot 'home-images.exe'
    if (-not (Test-Path -LiteralPath $BinaryPath)) { $BinaryPath = Join-Path $PSScriptRoot 'dist\home-images-windows-amd64.exe' }
}
function Check-Exit([string]$Action) { if ($LASTEXITCODE -ne 0) { throw "$Action failed: exit $LASTEXITCODE" } }
function Ensure-ServiceAccess([string]$Path, [Security.AccessControl.FileSystemRights]$Rights, [string]$Grant) {
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'
    foreach ($rule in (Get-Acl -LiteralPath $Path).Access) {
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($sid -eq 'S-1-5-19' -and $rule.AccessControlType -eq 'Allow' -and
            ($rule.FileSystemRights -band $Rights) -eq $Rights -and
            ($rule.InheritanceFlags -band $inherit) -eq $inherit -and $rule.PropagationFlags -eq 'None') { return }
    }
    & icacls $Path /grant $Grant | Out-Null
    Check-Exit "Grant service access to $Path"
}
function Wait-ProgramFiles([string]$Directory) {
    # Service status can change before its last preview child releases the files.
    for ($attempt=0; $attempt -lt 50; $attempt++) {
        $ready = $true
        foreach ($name in @('home-images.exe','turbojpeg.dll')) {
            $path = Join-Path $Directory $name
            if (Test-Path -LiteralPath $path) {
                try { $handle = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None); $handle.Dispose() }
                catch [IO.IOException] { $ready = $false }
            }
        }
        if ($ready) { return }
        Start-Sleep -Milliseconds 200
    }
    throw 'A preview worker is still shutting down; installation was not replaced. Retry shortly.'
}
$BinaryPath = (Resolve-Path -LiteralPath $BinaryPath).Path
$Destination = [IO.Path]::GetFullPath($Destination).TrimEnd('\')
$DataDirectory = [IO.Path]::GetFullPath($DataDirectory).TrimEnd('\')
# Validate directory boundaries before any directory move, including rollback.
if ($Destination -eq [IO.Path]::GetPathRoot($Destination).TrimEnd('\') -or
    $DataDirectory.StartsWith("$Destination\", [StringComparison]::OrdinalIgnoreCase) -or
    $Destination.StartsWith("$DataDirectory\", [StringComparison]::OrdinalIgnoreCase) -or
    $Destination -eq $DataDirectory -or
    $BinaryPath.StartsWith("$Destination\", [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Installation/data directories must be separate, non-root paths; source binary must be outside the installation'
}
if ((Test-Path -LiteralPath $Destination) -and (Get-Item -LiteralPath $Destination).Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) { throw 'Installation destination cannot be a junction or symlink' }
New-Item -ItemType Directory -Force $DataDirectory | Out-Null
$configPath = Join-Path $DataDirectory 'config.json'
if (-not (Test-Path -LiteralPath $configPath)) {
    @{root=$Root; data_dir=$DataDirectory; host='0.0.0.0'; port=$Port; scan_interval_seconds=900; cors_origins=@('*'); ffmpeg='ffmpeg'; max_pixels=80000000} |
        ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
if (-not (Test-Path -LiteralPath $config.root -PathType Container)) { throw "Photo root does not exist: $($config.root)" }
if ([IO.Path]::GetFullPath($config.data_dir).TrimEnd('\') -ne $DataDirectory) { throw 'config.data_dir must match DataDirectory' }
# Grant inherited access only when it is missing, before stopping the service.
# Reapplying an unchanged photo-tree ACL can otherwise make upgrades very slow.
Ensure-ServiceAccess $DataDirectory Modify '*S-1-5-19:(OI)(CI)(M)'
Ensure-ServiceAccess $config.root ReadAndExecute '*S-1-5-19:(OI)(CI)(RX)'
# Grant writes only to the new archive, never to the existing photo collection.
$phoneArchive = Join-Path $config.root 'Phone backups'
if ((Test-Path -LiteralPath $phoneArchive) -and (Get-Item -LiteralPath $phoneArchive).Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) { throw 'Phone backups cannot be a junction or symlink' }
New-Item -ItemType Directory -Force -Path $phoneArchive | Out-Null
Ensure-ServiceAccess $phoneArchive Modify '*S-1-5-19:(OI)(CI)(M)'
$old = Get-CimInstance Win32_Service -Filter "Name='HomeImages'"
$legacyIndex = Join-Path $DataDirectory 'index.sqlite3'
if ((Test-Path -LiteralPath $legacyIndex) -and (-not $old -or $old.PathName -notmatch 'home-images\.exe')) {
    # Do the potentially large import before stopping the existing service.
    & $BinaryPath --config $configPath --import-python-index $legacyIndex
    Check-Exit 'Import previous photo index'
}
$backup = Join-Path $DataDirectory ('backups\before-go-' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
New-Item -ItemType Directory -Force $backup | Out-Null
Copy-Item -LiteralPath $configPath -Destination (Join-Path $backup 'config.json')
if ($old) { $old | Select-Object Name,PathName,StartName,StartMode,State | ConvertTo-Json | Set-Content (Join-Path $backup 'service.json') }
$oldProgram = Join-Path $backup 'program'
$failedProgram = Join-Path $backup 'failed-program'
$binary = Join-Path $Destination 'home-images.exe'
$moved = $false
$installedNew = $false
try {
    if ($old) { Stop-Service HomeImages; (Get-Service HomeImages).WaitForStatus('Stopped', '00:00:30'); Wait-ProgramFiles $Destination }
    if (Test-Path -LiteralPath $Destination) { Move-Item -LiteralPath $Destination -Destination $oldProgram; $moved = $true }
    New-Item -ItemType Directory -Force $Destination | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination $binary
    $sourceDirectory = Split-Path -Parent $BinaryPath
    foreach ($notice in @('LICENSE','THIRD-PARTY-NOTICES.md')) {
        $noticePath = Join-Path $sourceDirectory $notice
        if (Test-Path -LiteralPath $noticePath) { Copy-Item -LiteralPath $noticePath -Destination $Destination }
    }
    $dependencyLicenses = Join-Path $sourceDirectory 'third-party-licenses'
    if (Test-Path -LiteralPath $dependencyLicenses) { Copy-Item -LiteralPath $dependencyLicenses -Destination $Destination -Recurse }
    $codec = Join-Path (Split-Path -Parent $BinaryPath) 'turbojpeg.dll'
    if (Test-Path -LiteralPath $codec) {
        Copy-Item -LiteralPath $codec -Destination $Destination
        $codecLicenses = Join-Path (Split-Path -Parent $BinaryPath) 'turbojpeg-licenses'
        if (-not (Test-Path -LiteralPath $codecLicenses -PathType Container)) { throw 'TurboJPEG license files are missing; run scripts/package-turbojpeg.ps1' }
        Copy-Item -LiteralPath $codecLicenses -Destination $Destination -Recurse
    }

    Ensure-ServiceAccess $Destination ReadAndExecute '*S-1-5-19:(OI)(CI)(RX)'
    if ($old) {
        $command = '"' + $binary + '" --config "' + $configPath + '"'
        $changed = Invoke-CimMethod -InputObject $old -MethodName Change -Arguments @{PathName=$command; StartName='NT AUTHORITY\LocalService'; StartMode='Automatic'}
        if ($changed.ReturnValue -ne 0) { throw "Replace service executable failed: Windows code $($changed.ReturnValue)" }
        & sc.exe config HomeImages start= delayed-auto
        Check-Exit 'Configure delayed startup'
    } else {
        & $binary --config $configPath --service install; Check-Exit 'Install native service'; $installedNew = $true
    }
    & sc.exe failure HomeImages reset= 86400 actions= restart/10000
    Check-Exit 'Configure recovery'
    Start-Service HomeImages
    $ready = $false
    for ($i=0; $i -lt 30; $i++) {
        try {
            $health = Invoke-RestMethod "http://127.0.0.1:$($config.port)/health" -TimeoutSec 2
            if ($health.implementation -eq 'go' -and $health.root_available) { $ready = $true; break }
        } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $ready) { throw 'Native service did not become healthy' }
    Get-NetFirewallRule -Name HomeImages-LAN -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    New-NetFirewallRule -Name HomeImages-LAN -DisplayName 'Home Images (local subnet)' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $config.port -RemoteAddress LocalSubnet -Profile Any | Out-Null
    # Keep the pinned version in sync with syncthing_install.go. Restrict the
    # rule to our executable, so existing Syncthing installations are unaffected.
    Get-NetFirewallRule -Name HomeImages-Syncthing-LAN -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    $managedSyncthing = Join-Path $DataDirectory 'syncthing\bin\v2.1.0\syncthing.exe'
    New-NetFirewallRule -Name HomeImages-Syncthing-LAN -DisplayName 'Home Images phone backup (local subnet)' -Direction Inbound -Action Allow -Protocol TCP -Program $managedSyncthing -RemoteAddress LocalSubnet -Profile Any | Out-Null
} catch {
    $failure = $_
    Stop-Service HomeImages -ErrorAction SilentlyContinue
    if ($installedNew) { & $binary --config $configPath --service uninstall }
    if ($moved) {
        if (Test-Path -LiteralPath $Destination) { Move-Item -LiteralPath $Destination -Destination $failedProgram }
        Move-Item -LiteralPath $oldProgram -Destination $Destination
    }
    if ($old) {
        $restored = Invoke-CimMethod -InputObject (Get-CimInstance Win32_Service -Filter "Name='HomeImages'") -MethodName Change -Arguments @{PathName=$old.PathName; StartName=$old.StartName}
        if ($restored.ReturnValue -ne 0) { Write-Warning "Service command rollback failed: Windows code $($restored.ReturnValue)" }
        if ($old.State -eq 'Running') { Start-Service HomeImages }
    }
    throw $failure
}
Get-CimInstance Win32_Service -Filter "Name='HomeImages'" | Select-Object Name,State,StartName,PathName
$health | ConvertTo-Json
Write-Host "Previous installation backed up to $backup"
