<#
.SYNOPSIS
    Installs the qase-tunnel customer-side agent on Windows.

.DESCRIPTION
    Fetches the latest qase-tunnel release from GitHub, downloads the binary
    that matches the host architecture (amd64 or arm64), verifies the SHA256
    against the published checksum, installs it to %LOCALAPPDATA%\qase-tunnel,
    and prepends that directory to the user PATH so `qase-tunnel` runs from any
    shell.

.PARAMETER Version
    Release tag to install (e.g. v0.1.0). Defaults to the latest published
    release.

.PARAMETER InstallDir
    Override the install directory. Defaults to %LOCALAPPDATA%\qase-tunnel.

.PARAMETER SkipPath
    Skip the user-PATH update. Use when you'll add the directory to PATH
    yourself (e.g. via Group Policy).

.EXAMPLE
    Invoke-WebRequest -Uri "https://raw.githubusercontent.com/qase-tms/qase-tunnel/main/script/install.ps1" -OutFile "install.ps1"
    .\install.ps1
#>

param(
    [string]$Version = "latest",
    [string]$InstallDir,
    [switch]$SkipPath
)

$ErrorActionPreference = "Stop"

$Repo = "qase-tms/qase-tunnel"
if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA "qase-tunnel" }

function Get-Arch {
    # Detect the running OS architecture (not the PowerShell host's).
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
    switch -Regex ($arch) {
        '^AMD64$' { return "amd64" }
        '^ARM64$' { return "arm64" }
        default {
            Write-Warning "Unrecognized PROCESSOR_ARCHITECTURE '$arch'; falling back to amd64"
            return "amd64"
        }
    }
}

function Resolve-ReleaseTag {
    param([string]$RequestedVersion)
    if ($RequestedVersion -and $RequestedVersion -ne "latest") {
        return $RequestedVersion
    }
    $api = "https://api.github.com/repos/$Repo/releases/latest"
    $resp = Invoke-RestMethod -Uri $api -Headers @{ "User-Agent" = "qase-tunnel-installer" }
    return $resp.tag_name
}

function Get-ExpectedSha256 {
    param([string]$Tag, [string]$Asset)
    $url = "https://github.com/$Repo/releases/download/$Tag/$Asset.sha256"
    try {
        $line = (Invoke-WebRequest -Uri $url -UseBasicParsing).Content.Trim()
        # Format produced by `shasum -a 256 <file>`: "<hex>  <filename>"
        return ($line -split '\s+')[0]
    } catch {
        Write-Warning "Could not fetch checksum from $url ($($_.Exception.Message)); skipping verification"
        return $null
    }
}

$arch = Get-Arch
$tag = Resolve-ReleaseTag -RequestedVersion $Version
$asset = "qase-tunnel-windows-$arch.exe"
$assetUrl = "https://github.com/$Repo/releases/download/$tag/$asset"

Write-Host ""
Write-Host "qase-tunnel installer"
Write-Host "  repo      : $Repo"
Write-Host "  tag       : $tag"
Write-Host "  arch      : windows/$arch"
Write-Host "  asset     : $asset"
Write-Host "  installDir: $InstallDir"
Write-Host ""

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

$dest = Join-Path $InstallDir "qase-tunnel.exe"
$tmp  = Join-Path $env:TEMP  "$asset.$([Guid]::NewGuid()).part"

Write-Host "Downloading $asset ..."
Invoke-WebRequest -Uri $assetUrl -OutFile $tmp -UseBasicParsing

$expected = Get-ExpectedSha256 -Tag $tag -Asset $asset
if ($expected) {
    $actual = (Get-FileHash -Path $tmp -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        Remove-Item -Path $tmp -Force -ErrorAction SilentlyContinue
        throw "SHA256 mismatch for $asset: expected $expected, got $actual"
    }
    Write-Host "SHA256 verified: $actual"
}

Move-Item -Path $tmp -Destination $dest -Force
Write-Host "Installed: $dest"

if (-not $SkipPath) {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($null -eq $userPath) { $userPath = "" }
    $pathDirs = $userPath -split ';' | Where-Object { $_ -ne "" }
    if ($pathDirs -notcontains $InstallDir) {
        $newPath = if ($userPath) { "$InstallDir;$userPath" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        Write-Host "Added $InstallDir to user PATH (open a new shell for it to take effect)."
    } else {
        Write-Host "$InstallDir is already on user PATH."
    }
}

Write-Host ""
Write-Host "Next:"
Write-Host "  qase-tunnel start -a <YOUR_QASE_API_TOKEN>"
Write-Host ""
