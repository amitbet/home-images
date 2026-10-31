#Requires -RunAsAdministrator
<#
Deploys the CI-built release of a main commit to this Windows host.

Every push to main publishes a GitHub release v0.0.<run> (Windows-only builds are
pre-releases, which install.ps1 -Version latest skips). This script finds the
release built from -Commit (default: origin/main of this checkout, or the newest
release when git is unavailable), waits for CI to publish it, then runs that
release's checksum-verifying install.ps1. Existing config.json always wins.

  git pull; .\scripts\deploy-windows.ps1
#>
[CmdletBinding()]
param(
    [string]$Commit = '',
    [int]$TimeoutMinutes = 25,
    [string]$Repository = 'amitbet/home-images'
)
$ErrorActionPreference = 'Stop'
if (-not $Commit -and (Get-Command git -ErrorAction SilentlyContinue)) {
    Push-Location $PSScriptRoot
    try {
        git fetch --quiet origin main
        if ($LASTEXITCODE -eq 0) { $Commit = (git rev-parse origin/main).Trim() }
    } finally { Pop-Location }
}
if ($Commit -and $Commit -notmatch '^[0-9a-fA-F]{7,40}$') { throw "Invalid commit: $Commit" }

$asset = 'home-images-windows-amd64.zip'
$deadline = (Get-Date).AddMinutes($TimeoutMinutes)
$release = $null
while (-not $release) {
    $releases = Invoke-RestMethod "https://api.github.com/repos/$Repository/releases?per_page=15" -UseBasicParsing
    # foreach, not a pipeline: Windows PowerShell 5.1 emits a JSON array as one object.
    foreach ($candidate in $releases) {
        $names = @($candidate.assets | ForEach-Object { $_.name })
        if (-not $candidate.draft -and $names -contains $asset -and $names -contains 'SHA256SUMS' -and
            (-not $Commit -or $candidate.target_commitish.StartsWith($Commit, [StringComparison]::OrdinalIgnoreCase))) {
            $release = $candidate; break
        }
    }
    if ($release) { break }
    if ((Get-Date) -gt $deadline) { throw "No release for commit $Commit after $TimeoutMinutes minutes; check https://github.com/$Repository/actions" }
    Write-Host "Waiting for CI to publish a release for $Commit..."
    Start-Sleep -Seconds 30
}
$tag = $release.tag_name
Write-Host "Installing $tag (commit $($release.target_commitish.Substring(0,7)))"

$installer = Join-Path ([IO.Path]::GetTempPath()) ("home-images-install-$tag.ps1")
try {
    Invoke-WebRequest "https://github.com/$Repository/releases/download/$tag/install.ps1" -OutFile $installer -UseBasicParsing
    & $installer -Version $tag
} finally { Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue }

$health = $null
for ($attempt = 0; $attempt -lt 30 -and -not $health; $attempt++) {
    try { $health = Invoke-RestMethod 'http://localhost:8372/health' -UseBasicParsing } catch { Start-Sleep -Seconds 2 }
}
if (-not $health) { throw 'Service did not answer /health after install; see C:\ProgramData\HomeImages\home-images.log' }
Get-Service HomeImages | Format-Table -AutoSize Name, Status
$health | Format-List
