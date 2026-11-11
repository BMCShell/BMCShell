#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $Archive,
    [Parameter(Mandatory)] [string] $PreviousArchive,
    [Parameter(Mandatory)] [string] $GitBash,
    [Parameter(Mandatory)] [string] $Msys2Bash
)

$ErrorActionPreference = 'Stop'

function Get-VerifiedRelease([string] $ZipPath) {
    $zip = (Resolve-Path -LiteralPath $ZipPath).Path
    $manifestPath = [IO.Path]::ChangeExtension($zip, '.manifest.json')
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.schema -ne 1 -or $manifest.product -cne 'BMCShell' -or
        $manifest.platform -cne 'windows-amd64' -or
        $manifest.version -cnotmatch '\A[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?\z' -or
        $manifest.artifact -cne [IO.Path]::GetFileName($zip) -or
        $manifest.sha256 -ne (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash) {
        throw "Invalid release or SHA-256 mismatch: $zip"
    }
    @{ Archive = $zip; Version = $manifest.version }
}

function Invoke-BashProfile([string] $Bash, [string] $Script) {
    $bashPath = (Resolve-Path -LiteralPath $Bash).Path
    $info = [Diagnostics.ProcessStartInfo]::new($bashPath)
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($arg in @('--noprofile', '--rcfile', $profilePath, '-i', '-c', $Script)) {
        $info.ArgumentList.Add($arg)
    }
    foreach ($name in @($info.Environment.Keys)) {
        if ($name -like 'BMCSHELL_*') { $null = $info.Environment.Remove($name) }
    }
    $bashDir = Split-Path -Parent $bashPath
    $posixDir = Join-Path (Split-Path -Parent $bashDir) 'usr/bin'
    $info.Environment['PATH'] = "$bashDir;$posixDir;$env:PATH"
    $info.Environment['BMCSHELL_RELEASE_TEST_ROOT'] = $installation
    $info.Environment['BMCSHELL_RELEASE_TEST_NEW'] = $current.Archive
    $info.Environment['BMCSHELL_RELEASE_TEST_PREVIOUS'] = $previous.Archive
    $info.Environment['BMCSHELL_RELEASE_TEST_NEW_VERSION'] = $current.Version
    $info.Environment['BMCSHELL_RELEASE_TEST_PREVIOUS_VERSION'] = $previous.Version

    $process = [Diagnostics.Process]::Start($info)
    try {
        $outputTask = $process.StandardOutput.ReadToEndAsync()
        $errorsTask = $process.StandardError.ReadToEndAsync()
        if (!$process.WaitForExit(90000)) {
            $process.Kill($true)
            throw "Bash verification timed out: $bashPath"
        }
        $output = $outputTask.GetAwaiter().GetResult()
        $errors = $errorsTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            throw "Bash verification failed ($bashPath): $output $errors"
        }
        $output.Trim()
    } finally {
        $process.Dispose()
    }
}

$current = Get-VerifiedRelease $Archive
$previous = Get-VerifiedRelease $PreviousArchive
if ($current.Version -eq $previous.Version) { throw 'Use two distinct portable release versions.' }
$buildPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'build'
$null = [IO.Directory]::CreateDirectory($buildPath)
$buildPath = (Resolve-Path -LiteralPath $buildPath).Path
$fixtureName = "release smoke $([guid]::NewGuid().ToString('N'))"
$fixturePath = Join-Path $buildPath $fixtureName
$null = [IO.Directory]::CreateDirectory($fixturePath)
try {
    $profilePath = Join-Path $fixturePath 'bashrc'
    $profileText = 'source "$(cygpath -u "$BMCSHELL_RELEASE_TEST_ROOT")/bin/bmcshell.sh"'
    [IO.File]::WriteAllText($profilePath, $profileText + [char]10)

    $working = Join-Path $fixturePath '安装目录'
    $installation = Join-Path $working 'BMCShell'
    Expand-Archive -LiteralPath $previous.Archive -DestinationPath $working
    $null = Invoke-BashProfile $GitBash @'
set -e
[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_PREVIOUS_VERSION" ]]
bmc proxy -m 127.0.0.1:1081
bmc update --file "$BMCSHELL_RELEASE_TEST_NEW"
[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_PREVIOUS_VERSION" ]]
'@

    $null = Invoke-BashProfile $Msys2Bash @'
set -e
[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_NEW_VERSION" ]]
root=$(cygpath -aw "$BMCSHELL_RELEASE_TEST_ROOT")
[[ $(bmc doctor -d) == *"$root"* ]]
bmc update --file "$BMCSHELL_RELEASE_TEST_PREVIOUS"
'@
    $null = Invoke-BashProfile $GitBash '[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_PREVIOUS_VERSION" ]]'

    $bin = Join-Path $installation 'bin'
    $marker = Join-Path $bin 'active'
    $launcher = Join-Path $bin 'bmcshell.sh'
    $activeExe = Join-Path $bin "releases/$($current.Version)/BMCShell.exe"
    $settings = Join-Path $installation 'settings.json'
    $registry = Join-Path $installation 'framework.db'
    $settingsHash = (Get-FileHash -LiteralPath $settings).Hash
    $registryHash = (Get-FileHash -LiteralPath $registry).Hash

    Set-Content -LiteralPath $marker -Value 'invalid release'
    $null = Invoke-BashProfile $GitBash '! declare -F bmc >/dev/null'
    [IO.File]::WriteAllText($launcher, '')
    & $activeExe update --file $current.Archive
    if ($LASTEXITCODE -ne 0) { throw 'Could not repair launcher and marker with the retained executable.' }
    $null = Invoke-BashProfile $Msys2Bash '[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_NEW_VERSION" ]]'

    [IO.File]::WriteAllBytes($activeExe, [byte[]]::new(0))
    $null = Invoke-BashProfile $Msys2Bash '! declare -F bmc >/dev/null'
    $staged = Join-Path $fixturePath 'recovery'
    Expand-Archive -LiteralPath $current.Archive -DestinationPath $staged
    $savedBin = Join-Path $installation 'bin.recovery'
    foreach ($directory in @($bin, (Join-Path $staged 'BMCShell/bin'))) {
        $resolved = (Resolve-Path -LiteralPath $directory).Path
        if ($resolved -ne $directory -or !$resolved.StartsWith($fixturePath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or
            ((Get-Item -LiteralPath $resolved).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Unexpected recovery path: $resolved"
        }
    }
    Move-Item -LiteralPath $bin -Destination $savedBin
    try {
        Move-Item -LiteralPath (Join-Path $staged 'BMCShell/bin') -Destination $bin
    } catch {
        Move-Item -LiteralPath $savedBin -Destination $bin
        throw
    }

    if ((Get-FileHash -LiteralPath $settings).Hash -ne $settingsHash -or
        (Get-FileHash -LiteralPath $registry).Hash -ne $registryHash) {
        throw 'Framework data changed during release recovery.'
    }
    $null = Invoke-BashProfile $GitBash '[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_NEW_VERSION" ]]'
    $null = Invoke-BashProfile $Msys2Bash '[[ $(bmc version) == "BMCShell $BMCSHELL_RELEASE_TEST_NEW_VERSION" ]]'
    'PASS: release SHA-256, fresh Bash profiles, upgrade, downgrade, and recovery'
} finally {
    $resolved = (Resolve-Path -LiteralPath $fixturePath).Path
    if ([IO.Path]::GetDirectoryName($resolved) -ne $buildPath -or [IO.Path]::GetFileName($resolved) -ne $fixtureName -or
        ((Get-Item -LiteralPath $resolved).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Unexpected cleanup path: $resolved"
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
