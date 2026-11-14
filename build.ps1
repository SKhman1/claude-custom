$ErrorActionPreference = "Stop"

$version = "dev"
if (Get-Command git -ErrorAction SilentlyContinue) {
    $old = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $tag = & git describe --tags --always --dirty 2>$null
    if ($LASTEXITCODE -eq 0 -and $tag) {
        $version = "$tag".Trim()
    }
    $ErrorActionPreference = $old
}

New-Item -ItemType Directory -Force -Path bin | Out-Null
go build -trimpath -ldflags "-s -w -X claude-custom/internal/cli.Version=$version" -o bin\claude-custom.exe .\cmd\claude-custom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "built bin\claude-custom.exe ($version)"
