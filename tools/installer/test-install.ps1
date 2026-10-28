# Offline tests; no network, launcher, game or retail assets required.
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot 'install.ps1'
$tokens = $null
$errors = $null
[void][Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
. $installer

function Assert-Throws([scriptblock]$Action, [string]$Message) {
    $threw = $false
    try { & $Action | Out-Null } catch { $threw = $true }
    if (!$threw) { throw "Expected rejection: $Message" }
}
function Assert-Equal($Actual, $Expected, [string]$Message) {
    if ($Actual -cne $Expected) { throw "$Message (actual: $Actual; expected: $Expected)" }
}

Assert-Equal (Resolve-NanolatheWindowsArchitecture @(9) $true) 'amd64' 'Native x64 selection'
Assert-Equal (Resolve-NanolatheWindowsArchitecture @(12) $true) 'arm64' 'Native ARM64 selection, including emulated shells'
Assert-Throws { Resolve-NanolatheWindowsArchitecture @(9) $false } '32-bit PowerShell'
Assert-Throws { Resolve-NanolatheWindowsArchitecture @() $true } 'missing CPU information'
Assert-Throws { Resolve-NanolatheWindowsArchitecture @(9, 12) $true } 'inconsistent CPU information'

$hash = 'a' * 64
$sums = Read-NanolatheChecksums "$hash  Nanolathe-Setup.exe`r`n$('b' * 64)  build.json`n"
Assert-Equal $sums['Nanolathe-Setup.exe'] $hash 'SHA256SUMS with CRLF lines'
Assert-Equal $sums['build.json'] ('b' * 64) 'SHA256SUMS second entry'
Assert-Throws { Read-NanolatheChecksums "$hash  a`n$hash  a" } 'duplicate SHA256SUMS entry'
Assert-Throws { Read-NanolatheChecksums "$hash ../escape" } 'path in SHA256SUMS'
Assert-Throws { Read-NanolatheChecksums "abc  Nanolathe-Setup.exe" } 'short digest'
Assert-Throws { Read-NanolatheChecksums "`$(throw 'run')  x" } 'code in SHA256SUMS'

# Authored GitHub listings: assets are named; URLs are never fetched here.
function New-Release([string]$Tag, [bool]$Prerelease, [string[]]$Assets, [bool]$Draft = $false) {
    [pscustomobject]@{
        tag_name = $Tag; prerelease = $Prerelease; draft = $Draft
        assets = @($Assets | ForEach-Object { [pscustomobject]@{ name = $_; browser_download_url = "https://example.invalid/$Tag/$_" } })
    }
}
$complete = @('Nanolathe-Setup.exe', 'SHA256SUMS', 'build.json')
$stable = New-Release 'v0.4.0' $false $complete
$listing = @(
    (New-Release 'native-12' $true $complete), (New-Release 'native-14' $true @('SHA256SUMS')),
    (New-Release 'native-15' $true $complete $true), (New-Release 'native-9' $true $complete),
    (New-Release 'browser-20' $true @('SHA256SUMS')), $stable
)
Assert-Equal (Select-NanolatheRelease $listing).tag_name 'native-12' 'Latest even beside a Stable release; incomplete and draft releases skipped'
Assert-Throws { Select-NanolatheRelease @($stable, (New-Release 'native-3' $true @('SHA256SUMS'))) } 'no Latest release carries the launcher'
Assert-Equal (Get-NanolatheAssetUrl $stable 'SHA256SUMS') 'https://example.invalid/v0.4.0/SHA256SUMS' 'Asset URL'
$plain = New-Release 'native-1' $true @('SHA256SUMS')
$plain.assets[0].browser_download_url = 'http://example.invalid/SHA256SUMS'
Assert-Throws { Get-NanolatheAssetUrl $plain 'SHA256SUMS' } 'download without HTTPS'

$base = Join-Path ([IO.Path]::GetTempPath()) ("nanolathe-test O'Brien & `$x; [alpha] " + [guid]::NewGuid().ToString('N'))
$environmentBefore = @{}
foreach ($key in @('NANOLATHE_INSTALL_DIR', 'NANOLATHE_RELEASES_API')) {
    $environmentBefore[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    [void][IO.Directory]::CreateDirectory($base)
    $source = Join-Path $base 'new launcher.exe'
    [IO.File]::WriteAllText($source, 'new launcher')
    $installed = Install-NanolatheLauncher $base $source
    Assert-Equal ([IO.File]::ReadAllText($installed)) 'new launcher' 'Launcher installed'
    [IO.File]::WriteAllText($source, 'newer launcher')
    [void](Install-NanolatheLauncher $base $source)
    Assert-Equal ([IO.File]::ReadAllText($installed)) 'newer launcher' 'Launcher replaced'
    Assert-Equal ([IO.File]::ReadAllText("$installed.old")) 'new launcher' 'Replaced launcher moved aside'
    Assert-Throws { Install-NanolatheLauncher $base (Join-Path $base 'missing.exe') } 'missing source'
    Assert-Equal ([IO.File]::ReadAllText($installed)) 'newer launcher' 'A failed copy keeps the installed launcher'
    Remove-Item -LiteralPath $base -Recurse -Force

    # Whole runs with authored GitHub, downloads and launcher. The launcher's
    # --update is replaced by one that installs an authored game.
    $setupBytes = [Text.Encoding]::UTF8.GetBytes('authored launcher')
    $setupHash = -join ([Security.Cryptography.SHA256]::Create().ComputeHash($setupBytes) | ForEach-Object { $_.ToString('x2') })
    $run = @{}
    function Get-CimInstance { [pscustomobject]@{ Architecture = 9 } }
    function Get-NanolatheJson([string]$Url) {
        $run.Requests += $Url
        return $listing
    }
    function Get-NanolatheDownload([string]$Url, [string]$Path) {
        $run.Downloads += $Url
        if ($Url.EndsWith('/SHA256SUMS')) { [IO.File]::WriteAllText($Path, "$($run.SetupHash)  Nanolathe-Setup.exe`n") }
        else { [IO.File]::WriteAllBytes($Path, $setupBytes) }
    }
    function Invoke-NanolatheLauncherUpdate([string]$Launcher, [string]$Work) {
        Assert-Equal ([IO.File]::ReadAllText($Launcher)) 'authored launcher' 'The verified launcher runs'
        Assert-Equal (Split-Path $Launcher) $run.Base 'The launcher runs from the installation'
        $run.Updates++
        if ($run.Mode -eq 'update-fails') { return 1 }
        $release = Join-Path (Join-Path $run.Base 'releases') '0.3.0.12-native-12'
        [void][IO.Directory]::CreateDirectory($release)
        [IO.File]::WriteAllText((Join-Path $release 'nanolathe.exe'), 'authored game')
        Write-NanolatheData (Join-Path $run.Base 'current.txt') '0.3.0.12-native-12'
        return 0
    }
    function Start-Process {
        param([string]$FilePath, [string]$WorkingDirectory)
        $run.Started += $FilePath
    }
    function Write-Host([string]$Object) { }
    function Start-Transcript { }
    function Stop-Transcript { }

    $cases = @(
        @{ Name = 'fresh install'; Mode = 'ok'; NoRun = $false; Root = $true },
        @{ Name = 'source installation update'; Mode = 'ok'; NoRun = $true; Source = $true },
        @{ Name = 'checksum mismatch'; Mode = 'bad-sum'; NoRun = $true; Fails = $true },
        @{ Name = 'launcher cannot install the game'; Mode = 'update-fails'; NoRun = $true; Fails = $true }
    )
    foreach ($case in $cases) {
        if ([IO.Directory]::Exists($base)) { Remove-Item -LiteralPath $base -Recurse -Force }
        [void][IO.Directory]::CreateDirectory($base)
        $gameRoot = Join-Path $base 'Total Annihilation'
        [void][IO.Directory]::CreateDirectory($gameRoot)
        if ($case.ContainsKey('Source')) {
            # What the PowerShell source installer left, minus its compiler.
            $old = Join-Path (Join-Path $base 'releases') 'main-297b0734f02f-x-y'
            [void][IO.Directory]::CreateDirectory($old)
            [IO.File]::WriteAllText((Join-Path $old 'nanolathe.exe'), 'old game')
            Write-NanolatheData (Join-Path $base 'current.txt') 'main-297b0734f02f-x-y'
        }
        $run = @{ Base = $base; Mode = $case.Mode; Requests = @(); Downloads = @(); Updates = 0; Started = @(); SetupHash = $setupHash }
        if ($case.Mode -eq 'bad-sum') { $run.SetupHash = '0' * 64 }
        $env:NANOLATHE_INSTALL_DIR = $base
        $env:NANOLATHE_RELEASES_API = ''
        $NoRun = $case.NoRun
        $Root = ''
        if ($case.ContainsKey('Root')) { $Root = $gameRoot }
        $Help = $false
        $launcherPath = Join-Path $base 'nanolathe-launcher.exe'
        if ($case.ContainsKey('Fails')) {
            Assert-Throws { Invoke-NanolatheInstaller } "$($case.Name) fails"
            Assert-Equal $run.Started.Count 0 "$($case.Name): nothing starts"
            if ($case.Mode -eq 'bad-sum') {
                Assert-Equal ([IO.File]::Exists($launcherPath)) $false "$($case.Name): an unverified launcher is never installed"
                Assert-Equal $run.Updates 0 "$($case.Name): an unverified launcher never runs"
            }
        } else {
            Invoke-NanolatheInstaller
            Assert-Equal ($run.Requests -join ' ') 'https://api.github.com/repos/nanolathe-gg/nanolathe/releases?per_page=50' "$($case.Name): reads only the release listing"
            Assert-Equal ($run.Downloads -join ' ') ('https://example.invalid/native-12/SHA256SUMS https://example.invalid/native-12/Nanolathe-Setup.exe') "$($case.Name): downloads the checksums, then the launcher"
            Assert-Equal ([IO.File]::ReadAllText($launcherPath)) 'authored launcher' "$($case.Name): installs the launcher"
            Assert-Equal $run.Updates 1 "$($case.Name): the launcher installs the game once"
            # A source installer's update check now runs the new release's loader.
            $current = [IO.File]::ReadAllText((Join-Path $base 'current.txt'))
            $loader = Join-Path (Join-Path (Join-Path $base 'releases') $current) 'launch.ps1'
            [void][Management.Automation.Language.Parser]::ParseFile($loader, [ref]$tokens, [ref]$errors)
            if ($errors.Count) { throw ($errors | Out-String) }
            $startedBefore = $run.Started.Count
            & ([scriptblock]::Create([IO.File]::ReadAllText($loader))) -Base $base -Root $gameRoot
            Assert-Equal $run.Started[$startedBefore] $launcherPath "$($case.Name): the release loader opens the launcher"
            Assert-Equal $run.Started.Count ($startedBefore + 1) "$($case.Name): the loader starts nothing else"
            Assert-Equal $startedBefore ([int](!$case.NoRun)) "$($case.Name): the launcher opens unless -NoRun"
            Assert-Equal ([IO.File]::Exists((Join-Path $base 'root.txt'))) ($case.ContainsKey('Root')) "$($case.Name): -Root is remembered"
            if ($case.ContainsKey('Root')) { Assert-Equal ([IO.File]::ReadAllText((Join-Path $base 'root.txt'))) $gameRoot "$($case.Name): remembered folder" }
        }
        Assert-Equal (@(Get-ChildItem -LiteralPath $base -Directory -Filter 'work-*').Count) 0 "$($case.Name): no work folder left behind"
    }
} finally {
    foreach ($key in $environmentBefore.Keys) { [Environment]::SetEnvironmentVariable($key, $environmentBefore[$key], 'Process') }
    if ([IO.Directory]::Exists($base)) { Remove-Item -LiteralPath $base -Recurse -Force }
}
Microsoft.PowerShell.Utility\Write-Host 'Windows installer offline tests: OK'
