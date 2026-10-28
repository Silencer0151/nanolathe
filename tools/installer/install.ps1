# Windows PowerShell 5.1. Invoke from the website in a child script scope:
# & ([scriptblock]::Create((irm https://nanolathe.gg/install.ps1)))
[CmdletBinding()]
param([string]$Root, [switch]$NoRun, [switch]$Help)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# The script installs the Nanolathe launcher from the newest published
# release and lets it install the game (tools/installer/README.md
# "Windows launcher"). Nothing is compiled.
$NanolatheReleasesApi = 'https://api.github.com/repos/nanolathe-gg/nanolathe'
$NanolatheSetupAsset = 'Nanolathe-Setup.exe'
$NanolatheLauncherName = 'nanolathe-launcher.exe'

function Resolve-NanolatheWindowsArchitecture([int[]]$NativeArchitectures, [bool]$Is64BitProcess) {
    if (!$Is64BitProcess) { throw 'Run 64-bit PowerShell to install Nanolathe.' }
    # Native CPU information also identifies ARM64 from an emulated x64 shell.
    if ($NativeArchitectures.Count -eq 0) { throw 'Could not identify the native Windows architecture.' }
    if (@($NativeArchitectures | Where-Object { $_ -ne 9 }).Count -eq 0) { return 'amd64' }
    if (@($NativeArchitectures | Where-Object { $_ -ne 12 }).Count -eq 0) { return 'arm64' }
    throw 'Nanolathe supports x64 and ARM64 Windows.'
}

function Test-NanolatheChecksum([string]$Path, [string]$Expected) {
    if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ine $Expected) {
        throw "SHA-256 verification failed: $Path"
    }
}

# Read-NanolatheChecksums reads SHA256SUMS ("<hex>  <name>" lines) as data.
function Read-NanolatheChecksums([string]$Text) {
    $sums = @{}
    foreach ($line in ($Text -split "`n")) {
        $line = $line.TrimEnd("`r")
        if ($line -eq '') { continue }
        if ($line -cnotmatch '^([0-9a-fA-F]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$') { throw 'Malformed SHA256SUMS line.' }
        if ($sums.ContainsKey($Matches[2])) { throw "SHA256SUMS lists $($Matches[2]) twice." }
        $sums[$Matches[2]] = $Matches[1]
    }
    return $sums
}

function Test-NanolatheReleaseAssets($Release) {
    $names = @($Release.assets | ForEach-Object { $_.name })
    return (!$Release.draft -and $names -ccontains $NanolatheSetupAsset -and $names -ccontains 'SHA256SUMS')
}

# Select-NanolatheRelease picks the release whose launcher to install: the
# highest-numbered complete native-<run> prerelease, Latest, which a player
# who has not chosen a channel follows. The launcher then installs the game
# from the player's channel and takes the launcher that build ships.
function Select-NanolatheRelease($Releases) {
    $best = $null
    $bestRun = -1
    foreach ($release in $Releases) {
        if ($release.tag_name -cnotmatch '^native-([0-9]{1,9})$') { continue }
        $run = [int]$Matches[1]
        if ($run -gt $bestRun -and (Test-NanolatheReleaseAssets $release)) { $best, $bestRun = $release, $run }
    }
    if ($null -eq $best) { throw 'No published Nanolathe release carries the Windows launcher yet.' }
    return $best
}

function Get-NanolatheAssetUrl($Release, [string]$Name) {
    foreach ($asset in $Release.assets) {
        if ($asset.name -ceq $Name) {
            $url = [string]$asset.browser_download_url
            if (([uri]$url).Scheme -ne 'https') { throw "Refusing a download without HTTPS: $url" }
            return $url
        }
    }
    throw "Release $($Release.tag_name) has no $Name."
}

function Get-NanolatheJson([string]$Url) {
    Invoke-RestMethod -UseBasicParsing -Uri $Url -TimeoutSec 60 -Headers @{ 'User-Agent' = 'Nanolathe-installer'; 'Accept' = 'application/vnd.github+json' }
}

function Get-NanolatheDownload([string]$Url, [string]$Path) {
    Write-Host "Downloading $Url"
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Path -TimeoutSec 600 -Headers @{ 'User-Agent' = 'Nanolathe-installer' }
}

function Write-NanolatheData([string]$Path, [string]$Value) {
    $temp = "$Path.$([guid]::NewGuid().ToString('N')).tmp"
    $backup = "$temp.previous"
    try {
        [IO.File]::WriteAllText($temp, $Value, (New-Object Text.UTF8Encoding($false)))
        if ([IO.File]::Exists($Path)) { [IO.File]::Replace($temp, $Path, $backup) }
        else { [IO.File]::Move($temp, $Path) }
    } finally {
        foreach ($cleanup in @($temp, $backup)) {
            if ([IO.File]::Exists($cleanup)) { Remove-Item -LiteralPath $cleanup -Force -ErrorAction SilentlyContinue }
        }
    }
}

# Install-NanolatheLauncher puts a verified launcher in the installation. A
# launcher already there may be running; Windows lets a running program be
# renamed but not replaced, so it is moved aside, as the launcher itself does.
function Install-NanolatheLauncher([string]$Base, [string]$Source) {
    $destination = Join-Path $Base $NanolatheLauncherName
    $old = "$destination.old"
    if ([IO.File]::Exists($old)) { Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue }
    if ([IO.File]::Exists($destination)) { [IO.File]::Move($destination, $old) }
    try { [IO.File]::Copy($Source, $destination) }
    catch {
        if ([IO.File]::Exists($old) -and ![IO.File]::Exists($destination)) { [IO.File]::Move($old, $destination) }
        throw
    }
    return $destination
}

# Invoke-NanolatheLauncherUpdate runs the launcher's windowless --update,
# which installs or updates the game, adopts a source installation and
# writes the Start Menu entry, and returns its exit code. Its report is
# shown here and kept in logs/launcher-update-*.log.
function Invoke-NanolatheLauncherUpdate([string]$Launcher, [string]$Work) {
    $report = Join-Path $Work 'launcher-update.txt'
    $process = Start-Process -FilePath $Launcher -ArgumentList '--update' -WorkingDirectory (Split-Path $Launcher) `
        -RedirectStandardError $report -NoNewWindow -Wait -PassThru
    if ([IO.File]::Exists($report)) { Get-Content -LiteralPath $report | ForEach-Object { Write-Host $_ } }
    return $process.ExitCode
}

# A source installer's update check runs this script with -NoRun and then
# starts releases/<selected>/launch.ps1. That loader now opens the launcher,
# which has replaced the shortcut, so the player's next start is the
# launcher's own.
function Get-NanolatheReleaseLoader {
    return @'
param([string]$Base, [string]$Root)
# The Nanolathe launcher replaced the source installer; open it.
Start-Process -FilePath (Join-Path $Base 'nanolathe-launcher.exe') -WorkingDirectory $Base
'@
}

function Write-NanolatheReleaseLoader([string]$Base) {
    $current = [IO.File]::ReadAllText((Join-Path $Base 'current.txt')).Trim()
    if ($current -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]+$') { throw 'Invalid current release pointer.' }
    $release = Join-Path (Join-Path $Base 'releases') $current
    if (![IO.File]::Exists((Join-Path $release 'nanolathe.exe'))) { throw 'The launcher did not install a game.' }
    Write-NanolatheData (Join-Path $release 'launch.ps1') (Get-NanolatheReleaseLoader)
    return $release
}

function Invoke-NanolatheInstaller {
    # Windows PowerShell 5.1 renders progress for each download chunk. Suppress
    # that overhead only in this invocation; explicit stage messages remain.
    $ProgressPreference = 'SilentlyContinue'
    if ($Help) {
        Write-Host @'
Nanolathe installer for x64 and ARM64 Windows (PowerShell 5.1 or later).
Usage: & ([scriptblock]::Create((irm https://nanolathe.gg/install.ps1))) [-Root PATH] [-NoRun]
Installs the Nanolathe launcher from the newest published release, then the game.
The launcher keeps both up to date. -NoRun installs without opening it.
-Root remembers a Total Annihilation folder; the launcher checks it before playing.
Set NANOLATHE_INSTALL_DIR to override the default LOCALAPPDATA\Nanolathe folder.
Original game assets are required to play.
'@
        return
    }
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'This installer supports Windows only.' }
    $nativeArchitectures = @(Get-CimInstance -ClassName Win32_Processor | Select-Object -ExpandProperty Architecture)
    [void](Resolve-NanolatheWindowsArchitecture $nativeArchitectures ([Environment]::Is64BitProcess))
    $base = $env:NANOLATHE_INSTALL_DIR
    if ([string]::IsNullOrWhiteSpace($base)) { $base = Join-Path $env:LOCALAPPDATA 'Nanolathe' }
    $base = [IO.Path]::GetFullPath($base)
    # releases marks the folder as an installation, so the launcher acts on it.
    foreach ($folder in @('', 'logs', 'releases')) {
        [void][IO.Directory]::CreateDirectory((Join-Path $base $folder))
    }
    $api = $NanolatheReleasesApi
    if (![string]::IsNullOrWhiteSpace($env:NANOLATHE_RELEASES_API)) { $api = $env:NANOLATHE_RELEASES_API.TrimEnd('/') }
    $work = $null
    $transcribing = $false
    $launcher = Join-Path $base $NanolatheLauncherName
    $log = Join-Path $base ('logs\install-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.log')
    $tlsBefore = [Net.ServicePointManager]::SecurityProtocol
    try {
        Start-Transcript -LiteralPath $log | Out-Null
        $transcribing = $true
        Write-Host "Installing Nanolathe into $base"
        [Net.ServicePointManager]::SecurityProtocol = $tlsBefore -bor [Net.SecurityProtocolType]::Tls12
        Write-Host 'Finding the newest Nanolathe release...'
        $release = Select-NanolatheRelease (Get-NanolatheJson "$api/releases?per_page=50")
        Write-Host "Using the launcher from release $($release.tag_name)"
        $work = Join-Path $base ('work-' + [guid]::NewGuid().ToString('N'))
        [void][IO.Directory]::CreateDirectory($work)
        $sumsPath = Join-Path $work 'SHA256SUMS'
        Get-NanolatheDownload (Get-NanolatheAssetUrl $release 'SHA256SUMS') $sumsPath
        $sums = Read-NanolatheChecksums ([IO.File]::ReadAllText($sumsPath))
        if (!$sums.ContainsKey($NanolatheSetupAsset)) { throw "SHA256SUMS does not list $NanolatheSetupAsset." }
        $setup = Join-Path $work $NanolatheSetupAsset
        Get-NanolatheDownload (Get-NanolatheAssetUrl $release $NanolatheSetupAsset) $setup
        Write-Host 'Download complete; verifying SHA-256...'
        Test-NanolatheChecksum $setup $sums[$NanolatheSetupAsset]
        $launcher = Install-NanolatheLauncher $base $setup
        Write-Host 'Installing the game. The launcher downloads and verifies it...'
        $code = Invoke-NanolatheLauncherUpdate $launcher $work
        if ($code -ne 0) { throw "The launcher could not install the game (exit code $code). See $(Join-Path $base 'logs')" }
        [void](Write-NanolatheReleaseLoader $base)
        if (![string]::IsNullOrWhiteSpace($Root)) {
            if (!(Test-Path -LiteralPath $Root -PathType Container)) { throw "The Total Annihilation folder does not exist: $Root" }
            # The launcher validates the remembered folder before playing.
            Write-NanolatheData (Join-Path $base 'root.txt') ([IO.Path]::GetFullPath($Root))
        }
        Write-Host 'Installed. Start Nanolathe from the Start Menu.'
        Write-Host "Settings and saves: $base"
    } catch {
        Write-Host "Nanolathe installation failed: $($_.Exception.Message)" -ForegroundColor Red
        Write-Host "Log: $log"
        throw
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $tlsBefore
        if ($work -and [IO.Directory]::Exists($work)) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
        if ($transcribing) { Stop-Transcript -ErrorAction SilentlyContinue | Out-Null }
    }
    if (!$NoRun) { Start-Process -FilePath $launcher -WorkingDirectory $base }
}

# Dot-sourcing exposes the pure helpers for offline tests without running setup.
if ($MyInvocation.InvocationName -ne '.') { Invoke-NanolatheInstaller }
