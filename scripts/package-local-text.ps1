# Package the CPU text encoder; never download models during a search request.
[CmdletBinding()]
param(
 [string]$OS = '',
 [ValidateSet('amd64','arm64')][string]$Architecture = 'amd64',
 [string]$OutputDirectory = 'dist/package'
)
$ErrorActionPreference = 'Stop'
if (-not $OS) { if ($env:OS -eq 'Windows_NT') { $OS='windows' } elseif ($IsMacOS) { $OS='darwin' } else { $OS='linux' } }
$version='1.23.2'
$revision='a857c8de2c07bbcfa6646adfcf31b798845afa1e'
$runtimePackages = @{
 'windows-amd64'=@('win-x64','zip','0b38df9af21834e41e73d602d90db5cb06dbd1ca618948b8f1d66d607ac9f3cd')
 'windows-arm64'=@('win-arm64','zip','1cfe88b6435df3b5fb0e9f6bd7d6f5df1e887b6174de7f6e2a47bab956f3f168')
 'linux-amd64'=@('linux-x64','tgz','1fa4dcaef22f6f7d5cd81b28c2800414350c10116f5fdd46a2160082551c5f9b')
 'linux-arm64'=@('linux-aarch64','tgz','7c63c73560ed76b1fac6cff8204ffe34fe180e70d6582b5332ec094810241e5c')
 'darwin-amd64'=@('osx-x86_64','tgz','d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6')
 'darwin-arm64'=@('osx-arm64','tgz','b4d513ab2b26f088c66891dbbc1408166708773d7cc4163de7bdca0e9bbb7856')
}
$spec=$runtimePackages["$OS-$Architecture"]
if (-not $spec) { throw "Unsupported text runtime: $OS-$Architecture" }
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force $OutputDirectory | Out-Null
$assets=Join-Path $OutputDirectory 'local-text'
New-Item -ItemType Directory -Force $assets | Out-Null
function Download-Verified([string]$URL,[string]$Path,[string]$SHA) {
 if ((Test-Path -LiteralPath $Path) -and (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -eq $SHA) { return }
 $partial="$Path.download"
 try {
  Invoke-WebRequest -UseBasicParsing $URL -OutFile $partial
  if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $SHA) { throw "Text encoder checksum mismatch: $URL" }
  Move-Item -LiteralPath $partial -Destination $Path -Force
 } finally { Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue }
}
$base="https://huggingface.co/immich-app/ViT-B-32__openai/resolve/$revision/textual"
Download-Verified "$base/model.onnx" (Join-Path $assets 'model.onnx') 'b80cf0af751533a6712d92247f0ddc0c95208748bc59a1a27f33e67be6864e3b'
Download-Verified "$base/tokenizer.json" (Join-Path $assets 'tokenizer.json') '6d9109cc838977f3ca94a379eec36aecc7c807e1785cd729660ca2fc0171fb35'
$archiveName="onnxruntime-$($spec[0])-$version.$($spec[1])"
$archive=Join-Path $OutputDirectory $archiveName
Download-Verified "https://github.com/microsoft/onnxruntime/releases/download/v$version/$archiveName" $archive $spec[2]
$extracted=Join-Path $OutputDirectory "onnxruntime-$($spec[0])-$version"
if ($spec[1] -eq 'zip') { Expand-Archive -LiteralPath $archive -DestinationPath $OutputDirectory -Force }
else { & tar -xzf $archive -C $OutputDirectory; if ($LASTEXITCODE -ne 0) { throw 'ONNX Runtime extraction failed' } }
if ($OS -eq 'windows') {
 Copy-Item -Path (Join-Path $extracted 'lib\*.dll') -Destination $assets -Force
} elseif ($OS -eq 'darwin') {
 Copy-Item -LiteralPath (Join-Path $extracted "lib/libonnxruntime.$version.dylib") -Destination (Join-Path $assets 'libonnxruntime.dylib') -Force
} else {
 Copy-Item -LiteralPath (Join-Path $extracted "lib/libonnxruntime.so.$version") -Destination (Join-Path $assets 'libonnxruntime.so') -Force
}
Copy-Item -LiteralPath (Join-Path $extracted 'LICENSE'),(Join-Path $extracted 'ThirdPartyNotices.txt') -Destination $assets -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot '../third-party-licenses/CLIP-LICENSE.txt') -Destination $assets -Force
@{model='ViT-B-32__openai';revision=$revision;runtime=$version} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $assets 'manifest.json') -Encoding UTF8
Write-Output $assets
