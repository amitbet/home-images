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
if (-not $BinaryPath) { $BinaryPath = Join-Path $PSScriptRoot 'dist\home-images-windows-amd64.exe' }
function Check-Exit([string]$Action) { if ($LASTEXITCODE -ne 0) { throw "$Action failed: exit $LASTEXITCODE" } }
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
    if ($old) { Stop-Service HomeImages; (Get-Service HomeImages).WaitForStatus('Stopped', '00:00:30') }
    if (Test-Path -LiteralPath $Destination) { Move-Item -LiteralPath $Destination -Destination $oldProgram; $moved = $true }
    New-Item -ItemType Directory -Force $Destination | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination $binary
    & icacls $Destination /grant '*S-1-5-19:(OI)(CI)(RX)' | Out-Null; Check-Exit 'Grant program access'
    & icacls $DataDirectory /grant '*S-1-5-19:(OI)(CI)(M)' | Out-Null; Check-Exit 'Grant data access'
    & icacls $config.root /grant '*S-1-5-19:(OI)(CI)(RX)' | Out-Null; Check-Exit 'Grant photo access'
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
