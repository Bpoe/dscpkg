[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $BinaryPath,
    [Parameter(Mandatory)][string] $Directory
)

. "$PSScriptRoot/common.ps1"
$binary = (Resolve-Path -LiteralPath $BinaryPath).Path
$platform = Get-BinaryPlatform $binary
$output = (Resolve-Path -LiteralPath $Directory).Path
$names = @(Get-AssetNames $Version $platform)
Assert-Checksums $output $names
$name = "dscpkg-$Version-$platform"
$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-verify-" + [guid]::NewGuid())
$executable = if ($IsWindows) { 'dscpkg.exe' } else { 'dscpkg' }
try {
    New-Item -ItemType Directory -Path $stage | Out-Null
    if ($IsWindows) {
        Expand-Archive -LiteralPath (Join-Path $output "$name.zip") -DestinationPath $stage
    } else {
        Invoke-Checked tar @('-xzf', (Join-Path $output "$name.tar.gz"), '-C', $stage)
    }
    $actual = @(Get-ChildItem -LiteralPath $stage -Recurse -File | ForEach-Object {
        [IO.Path]::GetRelativePath($stage, $_.FullName).Replace('\', '/')
    })
    $expected = @("$name/bin/$executable", "$name/bin/dscpkg.dsc.extension.json", "$name/LICENSE", "$name/README.md")
    Assert-Equal (($actual | Sort-Object) -join "`n") (($expected | Sort-Object) -join "`n") 'Archive contents'
    $unpacked = Join-Path $stage "$name/bin/$executable"
    Assert-Equal (Get-SHA256 $unpacked) (Get-SHA256 $binary) 'Archive executable hash'
    Assert-Equal (Get-SHA256 (Join-Path $stage "$name/bin/dscpkg.dsc.extension.json")) `
        (Get-SHA256 (Join-Path (Split-Path $PSScriptRoot -Parent) 'dscpkg.dsc.extension.json')) 'Archive discovery extension'
    foreach ($file in @('README.md', 'LICENSE')) {
        Assert-Equal (Get-SHA256 (Join-Path $stage "$name/$file")) `
            (Get-SHA256 (Join-Path (Split-Path $PSScriptRoot -Parent) $file)) "Archive $file"
    }
    Invoke-Checked $unpacked @('publish', '-h')
    if ($IsWindows) {
        & "$PSScriptRoot/windows/msi/inspect.ps1" -Version $Version -BinaryPath $binary `
            -MsiPath (Join-Path $output "$name.msi")
    } elseif ($IsLinux) {
        Invoke-Checked bash @("$PSScriptRoot/linux/inspect.sh", $Version, $binary, $output)
    }
} finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
Write-Host "Verified $platform release assets."
