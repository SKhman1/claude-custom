<#
.SYNOPSIS
  Installs claude-custom and Claude Code, then starts claude-custom (setup, then Claude Code launches).

.DESCRIPTION
  Public repository (one line):
    irm https://raw.githubusercontent.com/SKhman1/claude-custom/main/install.ps1 | iex

  What it does:
    1. Checks Windows, PowerShell, and CPU architecture.
    2. Installs Node.js LTS with winget when it is missing (required for
       both Claude Code and claude-custom).
    3. Installs Claude Code with npm when missing.
    4. Installs claude-custom from the npm registry into
       %LOCALAPPDATA%\claude-custom\node (or the directory -InstallDir
       points at) and adds that directory to the user PATH.
    5. Starts claude-custom: pick a provider, an API key and a default
       model; Claude Code then launches automatically.

  claude-custom runs inside signed node.exe, so Windows Application Control
  (Smart App Control / WDAC / AppLocker) does not block it.

.PARAMETER InstallDir
  Install the launcher shim somewhere else, for example
  "C:\Program Files\claude-custom" (elevated).
.PARAMETER SkipSetup
  Do not start claude-custom (no setup, no launch).
.PARAMETER SkipClaudeCode
  Do not install Claude Code when it is missing.
#>
[CmdletBinding()]
param(
[string]$InstallDir = (Join-Path $env:LOCALAPPDATA "claude-custom\bin"),
[switch]$SkipSetup,
[switch]$SkipClaudeCode
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Repo = "SKhman1/claude-custom"
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir).TrimEnd("\")

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

function Invoke-Native([string]$File, [string[]]$Arguments, [switch]$Echo) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $File @Arguments 2>&1 | ForEach-Object { if ($Echo) { Say "    $_" } }
        return $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-ProtectedDir([string]$Path) {
    $roots = @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:WINDIR) | Where-Object { $_ }
    foreach ($root in $roots) {
        if ($Path.StartsWith($root.TrimEnd("\"), [System.StringComparison]::OrdinalIgnoreCase)) { return $true }
    }
    return $false
}

function Invoke-VersionCheck([string]$Exe) {
    $result = [pscustomobject]@{ Ok = $false; Output = $null; Blocked = $false; Error = $null }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $out = & $Exe version 2>&1
        $code = $LASTEXITCODE
        $text = ("$out" -replace "\s+", " ").Trim()
        if ($code -eq 0 -and $text) {
            $result.Ok = $true
            $result.Output = $text
            return $result
        }
        $result.Error = if ($text) { $text } else { "exit code $code" }
    } catch {
        $result.Error = $_.Exception.Message
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($result.Error -match "Application Control|blocked this file|0x800704EC|0x800704E5") {
        $result.Blocked = $true
    }
    return $result
}

function Get-NodeExe {
    $node = Get-Command node -ErrorAction SilentlyContinue
    if (-not $node) { return $null }
    $ErrorActionPreference = "Continue"
    $version = & $node.Source --version 2>$null
    $ErrorActionPreference = "Stop"
    if (-not $version) { return $null }
    $major = 0
    if ($version -match '^v(\d+)') { $major = [int]$Matches[1] }
    if ($major -lt 18) { return $null }
    return $node.Source
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

function Ensure-Node {
    $node = Get-NodeExe
    if ($node) {
        Ok "Node $(& $node --version) found at $node"
        return $node
    }
    $winget = Get-Command winget -ErrorAction SilentlyContinue
    if (-not $winget) {
        Stop-Install "Node.js 18 or newer is required, and neither node nor winget was found. Install Node.js LTS from https://nodejs.org, reopen the terminal, and rerun this installer."
    }
    Say "    Node.js is not installed; installing Node.js LTS with winget."
    $code = Invoke-Native "winget" @("install", "--id", "OpenJS.NodeJS.LTS", "-e", "--silent", "--accept-source-agreements", "--accept-package-agreements")
    if ($code -ne 0) {
        Stop-Install "winget failed to install Node.js (exit $code). Install Node.js LTS from https://nodejs.org and rerun this installer."
    }
    Refresh-Path
    $node = Get-NodeExe
    if (-not $node) {
        Stop-Install "node is still not available after installing Node.js. Reopen the terminal and rerun this installer."
    }
    Ok "Node $(& $node --version) installed"
    return $node
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
    Say "    Running: npm install -g @anthropic-ai/claude-code"
    $code = Invoke-Native "npm" @("install", "-g", "@anthropic-ai/claude-code")
    if ($code -ne 0) {
        Stop-Install "npm failed to install Claude Code (exit $code)."
    }
    Refresh-Path
    $claude = Find-Claude
    if (-not $claude) {
        Stop-Install "Claude Code was installed but 'claude' is not on PATH yet. Reopen the terminal and rerun this installer."
    }
    Ok "Claude Code installed: $claude"
}

function Install-ClaudeCustom {
    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if (-not $npm) { Stop-Install "npm was not found even after installing Node.js. Reopen the terminal and rerun this installer." }
    $root = Join-Path $env:LOCALAPPDATA "claude-custom"
    $dest = Join-Path $root "node"
    Say "    Running: npm install -g --prefix `"$dest`" claude-custom@latest"
    $code = Invoke-Native "npm" @("install", "-g", "--prefix", $dest, "claude-custom@latest", "--no-fund", "--no-audit", "--prefer-online")
    $script = Join-Path $dest "node_modules\claude-custom\bin\claude-custom.js"
    if ($code -ne 0 -or -not (Test-Path $script)) {
        Stop-Install "npm could not install claude-custom (exit $code)."
    }
    $node = Get-NodeExe
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $blockedExe = Join-Path $InstallDir "claude-custom.exe"
    if (Test-Path $blockedExe) {
        try { Move-Item -LiteralPath $blockedExe -Destination "$blockedExe.blocked" -Force } catch { }
    }
    $shim = Join-Path $InstallDir "claude-custom.cmd"
    $lines = @(
        "@echo off",
        "`"$node`" `"$script`" %*"
    )
    [IO.File]::WriteAllText($shim, ($lines -join "`r`n") + "`r`n", (New-Object System.Text.AsciiEncoding))
    Ok "Installed $shim"
    return $shim
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

function Show-PathResolution {
    $matches = @()
    foreach ($dir in (($env:Path) -split ";" | Where-Object { $_ })) {
        foreach ($name in @("claude-custom.exe", "claude-custom.cmd", "claude-custom.ps1", "claude-custom")) {
            $candidate = Join-Path $dir $name
            if (Test-Path $candidate) { $matches += $candidate }
        }
    }
    if (-not $matches) { Warn "claude-custom was not found on PATH in this session; open a new terminal" ; return }
    Say ""
    Say "    PATH resolution order for 'claude-custom':"
    foreach ($m in $matches) { Say "      $m" }
    if ($matches[0].TrimEnd("").ToLowerInvariant() -ne (Join-Path $InstallDir "claude-custom.cmd").TrimEnd("").ToLowerInvariant()) {
        Say ""
        Warn "a different claude-custom shadows the one just installed; remove that one or put $InstallDir earlier on PATH"
    }
}

try {
    Step "Checking the environment"
    if ($env:OS -ne "Windows_NT") {
        Stop-Install "this installer is Windows-only; on macOS/Linux use npm install -g claude-custom"
    }
    Ok "Windows $([Environment]::OSVersion.Version)"
    Ok "PowerShell $($PSVersionTable.PSVersion)"
    Ok "Architecture $($env:PROCESSOR_ARCHITECTURE)"

    Step "Checking Node.js (for Claude Code and claude-custom)"
    Ensure-Node | Out-Null
    Ensure-ClaudeCode

    Step "Installing claude-custom"
    if ((Test-ProtectedDir $InstallDir) -and -not (Test-Admin)) {
        Stop-Install "installing to $InstallDir needs an elevated PowerShell (Run as Administrator)"
    }
    $shim = Install-ClaudeCustom
    Add-ToUserPath

    Step "Checking the installation"
    $check = Invoke-VersionCheck $shim
    if (-not $check.Ok) {
        Stop-Install "the installed shim did not run: $($check.Error) (Node.js itself being blocked is unlikely; check with IT whether node.exe may run)"
    }
    Ok $check.Output
    Show-PathResolution

    if ($SkipSetup) {
        Step "Done"
        Say "    Skipped setup because -SkipSetup was used." "Yellow"
        Say "    Start it with: claude-custom" "Yellow"
        return
    }

    Step "Starting claude-custom"
    & $shim
    Say ""
    Ok "Have a nice session"
} catch {
    Say ""
    Say "Install failed: $($_.Exception.Message)" "Red"
}
