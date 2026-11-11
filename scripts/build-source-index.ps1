#requires -Version 7.4
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $ArtifactsFile,
    [Parameter(Mandatory)][string] $Output,
    [switch] $Merge
)

$ErrorActionPreference = 'Stop'
$inputs = Get-Item -LiteralPath $ArtifactsFile
$artifacts = ConvertFrom-Json -InputObject (Get-Content -LiteralPath $inputs.FullName -Raw) -AsHashtable -NoEnumerate
if ($artifacts -isnot [array]) { throw 'The artifacts file must be a JSON array.' }
$destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Output)
$entries = [Collections.Generic.List[object]]::new()
if ($Merge -and [IO.File]::Exists($destination)) {
    $index = Get-Content -LiteralPath $destination -Raw | ConvertFrom-Json -AsHashtable
    if ($index.schema -ne 1 -or $index.packages -isnot [array]) { throw 'Invalid existing source index.' }
    foreach ($package in $index.packages) { $entries.Add($package) }
}

foreach ($artifact in $artifacts) {
    if ($artifact.path -isnot [string] -or [string]::IsNullOrWhiteSpace($artifact.path) -or
        $artifact.url -isnot [string] -or [string]::IsNullOrWhiteSpace($artifact.url)) {
        throw 'Each artifact needs a local path and a download URL.'
    }
    $path = [IO.Path]::GetFullPath($artifact.path, $inputs.DirectoryName)
    $archive = [IO.Compression.ZipFile]::OpenRead($path)
    try {
        $manifests = @($archive.Entries | Where-Object FullName -CEQ 'bmc-package.json')
        if ($manifests.Count -ne 1 -or $manifests[0].Length -gt 1MB) {
            throw "Expected one bmc-package.json at the ZIP root in '$path'."
        }
        $reader = [IO.StreamReader]::new($manifests[0].Open())
        try { $manifest = $reader.ReadToEnd() | ConvertFrom-Json -AsHashtable }
        finally { $reader.Dispose() }
        $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    } finally {
        $archive.Dispose()
    }

    if ($manifest.schema -ne 1 -or $manifest.coreApi -lt 1 -or
        $manifest.id -cnotmatch '\A[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}\z' -or
        $manifest.type -cnotin 'book', 'library', 'theme', 'tool', 'toolplugin', 'bundle' -or
        $manifest.version -cnotmatch '\A(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?\z' -or
        [string]::IsNullOrWhiteSpace($manifest.name)) {
        throw "Invalid package metadata in '$path'."
    }
    $entry = [ordered]@{
        id = $manifest.id
        name = $manifest.name
        type = $manifest.type
        version = $manifest.version
        coreApi = $manifest.coreApi
        url = $artifact.url
        sha256 = $hash
    }
    foreach ($field in 'platforms', 'dependencies', 'members', 'librarySchemas', 'libraryMigrations') {
        if ($manifest[$field]) { $entry[$field] = $manifest[$field] }
    }
    if ($manifest.library) { $entry.librarySchemaVersion = $manifest.library.schemaVersion }
    $newPlatforms = @($entry.platforms | Sort-Object)
    for ($i = $entries.Count - 1; $i -ge 0; $i--) {
        $old = $entries[$i]
        if ($old.id -cne $entry.id -or $old.version -cne $entry.version -or $old.coreApi -ne $entry.coreApi) { continue }
        $oldPlatforms = @($old.platforms | Sort-Object)
        $overlap = $oldPlatforms.Count -eq 0 -or $newPlatforms.Count -eq 0 -or
            'any' -cin $oldPlatforms -or 'any' -cin $newPlatforms -or
            @($oldPlatforms | Where-Object { $_ -cin $newPlatforms }).Count -gt 0
        if (-not $overlap) { continue }
        if ($old.sha256 -ine $entry.sha256 -or ($oldPlatforms -join ',') -cne ($newPlatforms -join ',')) {
            throw "Release $($entry.id) $($entry.version) already has a different overlapping artifact. Publish a new version."
        }
        $entries.RemoveAt($i)
    }
    $entries.Add($entry)
}

$packages = @($entries | Sort-Object -CaseSensitive id, version, coreApi, url)
$json = [ordered]@{ schema = 1; packages = $packages } | ConvertTo-Json -Depth 16
$directory = [IO.Path]::GetDirectoryName($destination)
$null = [IO.Directory]::CreateDirectory($directory)
$temporary = Join-Path $directory ('.index-' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    [IO.File]::WriteAllText($temporary, $json + "`n")
    [IO.File]::Move($temporary, $destination, $true)
} finally {
    [IO.File]::Delete($temporary)
}
Get-Item -LiteralPath $destination
