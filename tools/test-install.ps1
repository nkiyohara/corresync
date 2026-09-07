Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

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

# These tests use an in-memory HTTP handler and temporary files only. They can
# run on any PowerShell platform before the Windows lifecycle tests below.
Add-Type -AssemblyName System.Net.Http
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;

public sealed class CorresyncFixtureHandler : HttpMessageHandler {
  public readonly CorresyncFixtureContent Content;
  public CorresyncFixtureHandler(string mode, byte[] bytes) {
    Content = new CorresyncFixtureContent(mode, bytes);
  }
  protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
    return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) {
      RequestMessage = request, Content = Content
    });
  }
}

public sealed class CorresyncFixtureContent : HttpContent {
  private readonly string mode;
  private readonly CorresyncFixtureStream stream;
  private readonly TaskCompletionSource<Stream> pending = new TaskCompletionSource<Stream>();
  public bool Disposed;
  public CorresyncFixtureContent(string mode, byte[] bytes) {
    this.mode = mode;
    stream = new CorresyncFixtureStream(mode == "stall-body", bytes);
  }
  protected override bool TryComputeLength(out long length) { length = 0; return false; }
  protected override Task SerializeToStreamAsync(Stream target, TransportContext context) {
    throw new InvalidOperationException("The fixture must be read as a stream.");
  }
  protected override Task<Stream> CreateContentReadStreamAsync() {
    return mode == "stall-stream" ? pending.Task : Task.FromResult<Stream>(stream);
  }
  protected override void Dispose(bool disposing) {
    Disposed = true;
    pending.TrySetCanceled();
    stream.Dispose();
    base.Dispose(disposing);
  }
}

public sealed class CorresyncFixtureStream : Stream {
  private readonly bool stall;
  private readonly byte[] bytes;
  private int offset;
  private readonly TaskCompletionSource<int> pending = new TaskCompletionSource<int>();
  public CorresyncFixtureStream(bool stall, byte[] bytes) { this.stall = stall; this.bytes = bytes; }
  public override bool CanRead { get { return true; } }
  public override bool CanSeek { get { return false; } }
  public override bool CanWrite { get { return false; } }
  public override long Length { get { throw new NotSupportedException(); } }
  public override long Position { get { throw new NotSupportedException(); } set { throw new NotSupportedException(); } }
  public override int Read(byte[] buffer, int start, int count) { throw new InvalidOperationException("Use bounded async reads."); }
  public override Task<int> ReadAsync(byte[] buffer, int start, int count, CancellationToken token) {
    if (offset < bytes.Length) {
      int copied = Math.Min(count, bytes.Length - offset);
      Array.Copy(bytes, offset, buffer, start, copied);
      offset += copied;
      return Task.FromResult(copied);
    }
    // Deliberately ignore token: the caller's wait must still have a deadline.
    return stall ? pending.Task : Task.FromResult(0);
  }
  protected override void Dispose(bool disposing) { pending.TrySetCanceled(); base.Dispose(disposing); }
  public override void Flush() { }
  public override long Seek(long offset, SeekOrigin origin) { throw new NotSupportedException(); }
  public override void SetLength(long value) { throw new NotSupportedException(); }
  public override void Write(byte[] buffer, int start, int count) { throw new NotSupportedException(); }
}
'@

$blankMessage = @(Write-CorresyncMessage "")
Assert-True -Condition ($blankMessage.Count -eq 1 -and $blankMessage[0] -ceq "") `
  -Message "message output rejected the blank lines used by successful installations"

$httpTestRoot = Join-Path ([IO.Path]::GetTempPath()) "corresync-http-test-$([Guid]::NewGuid().ToString('N'))"
[IO.Directory]::CreateDirectory($httpTestRoot) | Out-Null
try {
  foreach ($mode in @("bytes", "oversized", "stall-stream", "stall-body")) {
    $payload = [Text.Encoding]::UTF8.GetBytes("synthetic download")
    $fixtureHandler = [CorresyncFixtureHandler]::new($mode, $payload)
    $fixtureClient = [Net.Http.HttpClient]::new($fixtureHandler)
    $destination = Join-Path $httpTestRoot "$mode.bin"
    $maximum = if ($mode -eq "oversized") { 4 } else { 1024 }
    $elapsed = [Diagnostics.Stopwatch]::StartNew()
    try {
      $download = {
        Save-CorresyncBoundedDownload `
          -Client $fixtureClient `
          -Uri ([Uri]"https://github.com/nkiyohara/corresync/releases/download/v9.8.7/synthetic") `
          -Destination $destination -MaximumBytes $maximum -TimeoutSeconds 1
      }
      if ($mode -eq "bytes") {
        & $download
        Assert-True `
          -Condition ([Convert]::ToBase64String([IO.File]::ReadAllBytes($destination)) -ceq
            [Convert]::ToBase64String($payload)) `
          -Message "bounded download did not preserve the synthetic bytes"
      } else {
        Assert-Throw -Action $download -Message "$mode download unexpectedly succeeded"
        Assert-True -Condition (-not (Test-Path -LiteralPath $destination)) `
          -Message "$mode download left a partial output"
      }
      if ($mode.StartsWith("stall-", [StringComparison]::Ordinal)) {
        Assert-True -Condition ($elapsed.Elapsed.TotalMilliseconds -ge 800 -and
          $elapsed.Elapsed.TotalSeconds -lt 5) `
          -Message "$mode download did not honor its one-second deadline"
      }
      Assert-True -Condition $fixtureHandler.Content.Disposed `
        -Message "$mode download did not dispose the HTTP response content"
    } finally {
      $fixtureClient.Dispose()
    }
  }
  Write-Output "PowerShell installer HTTP and message tests passed"
} finally {
  Remove-Item -LiteralPath $httpTestRoot -Recurse -Force
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
  Write-Output "PowerShell installer lifecycle tests skipped: Windows-only installation"
  exit 0
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
  $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  Assert-True `
    -Condition ((Get-CorresyncOwnerSid -Path $installDirectory) -ceq $currentSid) `
    -Message "fresh install directory is not owned by the current user"
  foreach ($installedName in @("corr.exe", "corresync.exe")) {
    Assert-True `
      -Condition ((Get-CorresyncOwnerSid `
          -Path (Join-Path $installDirectory $installedName)
        ) -ceq $currentSid) `
      -Message "$installedName is not owned by the current user"
  }
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
