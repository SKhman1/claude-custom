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
    4. Installs it to %LOCALAPPDATA%\claude-custom\bin (or -InstallDir)
       and adds that folder to PATH.
    5. Starts `claude-custom setup`.

  If Windows Application Control blocks the downloaded binary, the installer
  downloads the Go toolchain and builds claude-custom locally instead, which
  these policies generally allow.

.PARAMETER InstallDir
  Install somewhere else, for example "C:\Program Files\claude-custom".
  Required when a Windows Application Control policy blocks the default
  user-profile location; use an elevated PowerShell for protected folders.
.PARAMETER BuildFromSource
  Skip the release binary and build claude-custom locally with Go.
.PARAMETER SkipSetup
  Install the binary but do not start the interactive setup.
.PARAMETER SkipClaudeCode
  Do not try to install Claude Code when it is missing.
#>
[CmdletBinding()]
param(
[string]$InstallDir = (Join-Path $env:LOCALAPPDATA "claude-custom\bin"),
[switch]$BuildFromSource,
[switch]$SkipSetup,
[switch]$SkipClaudeCode,
[ValidateSet("auto", "binary", "node")][string]$Runtime = "auto"
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

function Show-AppControlHelp([string]$Exe) {
    Say ""
    Say "Windows Application Control blocked this file:" "Red"
    Say "    $Exe" "Red"
    Say ""
    Say "    This is a Windows or organization policy (Smart App Control, WDAC" "Gray"
    Say "    or AppLocker) blocking an unsigned executable. It is not an" "Gray"
    Say "    installer failure, and the same policy will block the app when" "Gray"
    Say "    you start it by hand." "Gray"

    $sac = $null
    try {
        $sac = (Get-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy" -Name "VerifiedAndReputablePolicyState" -ErrorAction Stop).VerifiedAndReputablePolicyState
    } catch { }
    if ($null -ne $sac) {
        $label = switch ([int]$sac) { 0 { "off" } 1 { "enforced" } 2 { "evaluation" } default { "state $sac" } }
        Say ""
        Say "    Smart App Control: $label" "Gray"
    }

    $programFilesDir = Join-Path $env:ProgramFiles "claude-custom"
    Say ""
    Say "    Options:" "Yellow"
    Say "      1. Use the Node build, which runs inside signed node.exe and is" "Gray"
    Say "         not affected by Application Control:" "Gray"
    Say "           .\install.ps1 -Runtime node" "Gray"
    Say "         (requires Node.js 18+; setup continues normally)" "Gray"
    Say "      2. Allow the app. Windows Security > App & browser control >" "Gray"
    Say "         Smart App Control, or ask IT to allow the file (hash below)." "Gray"
    Say "      3. Install into a policy-allowed folder from an elevated" "Gray"
    Say "         PowerShell:" "Gray"
    Say "           .\install.ps1 -InstallDir `"$programFilesDir`"" "Gray"
    Say "      4. Use a machine without the policy, or run Claude Code on WSL." "Gray"

    try {
        $hash = (Get-FileHash -LiteralPath $Exe -Algorithm SHA256).Hash
        Say ""
        Say "    SHA-256: $hash" "Gray"
    } catch { }
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
        $wingetCode = Invoke-Native "winget" @("install", "--id", "OpenJS.NodeJS.LTS", "-e", "--silent", "--accept-source-agreements", "--accept-package-agreements")
        if ($wingetCode -ne 0) {
            Stop-Install "winget failed to install Node.js (exit $wingetCode). Install Node.js LTS from https://nodejs.org and rerun this installer."
        }
        Refresh-Path
        $npm = Get-Command npm -ErrorAction SilentlyContinue
        if (-not $npm) {
            Stop-Install "npm is still not available after installing Node.js. Reopen the terminal and rerun this installer."
        }
    }

    Say "    Running: npm install -g @anthropic-ai/claude-code"
    $npmCode = Invoke-Native "npm" @("install", "-g", "@anthropic-ai/claude-code")
    if ($npmCode -ne 0) {
        Stop-Install "npm failed to install Claude Code (exit $npmCode)."
    }
    Refresh-Path
    $claude = Find-Claude
    if (-not $claude) {
        Stop-Install "Claude Code was installed but 'claude' is not on PATH yet. Reopen the terminal and rerun this installer."
    }
    Ok "Claude Code installed: $claude"
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

function Install-NodePackage([string]$Ref) {
    $node = Get-NodeExe
    if (-not $node) {
        Stop-Install "Node.js 18 or newer is required for the Node runtime. Install it from https://nodejs.org and rerun this installer."
    }
    if (-not $Ref) { $Ref = "main" }
    Ok "Node $(& $node --version) found at $node"
    Say "    It runs inside signed node.exe, so Application Control allows it"

    $root = Join-Path $env:LOCALAPPDATA "claude-custom"
    $dest = Join-Path $root "node"
    $script = $null

    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if ($npm) {
        $spec = "claude-custom@latest"
        if ($Ref -match '^v?(\d+\.\d+\.\d+.*)$') { $spec = "claude-custom@$($Matches[1])" }
        Say "    Running: npm install -g --prefix `"$dest`" $spec"
        $npmCode = Invoke-Native "npm" @("install", "-g", "--prefix", $dest, $spec, "--no-fund", "--no-audit")
        $candidate = Join-Path $dest "node_modules\claude-custom\bin\claude-custom.js"
        if ($npmCode -eq 0 -and (Test-Path $candidate)) {
            $script = $candidate
        } else {
            Warn "npm install did not complete (exit $npmCode); falling back to the GitHub archive"
        }
    } else {
        Warn "npm was not found; using the GitHub archive"
    }

    if (-not $script) {
        $refPath = if ($Ref -match '^v\d') { "refs/tags/$Ref" } else { "refs/heads/$Ref" }
        $url = "https://codeload.github.com/$Repo/zip/$refPath"
        Say "    Downloading the Node build ($Ref)"
        $tmp = Join-Path $env:TEMP ("claude-custom-node-" + [guid]::NewGuid().ToString("N"))
        New-Item -ItemType Directory -Path $tmp -Force | Out-Null
        try {
            $zip = Join-Path $tmp "package.zip"
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $zip -Headers @{ "User-Agent" = "claude-custom-installer" }
            Expand-Archive -LiteralPath $zip -DestinationPath $tmp -Force
            $inner = Get-ChildItem -LiteralPath $tmp -Directory | Select-Object -First 1
            if (-not $inner -or -not (Test-Path (Join-Path $inner.FullName "bin\claude-custom.js"))) {
                Stop-Install "the downloaded package does not contain bin/claude-custom.js"
            }
            if (Test-Path $dest) { Remove-Item -LiteralPath $dest -Recurse -Force }
            New-Item -ItemType Directory -Path $dest -Force | Out-Null
            Copy-Item -Path (Join-Path $inner.FullName "*") -Destination $dest -Recurse -Force
            $script = Join-Path $dest "bin\claude-custom.js"
        } finally {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

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
        try { Unblock-File -LiteralPath $downloaded -ErrorAction Stop } catch { }
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $target = Join-Path $InstallDir "claude-custom.exe"
        try {
            Copy-Item -LiteralPath $downloaded -Destination $target -Force
        } catch {
            Stop-Install "could not replace $target. Close any running claude-custom process and rerun this installer. Underlying error: $($_.Exception.Message)"
        }
        try { Unblock-File -LiteralPath $target -ErrorAction Stop } catch { }
        Ok "Installed $target"
        return $target
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Get-GoExe {
    $local = Join-Path $env:LOCALAPPDATA "claude-custom\go\bin\go.exe"
    if (Test-Path $local) { return $local }
    $existing = Get-Command go -ErrorAction SilentlyContinue
    if ($existing) { return $existing.Source }
    return $null
}

function Install-GoToolchain {
    $go = Get-GoExe
    if ($go) {
        Ok "Go toolchain found: $go"
        return $go
    }
    Say "    Downloading the Go toolchain (about 90 MB, one time)" "Yellow"
    $release = $null
    try {
        $releases = Invoke-RestMethod -UseBasicParsing -Uri "https://go.dev/dl/?mode=json" -Headers @{ "User-Agent" = "claude-custom-installer" }
    } catch {
        Stop-Install "could not reach go.dev to download the Go toolchain. Underlying error: $($_.Exception.Message)"
    }
    foreach ($candidate in $releases) {
        if (-not $candidate.stable) { continue }
        $file = $candidate.files | Where-Object { $_.os -eq "windows" -and $_.arch -eq "amd64" -and $_.kind -eq "archive" } | Select-Object -First 1
        if ($file) { $release = $file; break }
    }
    if (-not $release) { Stop-Install "could not find a Windows Go toolchain to download" }
    $zip = Join-Path $env:TEMP ("go-" + [guid]::NewGuid().ToString("N") + ".zip")
    try {
        Invoke-WebRequest -UseBasicParsing -Uri ("https://go.dev/dl/" + $release.filename) -OutFile $zip -Headers @{ "User-Agent" = "claude-custom-installer" }
        New-Item -ItemType Directory -Force -Path (Join-Path $env:LOCALAPPDATA "claude-custom") | Out-Null
        Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $env:LOCALAPPDATA "claude-custom") -Force
        Ok "Go toolchain installed: $($release.filename)"
    } catch {
        Stop-Install "could not install the Go toolchain. Underlying error: $($_.Exception.Message)"
    } finally {
        Remove-Item -LiteralPath $zip -Force -ErrorAction SilentlyContinue
    }
    $go = Join-Path $env:LOCALAPPDATA "claude-custom\go\bin\go.exe"
    if (-not (Test-Path $go)) { Stop-Install "the Go toolchain did not unpack as expected" }
    return $go
}

function Build-FromSource([string]$Tag) {
    $go = Install-GoToolchain
    $tmp = Join-Path $env:TEMP ("claude-custom-src-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $archive = Join-Path $tmp "source.zip"
        $ref = if ($Tag -match "^(v?\d|v\d)") { "tags/$Tag" } else { "heads/$Tag" }
        Say "    Downloading source ($Tag)"
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "https://codeload.github.com/$Repo/zip/refs/$ref" -OutFile $archive -Headers @{ "User-Agent" = "claude-custom-installer" }
        } catch {
            Stop-Install "could not download the source archive for $Tag. Underlying error: $($_.Exception.Message)"
        }
        Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force
        $root = Get-ChildItem -LiteralPath $tmp -Directory | Select-Object -First 1
        if (-not $root) { Stop-Install "the source archive did not contain the project" }

        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $target = Join-Path $InstallDir "claude-custom.exe"
        $env:CGO_ENABLED = "0"
        $env:GOTELEMETRY = "off"
        $env:GOFLAGS = ""
        $version = if ($Tag -match "^(v\d|main)") { $Tag } else { "dev" }
        if ($version -eq "main") { $version = "dev" }
        Say "    Building claude-custom locally (this takes a moment)"
        Push-Location $root.FullName
        try {
            $goFlags = "-s -w -X claude-custom/internal/cli.Version=$version"
            $buildCode = Invoke-Native $go @("build", "-trimpath", "-ldflags", $goFlags, "-o", $target, "./cmd/claude-custom") -Echo
            if ($buildCode -ne 0) { Stop-Install "the local build failed (exit $buildCode)" }
        } finally {
            Pop-Location
        }
        if (-not (Test-Path $target)) { Stop-Install "the local build produced no binary" }
        try { Unblock-File -LiteralPath $target -ErrorAction Stop } catch { }
        Ok "Built $target"
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
    if ((Test-ProtectedDir $InstallDir) -and -not (Test-Admin)) {
        Stop-Install "installing to $InstallDir needs an elevated PowerShell (Run as Administrator)"
    }
    $tag = $null
    try { $tag = Resolve-Release } catch { }

    if ($Runtime -eq "node") {
        $shim = Install-NodePackage "main"
        Add-ToUserPath
        $check = Invoke-VersionCheck $shim
        if (-not $check.Ok) {
            Stop-Install "the Node build did not run: $($check.Error)"
        }
        Ok $check.Output
        if ($SkipSetup) {
            Step "Done"
            Say "    Skipped setup because -SkipSetup was used." "Yellow"
            Say "    Start it with: claude-custom setup" "Yellow"
            return
        }
        Step "Starting claude-custom setup"
        & $shim setup
        Say ""
        Ok "Setup finished. Launch Claude Code with: claude-custom"
        return
    }

    $exe = Join-Path $InstallDir "claude-custom.exe"
    $fromRelease = $false
    if (-not $BuildFromSource) {
        try {
            if (-not $tag) { Stop-Install "could not read the latest release" }
            Ok "Latest release: $tag"
            $exe = Install-Binary $tag
            $fromRelease = $true
        } catch {
            Warn "the prebuilt binary could not be installed: $($_.Exception.Message)"
            Warn "Building claude-custom from source instead"
            $tag = $null
        }
    }
    Add-ToUserPath

    Step "Checking the installation"
    $check = if ($fromRelease -and (Test-Path $exe)) { Invoke-VersionCheck $exe } else { $null }

    if ($BuildFromSource -or -not $fromRelease -or ($check -and $check.Blocked)) {
        if ($check -and $check.Blocked) {
            Warn "Windows Application Control blocked the prebuilt binary; building it locally instead"
        }
        if (-not $tag) {
            try { $tag = Resolve-Release } catch { $tag = "main" }
        }
        $buildError = $null
        try {
            $exe = Build-FromSource $tag
            $check = Invoke-VersionCheck $exe
        } catch {
            $buildError = $_
            $check = $null
        }
        if ($buildError -or -not $check) {
            if (Get-NodeExe) {
                Warn "the local build failed; using the Node build instead (it needs no code signing)"
                $shim = Install-NodePackage "main"
                $check = Invoke-VersionCheck $shim
                if (-not $check.Ok) { Stop-Install "the Node build did not run: $($check.Error)" }
                Ok $check.Output
                if ($SkipSetup) {
                    Step "Done"
                    Say "    Skipped setup because -SkipSetup was used." "Yellow"
                    Say "    Start it with: claude-custom setup" "Yellow"
                    return
                }
                Step "Starting claude-custom setup"
                & $shim setup
                Say ""
                Ok "Setup finished. Launch Claude Code with: claude-custom"
                return
            }
            if ($buildError) { Stop-Install "build from source failed: $($buildError.Exception.Message)" }
            Stop-Install "the installed binary did not run: $($check.Error)"
        }
    }

    if ($check.Blocked) {
        if (Get-NodeExe) {
            Warn "Windows Application Control blocks unsigned executables on this machine"
            Warn "Installing the Node build instead (it runs inside signed node.exe)"
            $shim = Install-NodePackage "main"
            $check = Invoke-VersionCheck $shim
            if (-not $check.Ok) { Stop-Install "the Node build did not run: $($check.Error)" }
            Ok $check.Output
            if ($SkipSetup) {
                Step "Done"
                Say "    Skipped setup because -SkipSetup was used." "Yellow"
                Say "    Start it with: claude-custom setup" "Yellow"
                return
            }
            Step "Starting claude-custom setup"
            & $shim setup
            Say ""
            Ok "Setup finished. Launch Claude Code with: claude-custom"
            return
        }
        Show-AppControlHelp $exe
        Stop-Install "both the downloaded and the locally built binary are blocked by Windows Application Control"
    }
    if (-not $check.Ok) {
        Stop-Install "the installed binary did not run: $($check.Error)"
    }
    Ok $check.Output

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
