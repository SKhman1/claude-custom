<#
.SYNOPSIS
  Installs claude-custom and Claude Code, then starts claude-custom setup.

.DESCRIPTION
  Public repository (one line):
    irm https://raw.githubusercontent.com/SKhman1/claude-custom/main/install.ps1 | iex

  Private repository (uses an authenticated gh CLI):
    gh api repos/SKhman1/claude-custom/contents/install.ps1 -H "Accept: application/vnd.github.raw" | iex

  What it does:
    1. Checks Windows, PowerShell, and CPU architecture.
    2. Checks Claude Code and installs it with npm when missing.
    3. Downloads the latest claude-custom release binary.
    4. Installs it to %LOCALAPPDATA%\claude-custom\bin and adds that to PATH.
    5. Starts `claude-custom setup`.

.PARAMETER SkipSetup
  Install the binary but do not start the interactive setup.
.PARAMETER SkipClaudeCode
  Do not try to install Claude Code when it is missing.
#>
[CmdletBinding()]
param(
    [switch]$SkipSetup,
    [switch]$SkipClaudeCode
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Repo = "SKhman1/claude-custom"
$InstallDir = Join-Path $env:LOCALAPPDATA "claude-custom\bin"

function Say([string]$Message, [string]$Color = "Gray") {
    Write-Host $Message -ForegroundColor $Color
}

function Step([string]$Message) {
    Write-Host ""
    Say "==> $Message" "Cyan"
}

function Ok([string]$Message) {
    Say "    [ok] $Message" "Green"
}

function Warn([string]$Message) {
    Say "    [warn] $Message" "Yellow"
}

function Stop-Install([string]$Message) {
    throw [System.InvalidOperationException]::new($Message)
}

function Get-ArchSuffix {
    $arch = $env:PROCESSOR_ARCHITECTURE
    switch ($arch) {
        "AMD64" { return "windows_amd64" }
        "ARM64" { return "windows_arm64" }
        default { Stop-Install "unsupported CPU architecture '$arch'; only Windows x64 and arm64 are supported" }
    }
}

function Refresh-Path {
    $machine = [Environment]::GetEnvironmentVariable("Path", "Machine")
    $user = [Environment]::GetEnvironmentVariable("Path", "User")
    $env:Path = "$machine;$user"
}

function Find-Claude {
    $cmd = Get-Command claude -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $candidate = Join-Path $env:APPDATA "npm\claude.cmd"
    if (Test-Path $candidate) { return $candidate }
    return $null
}

function Resolve-Release {
    if (Get-Command gh -ErrorAction SilentlyContinue) {
        $ErrorActionPreference = "Continue"
        $tag = & gh release view --repo $Repo --json tagName -q ".tagName" 2>$null
        $ErrorActionPreference = "Stop"
        if ($LASTEXITCODE -eq 0 -and $tag) { return "$tag".Trim() }
    }
    try {
        $release = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "claude-custom-installer" }
        return $release.tag_name
    } catch {
        Stop-Install "could not read the latest release from github.com/$Repo. If the repository is private, install the gh CLI and run 'gh auth login' first. Underlying error: $($_.Exception.Message)"
    }
}

function Get-ReleaseAsset([string]$Tag, [string]$AssetName, [string]$Destination) {
    if (Get-Command gh -ErrorAction SilentlyContinue) {
        $ErrorActionPreference = "Continue"
        & gh release download $Tag --repo $Repo --pattern $AssetName --dir (Split-Path $Destination) --clobber 2>$null
        $code = $LASTEXITCODE
        $ErrorActionPreference = "Stop"
        if ($code -eq 0 -and (Test-Path $Destination)) { return }
    }
    $url = "https://github.com/$Repo/releases/download/$Tag/$AssetName"
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $Destination -Headers @{ "User-Agent" = "claude-custom-installer" }
}

function Ensure-ClaudeCode {
    $claude = Find-Claude
    if ($claude) {
        Ok "Claude Code found: $claude"
        return
    }
    if ($SkipClaudeCode) {
        Warn "Claude Code is not installed; skipping because -SkipClaudeCode was used"
        return
    }

    Say "    Claude Code is not installed; installing it now." "Yellow"
    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if (-not $npm) {
        $winget = Get-Command winget -ErrorAction SilentlyContinue
        if (-not $winget) {
            Stop-Install "Claude Code needs Node.js (npm) and neither npm nor winget was found. Install Node.js LTS from https://nodejs.org, reopen the terminal, and rerun this installer."
        }
        Say "    Node.js is not installed; installing Node.js LTS with winget."
        & winget install --id OpenJS.NodeJS.LTS -e --silent --accept-source-agreements --accept-package-agreements
        if ($LASTEXITCODE -ne 0) {
            Stop-Install "winget failed to install Node.js (exit $LASTEXITCODE). Install Node.js LTS from https://nodejs.org and rerun this installer."
        }
        Refresh-Path
        $npm = Get-Command npm -ErrorAction SilentlyContinue
        if (-not $npm) {
            Stop-Install "npm is still not available after installing Node.js. Reopen the terminal and rerun this installer."
        }
    }

    Say "    Running: npm install -g @anthropic-ai/claude-code"
    & npm install -g "@anthropic-ai/claude-code"
    if ($LASTEXITCODE -ne 0) {
        Stop-Install "npm failed to install Claude Code (exit $LASTEXITCODE)."
    }
    Refresh-Path
    $claude = Find-Claude
    if (-not $claude) {
        Stop-Install "Claude Code was installed but 'claude' is not on PATH yet. Reopen the terminal and rerun this installer."
    }
    Ok "Claude Code installed: $claude"
}

function Install-Binary([string]$Tag) {
    $suffix = Get-ArchSuffix
    $asset = "claude-custom_$suffix.exe"
    $tmp = Join-Path $env:TEMP ("claude-custom-install-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $downloaded = Join-Path $tmp $asset
        Say "    Downloading $asset ($Tag)"
        Get-ReleaseAsset $Tag $asset $downloaded
        if (-not (Test-Path $downloaded)) {
            Stop-Install "the release asset $asset was not found in $Tag"
        }
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $target = Join-Path $InstallDir "claude-custom.exe"
        try {
            Copy-Item -LiteralPath $downloaded -Destination $target -Force
        } catch {
            Stop-Install "could not replace $target. Close any running claude-custom process and rerun this installer. Underlying error: $($_.Exception.Message)"
        }
        Ok "Installed $target"
        return $target
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Add-ToUserPath {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @()
    if ($userPath) { $entries = $userPath -split ";" | Where-Object { $_ } }
    $present = $false
    foreach ($entry in $entries) {
        if ($entry.TrimEnd("\") -ieq $InstallDir.TrimEnd("\")) { $present = $true; break }
    }
    if ($present) {
        Ok "PATH already contains $InstallDir"
        return
    }
    $newPath = (@($entries) + $InstallDir) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Ok "Added $InstallDir to the user PATH (new terminals will pick it up)"
}

try {
    Step "Checking the environment"
    if ($env:OS -ne "Windows_NT") {
        Stop-Install "this installer is Windows-only; build from source on macOS/Linux (go build ./cmd/claude-custom)"
    }
    Ok "Windows $([Environment]::OSVersion.Version)"
    Ok "PowerShell $($PSVersionTable.PSVersion)"
    Ok "Architecture $($env:PROCESSOR_ARCHITECTURE)"

    Step "Checking Claude Code"
    Ensure-ClaudeCode

    Step "Installing claude-custom"
    $tag = Resolve-Release
    Ok "Latest release: $tag"
    $exe = Install-Binary $tag
    Add-ToUserPath

    Step "Checking the installation"
    $version = & $exe version
    Ok "$version"

    if ($SkipSetup) {
        Step "Done"
        Say "    Skipped setup because -SkipSetup was used." "Yellow"
        Say "    Start it with: claude-custom setup" "Yellow"
        return
    }

    Step "Starting claude-custom setup"
    & $exe setup
    Say ""
    Ok "Setup finished. Launch Claude Code with: claude-custom"
} catch {
    Say ""
    Say "Install failed: $($_.Exception.Message)" "Red"
}
