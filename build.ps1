param()

Write-Host "Creating build directories..."
$ErrorActionPreference = 'SilentlyContinue' # Suppress errors for existing dirs.
# Use -Force to create directories if they don't exist.
New-Item -ItemType Directory -Path "build\windows" -Force | Out-Null
New-Item -ItemType Directory -Path "build\linux" -Force | Out-Null
New-Item -ItemType Directory -Path "build\macos-amd64" -Force | Out-Null
New-Item -ItemType Directory -Path "build\macos-arm64" -Force | Out-Null
$ErrorActionPreference = 'Stop' # Reset error action preference.

function Run-Build {
    param(
        [string]$GOOS,
        [string]$GOARCH,
        [string]$OutputFile
    )
    Write-Host "Building for $GOOS ($GOARCH)..."
    $env:GOOS = $GOOS
    $env:GOARCH = $GOARCH
    go build -ldflags="-s -w" -o $OutputFile .
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Build failed for $GOOS ($GOARCH)!"
        exit 1 # Exit the script on failure
    }
}

# Build.
Run-Build -GOOS "windows" -GOARCH "amd64" -OutputFile "build\windows\wally-to-rbxmx.exe"
Run-Build -GOOS "linux"   -GOARCH "amd64" -OutputFile "build\linux\wally-to-rbxmx"
Run-Build -GOOS "darwin"  -GOARCH "amd64" -OutputFile "build\macos-amd64\wally-to-rbxmx"
Run-Build -GOOS "darwin"  -GOARCH "arm64" -OutputFile "build\macos-arm64\wally-to-rbxmx"

Write-Host "Builds complete."

# Zip.
Write-Host "Creating archives using Compress-Archive..."

try {
    Write-Host "  Zipping Windows build..."
    Compress-Archive -Path "build\windows\wally-to-rbxmx.exe" -DestinationPath "build\wally-to-rbxmx_windows_amd64.zip" -Force -ErrorAction Stop

    Write-Host "  Zipping Linux build..."
    Compress-Archive -Path "build\linux\wally-to-rbxmx" -DestinationPath "build\wally-to-rbxmx_linux_amd64.zip" -Force -ErrorAction Stop

    Write-Host "  Zipping macOS (amd64) build..."
    Compress-Archive -Path "build\macos-amd64\wally-to-rbxmx" -DestinationPath "build\wally-to-rbxmx_macos_amd64.zip" -Force -ErrorAction Stop

    Write-Host "  Zipping macOS (arm64) build..."
    Compress-Archive -Path "build\macos-arm64\wally-to-rbxmx" -DestinationPath "build\wally-to-rbxmx_macos_arm64.zip" -Force -ErrorAction Stop
} catch {
    Write-Warning "Failed to create archives. Error: $($_.Exception.Message)"
    # Continue the script even if archiving fails.
}

Write-Host "-----------------------------------------------------" -ForegroundColor Green
Write-Host "Build and archiving complete." -ForegroundColor Green
Write-Host "Binaries are in their 'build/<platform>' directories." -ForegroundColor Green
Write-Host "Archives (.zip) are in the 'build' directory." -ForegroundColor Green
Write-Host "-----------------------------------------------------" -ForegroundColor Green

exit 0
