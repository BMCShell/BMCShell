<#
.SYNOPSIS
    打包 BMCShell 便携版 ZIP，并生成发布校验文件。
.PARAMETER Version
    必填。与 BMCShell.exe 报告的版本一致的语义化版本号。
.PARAMETER Binary
    框架可执行文件。默认为本仓库的 build/BMCShell.exe。
.PARAMETER ShellBridge
    Bash 适配脚本。默认为本仓库的 shell/bmcshell.sh。
.PARAMETER Scaffold
    可选。要随框架一同打包的脚手架程序。
.PARAMETER OutputDirectory
    输出目录。默认为本仓库的 dist。
.EXAMPLE
    ./scripts/pack-release.ps1 -Version 3.0.0-dev.9 -OutputDirectory ./dist
#>

#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateLength(1, 128)]
    [ValidatePattern('\A(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?\z')]
    [string] $Version,

    [string] $Binary = (Join-Path $PSScriptRoot '../build/BMCShell.exe'),
    [string] $ShellBridge = (Join-Path $PSScriptRoot '../shell/bmcshell.sh'),
    [string] $Scaffold,
    [string] $OutputDirectory = (Join-Path $PSScriptRoot '../dist')
)

$ErrorActionPreference = 'Stop'

function Resolve-ReleaseFile([string] $Path) {
    $file = Get-Item -LiteralPath $Path
    if ($file -isnot [System.IO.FileInfo]) {
        throw "路径必须指向文件：$Path"
    }
    return $file.FullName
}

function Write-ReleaseZip([string] $Path, [hashtable[]] $Files) {
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew)
    try {
        $zip = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $true)
        try {
            foreach ($file in $Files) {
                if ($file.ContainsKey('Text')) {
                    $entry = $zip.CreateEntry($file.Entry)
                    $writer = [IO.StreamWriter]::new($entry.Open(), [Text.UTF8Encoding]::new($false))
                    try { $writer.Write($file.Text) } finally { $writer.Dispose() }
                } else {
                    $null = [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $file.Source, $file.Entry)
                }
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
            throw "文件必须是 Windows 可执行程序：$Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 64 -or $peOffset -gt ($stream.Length - 6)) {
            throw "可执行文件头无效：$Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550 -or $reader.ReadUInt16() -ne 0x8664) {
            throw "文件必须是 Windows x64 可执行程序：$Path"
        }
    } finally {
        $reader.Dispose()
    }
}

function Assert-ReleaseVersion([string] $Path, [string] $ExpectedVersion) {
    $info = [Diagnostics.ProcessStartInfo]::new($Path)
    $info.ArgumentList.Add('--version')
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($info)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (!$process.WaitForExit(10000)) {
            $process.Kill($true)
            throw 'BMCShell.exe 未在 10 秒内输出版本号。'
        }
        $reported = $stdout.GetAwaiter().GetResult().Trim()
        $null = $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0 -or $reported -cne "BMCShell $ExpectedVersion") {
            throw "BMCShell.exe 应输出 'BMCShell $ExpectedVersion'。请用发布版本号重新构建程序。"
        }
    } finally {
        $process.Dispose()
    }
}

$repo = Split-Path -Parent $PSScriptRoot
$platform = 'windows-amd64'
$binaryPath = Resolve-ReleaseFile $Binary
$releaseRoot = "BMCShell/bin/releases/$Version"
$coreFile = @{ Entry = "$releaseRoot/BMCShell.exe" }
$files = @(
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'shell/launcher.sh')); Entry = 'BMCShell/bin/bmcshell.sh' }
    @{ Text = "$Version`n"; Entry = 'BMCShell/bin/active' }
    $coreFile
    @{ Source = (Resolve-ReleaseFile $ShellBridge); Entry = "$releaseRoot/shell/bmcshell.sh" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'README.md')); Entry = "$releaseRoot/README.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/README.md')); Entry = "$releaseRoot/doc/README.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/SCAFFOLD-CLI.md')); Entry = "$releaseRoot/doc/SCAFFOLD-CLI.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/LIBRARY-PACKAGE.md')); Entry = "$releaseRoot/doc/LIBRARY-PACKAGE.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/SOFTWARE-SOURCES.md')); Entry = "$releaseRoot/doc/SOFTWARE-SOURCES.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'scripts/build-source-index.ps1')); Entry = "$releaseRoot/scripts/build-source-index.ps1" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'scripts/maintain-history.ps1')); Entry = "$releaseRoot/scripts/maintain-history.ps1" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/ENVIRONMENT-REGISTRY.md')); Entry = "$releaseRoot/doc/ENVIRONMENT-REGISTRY.md" }
    @{ Source = (Resolve-ReleaseFile (Join-Path $repo 'doc/ENVIRONMENT-REGISTRY-v3.csv')); Entry = "$releaseRoot/doc/ENVIRONMENT-REGISTRY-v3.csv" }
)
if ($Scaffold) {
    $scaffoldPath = Resolve-ReleaseFile $Scaffold
    Assert-WindowsX64 $scaffoldPath
    $files += @{ Source = $scaffoldPath; Entry = "$releaseRoot/BMCShell-Scaffold.exe" }
}
$license = Join-Path $repo 'LICENSE'
if (Test-Path -LiteralPath $license -PathType Leaf) {
    $files += @{ Source = (Resolve-ReleaseFile $license); Entry = "$releaseRoot/LICENSE" }
}
$thirdPartyNotices = Join-Path $repo 'THIRD_PARTY_NOTICES.txt'
if (Test-Path -LiteralPath $thirdPartyNotices -PathType Leaf) {
    $files += @{ Source = (Resolve-ReleaseFile $thirdPartyNotices); Entry = "$releaseRoot/THIRD_PARTY_NOTICES.txt" }
}

$output = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$null = [IO.Directory]::CreateDirectory($output)
$baseName = "BMCShell-$Version-$Platform"
$archivePath = Join-Path $output "$baseName.zip"
$manifestPath = Join-Path $output "$baseName.manifest.json"
if ((Test-Path -LiteralPath $archivePath) -or (Test-Path -LiteralPath $manifestPath)) {
    throw "版本 $Version 的 $Platform 发行包已存在。请指定新版本或其他输出目录。"
}

$stagingName = [guid]::NewGuid().ToString('N')
$stagedBinary = Join-Path $output ".$stagingName.version-check.exe"
$stagedArchive = Join-Path $output ".$stagingName.archive.tmp"
$stagedManifest = Join-Path $output ".$stagingName.manifest.tmp"
$archivePublished = $false
try {
    # Verify the private snapshot that will actually be archived.
    [IO.File]::Copy($binaryPath, $stagedBinary, $false)
    Assert-WindowsX64 $stagedBinary
    Assert-ReleaseVersion $stagedBinary $Version
    $coreFile.Source = $stagedBinary
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
    [IO.File]::Delete($stagedBinary)
    [IO.File]::Delete($stagedArchive)
    [IO.File]::Delete($stagedManifest)
}

Get-Item -LiteralPath $archivePath, $manifestPath
