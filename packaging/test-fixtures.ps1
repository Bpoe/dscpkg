[CmdletBinding()]
param([Parameter(Mandatory)][string] $OutputDirectory)

. "$PSScriptRoot/common.ps1"
$output = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $output -Force | Out-Null
Assert-FileSet $output @()
$previousCGO = $env:CGO_ENABLED
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $env:CGO_ENABLED = '0'
    foreach ($version in @('v1.0.0', 'v1.0.1')) {
        $directory = Join-Path $output $version
        New-Item -ItemType Directory -Path $directory | Out-Null
        $binary = Join-Path $directory $(if ($IsWindows) { 'dscpkg.exe' } else { 'dscpkg' })
        # Distinct build IDs prove upgrades replace bytes, not just package metadata.
        Invoke-Checked go @('build', '-trimpath', '-ldflags', "-buildid=dscpkg-upgrade-$version", '-o', $binary, './cmd/dscpkg')
        & "$PSScriptRoot/build.ps1" -Version $version -BinaryPath $binary `
            -OutputDirectory (Join-Path $directory 'assets')
    }
} finally {
    $env:CGO_ENABLED = $previousCGO
    Pop-Location
}
