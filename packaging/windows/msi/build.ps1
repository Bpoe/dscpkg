[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $BinaryPath,
    [Parameter(Mandatory)][string] $OutputDirectory
)

. "$PSScriptRoot/../../common.ps1"
if (-not $IsWindows) { throw 'MSI builds require Windows.' }
$productVersion = & "$PSScriptRoot/../../version.ps1" $Version
$binary = (Resolve-Path -LiteralPath $BinaryPath).Path
Assert-Equal (Get-BinaryPlatform $binary) 'windows-amd64' 'MSI binary target'
$output = [IO.Path]::GetFullPath($OutputDirectory)
$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-msi-" + [guid]::NewGuid())
try {
    New-Item -ItemType Directory -Path $stage -Force | Out-Null
    Invoke-Checked dotnet @(
        'build', "$PSScriptRoot/dscpkg.wixproj", '--configuration', 'Release', '--nologo',
        '--no-incremental', "-p:ReleaseVersion=$Version", "-p:ProductVersion=$productVersion",
        "-p:BinaryPath=$binary", "-p:BaseIntermediateOutputPath=$stage/obj/",
        "-p:IntermediateOutputPath=$stage/obj/", "-p:MSBuildProjectExtensionsPath=$stage/obj/",
        "-p:OutputPath=$stage/output/"
    )
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    Copy-Item -LiteralPath "$stage/output/dscpkg-$Version-windows-amd64.msi" -Destination $output
} finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
