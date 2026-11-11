<#
.SYNOPSIS
    将已生成的版本索引发布至 BMCShell 的 GitHub 版本索引页面。
.DESCRIPTION
    需要配置拥有仓库内容写权限的 GITHUB_TOKEN。上传和验证成功后替换当前索引。
.PARAMETER IndexPath
    必填。待发布的版本索引 JSON 文件。
.EXAMPLE
    ./scripts/publish-core-release-index.ps1 -IndexPath ./index.json
#>

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

if (!$env:GITHUB_TOKEN) { throw '请配置具有仓库内容写权限的 GITHUB_TOKEN。' }
$request = @{
    Headers = @{
        Authorization = "Bearer $env:GITHUB_TOKEN"
        Accept = 'application/vnd.github+json'
        'X-GitHub-Api-Version' = '2022-11-28'
    }
    TimeoutSec = 45
}
$file = Get-Item -LiteralPath $IndexPath
if ($file -isnot [IO.FileInfo]) { throw '索引路径必须指向文件。' }
$index = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json -AsHashtable
if ($index.schema -ne 1 -or $index.product -cne 'BMCShell' -or $index.releases -isnot [array]) {
    throw '索引必须为 schema 1 的 BMCShell 框架版本索引。'
}
$hash = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()

function Get-IndexRelease {
    $release = Invoke-RestMethod "$api/releases/tags/$tag" @request
    if ($release.tag_name -cne $tag -or $release.draft) { throw "找不到标签为 '$tag' 的已发布版本。" }
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
        throw "GitHub 未确认发布文件已重命名为 '$Name'。"
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
if ($existing -and $existing.state -cne 'uploaded') { throw "发布文件 '$indexName' 尚未就绪。" }

$indexUrl = "https://github.com/$repository/releases/download/$tag/$indexName"
if (Test-AssetMatches $existing) {
    Remove-ReservedAsset $pending
    Remove-ReservedAsset $backup
    Write-Host "框架版本索引已是最新。$indexUrl"
    return
}
if ($release.immutable) { throw "标签 '$tag' 的发布记录必须允许更新文件。" }

if (!(Test-AssetMatches $pending)) {
    Remove-ReservedAsset $pending
    $upload = $release.upload_url -replace '\{.*\}$', ''
    if ($upload -cne "https://uploads.github.com/repos/$repository/releases/$($release.id)/assets") {
        throw 'GitHub 返回的上传 URL 与预期不符。'
    }
    $pending = Invoke-RestMethod "${upload}?name=$pendingName" @request -Method Post -ContentType 'application/json' -InFile $file.FullName
    if ($pending.name -cne $pendingName -or !(Test-AssetMatches $pending)) {
        throw '待发布索引未通过 SHA-256 校验。'
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
        Write-Warning "无法恢复上一个索引。重新运行会重试恢复。$($_.Exception.Message)"
    }
    throw $failure
}
if (!(Test-AssetMatches $published)) { throw '已发布索引未通过 SHA-256 校验。' }
Remove-ReservedAsset $backup
Write-Host "已发布框架版本索引。$indexUrl"
