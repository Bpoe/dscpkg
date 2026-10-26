[CmdletBinding()]
param([string] $DscPath = 'dsc')

$ErrorActionPreference = 'Stop'
$dsc = (Get-Command $DscPath -CommandType Application).Source
$version = & $dsc --version
if ($LASTEXITCODE -ne 0 -or $version -ne 'dsc 3.3.0') { throw "Expected DSC 3.3.0, got $version" }
Push-Location (Split-Path $PSScriptRoot -Parent)
$previous = $env:DSC_TEST_BIN
try {
    # Use the official bundled schema exported by the pinned engine, without network resolution.
    $schema = & $dsc schema --type extension-manifest --output-format json
    if ($LASTEXITCODE -ne 0) { throw 'Could not export the DSC extension manifest schema.' }
    if (-not (Get-Content -LiteralPath ./dscpkg.dsc.extension.json -Raw | Test-Json -Schema ($schema -join "`n"))) {
        throw 'The discovery extension does not validate against the DSC 3.3.0 schema.'
    }
    $env:DSC_TEST_BIN = $dsc
    & go test -tags=integration -count=1 -v ./tests
    if ($LASTEXITCODE -ne 0) { throw 'DSC discovery integration tests failed.' }
} finally {
    $env:DSC_TEST_BIN = $previous
    Pop-Location
}
