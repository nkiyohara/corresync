param(
  [switch]$NoRun
)

# Install the latest stable Corresync release on Windows without elevation.
# Review this script before running it:
#   https://corresync.org/install.ps1
#
# Optional process environment variables:
#   CORRESYNC_VERSION=v0.8.0
#   CORRESYNC_INSTALL_DIR=C:\Users\you\.local\bin
#   CORRESYNC_NO_PATH_UPDATE=1

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

function Write-CorresyncMessage {
  param([Parameter(Mandatory = $true)][string]$Message)
  Write-Output $Message
}

function Assert-CorresyncHttpsUri {
  param([Parameter(Mandatory = $true)][Uri]$Uri)

  if (-not $Uri.IsAbsoluteUri -or $Uri.Scheme -ne "https") {
    throw "release downloads must use an absolute HTTPS URL"
  }
  if (-not $Uri.IsDefaultPort -and $Uri.Port -ne 443) {
    throw "release downloads must use the default HTTPS port"
  }
  if ($Uri.UserInfo.Length -ne 0 -or $Uri.Fragment.Length -ne 0) {
    throw "release download URLs must not contain user information or fragments"
  }

  $hostName = $Uri.IdnHost.ToLowerInvariant()
  switch ($hostName) {
    "github.com" {
      if (-not $Uri.AbsolutePath.StartsWith(
          "/nkiyohara/corresync/releases/",
          [StringComparison]::Ordinal
        )) {
        throw "release URL left the canonical Corresync repository"
      }
    }
    "release-assets.githubusercontent.com" {
      if (-not $Uri.AbsolutePath.StartsWith(
          "/github-production-release-asset/",
          [StringComparison]::Ordinal
        )) {
        throw "release asset URL has an unexpected path"
      }
    }
    default {
      throw "release download redirected to an unapproved host: $hostName"
    }
  }
}

function Get-CorresyncHttpClient {
  Add-Type -AssemblyName System.Net.Http
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

  $handler = New-Object System.Net.Http.HttpClientHandler
  $handler.AllowAutoRedirect = $false
  $client = New-Object System.Net.Http.HttpClient($handler)
  $client.Timeout = [TimeSpan]::FromSeconds(90)
  $client.DefaultRequestHeaders.UserAgent.ParseAdd("Corresync-Installer/1")
  return $client
}

function Get-CorresyncHttpsResponse {
  param(
    [Parameter(Mandatory = $true)][System.Net.Http.HttpClient]$Client,
    [Parameter(Mandatory = $true)][Uri]$Uri
  )

  $current = $Uri
  for ($redirects = 0; $redirects -le 10; $redirects++) {
    Assert-CorresyncHttpsUri -Uri $current
    $request = New-Object System.Net.Http.HttpRequestMessage(
      [System.Net.Http.HttpMethod]::Get,
      $current
    )
    try {
      $response = $Client.SendAsync(
        $request,
        [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead
      ).GetAwaiter().GetResult()
    } finally {
      $request.Dispose()
    }

    $status = [int]$response.StatusCode
    if ($status -in @(301, 302, 303, 307, 308)) {
      try {
        $location = $response.Headers.Location
        if ($null -eq $location) {
          throw "release redirect did not include a Location header"
        }
        if ($location.IsAbsoluteUri) {
          $next = $location
        } else {
          $next = [Uri]::new($current, $location)
        }
      } finally {
        $response.Dispose()
      }
      $current = $next
      continue
    }

    Assert-CorresyncHttpsUri -Uri $current
    return @{
      Response = $response
      Uri      = $current
    }
  }

  throw "release download exceeded the redirect limit"
}

function Get-CorresyncFinalUri {
  param(
    [Parameter(Mandatory = $true)][System.Net.Http.HttpClient]$Client,
    [Parameter(Mandatory = $true)][Uri]$Uri
  )

  $result = Get-CorresyncHttpsResponse -Client $Client -Uri $Uri
  try {
    if ([int]$result.Response.StatusCode -ne 200) {
      throw "release lookup returned HTTP $([int]$result.Response.StatusCode)"
    }
    return [Uri]$result.Uri
  } finally {
    $result.Response.Dispose()
  }
}

function Save-CorresyncBoundedDownload {
  param(
    [Parameter(Mandatory = $true)][System.Net.Http.HttpClient]$Client,
    [Parameter(Mandatory = $true)][Uri]$Uri,
    [Parameter(Mandatory = $true)][string]$Destination,
    [Parameter(Mandatory = $true)][long]$MaximumBytes
  )

  if (Test-Path -LiteralPath $Destination) {
    throw "download destination already exists: $Destination"
  }

  $result = Get-CorresyncHttpsResponse -Client $Client -Uri $Uri
  try {
    if ([int]$result.Response.StatusCode -ne 200) {
      throw "release download returned HTTP $([int]$result.Response.StatusCode)"
    }
    $contentLength = $result.Response.Content.Headers.ContentLength
    if ($null -ne $contentLength -and [long]$contentLength -gt $MaximumBytes) {
      throw "release download exceeds the $MaximumBytes-byte limit"
    }

    $inputStream = $result.Response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
    $outputStream = New-Object System.IO.FileStream(
      $Destination,
      [System.IO.FileMode]::CreateNew,
      [System.IO.FileAccess]::Write,
      [System.IO.FileShare]::None
    )
    try {
      $buffer = New-Object byte[] 65536
      [long]$written = 0
      while (($read = $inputStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
        $written += $read
        if ($written -gt $MaximumBytes) {
          throw "release download exceeds the $MaximumBytes-byte limit"
        }
        $outputStream.Write($buffer, 0, $read)
      }
      $outputStream.Flush($true)
    } finally {
      $outputStream.Dispose()
      $inputStream.Dispose()
    }
  } catch {
    Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
    throw
  } finally {
    $result.Response.Dispose()
  }
}

function Save-CorresyncDownloadWithRetry {
  param(
    [Parameter(Mandatory = $true)][System.Net.Http.HttpClient]$Client,
    [Parameter(Mandatory = $true)][Uri]$Uri,
    [Parameter(Mandatory = $true)][string]$Destination,
    [Parameter(Mandatory = $true)][long]$MaximumBytes
  )

  for ($attempt = 1; $attempt -le 3; $attempt++) {
    try {
      Save-CorresyncBoundedDownload `
        -Client $Client `
        -Uri $Uri `
        -Destination $Destination `
        -MaximumBytes $MaximumBytes
      return
    } catch {
      Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
      if ($attempt -eq 3) {
        throw
      }
      Start-Sleep -Seconds 1
    }
  }
}

function Initialize-CorresyncPrivateDirectory {
  param(
    [Parameter(Mandatory = $true)][string]$Parent,
    [Parameter(Mandatory = $true)][string]$Prefix
  )

  $name = "$Prefix$([Guid]::NewGuid().ToString('N'))"
  $path = Join-Path $Parent $name
  [IO.Directory]::CreateDirectory($path) | Out-Null

  $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
  $acl = Get-Acl -LiteralPath $path
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($rule in @($acl.Access)) {
    $acl.RemoveAccessRuleAll($rule)
  }
  $rights = [Security.AccessControl.FileSystemRights]::FullControl
  $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor `
    [Security.AccessControl.InheritanceFlags]::ObjectInherit
  $propagation = [Security.AccessControl.PropagationFlags]::None
  $allow = [Security.AccessControl.AccessControlType]::Allow
  $accessRule = New-Object Security.AccessControl.FileSystemAccessRule(
    $currentSid,
    $rights,
    $inheritance,
    $propagation,
    $allow
  )
  $acl.SetOwner($currentSid)
  $acl.AddAccessRule($accessRule) | Out-Null
  Set-Acl -LiteralPath $path -AclObject $acl
  return $path
}

function Get-CorresyncOwnerSid {
  param([Parameter(Mandatory = $true)][string]$Path)
  $acl = Get-Acl -LiteralPath $Path
  return $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
}

function Test-CorresyncWriteAccess {
  param(
    [Parameter(Mandatory = $true)]
    [Security.AccessControl.FileSystemRights]$Rights
  )

  $writeMask = [Security.AccessControl.FileSystemRights]::Write -bor `
    [Security.AccessControl.FileSystemRights]::Delete -bor `
    [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor `
    [Security.AccessControl.FileSystemRights]::ChangePermissions -bor `
    [Security.AccessControl.FileSystemRights]::TakeOwnership
  return ($Rights -band $writeMask) -ne 0
}

function Assert-CorresyncOwnedPath {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][bool]$Directory
  )

  $item = Get-Item -LiteralPath $Path -Force
  if ($Directory -ne [bool]$item.PSIsContainer) {
    throw "install target has the wrong file type: $Path"
  }
  if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "install target must not be a symbolic link or reparse point: $Path"
  }

  $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  if ((Get-CorresyncOwnerSid -Path $Path) -ne $currentSid) {
    throw "install target must be owned by the current user: $Path"
  }

  $broadWriteSids = @("S-1-1-0", "S-1-5-11", "S-1-5-32-545")
  $acl = Get-Acl -LiteralPath $Path
  foreach ($rule in $acl.Access) {
    if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
      continue
    }
    $sid = $rule.IdentityReference.Translate(
      [Security.Principal.SecurityIdentifier]
    ).Value
    if ($sid -in $broadWriteSids -and
      (Test-CorresyncWriteAccess -Rights $rule.FileSystemRights)) {
      throw "install target grants broad write access: $Path"
    }
  }
}

function Get-CorresyncChecksumEntry {
  param(
    [Parameter(Mandatory = $true)][string]$Manifest,
    [Parameter(Mandatory = $true)][string]$FileName
  )

  $entries = @()
  foreach ($line in Get-Content -LiteralPath $Manifest -Encoding UTF8) {
    if ($line -match '^([0-9A-Fa-f]{64})  (.+)$' -and $Matches[2] -ceq $FileName) {
      $entries += $Matches[1].ToLowerInvariant()
    }
  }
  if ($entries.Count -ne 1) {
    throw "checksum manifest must contain exactly one $FileName entry"
  }
  return $entries[0]
}

function Get-CorresyncSha256 {
  param([Parameter(Mandatory = $true)][string]$Path)

  $stream = [IO.File]::OpenRead($Path)
  $sha = [Security.Cryptography.SHA256]::Create()
  try {
    $digest = $sha.ComputeHash($stream)
  } finally {
    $sha.Dispose()
    $stream.Dispose()
  }
  return ([BitConverter]::ToString($digest)).Replace("-", "").ToLowerInvariant()
}

function Copy-CorresyncBoundedStream {
  param(
    [Parameter(Mandatory = $true)][IO.Stream]$Source,
    [Parameter(Mandatory = $true)][string]$Destination,
    [Parameter(Mandatory = $true)][long]$ExpectedBytes,
    [Parameter(Mandatory = $true)][long]$MaximumBytes
  )

  if ($ExpectedBytes -lt 1 -or $ExpectedBytes -gt $MaximumBytes) {
    throw "release archive candidate has an invalid size"
  }
  $output = New-Object IO.FileStream(
    $Destination,
    [IO.FileMode]::CreateNew,
    [IO.FileAccess]::Write,
    [IO.FileShare]::None
  )
  $copySucceeded = $false
  try {
    $buffer = New-Object byte[] 65536
    [long]$written = 0
    while (($read = $Source.Read($buffer, 0, $buffer.Length)) -gt 0) {
      $written += $read
      if ($written -gt $MaximumBytes) {
        throw "release archive candidate exceeds the size limit"
      }
      $output.Write($buffer, 0, $read)
    }
    if ($written -ne $ExpectedBytes) {
      throw "release archive candidate size does not match its inventory"
    }
    $output.Flush($true)
    $copySucceeded = $true
  } finally {
    $output.Dispose()
    if (-not $copySucceeded) {
      Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
    }
  }
}

function Expand-CorresyncCandidateArchive {
  param(
    [Parameter(Mandatory = $true)][string]$Archive,
    [Parameter(Mandatory = $true)][string]$Destination
  )

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
  try {
    if ($zip.Entries.Count -gt 4096) {
      throw "release archive contains more than 4096 entries"
    }
    [long]$totalBytes = 0
    $selected = @{}
    foreach ($entry in $zip.Entries) {
      if ($entry.FullName.Length -gt 512 -or $entry.FullName.Contains("\")) {
        throw "release archive contains an invalid entry name"
      }
      if ($entry.FullName.IndexOf([char]0) -ge 0) {
        throw "release archive contains a null byte in an entry name"
      }
      $segments = $entry.FullName.Split('/')
      if ($entry.FullName.StartsWith("/") -or $segments -contains "..") {
        throw "release archive contains an unsafe entry name"
      }
      if ($entry.Length -lt 0 -or $entry.Length -gt (268435456 - $totalBytes)) {
        throw "release archive expands beyond the 256 MiB limit"
      }
      $totalBytes += [long]$entry.Length
      if ($entry.FullName -ceq "corr.exe" -or $entry.FullName -ceq "corresync.exe") {
        if ($selected.ContainsKey($entry.FullName)) {
          throw "release archive contains duplicate $($entry.FullName) entries"
        }
        if ($entry.Length -lt 1 -or $entry.Length -gt 67108864) {
          throw "release archive $($entry.FullName) is not a bounded regular file"
        }
        $selected[$entry.FullName] = $entry
      }
    }

    foreach ($name in @("corr.exe", "corresync.exe")) {
      if (-not $selected.ContainsKey($name)) {
        throw "release archive is missing $name"
      }
      $entry = $selected[$name]
      $source = $entry.Open()
      try {
        Copy-CorresyncBoundedStream `
          -Source $source `
          -Destination (Join-Path $Destination $name) `
          -ExpectedBytes $entry.Length `
          -MaximumBytes 67108864
      } finally {
        $source.Dispose()
      }
    }
  } finally {
    $zip.Dispose()
  }

  $corrHash = Get-CorresyncSha256 -Path (Join-Path $Destination "corr.exe")
  $compatHash = Get-CorresyncSha256 -Path (Join-Path $Destination "corresync.exe")
  if ($corrHash -cne $compatHash) {
    throw "corr.exe and corresync.exe compatibility executables are not identical"
  }
}

function Test-CorresyncCandidate {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$Architecture,
    [Parameter(Mandatory = $true)][string]$WorkDirectory
  )

  $stdout = Join-Path $WorkDirectory "candidate-version.json"
  $stderr = Join-Path $WorkDirectory "candidate-version.stderr"
  $process = Start-Process `
    -FilePath $Path `
    -ArgumentList @("version", "--json") `
    -NoNewWindow `
    -PassThru `
    -RedirectStandardOutput $stdout `
    -RedirectStandardError $stderr
  $deadline = [DateTime]::UtcNow.AddSeconds(5)
  while (-not $process.HasExited) {
    foreach ($outputPath in @($stdout, $stderr)) {
      if ((Test-Path -LiteralPath $outputPath) -and
        (Get-Item -LiteralPath $outputPath).Length -gt 65536) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        try {
          $process.WaitForExit(2000) | Out-Null
        } catch {
          Write-Verbose "candidate process was already reaped by Windows"
        }
        throw "candidate version response exceeds the 65536-byte limit"
      }
    }
    if ([DateTime]::UtcNow -ge $deadline) {
      Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
      try {
        $process.WaitForExit(2000) | Out-Null
      } catch {
        Write-Verbose "candidate process was already reaped by Windows"
      }
      throw "candidate version check exceeded the five-second limit"
    }
    Start-Sleep -Milliseconds 25
  }
  $process.WaitForExit()
  if ($process.ExitCode -ne 0) {
    throw "candidate version check failed"
  }
  foreach ($outputPath in @($stdout, $stderr)) {
    if ((Get-Item -LiteralPath $outputPath).Length -gt 65536) {
      throw "candidate version response exceeds the 65536-byte limit"
    }
  }

  $candidate = Get-Content -LiteralPath $stdout -Raw -Encoding UTF8 | ConvertFrom-Json
  if ([string]$candidate.version -cne $Version) {
    throw "candidate version $($candidate.version) does not match $Version"
  }
  if ([string]$candidate.os -cne "windows") {
    throw "candidate operating system $($candidate.os) is not windows"
  }
  if ([string]$candidate.arch -cne $Architecture) {
    throw "candidate architecture $($candidate.arch) does not match $Architecture"
  }
}

function Install-CorresyncCandidateSet {
  param(
    [Parameter(Mandatory = $true)][string]$CandidateDirectory,
    [Parameter(Mandatory = $true)][string]$InstallDirectory
  )

  if (Test-Path -LiteralPath $InstallDirectory) {
    Assert-CorresyncOwnedPath -Path $InstallDirectory -Directory $true
  } else {
    [IO.Directory]::CreateDirectory($InstallDirectory) | Out-Null
    Assert-CorresyncOwnedPath -Path $InstallDirectory -Directory $true
  }

  $targets = @{
    "corr.exe"       = Join-Path $InstallDirectory "corr.exe"
    "corresync.exe" = Join-Path $InstallDirectory "corresync.exe"
  }
  foreach ($target in $targets.Values) {
    if (Test-Path -LiteralPath $target) {
      Assert-CorresyncOwnedPath -Path $target -Directory $false
    }
  }

  $transaction = Join-Path `
    $InstallDirectory `
    ".corresync-install-$([Guid]::NewGuid().ToString('N'))"
  [IO.Directory]::CreateDirectory($transaction) | Out-Null
  $old = @{}
  $installed = @{}
  $committed = $false
  try {
    foreach ($name in @("corr.exe", "corresync.exe")) {
      Copy-Item `
        -LiteralPath (Join-Path $CandidateDirectory $name) `
        -Destination (Join-Path $transaction "$name.new")
    }

    foreach ($name in @("corr.exe", "corresync.exe")) {
      $target = $targets[$name]
      $backup = Join-Path $transaction "$name.old"
      if (Test-Path -LiteralPath $target) {
        Move-Item -LiteralPath $target -Destination $backup
        $old[$name] = $backup
      }
      Move-Item -LiteralPath (Join-Path $transaction "$name.new") -Destination $target
      $installed[$name] = $true
    }
    $committed = $true
  } finally {
    if (-not $committed) {
      foreach ($name in @("corresync.exe", "corr.exe")) {
        $target = $targets[$name]
        if ($installed.ContainsKey($name) -and (Test-Path -LiteralPath $target)) {
          Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
        }
        if ($old.ContainsKey($name) -and (Test-Path -LiteralPath $old[$name])) {
          Move-Item -LiteralPath $old[$name] -Destination $target -Force
        }
      }
    }
    if (Test-Path -LiteralPath $transaction) {
      Remove-Item -LiteralPath $transaction -Recurse -Force
    }
  }
}

function Test-CorresyncTruthy {
  param([AllowNull()][string]$Value)
  return $Value -in @("1", "true", "TRUE", "yes", "YES")
}

function Add-CorresyncPathEntry {
  param(
    [AllowNull()][string]$Existing,
    [Parameter(Mandatory = $true)][string]$Directory
  )

  $normalized = [Environment]::ExpandEnvironmentVariables(
    $Directory.Trim()
  ).TrimEnd('\')
  foreach ($entry in @($Existing -split ';')) {
    $expandedEntry = [Environment]::ExpandEnvironmentVariables(
      $entry.Trim()
    ).TrimEnd('\')
    if ($expandedEntry -ieq $normalized) {
      return $Existing
    }
  }
  if ([string]::IsNullOrWhiteSpace($Existing)) {
    return $Directory
  }
  return "$Directory;$($Existing.Trim(';'))"
}

function Invoke-CorresyncInstall {
  $repositoryUrl = "https://github.com/nkiyohara/corresync"
  $latestUrl = [Uri]"$repositoryUrl/releases/latest"
  $identityPrefix = "$repositoryUrl/.github/workflows/release.yml@refs/tags/"
  $oidcIssuer = "https://token.actions.githubusercontent.com"

  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "the PowerShell installer supports Windows; use install.sh on macOS or Linux"
  }

  $nativeArchitecture = [Environment]::GetEnvironmentVariable(
    "PROCESSOR_ARCHITEW6432",
    "Process"
  )
  if ([string]::IsNullOrWhiteSpace($nativeArchitecture)) {
    $nativeArchitecture = [Environment]::GetEnvironmentVariable(
      "PROCESSOR_ARCHITECTURE",
      "Process"
    )
  }
  switch ($nativeArchitecture.ToUpperInvariant()) {
    "AMD64" { $architecture = "amd64" }
    "ARM64" { $architecture = "arm64" }
    default { throw "unsupported Windows architecture: $nativeArchitecture" }
  }

  $client = Get-CorresyncHttpClient
  $workDirectory = $null
  try {
    $requestedVersion = [Environment]::GetEnvironmentVariable(
      "CORRESYNC_VERSION",
      "Process"
    )
    if ([string]::IsNullOrWhiteSpace($requestedVersion)) {
      $finalUri = Get-CorresyncFinalUri -Client $client -Uri $latestUrl
      if ($finalUri.Query.Length -ne 0 -or
        $finalUri.AbsolutePath -notmatch '^/nkiyohara/corresync/releases/tag/(v[0-9]+\.[0-9]+\.[0-9]+)$') {
        throw "latest release redirected outside a stable canonical tag"
      }
      $tag = $Matches[1]
    } elseif ($requestedVersion.StartsWith("v", [StringComparison]::Ordinal)) {
      $tag = $requestedVersion
    } else {
      $tag = "v$requestedVersion"
    }
    if ($tag -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
      throw "version must be a stable tag such as v0.8.0"
    }

    $version = $tag.Substring(1)
    $archiveName = "corresync_${version}_windows_${architecture}.zip"
    $releaseUrl = "$repositoryUrl/releases/download/$tag"

    $userProfile = [Environment]::GetFolderPath(
      [Environment+SpecialFolder]::UserProfile
    )
    if ([string]::IsNullOrWhiteSpace($userProfile) -or
      -not [IO.Path]::IsPathRooted($userProfile)) {
      throw "the current user profile is unavailable"
    }
    $defaultInstallDirectory = Join-Path $userProfile ".local\bin"
    $requestedInstallDirectory = [Environment]::GetEnvironmentVariable(
      "CORRESYNC_INSTALL_DIR",
      "Process"
    )
    if ([string]::IsNullOrWhiteSpace($requestedInstallDirectory)) {
      $installDirectory = $defaultInstallDirectory
    } else {
      if (-not [IO.Path]::IsPathRooted($requestedInstallDirectory) -or
        $requestedInstallDirectory.StartsWith("\\", [StringComparison]::Ordinal)) {
        throw "CORRESYNC_INSTALL_DIR must be an absolute local path"
      }
      if ($requestedInstallDirectory.Contains("`n") -or
        $requestedInstallDirectory.Contains("`r")) {
        throw "CORRESYNC_INSTALL_DIR must not contain a newline"
      }
      $installDirectory = [IO.Path]::GetFullPath($requestedInstallDirectory)
    }
    if ($installDirectory.StartsWith("\\", [StringComparison]::Ordinal)) {
      throw "the install directory must be on a local Windows drive"
    }

    $workDirectory = Initialize-CorresyncPrivateDirectory `
      -Parent ([IO.Path]::GetTempPath()) `
      -Prefix "corresync-install-"
    $manifestPath = Join-Path $workDirectory "checksums.txt"
    $bundlePath = Join-Path $workDirectory "checksums.txt.sigstore.json"
    $archivePath = Join-Path $workDirectory $archiveName

    Write-CorresyncMessage "Downloading Corresync $tag for windows/$architecture..."
    Save-CorresyncDownloadWithRetry `
      -Client $client `
      -Uri ([Uri]"$releaseUrl/checksums.txt") `
      -Destination $manifestPath `
      -MaximumBytes 65536
    Save-CorresyncDownloadWithRetry `
      -Client $client `
      -Uri ([Uri]"$releaseUrl/checksums.txt.sigstore.json") `
      -Destination $bundlePath `
      -MaximumBytes 1048576
    Save-CorresyncDownloadWithRetry `
      -Client $client `
      -Uri ([Uri]"$releaseUrl/$archiveName") `
      -Destination $archivePath `
      -MaximumBytes 67108864

    $expectedChecksum = Get-CorresyncChecksumEntry `
      -Manifest $manifestPath `
      -FileName $archiveName
    $actualChecksum = Get-CorresyncSha256 -Path $archivePath
    if ($actualChecksum -cne $expectedChecksum) {
      throw "release archive checksum does not match checksums.txt"
    }
    Write-CorresyncMessage "Verified the release archive SHA-256 checksum."

    $cosign = Get-Command cosign -CommandType Application -ErrorAction SilentlyContinue |
      Select-Object -First 1
    if ($null -ne $cosign) {
      & $cosign.Source verify-blob `
        --bundle $bundlePath `
        --certificate-identity "$identityPrefix$tag" `
        --certificate-oidc-issuer $oidcIssuer `
        $manifestPath | Out-Null
      if ($LASTEXITCODE -ne 0) {
        throw "Sigstore verification failed"
      }
      Write-CorresyncMessage "Verified the exact GitHub Actions Sigstore identity."
    } else {
      Write-Warning "cosign was not found; SHA-256 is verified, but Sigstore provenance was not checked"
    }

    $candidateDirectory = Join-Path $workDirectory "candidates"
    [IO.Directory]::CreateDirectory($candidateDirectory) | Out-Null
    Expand-CorresyncCandidateArchive -Archive $archivePath -Destination $candidateDirectory
    Test-CorresyncCandidate `
      -Path (Join-Path $candidateDirectory "corr.exe") `
      -Version $version `
      -Architecture $architecture `
      -WorkDirectory $workDirectory
    Write-CorresyncMessage "Validated candidate version, operating system, and architecture."

    Install-CorresyncCandidateSet `
      -CandidateDirectory $candidateDirectory `
      -InstallDirectory $installDirectory

    $pathUpdated = $false
    $noPathUpdate = [Environment]::GetEnvironmentVariable(
      "CORRESYNC_NO_PATH_UPDATE",
      "Process"
    )
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "Process")
    $processPath = Add-CorresyncPathEntry -Existing $currentPath -Directory $installDirectory
    $alreadyOnPath = $processPath -ceq $currentPath
    if (-not $alreadyOnPath -and -not (Test-CorresyncTruthy -Value $noPathUpdate)) {
      try {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $newUserPath = Add-CorresyncPathEntry `
          -Existing $userPath `
          -Directory $installDirectory
        if ($newUserPath.Length -gt 32767) {
          throw "the updated user PATH would exceed the Windows environment limit"
        }
        if ($newUserPath -cne $userPath) {
          [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
          $pathUpdated = $true
        }
      } catch {
        Write-Warning "could not add $installDirectory to the user PATH: $($_.Exception.Message)"
      }
    }

    Write-CorresyncMessage ""
    Write-CorresyncMessage "Installed Corresync ${version}:"
    Write-CorresyncMessage "  $(Join-Path $installDirectory 'corr.exe')"
    Write-CorresyncMessage "  $(Join-Path $installDirectory 'corresync.exe') (v0.8-v0.9 compatibility)"
    if ($pathUpdated) {
      Write-CorresyncMessage "Added $installDirectory to PATH for new terminals. Open a new terminal before using corr."
    } elseif (-not $alreadyOnPath) {
      Write-CorresyncMessage "Add $installDirectory to PATH, then open a new terminal."
    }
    Write-CorresyncMessage ""
    Write-CorresyncMessage "Next:"
    Write-CorresyncMessage "  corr setup you@example.com --alias personal"
    Write-CorresyncMessage "  corr auth login --account personal"
    Write-CorresyncMessage "  corr mcp setup codex"
    Write-CorresyncMessage ""
    Write-CorresyncMessage "No account was configured, no login was started, and no MCP client was changed."
  } finally {
    if ($null -ne $client) {
      $client.Dispose()
    }
    if (-not [string]::IsNullOrWhiteSpace($workDirectory) -and
      (Test-Path -LiteralPath $workDirectory)) {
      try {
        Remove-Item -LiteralPath $workDirectory -Recurse -Force
      } catch {
        Write-Warning "could not remove temporary installer files: $($_.Exception.Message)"
      }
    }
  }
}

if (-not $NoRun) {
  Invoke-CorresyncInstall
}
