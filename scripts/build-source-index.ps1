<#
.SYNOPSIS
    从扩展 ZIP 和下载地址生成 BMCShell 软件源索引。
.PARAMETER ArtifactsFile
    必填。JSON 数组文件，每项包含本地 ZIP 的 path 和下载 url。
.PARAMETER Output
    必填。索引 JSON 的输出路径。
.PARAMETER Merge
    保留已有索引中的条目，并合入新版本。
.EXAMPLE
    ./scripts/build-source-index.ps1 -ArtifactsFile ./artifacts.json -Output ./index.json -Merge
#>

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
if ($artifacts -isnot [array]) { throw '发布文件列表必须是 JSON 数组。' }
$destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Output)
$entries = [Collections.Generic.List[object]]::new()
if ($Merge -and [IO.File]::Exists($destination)) {
    $index = Get-Content -LiteralPath $destination -Raw | ConvertFrom-Json -AsHashtable
    if ($index.schema -ne 1 -or $index.packages -isnot [array]) { throw '现有软件源索引格式无效。' }
    foreach ($package in $index.packages) { $entries.Add($package) }
}

foreach ($artifact in $artifacts) {
    if ($artifact.path -isnot [string] -or [string]::IsNullOrWhiteSpace($artifact.path) -or
        $artifact.url -isnot [string] -or [string]::IsNullOrWhiteSpace($artifact.url)) {
        throw '每个发布文件必须提供本地路径和下载 URL。'
    }
    $path = [IO.Path]::GetFullPath($artifact.path, $inputs.DirectoryName)
    $archive = [IO.Compression.ZipFile]::OpenRead($path)
    try {
        $manifests = @($archive.Entries | Where-Object FullName -CEQ 'bmc-package.json')
        if ($manifests.Count -ne 1 -or $manifests[0].Length -gt 1MB) {
            throw "ZIP '$path' 的根目录必须恰好包含一个 bmc-package.json。"
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
        throw "ZIP '$path' 中的扩展包元数据无效。"
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
    $description = $manifest.description
    if ($description -isnot [string] -or [string]::IsNullOrWhiteSpace($description)) {
        $description = $manifest.summary
    }
    if ($description -is [string] -and -not [string]::IsNullOrWhiteSpace($description)) {
        $entry.description = $description.Trim()
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
            throw "版本 $($entry.id) $($entry.version) 已有适用范围重叠、内容不同的发布文件。请使用新版本号发布。"
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
