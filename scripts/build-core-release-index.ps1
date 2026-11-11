#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Output
)

$ErrorActionPreference = 'Stop'
$repository = 'BMCShell/BMCShell'
$releaseApi = "https://api.github.com/repos/$repository/releases"
$tagPattern = '\Av?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)\z'
$manifestLimit = 16KB
$headers = @{
    Accept = 'application/vnd.github+json'
    'X-GitHub-Api-Version' = '2022-11-28'
}
if ($env:GITHUB_TOKEN) {
    $headers.Authorization = "Bearer $env:GITHUB_TOKEN"
}

function Get-AssetDownloadUrl($Asset, [string] $Tag) {
    $url = [uri] $Asset.browser_download_url
    $prefix = "/$repository/releases/download/"
    if (!$url.IsAbsoluteUri -or $url.Scheme -cne 'https' -or $url.Host -cne 'github.com' -or
        !$url.IsDefaultPort -or $url.UserInfo -or $url.Query -or $url.Fragment) {
        throw "Invalid download URL for $Tag asset '$($Asset.name)'."
    }
    $path = [uri]::UnescapeDataString($url.AbsolutePath)
    if (!$path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) -or
        $path.Substring($prefix.Length) -cne "$Tag/$($Asset.name)") {
        throw "Invalid download path for $Tag asset '$($Asset.name)'."
    }
    $url.AbsoluteUri
}

function Get-CoreArtifact($Release, [string] $Version) {
    $baseName = "BMCShell-$Version-windows-amd64"
    $zip = @($Release.assets | Where-Object { $_.name -ceq "$baseName.zip" -and $_.state -ceq 'uploaded' })
    $manifest = @($Release.assets | Where-Object { $_.name -ceq "$baseName.manifest.json" -and $_.state -ceq 'uploaded' })
    if ($zip.Count -ne 1 -or $manifest.Count -ne 1) { return }

    $archive = $zip[0]
    $checksum = $manifest[0]
    $tag = $Release.tag_name
    $url = Get-AssetDownloadUrl $archive $tag
    $manifestUrl = Get-AssetDownloadUrl $checksum $tag
    if ($archive.size -le 0 -or $checksum.size -le 0 -or $checksum.size -gt $manifestLimit) {
        throw "Invalid asset size for $tag."
    }

    $temporary = New-TemporaryFile
    try {
        # The release URL is public. Send no API token to the asset or its CDN.
        Invoke-WebRequest -Uri $manifestUrl -OutFile $temporary.FullName -TimeoutSec 60 -MaximumRetryCount 2 -RetryIntervalSec 2
        if ((Get-Item -LiteralPath $temporary.FullName).Length -ne $checksum.size) {
            throw "Checksum manifest size mismatch for $tag."
        }
        if ($checksum.digest) {
            $digest = (Get-FileHash -LiteralPath $temporary.FullName -Algorithm SHA256).Hash
            if ($checksum.digest -ine "sha256:$digest") {
                throw "Checksum manifest digest mismatch for $tag."
            }
        }
        $metadata = Get-Content -LiteralPath $temporary.FullName -Raw | ConvertFrom-Json -AsHashtable
    } finally {
        [IO.File]::Delete($temporary.FullName)
    }

    if (($metadata.schema -isnot [int] -and $metadata.schema -isnot [long]) -or $metadata.schema -ne 1 -or
        $metadata.product -cne 'BMCShell' -or $metadata.version -cne $Version -or
        $metadata.platform -cne 'windows-amd64' -or $metadata.artifact -cne $archive.name -or
        $metadata.sha256 -isnot [string] -or $metadata.sha256 -notmatch '\A[0-9a-f]{64}\z') {
        throw "Invalid BMCShell release manifest for $tag."
    }
    $hash = $metadata.sha256.ToLowerInvariant()
    if ($archive.digest -and $archive.digest -ine "sha256:$hash") {
        throw "ZIP digest differs from the checksum manifest for $tag."
    }

    [ordered]@{
        platform = 'windows-amd64'
        url = $url
        manifestUrl = $manifestUrl
        sha256 = $hash
        bytes = [long] $archive.size
    }
}

$entries = [Collections.Generic.List[object]]::new()
$tags = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
for ($page = 1; ; $page++) {
    $releases = Invoke-RestMethod -Uri "$($releaseApi)?per_page=100&page=$page" -Headers $headers -MaximumRedirection 0 `
        -TimeoutSec 60 -MaximumRetryCount 2 -RetryIntervalSec 2
    foreach ($release in $releases) {
        $tag = $release.tag_name
        $match = [regex]::Match($tag, $tagPattern)
        if ($release.draft -or !$match.Success -or !$tags.Add($tag)) { continue }
        $version = $match.Groups[1].Value
        [System.Management.Automation.SemanticVersion] $sortVersion = $null
        if ($version.Length -gt 128 -or
            ![System.Management.Automation.SemanticVersion]::TryParse($version, [ref] $sortVersion)) { continue }
        $artifact = Get-CoreArtifact $release $version
        if (!$artifact) { continue }

        $channel = if (!$release.prerelease -and !$sortVersion.PreReleaseLabel) { 'stable' } else { 'dev' }
        $entries.Add([pscustomobject]@{
            SortVersion = $sortVersion
            Release = [ordered]@{
                version = $version
                tag = $tag
                publishedAt = ([DateTimeOffset] $release.published_at).UtcDateTime.ToString('yyyy-MM-ddTHH:mm:ssZ')
                channel = $channel
                prerelease = [bool] $release.prerelease
                releaseUrl = "https://github.com/$repository/releases/tag/$([uri]::EscapeDataString($tag))"
                artifacts = @($artifact)
            }
        })
    }
    if ($releases.Count -lt 100) { break }
}

$ordered = @($entries | Sort-Object -Stable -Property SortVersion -Descending)
$latest = $ordered | Select-Object -First 1
$latestStable = $ordered | Where-Object { $_.Release.channel -ceq 'stable' } | Select-Object -First 1
$latestDev = $ordered | Where-Object { $_.Release.channel -ceq 'dev' } | Select-Object -First 1
$index = [ordered]@{
    schema = 1
    product = 'BMCShell'
    latest = $latest.Release.version
    latestStable = $latestStable.Release.version
    latestDev = $latestDev.Release.version
    releases = @($ordered | ForEach-Object { $_.Release })
}

$destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Output)
$directory = [IO.Path]::GetDirectoryName($destination)
$null = [IO.Directory]::CreateDirectory($directory)
$temporary = Join-Path $directory ('.release-index-' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    $json = ($index | ConvertTo-Json -Depth 8) -replace '\r\n', "`n"
    [IO.File]::WriteAllText($temporary, $json + "`n")
    [IO.File]::Move($temporary, $destination, $true)
} finally {
    [IO.File]::Delete($temporary)
}
Get-Item -LiteralPath $destination
