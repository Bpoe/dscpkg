[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Version,
    [Parameter(Mandatory)][string] $BinaryPath,
    [Parameter(Mandatory)][string] $MsiPath
)

. "$PSScriptRoot/../../common.ps1"
if (-not $IsWindows) { throw 'MSI inspection requires Windows.' }
$productVersion = & "$PSScriptRoot/../../version.ps1" $Version
$msi = (Resolve-Path -LiteralPath $MsiPath).Path
$properties = @{}
foreach ($row in Read-MsiTable $msi 'SELECT `Property`, `Value` FROM `Property`' @('Name', 'Value')) {
    $properties[$row.Name] = $row.Value
}
Assert-Equal $properties['ProductName'] 'dscpkg' 'MSI product name'
Assert-Equal $properties['ProductVersion'] $productVersion 'MSI product version'
Assert-Equal $properties['UpgradeCode'] '{5E40E180-FD44-4F09-AB2F-09D02D59CE8A}' 'MSI upgrade identity'
Assert-Equal $properties['ALLUSERS'] '1' 'MSI installation scope'
$installer = New-Object -ComObject WindowsInstaller.Installer
$database = $installer.OpenDatabase($msi, 0)
$summary = $database.SummaryInformation(0)
try {
    Assert-Equal $summary.Property(7) 'x64;1033' 'MSI platform'
} finally {
    foreach ($object in @($summary, $database, $installer)) {
        [Runtime.InteropServices.Marshal]::FinalReleaseComObject($object) | Out-Null
    }
}
$files = @(Read-MsiTable $msi 'SELECT `FileName` FROM `File`' @('Name'))
$names = @($files | ForEach-Object { ($_.Name -split '\|')[-1] })
Assert-Equal (($names | Sort-Object) -join ',') 'dscpkg.dsc.extension.json,dscpkg.exe,LICENSE,README.md' 'MSI file set'
$directories = @(Read-MsiTable $msi 'SELECT `Directory_Parent`, `DefaultDir` FROM `Directory` WHERE `Directory` = ''INSTALLFOLDER''' @('Parent', 'Name'))
Assert-Equal $directories[0].Parent 'ProgramFiles64Folder' 'MSI destination parent'
Assert-Equal (($directories[0].Name -split '\|')[-1]) 'dscpkg' 'MSI destination directory'
foreach ($row in Read-MsiTable $msi 'SELECT `Attributes` FROM `Component`' @('Attributes')) {
    Assert-Equal ([int]$row.Attributes -band 256) 256 'MSI component bitness'
}
$environment = @(Read-MsiTable $msi 'SELECT `Name`, `Value` FROM `Environment`' @('Name', 'Value'))
Assert-Equal $environment.Count 1 'MSI environment entry count'
if ($environment[0].Name -notmatch '\*PATH$') { throw 'MSI must manage the machine PATH.' }
Assert-Equal $environment[0].Value '[~];[INSTALLFOLDER]' 'MSI PATH append'
$tables = @(Read-MsiTable $msi 'SELECT `Name` FROM `_Tables`' @('Name'))
foreach ($table in @('CustomAction', 'ServiceInstall', 'ServiceControl')) {
    if ($tables.Name -contains $table) { throw "Unexpected MSI table: $table" }
}

$stage = Join-Path ([IO.Path]::GetTempPath()) ("dscpkg-inspect-" + [guid]::NewGuid())
$image = Join-Path $stage 'image'
$logPath = Join-Path $stage 'extract.log'
$verified = $false
try {
    New-Item -ItemType Directory -Path $stage | Out-Null
    # Administrative extraction copies the payload; it does not install the product.
    Invoke-Msi '/a' $msi $logPath @("TARGETDIR=`"$image`"")
    $binaries = @(Get-ChildItem -LiteralPath $image -Recurse -File -Filter dscpkg.exe)
    Assert-Equal $binaries.Count 1 'Extracted MSI executable count'
    Assert-Equal (Get-SHA256 $binaries[0].FullName) (Get-SHA256 $BinaryPath) 'MSI executable hash'
    $repo = [IO.Path]::GetFullPath("$PSScriptRoot/../../..")
    foreach ($file in @('README.md', 'LICENSE')) {
        Assert-Equal (Get-SHA256 (Join-Path $binaries[0].DirectoryName $file)) `
            (Get-SHA256 (Join-Path $repo $file)) "MSI $file"
    }
    Assert-Equal (Get-SHA256 (Join-Path $binaries[0].DirectoryName 'dscpkg.dsc.extension.json')) `
        (Get-SHA256 (Join-Path $repo 'dscpkg.dsc.extension.json')) 'MSI discovery extension'
    Invoke-Checked $binaries[0].FullName @('publish', '-h')
    $verified = $true
} finally {
    if (-not $verified -and (Test-Path -LiteralPath $logPath)) {
        Get-Content -LiteralPath $logPath -Tail 80 | Out-Host
        if ($env:DSCPKG_LOG_DIRECTORY) {
            New-Item -ItemType Directory -Path $env:DSCPKG_LOG_DIRECTORY -Force | Out-Null
            Copy-Item -LiteralPath $logPath -Destination (Join-Path $env:DSCPKG_LOG_DIRECTORY 'msi-extract.log')
        }
    }
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
Write-Host 'MSI scope, version, PATH, metadata, and executable payload verified.'
