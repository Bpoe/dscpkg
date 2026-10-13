Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-Checked {
    param([string] $Command, [string[]] $Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE."
    }
}

function Assert-Equal {
    param($Actual, $Expected, [string] $Context)
    if ($Actual -cne $Expected) {
        throw "${Context}: expected '$Expected', got '$Actual'."
    }
}

function Get-SHA256 {
    param([string] $Path)
    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-AssetNames {
    param([string] $Version, [string] $Platform)
    $packageVersion = & "$PSScriptRoot/version.ps1" -Version $Version
    switch -Regex -CaseSensitive ($Platform) {
        '^linux-amd64$' {
            "dscpkg-$Version-linux-amd64.tar.gz"
            "dscpkg_${packageVersion}_amd64.deb"
            "dscpkg-${packageVersion}-1.x86_64.rpm"
        }
        '^windows-amd64$' {
            "dscpkg-$Version-windows-amd64.zip"
            "dscpkg-$Version-windows-amd64.msi"
        }
        '^darwin-(amd64|arm64)$' { "dscpkg-$Version-$Platform.tar.gz" }
        default { throw "Unsupported release platform: $Platform" }
    }
}

function Assert-FileSet {
    param([string] $Directory, [string[]] $Expected)
    $actual = @(Get-ChildItem -LiteralPath $Directory -Force | ForEach-Object {
        if ($_.PSIsContainer) { throw "Unexpected directory: $($_.FullName)" }
        $_.Name
    })
    Assert-Equal (($actual | Sort-Object -CaseSensitive) -join "`n") `
        (($Expected | Sort-Object -CaseSensitive) -join "`n") "Files in $Directory"
}

function Write-Checksums {
    param([string] $Directory, [string[]] $Names)
    Assert-FileSet $Directory $Names
    $lines = foreach ($name in ($Names | Sort-Object -CaseSensitive)) {
        "$(Get-SHA256 (Join-Path $Directory $name))  $name"
    }
    Set-Content -LiteralPath (Join-Path $Directory 'SHA256SUMS') `
        -Value (($lines -join "`n") + "`n") -Encoding utf8NoBOM -NoNewline
}

function Assert-Checksums {
    param([string] $Directory, [string[]] $Names)
    Assert-FileSet $Directory @($Names + 'SHA256SUMS')
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($line in Get-Content -LiteralPath (Join-Path $Directory 'SHA256SUMS')) {
        if ($line -cnotmatch '\A([0-9a-f]{64})  ([A-Za-z0-9_.-]+)\z') {
            throw "Invalid checksum entry: $line"
        }
        $hash, $name = $Matches[1], $Matches[2]
        if ($Names -cnotcontains $name -or -not $seen.Add($name)) {
            throw "Unexpected or duplicate checksum entry: $name"
        }
        Assert-Equal (Get-SHA256 (Join-Path $Directory $name)) $hash "Checksum for $name"
    }
    Assert-Equal $seen.Count $Names.Count 'Checksum entry count'
}

function Get-BinaryPlatform {
    param([string] $BinaryPath)
    $settings = (Invoke-Checked go @('version', '-m', $BinaryPath)) -join "`n"
    if ($settings -notmatch '(?m)^\s+build\s+GOOS=(\w+)\s*$') {
        throw "Missing Go target OS in $BinaryPath."
    }
    $targetOS = $Matches[1]
    if ($settings -notmatch '(?m)^\s+build\s+GOARCH=(\w+)\s*$') {
        throw "Missing Go target architecture in $BinaryPath."
    }
    "$targetOS-$($Matches[1])"
}

function Read-MsiTable {
    param([string] $Path, [string] $Query, [string[]] $Columns)
    $installer = New-Object -ComObject WindowsInstaller.Installer
    $database = $installer.OpenDatabase($Path, 0)
    $view = $database.OpenView($Query)
    try {
        $view.Execute()
        while ($record = $view.Fetch()) {
            $row = [ordered]@{}
            for ($i = 0; $i -lt $Columns.Count; $i++) {
                $row[$Columns[$i]] = $record.StringData($i + 1)
            }
            [pscustomobject]$row
            [Runtime.InteropServices.Marshal]::FinalReleaseComObject($record) | Out-Null
        }
    } finally {
        $view.Close()
        foreach ($object in @($view, $database, $installer)) {
            [Runtime.InteropServices.Marshal]::FinalReleaseComObject($object) | Out-Null
        }
    }
}

function Invoke-Msi {
    param(
        [string] $Operation, [string] $Path, [string] $LogPath,
        [string[]] $Properties = @(), [int[]] $ExpectedCodes = @(0, 3010)
    )
    $arguments = @($Operation, "`"$Path`"", '/qn', '/norestart', '/L*v', "`"$LogPath`"") + $Properties
    $process = Start-Process -FilePath "$env:SystemRoot/System32/msiexec.exe" `
        -ArgumentList $arguments -Wait -PassThru
    if ($ExpectedCodes -notcontains $process.ExitCode) {
        throw "msiexec $Operation failed with exit code $($process.ExitCode); see $LogPath."
    }
}
