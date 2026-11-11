#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $IndexPath
)

$ErrorActionPreference = 'Stop'
$repository = 'BMCShell/BMCShell'
$tag = 'release-index'
$indexName = 'index.json'
$pendingName = 'index.pending.json'
$backupName = 'index.previous.json'
$api = "https://api.github.com/repos/$repository"

if (!$env:GITHUB_TOKEN) { throw 'GITHUB_TOKEN with repository contents write permission is required.' }
$request = @{
    Headers = @{
        Authorization = "Bearer $env:GITHUB_TOKEN"
        Accept = 'application/vnd.github+json'
        'X-GitHub-Api-Version' = '2022-11-28'
    }
    TimeoutSec = 45
}
$file = Get-Item -LiteralPath $IndexPath
if ($file -isnot [IO.FileInfo]) { throw 'The index path must identify a file.' }
$index = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json -AsHashtable
if ($index.schema -ne 1 -or $index.product -cne 'BMCShell' -or $index.releases -isnot [array]) {
    throw 'Expected a schema 1 BMCShell framework release index.'
}
$hash = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()

function Get-IndexRelease {
    $release = Invoke-RestMethod "$api/releases/tags/$tag" @request
    if ($release.tag_name -cne $tag -or $release.draft) { throw "Expected the published '$tag' release." }
    $release
}

function Get-IndexAsset($Release, [string] $Name) {
    $Release.assets | Where-Object name -CEQ $Name | Select-Object -First 1
}

function Test-AssetMatches($Asset) {
    if (!$Asset -or $Asset.state -cne 'uploaded' -or $Asset.size -ne $file.Length) { return $false }
    if ($Asset.digest) { return $Asset.digest -ieq "sha256:$hash" }

    $headers = $request.Headers.Clone()
    $headers.Accept = 'application/octet-stream'
    $temporary = New-TemporaryFile
    try {
        Invoke-WebRequest "$api/releases/assets/$($Asset.id)" -Headers $headers -OutFile $temporary.FullName -TimeoutSec 45
        return (Get-FileHash -LiteralPath $temporary.FullName -Algorithm SHA256).Hash -ieq $hash
    } finally {
        [IO.File]::Delete($temporary.FullName)
    }
}

function Set-AssetName($Asset, [string] $Name) {
    $body = @{ name = $Name } | ConvertTo-Json -Compress
    $renamed = Invoke-RestMethod "$api/releases/assets/$($Asset.id)" @request -Method Patch -ContentType 'application/json' -Body $body
    if ($renamed.id -ne $Asset.id -or $renamed.name -cne $Name -or $renamed.state -cne 'uploaded') {
        throw "GitHub did not confirm the asset rename to '$Name'."
    }
    $renamed
}

function Remove-ReservedAsset($Asset) {
    if ($Asset -and $Asset.name -cin $pendingName, $backupName) {
        $null = Invoke-RestMethod "$api/releases/assets/$($Asset.id)" @request -Method Delete
    }
}

$release = Get-IndexRelease
$existing = Get-IndexAsset $release $indexName
$pending = Get-IndexAsset $release $pendingName
$backup = Get-IndexAsset $release $backupName
if (!$existing -and $backup.state -ceq 'uploaded') {
    $existing = Set-AssetName $backup $indexName
    $backup = $null
}
if ($existing -and $existing.state -cne 'uploaded') { throw "The '$indexName' asset is not ready." }

$indexUrl = "https://github.com/$repository/releases/download/$tag/$indexName"
if (Test-AssetMatches $existing) {
    Remove-ReservedAsset $pending
    Remove-ReservedAsset $backup
    Write-Host "Framework release index is current. $indexUrl"
    return
}
if ($release.immutable) { throw "The '$tag' release must allow updating its assets." }

if (!(Test-AssetMatches $pending)) {
    Remove-ReservedAsset $pending
    $upload = $release.upload_url -replace '\{.*\}$', ''
    if ($upload -cne "https://uploads.github.com/repos/$repository/releases/$($release.id)/assets") {
        throw 'Unexpected GitHub upload URL.'
    }
    $pending = Invoke-RestMethod "${upload}?name=$pendingName" @request -Method Post -ContentType 'application/json' -InFile $file.FullName
    if ($pending.name -cne $pendingName -or !(Test-AssetMatches $pending)) {
        throw 'The staged index did not pass SHA-256 verification.'
    }
}

# Keep the last valid index available until the replacement has been uploaded and verified.
Remove-ReservedAsset $backup
try {
    if ($existing) { $backup = Set-AssetName $existing $backupName }
    $published = Set-AssetName $pending $indexName
} catch {
    $failure = $_
    try {
        $current = Get-IndexRelease
        $previous = Get-IndexAsset $current $backupName
        if (!(Get-IndexAsset $current $indexName) -and $previous.state -ceq 'uploaded') {
            $null = Set-AssetName $previous $indexName
        }
    } catch {
        Write-Warning "The previous index could not be restored. A rerun will retry recovery. $($_.Exception.Message)"
    }
    throw $failure
}
if (!(Test-AssetMatches $published)) { throw 'The published index did not pass SHA-256 verification.' }
Remove-ReservedAsset $backup
Write-Host "Published the framework release index. $indexUrl"
