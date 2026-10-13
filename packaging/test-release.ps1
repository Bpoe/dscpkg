. "$PSScriptRoot/common.ps1"
$releaseScript = Join-Path $PSScriptRoot 'release.ps1'
$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-release-test-" + [guid]::NewGuid())
$global:DscpkgReleaseTestState = @{
    Commit = ''; TagObject = $null; Releases = '[[]]'; FailAPI = $false
    Created = 0; Assets = @(); Arguments = @()
}

function global:gh {
    param([Parameter(ValueFromRemainingArguments)][string[]] $Arguments)
    $state = $global:DscpkgReleaseTestState
    $global:LASTEXITCODE = 0
    if ($Arguments[0] -eq 'api') {
        if ($state.FailAPI) { $global:LASTEXITCODE = 1; return }
        $endpoint = $Arguments[-1]
        if ($endpoint -match '/releases\?') { $state.Releases; return }
        if ($endpoint -match '/git/ref/tags/') {
            $object = if ($state.TagObject) {
                @{ type = 'tag'; sha = $state.TagObject }
            } else { @{ type = 'commit'; sha = $state.Commit } }
            @{ object = $object } | ConvertTo-Json -Compress
            return
        }
        if ($endpoint -match '/git/tags/') {
            @{ object = @{ type = 'commit'; sha = $state.Commit } } | ConvertTo-Json -Compress
            return
        }
        throw "Unexpected API request: $endpoint"
    }
    if ($Arguments[0] -eq 'release' -and $Arguments[1] -eq 'create') {
        $state.Created++
        $state.Arguments = $Arguments
        $state.Assets = @($Arguments | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
            ForEach-Object { Split-Path $_ -Leaf })
        return
    }
    throw "Unexpected gh command: $Arguments"
}

function Assert-Rejected {
    param([scriptblock] $Action, [string] $Message)
    $before = $global:DscpkgReleaseTestState.Created
    $rejected = $false
    try { & $Action | Out-Null }
    catch {
        if ($_.Exception.Message -notmatch $Message) { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected rejection matching: $Message" }
    Assert-Equal $global:DscpkgReleaseTestState.Created $before 'Rejected operation must not create a release'
}

function New-TestAssets {
    param([string] $Directory)
    foreach ($entry in @(
        @{ Name = 'linux'; Platform = 'linux-amd64' },
        @{ Name = 'windows'; Platform = 'windows-amd64' },
        @{ Name = 'macos'; Platform = 'darwin-arm64' }
    )) {
        $path = Join-Path $Directory $entry.Name
        New-Item -ItemType Directory -Path $path -Force | Out-Null
        $names = @(Get-AssetNames 'v1.2.3' $entry.Platform)
        foreach ($name in $names) { Set-Content -LiteralPath (Join-Path $path $name) -Value $name }
        Write-Checksums $path $names
    }
}

New-Item -ItemType Directory -Path $stage | Out-Null
Push-Location $stage
try {
    Invoke-Checked git @('init', '--quiet')
    Invoke-Checked git @('config', 'user.name', 'Release tests')
    Invoke-Checked git @('config', 'user.email', 'release-tests@example.invalid')
    Invoke-Checked git @('-c', 'commit.gpgsign=false', 'commit', '--quiet', '--allow-empty', '-m', 'Fixture')
    $commit = Invoke-Checked git @('rev-parse', 'HEAD')
    $global:DscpkgReleaseTestState.Commit = $commit
    Invoke-Checked git @('update-ref', 'refs/remotes/origin/main', $commit)
    Invoke-Checked git @('-c', 'tag.gpgsign=false', 'tag', 'v1.2.3')
    $parameters = @{ Version = 'v1.2.3'; Commit = $commit; Repository = 'example/dscpkg' }
    Assert-Equal (& $releaseScript -Operation Preflight @parameters) $commit 'Lightweight tag preflight'

    Invoke-Checked git @('tag', '--delete', 'v1.2.3') | Out-Null
    Invoke-Checked git @('-c', 'tag.gpgsign=false', 'tag', '-a', 'v1.2.3', '-m', 'Annotated fixture')
    $tagObject = Invoke-Checked git @('rev-parse', 'refs/tags/v1.2.3')
    $global:DscpkgReleaseTestState.TagObject = $tagObject
    $parameters.Commit = $tagObject
    Assert-Equal (& $releaseScript -Operation Preflight @parameters) $commit 'Annotated tag preflight'
    $parameters.Commit = $commit

    $outside = Invoke-Checked git @('-c', 'commit.gpgsign=false', 'commit-tree', 'HEAD^{tree}', '-m', 'Outside main')
    Invoke-Checked git @('update-ref', 'refs/tags/v1.2.3', $outside)
    $parameters.Commit = $outside
    Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'git failed'
    Invoke-Checked git @('update-ref', 'refs/tags/v1.2.3', $commit)
    $parameters.Commit = $commit
    $global:DscpkgReleaseTestState.TagObject = $null

    $global:DscpkgReleaseTestState.Commit = $outside
    Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'Remote tag commit'
    $global:DscpkgReleaseTestState.Commit = $commit
    $global:DscpkgReleaseTestState.FailAPI = $true
    Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'gh failed'
    $global:DscpkgReleaseTestState.FailAPI = $false
    foreach ($draft in @('true', 'false')) {
        $global:DscpkgReleaseTestState.Releases = "[[],[{`"tag_name`":`"v1.2.3`",`"draft`":$draft}]]"
        Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'already exists'
    }
    $global:DscpkgReleaseTestState.Releases = '[[]]'
    $parameters.Version = 'v1.2.3-rc.1'
    Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'Expected a stable'
    $parameters.Version = 'v1.2.3'

    $downloads = Join-Path $stage 'downloads'
    New-TestAssets $downloads
    & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads
    Assert-Equal $global:DscpkgReleaseTestState.Created 1 'Draft creation count'
    Assert-Equal $global:DscpkgReleaseTestState.Assets.Count 7 'Draft asset count'
    foreach ($flag in @('--draft', '--verify-tag', '--generate-notes')) {
        if ($global:DscpkgReleaseTestState.Arguments -cnotcontains $flag) { throw "Missing release flag: $flag" }
    }
    $global:DscpkgReleaseTestState.Releases = '[[{"tag_name":"v1.2.3","draft":true}]]'
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'already exists'
    $global:DscpkgReleaseTestState.Releases = '[[]]'
    $global:DscpkgReleaseTestState.Commit = $outside
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'Remote tag commit'
    $global:DscpkgReleaseTestState.Commit = $commit
    $global:DscpkgReleaseTestState.FailAPI = $true
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'gh failed'
    $global:DscpkgReleaseTestState.FailAPI = $false

    $extra = Join-Path $downloads 'linux/unexpected.txt'
    Set-Content -LiteralPath $extra -Value extra
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'Files in'
    Remove-Item -LiteralPath $extra
    $asset = Join-Path $downloads 'linux/dscpkg-v1.2.3-linux-amd64.tar.gz'
    $bytes = [IO.File]::ReadAllBytes($asset)
    Add-Content -LiteralPath $asset -Value corrupt
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'Checksum for'
    [IO.File]::WriteAllBytes($asset, $bytes)
    Remove-Item -LiteralPath $asset
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'Files in'
    [IO.File]::WriteAllBytes($asset, $bytes)
    $manifest = Join-Path $downloads 'linux/SHA256SUMS'
    $original = [IO.File]::ReadAllBytes($manifest)
    Add-Content -LiteralPath $manifest -Value (Get-Content -LiteralPath $manifest)[0]
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'duplicate checksum'
    [IO.File]::WriteAllBytes($manifest, $original)
    Set-Content -LiteralPath $manifest -Value ('0' * 64 + '  ../escape')
    Assert-Rejected { & $releaseScript -Operation Draft @parameters -AssetDirectory $downloads } 'Invalid checksum'
    [IO.File]::WriteAllBytes($manifest, $original)

    Invoke-Checked git @('tag', '--delete', 'v1.2.3') | Out-Null
    Assert-Rejected { & $releaseScript -Operation Preflight @parameters } 'git failed'
    Write-Host 'Release preflight, API failures, draft-only creation, and asset rejection checks passed.'
} finally {
    Pop-Location
    Remove-Item -LiteralPath $stage -Recurse -Force
    Remove-Item -LiteralPath Function:\gh
    Remove-Variable -Name DscpkgReleaseTestState -Scope Global
}
