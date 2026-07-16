$ErrorActionPreference = "Stop"

$Repo = if ($env:RHIZOME_REPO) { $env:RHIZOME_REPO } else { "Odrin/rhizome-mcp" }
$Version = if ($env:RHIZOME_VERSION) { $env:RHIZOME_VERSION } else { "latest" }
$InstallDir = if ($env:RHIZOME_INSTALL_DIR) { $env:RHIZOME_INSTALL_DIR } else { Join-Path $HOME ".local\bin" }
$BinName = "rhizome-mcp.exe"

$os = "windows"
$arch = switch ($env:PROCESSOR_ARCHITECTURE.ToLowerInvariant()) {
  "amd64" { "amd64" }
  "arm64" { "arm64" }
  default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

if ($Version -eq "latest") {
  $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
} else {
  $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/tags/$Version"
}

$tag = $release.tag_name
if (-not $tag) {
  throw "Failed to resolve release tag."
}

$versionNoPrefix = $tag.TrimStart("v")
$asset = "rhizome-mcp_${versionNoPrefix}_${os}_${arch}.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$tag"
$archiveUrl = "$baseUrl/$asset"
$checksumUrl = "$archiveUrl.sha256"

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("rhizome-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null
try {
  $archivePath = Join-Path $tmpDir $asset
  $checksumPath = "$archivePath.sha256"

  Invoke-WebRequest -Uri $archiveUrl -OutFile $archivePath
  Invoke-WebRequest -Uri $checksumUrl -OutFile $checksumPath

  $expected = (Get-Content -Path $checksumPath -Raw).Trim()
  $actual = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($expected.ToLowerInvariant() -ne $actual) {
    throw "Checksum verification failed for $asset"
  }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force
  Copy-Item -Path (Join-Path $tmpDir $BinName) -Destination (Join-Path $InstallDir $BinName) -Force

  Write-Host "Installed $BinName $tag to $InstallDir"
  $pathEntries = ($env:PATH -split ';') | ForEach-Object { $_.TrimEnd('\') }
  $normalizedInstallDir = $InstallDir.TrimEnd('\')
  if ($pathEntries -contains $normalizedInstallDir) {
    Write-Host "PATH already includes $InstallDir"
  } else {
    Write-Host "$InstallDir is not in PATH."
    Write-Host "Add it manually in PowerShell:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$InstallDir`", 'User')"
  }
} finally {
  Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}

