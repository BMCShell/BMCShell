#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('\A[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?\z')]
    [string] $Version,

    [Parameter(Mandatory)] [string] $Binary,
    [Parameter(Mandatory)] [string] $ShellBridge,
    [string] $Scaffold,
    [string] $OutputDirectory = (Join-Path $PSScriptRoot '../dist')
)

$ErrorActionPreference = 'Stop'

function Resolve-ReleaseFile([string] $Path) {
    $file = Get-Item -LiteralPath $Path
    if ($file -isnot [System.IO.FileInfo]) {
        throw "Expected a file: $Path"
    }
    return $file.FullName
}

function Write-ReleaseZip([string] $Path, [hashtable[]] $Files) {
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew)
    try {
        $zip = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $true)
        try {
            foreach ($file in $Files) {
                $null = [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $file.Source, $file.Entry)
            }
        } finally {
            $zip.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function Assert-WindowsX64([string] $Path) {
    $reader = [IO.BinaryReader]::new([IO.File]::OpenRead($Path))
    try {
        $stream = $reader.BaseStream
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) {
            throw "Expected a Windows executable: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 64 -or $peOffset -gt ($stream.Length - 6)) {
            throw "Invalid executable header: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550 -or $reader.ReadUInt16() -ne 0x8664) {
            throw "Expected a Windows x64 executable: $Path"
        }
    } finally {
        $reader.Dispose()
    }
}

$repo = Split-Path -Parent $PSScriptRoot
$platform = 'windows-amd64'
$binaryPath = Resolve-ReleaseFile $Binary
Assert-WindowsX64 $binaryPath
$files = @(
    @{ Source = $binaryPath; Entry = 'BMCShell/BMCShell.exe' }
    @{ Source = (Resolve-ReleaseFile $ShellBridge); Entry = 'BMCShell/shell/bmcshell.sh' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'README.md')); Entry = 'BMCShell/README.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/README.md')); Entry = 'BMCShell/doc/README.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/SCAFFOLD-CLI.md')); Entry = 'BMCShell/doc/SCAFFOLD-CLI.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/LIBRARY-PACKAGE.md')); Entry = 'BMCShell/doc/LIBRARY-PACKAGE.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/SOFTWARE-SOURCES.md')); Entry = 'BMCShell/doc/SOFTWARE-SOURCES.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'scripts/build-source-index.ps1')); Entry = 'BMCShell/scripts/build-source-index.ps1' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/ENVIRONMENT-REGISTRY.md')); Entry = 'BMCShell/doc/ENVIRONMENT-REGISTRY.md' }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/ENVIRONMENT-REGISTRY-v3.csv')); Entry = 'BMCShell/doc/ENVIRONMENT-REGISTRY-v3.csv' }
)
if ($Scaffold) {
    $scaffoldPath = Resolve-ReleaseFile $Scaffold
    Assert-WindowsX64 $scaffoldPath
    $files += @{ Source = $scaffoldPath; Entry = 'BMCShell/BMCShell-Scaffold.exe' }
}
$license = Join-Path $repo 'LICENSE'
if (Test-Path -LiteralPath $license -PathType Leaf) {
    $files += @{ Source = (Resolve-ReleaseFile $license); Entry = 'BMCShell/LICENSE' }
}
$thirdPartyNotices = Join-Path $repo 'THIRD_PARTY_NOTICES.txt'
if (Test-Path -LiteralPath $thirdPartyNotices -PathType Leaf) {
    $files += @{ Source = (Resolve-ReleaseFile $thirdPartyNotices); Entry = 'BMCShell/THIRD_PARTY_NOTICES.txt' }
}

$output = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$null = [IO.Directory]::CreateDirectory($output)
$baseName = "BMCShell-$Version-$Platform"
$archivePath = Join-Path $output "$baseName.zip"
$manifestPath = Join-Path $output "$baseName.manifest.json"
if ((Test-Path -LiteralPath $archivePath) -or (Test-Path -LiteralPath $manifestPath)) {
    throw "A release already exists for $Version / $Platform. Choose a new version or output directory."
}

$stagingName = [guid]::NewGuid().ToString('N')
$stagedArchive = Join-Path $output ".$stagingName.archive.tmp"
$stagedManifest = Join-Path $output ".$stagingName.manifest.tmp"
$archivePublished = $false
try {
    Write-ReleaseZip $stagedArchive $files

    $manifest = [ordered]@{
        schema = 1
        product = 'BMCShell'
        version = $Version
        platform = $Platform
        artifact = [IO.Path]::GetFileName($archivePath)
        sha256 = (Get-FileHash -LiteralPath $stagedArchive -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    [IO.File]::WriteAllText($stagedManifest, ($manifest | ConvertTo-Json) + "`n")
    [IO.File]::Move($stagedArchive, $archivePath)
    $archivePublished = $true
    [IO.File]::Move($stagedManifest, $manifestPath)
} catch {
    if ($archivePublished) { [IO.File]::Delete($archivePath) }
    throw
} finally {
    [IO.File]::Delete($stagedArchive)
    [IO.File]::Delete($stagedManifest)
}

Get-Item -LiteralPath $archivePath, $manifestPath
