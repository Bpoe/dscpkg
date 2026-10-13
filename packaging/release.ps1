[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Preflight', 'Draft')][string] $Operation,
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $Commit,
    [Parameter(Mandatory)][string] $Repository,
    [string] $AssetDirectory
)

. "$PSScriptRoot/common.ps1"
$null = & "$PSScriptRoot/version.ps1" $Version
if ($Commit -cnotmatch '\A[0-9a-f]{40}\z') { throw 'Expected an exact Git commit/object SHA.' }
if ($Repository -cnotmatch '\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\z') { throw 'Expected owner/repository.' }

function Get-GitHubJSON {
    param([string[]] $Arguments)
    ((Invoke-Checked gh $Arguments) -join "`n") | ConvertFrom-Json -NoEnumerate
}

function Assert-NoRelease {
    $pages = Get-GitHubJSON @('api', '--paginate', '--slurp', "repos/$Repository/releases?per_page=100")
    if ($pages -isnot [array]) { throw 'Expected a paginated release listing.' }
    foreach ($page in $pages) {
        if ($page -isnot [array]) { throw 'Expected a release-list page.' }
        foreach ($release in $page) {
            if ($release.tag_name -ceq $Version) {
                throw "Release $Version already exists (including drafts); inspect it manually. It will not be overwritten."
            }
        }
    }
}

function Assert-RemoteTag {
    param([string] $ExpectedCommit)
    $reference = Get-GitHubJSON @('api', "repos/$Repository/git/ref/tags/$Version")
    $object = $reference.object
    for ($depth = 0; $depth -lt 16; $depth++) {
        if ($object.sha -cnotmatch '\A[0-9a-f]{40}\z') { throw 'Invalid remote Git object SHA.' }
        if ($object.type -ceq 'commit') {
            Assert-Equal $object.sha $ExpectedCommit 'Remote tag commit'
            return
        }
        if ($object.type -cne 'tag') { throw "Tag points to unsupported Git object type: $($object.type)" }
        $tag = Get-GitHubJSON @('api', "repos/$Repository/git/tags/$($object.sha)")
        $object = $tag.object
    }
    throw 'Remote annotated tag nesting exceeds the supported depth.'
}

if ($Operation -eq 'Preflight') {
    $resolved = Invoke-Checked git @('rev-parse', "$Commit^{commit}")
    $tagged = Invoke-Checked git @('rev-parse', "refs/tags/$Version^{commit}")
    Assert-Equal $tagged $resolved 'Checked-out tag commit'
    Invoke-Checked git @('merge-base', '--is-ancestor', $resolved, 'origin/main')
    Assert-RemoteTag $resolved
    Assert-NoRelease
    $resolved
    return
}

if (-not $AssetDirectory) { throw 'Draft creation requires AssetDirectory.' }
$downloads = (Resolve-Path -LiteralPath $AssetDirectory).Path
$directories = @(Get-ChildItem -LiteralPath $downloads -Force)
Assert-Equal (($directories.Name | Sort-Object) -join ',') 'linux,macos,windows' 'Downloaded platform directories'
foreach ($directory in $directories) {
    if (-not $directory.PSIsContainer) { throw "Expected a platform directory: $($directory.Name)" }
}
$macPattern = '\Adscpkg-' + [regex]::Escape($Version) + '-(darwin-(?:amd64|arm64))\.tar\.gz\z'
$macArchives = @(Get-ChildItem -LiteralPath (Join-Path $downloads 'macos') -File |
    Where-Object { $_.Name -cmatch $macPattern })
Assert-Equal $macArchives.Count 1 'macOS archive count'
$macPlatform = [regex]::Match($macArchives[0].Name, $macPattern).Groups[1].Value
$platforms = @(
    @{ Directory = 'linux'; Platform = 'linux-amd64' },
    @{ Directory = 'windows'; Platform = 'windows-amd64' },
    @{ Directory = 'macos'; Platform = $macPlatform }
)
$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-release-" + [guid]::NewGuid())
try {
    New-Item -ItemType Directory -Path $stage | Out-Null
    $allNames = @()
    foreach ($platform in $platforms) {
        $source = Join-Path $downloads $platform.Directory
        $names = @(Get-AssetNames $Version $platform.Platform)
        Assert-Checksums $source $names
        foreach ($name in $names) {
            if ($allNames -ccontains $name) { throw "Duplicate release asset: $name" }
            Copy-Item -LiteralPath (Join-Path $source $name) -Destination $stage
            $allNames += $name
        }
    }
    Write-Checksums $stage $allNames
    Assert-Checksums $stage $allNames
    Assert-RemoteTag $Commit
    Assert-NoRelease
    $assets = @($allNames + 'SHA256SUMS' | ForEach-Object { Join-Path $stage $_ })
    $notes = "Version $Version, built from $Commit. These exact assets passed archive and package inspection, native CLI smoke tests, and Windows/Ubuntu/Fedora fresh-install and uninstall checks. Separate package fixtures passed upgrade checks; Windows also passed repair, PATH, and downgrade-rejection checks. Resource/cache retention was checked. Packages are unsigned. Review this draft before publishing."
    Invoke-Checked gh (@('release', 'create', $Version) + $assets + @(
        '--repo', $Repository, '--verify-tag', '--draft', '--title', "dscpkg $Version",
        '--generate-notes', '--notes', $notes
    ))
} finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
