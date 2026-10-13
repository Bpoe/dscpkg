. "$PSScriptRoot/common.ps1"

foreach ($version in @('v0.0.0', 'v1.2.3', 'v255.255.65535')) {
    Assert-Equal (& "$PSScriptRoot/version.ps1" $version) $version.Substring(1) $version
}
foreach ($version in @(
    '', '1.2.3', 'V1.2.3', 'v1.2', 'v1.2.3.4', 'v01.2.3', 'v1.02.3', 'v1.2.03',
    'v1.2.3-rc.1', 'v1.2.3+build', "v1.2.3`n", ' v1.2.3', 'v-1.2.3',
    'v256.0.0', 'v0.256.0', 'v0.0.65536', 'v999999999999999999999999999.0.0'
)) {
    $rejected = $false
    try { & "$PSScriptRoot/version.ps1" $version | Out-Null }
    catch {
        if ($_.Exception.Message -notmatch 'Expected a stable|MSI versions require') { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Accepted invalid release version: '$version'." }
}
Write-Host 'Release version boundaries and invalid inputs passed.'
