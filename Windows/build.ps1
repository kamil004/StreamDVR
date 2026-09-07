$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$versionFile = Join-Path $root "version.txt"

# Read / default version, bump PATCH on every build (mirrors the macOS build.sh).
$version = "1.0.5"
if (Test-Path $versionFile) {
    $version = (Get-Content $versionFile).Trim()
}
if (-not $version) { $version = "1.0.5" }

$parts = $version.Split(".")
if ($parts.Count -ne 3) { $parts = @("1", "0", "5") }
$parts[2] = ([int]$parts[2] + 1).ToString()
$newVersion = $parts -join "."
Set-Content -Path $versionFile -Value $newVersion -NoNewline

# Keep the csproj default Version in sync so plain `dotnet build` also reports it.
$csproj = Join-Path $root "TwitchDVR.App\TwitchDVR.App.csproj"
$csprojText = Get-Content $csproj -Raw
$csprojText = [regex]::Replace(
    $csprojText,
    '<Version>.*?</Version>',
    "<Version>$newVersion</Version>")
Set-Content -Path $csproj -Value $csprojText -NoNewline

Write-Host ""
Write-Host "==== Build StreamDVR $newVersion (Windows) ===="
Write-Host ""

dotnet publish (Join-Path $root "TwitchDVR.App\TwitchDVR.App.csproj") -c Release -r win-x64 `
  --self-contained true `
  -p:PublishSingleFile=true `
  -p:IncludeNativeLibrariesForSelfExtract=true `
  -p:EnableCompressionInSingleFile=true `
  -p:EnableWindowsTargeting=true `
  -p:Version=$newVersion

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$publishDir = Join-Path $root "TwitchDVR.App\bin\Release\net8.0-windows\win-x64\publish"
Write-Host ""
Write-Host "==== Output: $publishDir\StreamDVR.exe ===="
Write-Host ""

$zipPath = Join-Path $root "StreamDVR-Windows-v$newVersion.zip"
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $publishDir "StreamDVR.exe") -DestinationPath $zipPath
Write-Host "Zipped: $zipPath"