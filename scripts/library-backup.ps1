[CmdletBinding()]
param(
    [string]$Config = 'C:\ProgramData\HomeImages\library-backup\config.json',
    [switch]$Preview
)
$ErrorActionPreference = 'Stop'
$backupStateDirectory = Split-Path -Parent $Config
$backupStatusPath = Join-Path $backupStateDirectory 'status.json'
$backupStatus = [ordered]@{ startedAt = (Get-Date).ToUniversalTime().ToString('o'); state = 'checking'; preview = [bool]$Preview }
try {
    $backupConfig = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
    foreach ($backupSide in @('source', 'destination')) {
        $backupPath = [IO.Path]::GetFullPath($backupConfig.$backupSide).TrimEnd('\')
        if (-not (Test-Path -LiteralPath $backupPath -PathType Container)) { throw "$backupSide directory is unavailable: $backupPath" }
        $backupDrive = [IO.Path]::GetPathRoot($backupPath).Substring(0,1)
        $backupVolume = Get-Volume -DriveLetter $backupDrive -ErrorAction Stop
        $backupExpectedVolume = $backupConfig.($backupSide + 'VolumeId')
        if (-not $backupExpectedVolume -or $backupVolume.UniqueId -ne $backupExpectedVolume) { throw "$backupSide volume does not match configured disk" }
    }
    $backupSource = (Resolve-Path -LiteralPath $backupConfig.source).Path.TrimEnd('\')
    $backupDestination = (Resolve-Path -LiteralPath $backupConfig.destination).Path.TrimEnd('\')
    if ($backupSource -eq $backupDestination -or $backupDestination.StartsWith($backupSource + '\', [StringComparison]::OrdinalIgnoreCase) -or $backupSource.StartsWith($backupDestination + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Source and destination must be separate directory trees' }
    $backupLog = Join-Path $backupStateDirectory ('backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.log')
    $backupStatus.source = $backupSource
    $backupStatus.destination = $backupDestination
    $backupStatus.log = $backupLog
    $backupStatus.state = 'running'
    $backupStatus | ConvertTo-Json | Set-Content -LiteralPath $backupStatusPath -Encoding UTF8
    # Intentionally no MIR, PURGE, MOVE or MOV: destination-only files stay put.
    # Copy differing files even when source mtime is older; source is authoritative.
    $backupArguments = @($backupSource, $backupDestination, '/E', '/COPY:DAT', '/DCOPY:DAT', '/XJ', '/Z', '/MT:4', '/R:2', '/W:3', '/NFL', '/NDL', '/NP', '/BYTES', '/XD', '.stfolder', '.stversions', '/XF', '.stignore', '.syncthing.*', "/UNILOG:$backupLog")
    if ($Preview) { $backupArguments += '/L' }
    & "$env:SystemRoot\System32\robocopy.exe" @backupArguments
    $backupExit = $LASTEXITCODE
    $backupStatus.robocopyExitCode = $backupExit
    if ($backupExit -ge 8) { throw "Robocopy failed with exit code $backupExit; see $backupLog" }
    $backupStatus.state = 'completed'
    $backupStatus.finishedAt = (Get-Date).ToUniversalTime().ToString('o')
    $backupStatus | ConvertTo-Json | Set-Content -LiteralPath $backupStatusPath -Encoding UTF8
    exit 0
} catch {
    $backupStatus.state = 'failed'
    $backupStatus.error = $_.Exception.Message
    $backupStatus.finishedAt = (Get-Date).ToUniversalTime().ToString('o')
    $backupStatus | ConvertTo-Json | Set-Content -LiteralPath $backupStatusPath -Encoding UTF8
    Write-Error -ErrorAction Continue $_
    exit 1
}
