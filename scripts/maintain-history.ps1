#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $InstallationDirectory,
    [string] $DataDirectory,
    [string] $BackupDirectory,
    [switch] $Apply
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$pathComparison = [StringComparison]::OrdinalIgnoreCase

function Test-Within([string] $Path, [string] $Root) {
    $prefix = $Root.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    $Path.StartsWith($prefix, $pathComparison)
}

function Assert-NoLinks([string] $Path) {
    $node = Get-Item -LiteralPath $Path -Force
    while ($null -ne $node) {
        if ($node.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw "Reparse points are not supported by history maintenance: $($node.FullName)"
        }
        $node = if ($node -is [IO.FileInfo]) { $node.Directory } else { $node.Parent }
    }
}

function Resolve-Directory([string] $Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if ($item -isnot [IO.DirectoryInfo]) { throw "Expected a directory: $Path" }
    Assert-NoLinks $item.FullName
    return $item.FullName
}

function Get-ActiveExecutable([string] $Root) {
    $marker = Join-Path $Root 'bin/active'
    Assert-NoLinks $marker
    $version = ([IO.File]::ReadAllText($marker)).TrimEnd([char]13, [char]10)
    if ($version -cnotmatch '\A[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?\z') {
        throw "Invalid active release: $marker"
    }
    $path = Join-Path $Root "bin/releases/$version/BMCShell.exe"
    $item = Get-Item -LiteralPath $path -Force
    if ($item -isnot [IO.FileInfo]) { throw "Missing release executable: $path" }
    Assert-NoLinks $item.FullName
    return $item.FullName
}

function Invoke-History([string] $Executable, [string] $Profile, [string] $Action, [string] $InputJson) {
    $previousData = $env:BMCSHELL_HOME
    $previousMode = $env:BMCSHELL_BRIDGE_MAINTENANCE
    try {
        $env:BMCSHELL_HOME = $Profile
        $env:BMCSHELL_BRIDGE_MAINTENANCE = '1'
        $result = if ($Action -eq 'retire') {
            $InputJson | & $Executable bridge history $Action
        } else {
            & $Executable bridge history $Action
        }
        if ($LASTEXITCODE -ne 0) { throw "BMCShell history $Action failed (exit $LASTEXITCODE)." }
        if ($Action -in 'preview', 'plan') { return $result | ConvertFrom-Json }
    } finally {
        $env:BMCSHELL_HOME = $previousData
        $env:BMCSHELL_BRIDGE_MAINTENANCE = $previousMode
    }
}

function Get-Candidates($Plan) {
    foreach ($group in 'Core', 'Packages', 'Libraries') {
        foreach ($path in $Plan.$group) {
            if ($path) { [string]$path }
        }
    }
}

function Open-ProfileLock([string] $Path) {
    if (Test-Path -LiteralPath $Path) { Assert-NoLinks $Path }
    $lock = [IO.File]::Open($Path, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
    try {
        $lock.Lock(0, 1)
        return $lock
    } catch {
        $lock.Dispose()
        throw
    }
}

function Read-Tree([string] $Root, [string] $CopyTo) {
    $entries = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::OrdinalIgnoreCase)
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($Root)
    if ($CopyTo) { $null = [IO.Directory]::CreateDirectory($CopyTo) }
    while ($pending.Count -gt 0) {
        foreach ($item in (Get-ChildItem -LiteralPath ($pending.Pop()) -Force)) {
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Reparse point in BMCShell profile: $($item.FullName)"
            }
            if ($excludedLocks.Contains($item.FullName)) { continue }
            $relative = [IO.Path]::GetRelativePath($Root, $item.FullName)
            if ($item -is [IO.DirectoryInfo]) {
                $entries.Add($relative, 'directory')
                $pending.Push($item.FullName)
                if ($CopyTo) { $null = [IO.Directory]::CreateDirectory((Join-Path $CopyTo $relative)) }
            } else {
                $entries.Add($relative, (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash)
                if ($CopyTo) {
                    $destination = Join-Path $CopyTo $relative
                    [IO.File]::Copy($item.FullName, $destination, $false)
                    [IO.File]::SetLastWriteTimeUtc($destination, $item.LastWriteTimeUtc)
                }
            }
        }
    }
    return ,$entries
}

function Assert-SameTree($Expected, $Actual, [string] $Description) {
    if ($Expected.Count -ne $Actual.Count) { throw "File list changed: $Description" }
    foreach ($path in $Expected.Keys) {
        if (!$Actual.ContainsKey($path) -or $Actual[$path] -ne $Expected[$path]) {
            throw "File did not verify ($Description): $path"
        }
    }
}

function Save-VerifiedTree([string] $Source, [string] $Destination) {
    $snapshot = Read-Tree $Source $Destination
    Assert-SameTree $snapshot (Read-Tree $Destination) $Destination
    Assert-SameTree $snapshot (Read-Tree $Source) $Source
}

$installation = Resolve-Directory $InstallationDirectory
if (!$DataDirectory) {
    $DataDirectory = if ($env:BMCSHELL_HOME) { $env:BMCSHELL_HOME } else { $installation }
}
$profile = Resolve-Directory $DataDirectory
$executable = Get-ActiveExecutable $installation
$preview = Invoke-History $executable $profile 'preview'
$planned = @(Get-Candidates $preview | Sort-Object)
if ($planned.Count -eq 0) {
    'No verified historical versions are eligible for cleanup.'
    return
}
if (!$Apply) {
    $planned
    return
}
if (!$BackupDirectory) { throw 'Use -BackupDirectory with -Apply.' }
$backup = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($BackupDirectory)
$backupParent = Resolve-Directory ([IO.Path]::GetDirectoryName($backup))
$backup = Join-Path $backupParent ([IO.Path]::GetFileName($backup))
foreach ($root in @($installation, $profile)) {
    if ($backup -eq $root -or (Test-Within $backup $root) -or (Test-Within $root $backup)) {
        throw 'Place the new backup directory outside the installation and data directories.'
    }
}
if (Test-Path -LiteralPath $backup) { throw "Backup directory already exists: $backup" }

foreach ($process in Get-Process -Name 'BMCShell' -ErrorAction SilentlyContinue) {
    if ($process.Path -and ((Test-Within $process.Path $installation) -or (Test-Within $process.Path $profile))) {
        throw 'Close BMCShell terminals and running BMCShell processes before applying cleanup.'
    }
}
$releaseLockPath = Join-Path $installation '.release-update.lock'
$profileLockPath = Join-Path $profile '.package-store.lock'
$excludedLocks = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$null = $excludedLocks.Add($releaseLockPath)
$null = $excludedLocks.Add($profileLockPath)
$releaseLock = Open-ProfileLock $releaseLockPath
$profileLock = $null
try {
    $profileLock = Open-ProfileLock $profileLockPath
    $currentPreview = Invoke-History $executable $profile 'preview'
    $currentPaths = @(Get-Candidates $currentPreview | Sort-Object)
    if ($currentPaths.Count -ne $planned.Count -or (Compare-Object $planned $currentPaths)) {
        throw 'History changed. Run the preview again.'
    }

    $null = [IO.Directory]::CreateDirectory($backup)
    $backup = Resolve-Directory $backup
    $backupInstall = Join-Path $backup 'installation'
    $backupProfile = Join-Path $backup 'data'
    if ($installation -eq $profile -or (Test-Within $profile $installation)) {
        Save-VerifiedTree $installation $backupInstall
        $backupProfile = if ($installation -eq $profile) { $backupInstall } else {
            Join-Path $backupInstall ([IO.Path]::GetRelativePath($installation, $profile))
        }
    } elseif (Test-Within $installation $profile) {
        Save-VerifiedTree $profile $backupProfile
        $backupInstall = Join-Path $backupProfile ([IO.Path]::GetRelativePath($profile, $installation))
    } else {
        Save-VerifiedTree $installation $backupInstall
        Save-VerifiedTree $profile $backupProfile
    }
    $backupExe = Get-ActiveExecutable $backupInstall
    $backupVersion = & $backupExe --version
    if ($LASTEXITCODE -ne 0 -or $backupVersion -ne (& $executable --version)) {
        throw 'The copied installation could not be started.'
    }
    Invoke-History $backupExe $backupProfile 'verify'
    "Verified backup: $backup"

    if (@($currentPreview.Libraries).Count -gt 0 -and $null -ne $currentPreview.Libraries) {
        $profileLock.Dispose()
        $profileLock = $null
        $expectedLibraries = ConvertTo-Json -InputObject @($currentPreview.Libraries) -Compress
        Invoke-History $executable $profile 'retire' $expectedLibraries
        $profileLock = Open-ProfileLock $profileLockPath
    }
    $ready = Invoke-History $executable $profile 'plan'
    $readyPaths = @(Get-Candidates $ready | Sort-Object)
    if ($readyPaths.Count -ne $planned.Count -or (Compare-Object $planned $readyPaths)) {
        throw 'History changed after backup. Run the preview again.'
    }
    foreach ($path in $readyPaths) {
        $resolved = Resolve-Directory $path
        if ($resolved -ne $path -or $resolved -eq $profile -or (Test-Within $profile $resolved)) {
            throw "Unexpected cleanup path: $path"
        }
        $copied = if (Test-Within $resolved (Join-Path $installation 'bin/releases')) {
            Join-Path $backupInstall ([IO.Path]::GetRelativePath($installation, $resolved))
        } elseif ((Test-Within $resolved (Join-Path $profile 'books')) -or
                  (Test-Within $resolved (Join-Path $profile 'plugins')) -or
                  (Test-Within $resolved (Join-Path $profile 'libraries'))) {
            Join-Path $backupProfile ([IO.Path]::GetRelativePath($profile, $resolved))
        } else {
            throw "Cleanup path is outside version storage: $resolved"
        }
        Assert-SameTree (Read-Tree $resolved) (Read-Tree (Resolve-Directory $copied)) $resolved
        Remove-Item -LiteralPath $resolved -Recurse -Force
        "Removed history: $resolved"
    }
} finally {
    if ($null -ne $profileLock) { $profileLock.Dispose() }
    $releaseLock.Dispose()
}
