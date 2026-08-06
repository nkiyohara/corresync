Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
  Write-Output "PowerShell installer tests skipped: Windows-only installer"
  exit 0
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $repositoryRoot "site\install.ps1"
. $installer -NoRun

function Assert-True {
  param(
    [Parameter(Mandatory = $true)][bool]$Condition,
    [Parameter(Mandatory = $true)][string]$Message
  )
  if (-not $Condition) {
    throw "installer test failed: $Message"
  }
}

function Assert-Throw {
  param(
    [Parameter(Mandatory = $true)][ScriptBlock]$Action,
    [Parameter(Mandatory = $true)][string]$Message
  )
  $thrown = $false
  try {
    & $Action | Out-Null
  } catch {
    $thrown = $true
  }
  Assert-True -Condition $thrown -Message $Message
}

function Assert-BytesEqual {
  param(
    [Parameter(Mandatory = $true)][string]$Expected,
    [Parameter(Mandatory = $true)][string]$Actual,
    [Parameter(Mandatory = $true)][string]$Message
  )
  $expectedHash = Get-CorresyncSha256 -Path $Expected
  $actualHash = Get-CorresyncSha256 -Path $Actual
  Assert-True -Condition ($expectedHash -ceq $actualHash) -Message $Message
}

$testRoot = Initialize-CorresyncPrivateDirectory `
  -Parent ([IO.Path]::GetTempPath()) `
  -Prefix "corresync-install-test-"
try {
  Assert-CorresyncHttpsUri `
    -Uri ([Uri]"https://github.com/nkiyohara/corresync/releases/latest")
  Assert-CorresyncHttpsUri `
    -Uri ([Uri]"https://release-assets.githubusercontent.com/github-production-release-asset/1/file")
  foreach ($invalidUri in @(
      "http://github.com/nkiyohara/corresync/releases/latest",
      "https://example.com/nkiyohara/corresync/releases/latest",
      "https://github.com/another/project/releases/latest",
      "https://release-assets.githubusercontent.com/unexpected/file"
    )) {
    Assert-Throw `
      -Action { Assert-CorresyncHttpsUri -Uri ([Uri]$invalidUri) } `
      -Message "unsafe release URI was accepted: $invalidUri"
  }

  $manifest = Join-Path $testRoot "checksums.txt"
  $archiveName = "corresync_9.8.7_windows_amd64.zip"
  $checksum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  [IO.File]::WriteAllText($manifest, "$checksum  $archiveName`n")
  Assert-True `
    -Condition ((Get-CorresyncChecksumEntry -Manifest $manifest -FileName $archiveName) -ceq $checksum) `
    -Message "exact checksum entry was not returned"
  [IO.File]::AppendAllText($manifest, "$checksum  $archiveName`n")
  Assert-Throw `
    -Action { Get-CorresyncChecksumEntry -Manifest $manifest -FileName $archiveName } `
    -Message "duplicate checksum entry was accepted"

  Assert-True `
    -Condition ((Add-CorresyncPathEntry -Existing "C:\Tools" -Directory "C:\Tools") -ceq "C:\Tools") `
    -Message "PATH helper duplicated an existing entry"
  Assert-True `
    -Condition ((Add-CorresyncPathEntry -Existing "C:\Tools" -Directory "C:\Corr") -ceq "C:\Corr;C:\Tools") `
    -Message "PATH helper did not prioritize a missing entry"
  Assert-True `
    -Condition ((Add-CorresyncPathEntry `
        -Existing '%USERPROFILE%\.local\bin' `
        -Directory (Join-Path $env:USERPROFILE ".local\bin")
      ) -ceq '%USERPROFILE%\.local\bin') `
    -Message "PATH helper duplicated an expanded environment-variable entry"
  Assert-True `
    -Condition (-not (Test-CorresyncWriteAccess `
        -Rights ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
      )) `
    -Message "read-only access was treated as broad write access"
  foreach ($writeRight in @(
      [Security.AccessControl.FileSystemRights]::Write,
      [Security.AccessControl.FileSystemRights]::Modify,
      [Security.AccessControl.FileSystemRights]::FullControl
    )) {
    Assert-True `
      -Condition (Test-CorresyncWriteAccess -Rights $writeRight) `
      -Message "$writeRight was not treated as broad write access"
  }

  $fixtureDirectory = Join-Path $testRoot "fixture"
  [IO.Directory]::CreateDirectory($fixtureDirectory) | Out-Null
  $corrFixture = Join-Path $fixtureDirectory "corr.exe"
  $versionSymbol = "github.com/nkiyohara/corresync/internal/buildinfo.version"
  Push-Location $repositoryRoot
  try {
    & go build `
      -trimpath `
      -buildvcs=false `
      "-ldflags=-s -w -buildid= -X $versionSymbol=9.8.7" `
      -o $corrFixture `
      ./cmd/corr
    if ($LASTEXITCODE -ne 0) {
      throw "installer test failed: build fixture"
    }
  } finally {
    Pop-Location
  }
  Copy-Item -LiteralPath $corrFixture -Destination (Join-Path $fixtureDirectory "corresync.exe")

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $archive = Join-Path $testRoot "fixture.zip"
  $zip = [IO.Compression.ZipFile]::Open($archive, [IO.Compression.ZipArchiveMode]::Create)
  try {
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
      $zip,
      $corrFixture,
      "corr.exe",
      [IO.Compression.CompressionLevel]::Optimal
    ) | Out-Null
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
      $zip,
      (Join-Path $fixtureDirectory "corresync.exe"),
      "corresync.exe",
      [IO.Compression.CompressionLevel]::Optimal
    ) | Out-Null
  } finally {
    $zip.Dispose()
  }

  $candidateDirectory = Join-Path $testRoot "candidates"
  [IO.Directory]::CreateDirectory($candidateDirectory) | Out-Null
  Expand-CorresyncCandidateArchive -Archive $archive -Destination $candidateDirectory
  Assert-BytesEqual `
    -Expected $corrFixture `
    -Actual (Join-Path $candidateDirectory "corr.exe") `
    -Message "corr candidate does not match the fixture"

  $architecture = switch ([Environment]::GetEnvironmentVariable("PROCESSOR_ARCHITECTURE")) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "installer test failed: unsupported Windows architecture" }
  }
  Test-CorresyncCandidate `
    -Path (Join-Path $candidateDirectory "corr.exe") `
    -Version "9.8.7" `
    -Architecture $architecture `
    -WorkDirectory $testRoot

  $installDirectory = Join-Path $testRoot "install"
  Install-CorresyncCandidateSet `
    -CandidateDirectory $candidateDirectory `
    -InstallDirectory $installDirectory
  Assert-BytesEqual `
    -Expected $corrFixture `
    -Actual (Join-Path $installDirectory "corr.exe") `
    -Message "fresh corr installation does not match the fixture"
  Install-CorresyncCandidateSet `
    -CandidateDirectory $candidateDirectory `
    -InstallDirectory $installDirectory

  $oldCorr = [Text.Encoding]::UTF8.GetBytes("working corr")
  $oldCompat = [Text.Encoding]::UTF8.GetBytes("working compatibility")
  [IO.File]::WriteAllBytes((Join-Path $installDirectory "corr.exe"), $oldCorr)
  [IO.File]::WriteAllBytes((Join-Path $installDirectory "corresync.exe"), $oldCompat)
  $lockedCompat = [IO.File]::Open(
    (Join-Path $installDirectory "corresync.exe"),
    [IO.FileMode]::Open,
    [IO.FileAccess]::Read,
    [IO.FileShare]::None
  )
  try {
    Assert-Throw `
      -Action {
        Install-CorresyncCandidateSet `
          -CandidateDirectory $candidateDirectory `
          -InstallDirectory $installDirectory
      } `
      -Message "locked compatibility target did not trigger rollback"
  } finally {
    $lockedCompat.Dispose()
  }
  Assert-True `
    -Condition ([Convert]::ToBase64String($oldCorr) -ceq
      [Convert]::ToBase64String(
        [IO.File]::ReadAllBytes((Join-Path $installDirectory "corr.exe"))
      )) `
    -Message "rollback did not restore corr.exe"
  Assert-True `
    -Condition ([Convert]::ToBase64String($oldCompat) -ceq
      [Convert]::ToBase64String(
        [IO.File]::ReadAllBytes((Join-Path $installDirectory "corresync.exe"))
      )) `
    -Message "rollback changed corresync.exe"

  $unsafeArchive = Join-Path $testRoot "unsafe.zip"
  $unsafeZip = [IO.Compression.ZipFile]::Open(
    $unsafeArchive,
    [IO.Compression.ZipArchiveMode]::Create
  )
  try {
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
      $unsafeZip,
      $corrFixture,
      "../corr.exe",
      [IO.Compression.CompressionLevel]::Optimal
    ) | Out-Null
  } finally {
    $unsafeZip.Dispose()
  }
  $unsafeDestination = Join-Path $testRoot "unsafe-candidates"
  [IO.Directory]::CreateDirectory($unsafeDestination) | Out-Null
  Assert-Throw `
    -Action {
      Expand-CorresyncCandidateArchive `
        -Archive $unsafeArchive `
        -Destination $unsafeDestination
    } `
    -Message "unsafe ZIP entry was accepted"

  Write-Output "PowerShell installer tests passed"
} finally {
  if (Test-Path -LiteralPath $testRoot) {
    Remove-Item -LiteralPath $testRoot -Recurse -Force
  }
}
