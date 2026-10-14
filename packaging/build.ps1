[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $BinaryPath,
    [Parameter(Mandatory)][string] $OutputDirectory
)

. "$PSScriptRoot/common.ps1"
$null = & "$PSScriptRoot/version.ps1" $Version
$binary = (Resolve-Path -LiteralPath $BinaryPath).Path
$platform = Get-BinaryPlatform $binary
$names = @(Get-AssetNames $Version $platform)
$hostTarget = (Invoke-Checked go @('env', 'GOHOSTOS', 'GOHOSTARCH')) -join '-'
Assert-Equal $platform $hostTarget 'Native packaging target'
$output = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $output -Force | Out-Null
Assert-FileSet $output @()
$repo = Split-Path $PSScriptRoot -Parent
$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-package-" + [guid]::NewGuid())
$name = "dscpkg-$Version-$platform"
$package = Join-Path $stage $name
$executable = if ($IsWindows) { 'dscpkg.exe' } else { 'dscpkg' }

try {
    New-Item -ItemType Directory -Path (Join-Path $package 'bin') -Force | Out-Null
    Copy-Item -LiteralPath $binary -Destination (Join-Path $package "bin/$executable")
    foreach ($file in @('README.md', 'LICENSE')) {
        Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $package
    }
    if ($IsWindows) {
        Compress-Archive -LiteralPath $package -DestinationPath (Join-Path $output "$name.zip")
        & "$PSScriptRoot/windows/msi/build.ps1" -Version $Version -BinaryPath $binary -OutputDirectory $output
    } else {
        Invoke-Checked chmod @('755', (Join-Path $package "bin/$executable"))
        Invoke-Checked tar @('-czf', (Join-Path $output "$name.tar.gz"), '-C', $stage, $name)
        if ($IsLinux) {
            Invoke-Checked bash @("$PSScriptRoot/linux/build.sh", $Version, $binary, $output)
        }
    }
    Write-Checksums $output $names
} finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
