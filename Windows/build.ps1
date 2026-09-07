$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$versionFile = Join-Path $root "..\build_version.txt"

# Windows shares the version number with macOS (repo root build_version.txt),
# so both platforms always ship in the same release. No OS-specific bump here:
# the version only moves when the macOS build.sh bumps it.
$version = "1.1.13"
if (Test-Path $versionFile) {
    $version = (Get-Content $versionFile).Trim()
}
if (-not $version) { $version = "1.1.13" }

# Keep the csproj default Version in sync so plain `dotnet build` also reports it.
$csproj = Join-Path $root "TwitchDVR.App\TwitchDVR.App.csproj"
$csprojText = Get-Content $csproj -Raw
$csprojText = [regex]::Replace(
    $csprojText,
    '<Version>.*?</Version>',
    "<Version>$version</Version>")
Set-Content -Path $csproj -Value $csprojText -NoNewline

Write-Host ""
Write-Host "==== Build StreamDVR $version (Windows, shared with macOS) ===="
Write-Host ""

dotnet publish (Join-Path $root "TwitchDVR.App\TwitchDVR.App.csproj") -c Release -r win-x64 `
  --self-contained true `
  -p:PublishSingleFile=true `
  -p:IncludeNativeLibrariesForSelfExtract=true `
  -p:EnableCompressionInSingleFile=true `
  -p:EnableWindowsTargeting=true `
  -p:Version=$version

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$publishDir = Join-Path $root "TwitchDVR.App\bin\Release\net8.0-windows\win-x64\publish"
Write-Host ""
Write-Host "==== Output: $publishDir\StreamDVR.exe ===="
Write-Host ""

$zipPath = Join-Path $root "StreamDVR-Windows-v$version.zip"
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $publishDir "StreamDVR.exe") -DestinationPath $zipPath
Write-Host "Zipped: $zipPath"