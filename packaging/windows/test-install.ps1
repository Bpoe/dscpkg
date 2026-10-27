[CmdletBinding()]
param(
    [switch] $Disposable,
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $Directory,
    [Parameter(Mandatory)][string] $BinaryPath,
    [Parameter(Mandatory)][string] $FixtureDirectory,
    [Parameter(Mandatory)][string] $LogDirectory
)

. "$PSScriptRoot/../common.ps1"
if (-not $IsWindows -or -not $Disposable -or $env:GITHUB_ACTIONS -ne 'true' -or
    $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'Installation tests require -Disposable on a GitHub-hosted Windows runner.'
}
if (-not $env:DSC_TEST_BIN) { throw 'DSC_TEST_BIN must identify the isolated DSC 3.3.0 executable.' }
$null = & "$PSScriptRoot/../version.ps1" $Version
$installDirectory = Join-Path $env:ProgramFiles 'dscpkg'
$installedBinary = Join-Path $installDirectory 'dscpkg.exe'
$installedExtension = Join-Path $installDirectory 'dscpkg.dsc.extension.json'
$installedResource = Join-Path $installDirectory 'dscpkg.dsc.resource.json'
if (Test-Path -LiteralPath $installDirectory) { throw 'dscpkg is already installed; refusing to modify it.' }
$logs = [IO.Path]::GetFullPath($LogDirectory)
New-Item -ItemType Directory -Path $logs -Force | Out-Null
$originalPath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$processPath = $env:PATH
$sentinelPath = Join-Path $env:SystemDrive 'dscpkg-unrelated-path-entry'
$baselinePath = "$originalPath;$sentinelPath"
$cache = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'dscpkg/packages'
if (Test-Path -LiteralPath $cache) { throw 'Test cache already exists; refusing to overwrite it.' }
$sentinels = @("$cache/resources.json", "$cache/.dscpkg/metadata", "$cache/test-resource/resource")
$installed = $null

function Assert-Preserved {
    foreach ($path in $sentinels) { Assert-Equal (Get-Content -LiteralPath $path -Raw).Trim() 'preserve-me' $path }
    $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    if (($machinePath -split ';') -notcontains $sentinelPath) { throw 'Unrelated PATH entry was removed.' }
}

function Assert-Installed {
    param([string] $ExpectedBinary)
    Assert-Equal (Get-SHA256 $installedBinary) (Get-SHA256 $ExpectedBinary) 'Installed executable'
    Assert-Equal (Get-SHA256 $installedExtension) `
        (Get-SHA256 (Join-Path $PSScriptRoot '../../dscpkg.dsc.extension.json')) 'Installed discovery extension'
    Assert-Equal (Get-SHA256 $installedResource) `
        (Get-SHA256 (Join-Path $PSScriptRoot '../../dscpkg.dsc.resource.json')) 'Installed resource manifest'
    $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $entries = @($machinePath -split ';' | ForEach-Object { $_.TrimEnd('\') })
    Assert-Equal @($entries | Where-Object { $_ -ieq $installDirectory }).Count 1 'Product PATH entry count'
    $env:PATH = "$machinePath;$processPath"
    Assert-Equal (Get-Command dscpkg -CommandType Application).Source $installedBinary 'PATH lookup'
    $resourcePath, $restrictedPath = $env:DSC_RESOURCE_PATH, $env:DSC_RESTRICTED_PATH
    try {
        Remove-Item Env:DSC_RESOURCE_PATH, Env:DSC_RESTRICTED_PATH -ErrorAction SilentlyContinue
        $extensions = & $env:DSC_TEST_BIN --ignore-settings-file extension list DscPkg/Discover -o json
        if ($LASTEXITCODE -ne 0) { throw 'DSC extension discovery failed.' }
        $extension = $extensions | ConvertFrom-Json
        Assert-Equal $extension.type 'DscPkg/Discover' 'Installed DSC extension'
        Assert-Equal $extension.path $installedExtension 'Installed DSC extension path'
        $resources = & $env:DSC_TEST_BIN --ignore-settings-file resource list DscPkg/resources -o json
        if ($LASTEXITCODE -ne 0) { throw 'DSC resource discovery failed.' }
        $resource = $resources | ConvertFrom-Json
        Assert-Equal $resource.type 'DscPkg/resources' 'Installed DSC resource'
        Assert-Equal $resource.path $installedResource 'Installed DSC resource path'
    } finally {
        $env:DSC_RESOURCE_PATH, $env:DSC_RESTRICTED_PATH = $resourcePath, $restrictedPath
    }
    Invoke-Checked $installedBinary @('publish', '-h')
    Assert-Preserved
}

function Remove-TestProduct {
    param([string] $Label)
    Invoke-Msi '/x' $script:installed (Join-Path $logs "$Label.log")
    $script:installed = $null
    if (Test-Path -LiteralPath $installedBinary) { throw 'Uninstall left the executable behind.' }
    if (Test-Path -LiteralPath $installedExtension) { throw 'Uninstall left the discovery extension behind.' }
    if (Test-Path -LiteralPath $installedResource) { throw 'Uninstall left the resource manifest behind.' }
    $entries = @([Environment]::GetEnvironmentVariable('Path', 'Machine') -split ';' |
        ForEach-Object { $_.TrimEnd('\') })
    if ($entries -icontains $installDirectory) { throw 'Uninstall left its PATH entry behind.' }
    Assert-Preserved
}

try {
    [Environment]::SetEnvironmentVariable('Path', $baselinePath, 'Machine')
    New-Item -ItemType Directory -Path "$cache/.dscpkg", "$cache/test-resource" -Force | Out-Null
    foreach ($path in $sentinels) { Set-Content -LiteralPath $path -Value 'preserve-me' }
    $candidate = (Resolve-Path -LiteralPath (Join-Path $Directory "dscpkg-$Version-windows-amd64.msi")).Path
    Invoke-Msi '/i' $candidate (Join-Path $logs 'install-release.log')
    $installed = $candidate
    Assert-Installed $BinaryPath
    Remove-Item -LiteralPath $installedExtension
    Remove-Item -LiteralPath $installedResource
    Invoke-Msi '/fa' $candidate (Join-Path $logs 'repair-release.log')
    Assert-Installed $BinaryPath
    Remove-TestProduct 'uninstall-release'

    $old = (Resolve-Path -LiteralPath "$FixtureDirectory/v1.0.0/assets/dscpkg-v1.0.0-windows-amd64.msi").Path
    $new = (Resolve-Path -LiteralPath "$FixtureDirectory/v1.0.1/assets/dscpkg-v1.0.1-windows-amd64.msi").Path
    $oldBinary = Join-Path $FixtureDirectory 'v1.0.0/dscpkg.exe'
    $newBinary = Join-Path $FixtureDirectory 'v1.0.1/dscpkg.exe'
    if ((Get-SHA256 $oldBinary) -eq (Get-SHA256 $newBinary)) { throw 'Upgrade fixture executables must differ.' }
    Invoke-Msi '/i' $old (Join-Path $logs 'install-old.log')
    $installed = $old
    Assert-Installed $oldBinary
    Set-Content -LiteralPath $installedExtension -Value '{"stale":"upgrade fixture"}'
    Set-Content -LiteralPath $installedResource -Value '{"stale":"upgrade fixture"}'
    Invoke-Msi '/i' $new (Join-Path $logs 'upgrade.log')
    $installed = $new
    Assert-Installed $newBinary
    $downgradeLog = Join-Path $logs 'reject-downgrade.log'
    Invoke-Msi -Operation '/i' -Path $old -LogPath $downgradeLog -ExpectedCodes @(1603, 1638)
    if (-not (Select-String -LiteralPath $downgradeLog -SimpleMatch 'A newer version of dscpkg is already installed.')) {
        throw 'Downgrade did not fail for the intended version check.'
    }
    Assert-Installed $newBinary
    Remove-TestProduct 'uninstall-upgrade'
} finally {
    if ($installed) { Invoke-Msi '/x' $installed (Join-Path $logs 'cleanup.log') }
    [Environment]::SetEnvironmentVariable('Path', $originalPath, 'Machine')
    $env:PATH = $processPath
}
Write-Host 'MSI install, repair, upgrade, downgrade rejection, PATH and data retention passed.'
