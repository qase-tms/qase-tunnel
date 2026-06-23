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
    # Resolve the latest release WITHOUT the GitHub REST API: api.github.com is
    # rate-limited to 60 req/hour per IP for unauthenticated callers, which
    # broke `latest` installs once an IP ran out of budget. The releases Atom
    # feed is served from github.com (not REST-rate-limited) AND includes
    # prereleases, so /releases/latest's prerelease exclusion isn't a problem.
    # Entries are newest-first; the first /releases/tag/<tag> link is latest.
    $feed = "https://github.com/$Repo/releases.atom"
    try {
        $content = (Invoke-WebRequest -Uri $feed -UseBasicParsing -Headers @{ "User-Agent" = "qase-tunnel-installer" }).Content
    } catch {
        throw "Could not fetch release feed $feed ($($_.Exception.Message))"
    }
    $m = [regex]::Match($content, '/releases/tag/([^"<]+)')
    if (-not $m.Success) {
        throw "No releases found at https://github.com/$Repo/releases"
    }
    return $m.Groups[1].Value
}

function Get-ExpectedSha256 {
    # GoReleaser ships a single checksums.txt (sha256). Lines are
    # "<hex>  <filename>" — fetch once, find the row for our archive.
    param([string]$Tag, [string]$Asset)
    $url = "https://github.com/$Repo/releases/download/$Tag/checksums.txt"
    try {
        $content = (Invoke-WebRequest -Uri $url -UseBasicParsing).Content
        foreach ($line in $content -split "`n") {
            $trim = $line.Trim()
            if (-not $trim) { continue }
            $parts = $trim -split '\s+', 2
            if ($parts.Length -eq 2 -and $parts[1] -eq $Asset) {
                return $parts[0]
            }
        }
        Write-Warning "Asset '$Asset' not found in $url; skipping checksum verification"
        return $null
    } catch {
        Write-Warning "Could not fetch checksum from $url ($($_.Exception.Message)); skipping verification"
        return $null
    }
}

$arch = Get-Arch
$tag = Resolve-ReleaseTag -RequestedVersion $Version
# GoReleaser archive name_template is "<project>_<version>_<os>_<arch>";
# tags are vX.Y.Z, filenames carry the bare X.Y.Z, so strip the leading 'v'.
$versionForFile = $tag -replace '^v', ''
$asset = "qase-tunnel_${versionForFile}_windows_${arch}.zip"
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

$tmpDir = Join-Path $env:TEMP "qase-tunnel-installer-$([Guid]::NewGuid())"
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
$tmpZip = Join-Path $tmpDir $asset

Write-Host "Downloading $asset ..."
Invoke-WebRequest -Uri $assetUrl -OutFile $tmpZip -UseBasicParsing

$expected = Get-ExpectedSha256 -Tag $tag -Asset $asset
if ($expected) {
    $actual = (Get-FileHash -Path $tmpZip -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
        throw "SHA256 mismatch for ${asset}: expected $expected, got $actual"
    }
    Write-Host "SHA256 verified: $actual"
}

Write-Host "Extracting archive ..."
Expand-Archive -Path $tmpZip -DestinationPath $tmpDir -Force

$extractedExe = Join-Path $tmpDir "qase-tunnel.exe"
if (-not (Test-Path $extractedExe)) {
    Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    throw "qase-tunnel.exe not found inside $asset"
}
# frpc.exe is bundled in the archive (see scripts/fetch-frpc.sh on the
# release pipeline). Installing it next to qase-tunnel.exe puts it on the
# same PATH the script just configured.
$extractedFrpc = Join-Path $tmpDir "frpc.exe"
if (-not (Test-Path $extractedFrpc)) {
    Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    throw "frpc.exe not found inside $asset"
}

$dest = Join-Path $InstallDir "qase-tunnel.exe"
$destFrpc = Join-Path $InstallDir "frpc.exe"
Move-Item -Path $extractedExe -Destination $dest -Force
Move-Item -Path $extractedFrpc -Destination $destFrpc -Force
Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
Write-Host "Installed: $dest"
Write-Host "Installed: $destFrpc"

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
