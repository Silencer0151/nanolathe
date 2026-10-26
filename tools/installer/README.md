# Installers

On Mac and Linux, the public alpha installer builds the latest commit on
`main` on the player's computer. It downloads a private Go toolchain and
dependencies, creates a shortcut, and starts the game. On Windows, the
installer script installs the Nanolathe launcher from the newest published
release, and the launcher installs prebuilt builds ("Windows launcher" below);
nothing is compiled. Original Total Annihilation assets are supplied by the
player. This is Nanolathe host installation policy, not retail behavior.

## Install and update

Mac or Linux, in Terminal:

```sh
curl -fsSL https://nanolathe.gg/install.sh | bash
```

Windows x64 or ARM64: download `Nanolathe-Setup.exe` from the newest release,
or in PowerShell:

```powershell
& ([scriptblock]::Create((irm https://nanolathe.gg/install.ps1)))
```

The PowerShell command finds the newest published release (Stable when one
carries the launcher, otherwise Latest), downloads `Nanolathe-Setup.exe`,
checks it against the release's `SHA256SUMS`, installs it as
`nanolathe-launcher.exe`, and runs it windowless (`--update`) to install the
game and the Start Menu shortcut. It then opens the launcher, which keeps the
game up to date from then on. A Windows installation made by the earlier
source installer moves to the launcher the first time its update check offers
**Update & play**: that runs this script, and the new release's `launch.ps1`
opens the launcher.

On Mac and Linux, the shortcut checks the latest commit on `main` at launch. If it
differs from the installed release, it offers **Update & play**
or **Play current version**. The check has a short timeout; an unavailable or
invalid response leaves the installed game usable. Declining the update also
starts the current build. Downloads and compilation only happen after accepting
an update, or when you run the install command yourself.

An accepted update verifies the downloaded installer against the manifest's
SHA-256 before running it. It reuses the existing compiler and build cache,
installs into the same user directory, then launches the selected release.
Build or verification failures leave the previous build selected. Saves and
settings are outside the versioned release directories.

On Mac, accepting an update from the app opens a native progress window with
an activity bar and the current download, verification, build or installation
stage. Compilation has no reliable percentage or time estimate; the window
stays active until the updater finishes, then closes before the game launches.
Terminal launches print the same stages. The progress window uses macOS's
built-in scripting runtime and adds no compiler or application dependency.

**Upgrading the first alpha:** rerun the install command once to replace the
original shortcut launcher with one that checks for updates. You can also close
the game and rerun that command whenever you want to update manually.

Supported installer targets: Mac Intel/Apple Silicon (macOS 13 or newer),
Linux x86-64/ARM64, and Windows x64/ARM64. Linux requires a graphical desktop and the window-system, graphics,
and audio runtime libraries used by Ebitengine. Go alone builds the desktop
engine; a separate C compiler is not required. The Windows launcher installs
the build for the host CPU, including when it runs under x64 emulation on ARM64.

Locally built games and Latest builds are unsigned; Stable releases will be
code signed. A downloaded unsigned `Nanolathe-Setup.exe` meets the ordinary
Windows SmartScreen dialog, which offers **More info → Run anyway**; files the
PowerShell script and the launcher download do not. Smart App Control and
managed-device policies can block unsigned programs without that option. The
installers do not change security policy.

## Files and shortcuts

| Platform | Default installation directory | Shortcut |
|---|---|---|
| Mac | `~/Library/Application Support/Nanolathe` | `~/Applications/Nanolathe.app` |
| Linux | `$XDG_DATA_HOME/nanolathe`, or `~/.local/share/nanolathe` | `$XDG_DATA_HOME/applications/nanolathe.desktop`, or `~/.local/share/applications/nanolathe.desktop` |
| Windows | `%LOCALAPPDATA%\Nanolathe` | Nanolathe in the user's Start Menu, starting `nanolathe-launcher.exe`; an Apps & Features entry |

The Mac app and Windows launcher use the Nanolathe website's green N mark. The
Mac icon and update-window script are copied from the resolved source commit;
they require no separate download. Every successful install or update
refreshes the existing Mac app bundle. The Windows icon is embedded in the
launcher, which rewrites its own Start Menu shortcut and Apps & Features entry
at every start.

The earlier Windows source installer's shortcut ran a fixed encoded PowerShell
loader that read `<install>/launch.ps1`. The launcher replaces that shortcut
and rewrites `launch.ps1` to open the launcher, so a copy of the old shortcut
pinned elsewhere still works.

Within the installation directory:

- `saves`: newly written `.SAV` files.
- `settings.json`: launcher-specific preferences.
- `logs`: installation and game diagnostics.
- `releases`: versioned builds. A source build holds the toolchain `release.txt` manifest and actual `source-revision`; a launcher build holds the release's `build.json`, `release-tag` and `source-revision`.
- `toolchains` and `cache`: private Go installation, dependencies, and build cache (Mac and Linux; the launcher removes them from a Windows installation it adopts).
- Windows: `nanolathe-launcher.exe` and its `launcher.json` (the chosen channel).
- Unix `current` symlink or Windows `current.txt`: selected release.
- Unix `game-root` or Windows `root.txt`: the remembered game-data directory.

The launcher validates and explicitly selects one content root. Several detected
installations require a selection; they are not automatically overlaid. Missing
or moved data triggers selection again. Mac app launches use a native picker;
Terminal/Linux launches can prompt in the terminal. A single usable detected
folder is selected automatically. On Windows the launcher confirms a single
detected folder, lists several to choose from, and opens the Explorer folder
picker when it finds none ("Windows launcher" below).
Starting the installed `nanolathe` executable directly,
without the shortcut, also uses the remembered folder while it still holds game data.
`--check-install` validates startup mounts and required products, not
every asset in the game. A later content error is recorded in the game log.

When reporting a problem, attach the run log from `logs` (or paste the startup
lines and the error). Startup records the build/source revision, OS and CPU
architecture, Go runtime, content profile/mod, archive counts and provider
precedence, map count and skipped maps, effective game/display settings, load
times, heap allocation, graphics backend and configured audio rate. The source
revision comes from `source-revision` when VCS metadata is absent, falling
back to the release manifest for older installations.
Rejected archives and unreadable maps include their provider names. More than
ten HPI archives is valid and does not produce a warning. Manual launches write
these diagnostics to standard error; capture them with `2>nanolathe.log`.

Existing retail saves and manual-build settings are not moved or overwritten.
To import a save, copy its `.SAV` file into the new `saves` directory with the
game closed. Save compatibility remains a work in progress. The launcher passes
`--save-dir` and `NANOLATHE_SETTINGS`; manual engine launches preserve their
existing defaults unless given overrides.

## Options

To install without launching or selecting game data:

```sh
curl -fsSL https://nanolathe.gg/install.sh | bash -s -- --no-run
```

```powershell
& ([scriptblock]::Create((irm https://nanolathe.gg/install.ps1))) -NoRun
```

Pass `--root "/path/to/TotalAnnihilation"` on Unix or
`-Root 'C:\Games\Total Annihilation'` on Windows to select a folder explicitly.
Unix validates and remembers an explicit root during installation, including
with `--no-run`; Windows remembers it, and the launcher validates it before
playing. Set `NANOLATHE_INSTALL_DIR` to an absolute path to choose another
installation directory. It does not change the shortcut location. On Windows,
`NANOLATHE_RELEASES_API` installs a fork's releases instead
(`https://api.github.com/repos/<owner>/nanolathe`).

On Unix, `<installation directory>/launch.sh --root PATH` changes the remembered
folder without rebuilding. Removing the remembered-root file on either platform
makes the next launch select again. Deleting a stale Unix `.install-lock`
directory is appropriate only after confirming no installer is still running.

## Remove

On Windows, uninstall Nanolathe from **Settings → Apps**; it asks whether to
keep saves and settings. On Mac and Linux, close the game, remove its shortcut,
and delete the installation directory. Copy out any saves/settings you want to
keep first. This removes the private compiler and cache too. The original TA
installation remains separate.

## Release maintenance

The canonical scripts live here; the website serves byte-for-byte copies as
`/install.sh` and `/install.ps1`. A website-owned `/install/release.txt` records a
legacy source snapshot and pins the Go patch version, per-platform Go archive
hashes, and public installer hashes.
Installers resolve current `main` once and fetch its immutable archive over
GitHub HTTPS, recording the commit in `source-revision`; the legacy source
archive hashes do not verify current-main downloads. Manifest data is never
evaluated as code. Downloads use HTTPS; hashes pin the expected bytes but are not
a separate publisher signature.

1. Integrate changes and run `tools/check`, `tools/check-retail`, and the native
   **Source installer** CI workflow. Offline tests run with
   `python3 tools/installer/test_install.py` and
   `powershell -NoProfile -File tools/installer/test-install.ps1`.
2. Push the tested engine commit. In the website checkout, run
   `python3 scripts/prepare-source-release.py --sync-installers --engine /path/to/nanolathe --revision FULL_COMMIT --version RELEASE_LABEL --go-version GO_PATCH`.
   It checks public archive scripts against that local commit and reads official
   Go checksums. Review the scripts and manifest together.
3. Push the website changes to a review branch. Dispatch the engine's
   **Source installer** workflow with that branch's raw HTTPS manifest URL to
   test actual toolchain download and installation on native runners. These
   checks omit game launch and require no retail data.
4. On a desktop with retail assets, run the installer, select data, start a
   battle, and check saving/loading. A successful compiler run alone does not
   establish a playable installation. Check platform-specific shortcuts and
   folder selection on the platforms being promoted.
5. Run website `make check` and publish its reviewed commit. This refreshes the
   public installer scripts and their checksums. Future installs and launch-time
   update checks resolve engine `main`; new main commits need no manifest change.
   Prior builds stay installed until the user removes them.

The Windows script no longer reads the manifest's Go or source entries; it
installs from the published releases, and its offline test
(`test-install.ps1`) authors GitHub's answers, the downloads and the
launcher's `--update`. Its hash in `/install/release.txt` still matters: an
installation made by the earlier source installer verifies the downloaded
script against `installer_ps1_sha256` before running it, which is how it moves
to the launcher. A manual run of the **Source installer** workflow also
installs from the published releases on the Windows runners (optionally a
fork's, through `releases_api`) and checks the native build, the launcher and
its Start Menu shortcut. Shortcut creation is tested in Go
(`internal/launcher/shell`).

The first Mac or Linux install has a cold compiler/dependency cache, so measure
its time separately from updates. No first-install duration is promised.

## Prebuilt native builds

CI also builds release executables, so a launcher can install a build
instead of compiling one; on Windows the launcher installs them.
`tools/native-build` reproduces the Mac and Linux installer's build exactly, so
those players and Windows players share stamps online. It downloads
the Go release that `/install/release.txt` pins and checks its hash, downloads
the commit's codeload archive, stamps it with stampgen using the installers'
build arguments and all six toolchain hashes, then cross-compiles Windows and
Linux x64/ARM64 with `CGO_ENABLED=0`, `GOAMD64=v1` and `GOARM64=v8.0`. Its
executables therefore carry the same build manifest as an installer build of
the same commit, which online rooms require; `tools/native_build_test.py`
fails if the installers' build arguments drift from it.

After stamping, `internal/version/winresgen` gives each Windows executable a
version resource (product name Nanolathe and a four-part file version) and
the shortcut icon, as code signing requires. The resource is not part of the
stamped source, so it leaves the stamp unchanged. A build numbers itself from
the highest `vX.Y.Z` tag plus the workflow run number, for example
`0.3.0.412`.

`tools/native-package` writes `nanolathe-windows-{amd64,arm64}.zip` and
`nanolathe-linux-{amd64,arm64}.tar.gz`. Each holds the game executable,
`source-revision` (which the game shows as its revision), `LICENSE` and
`NOTICE.md`; the Windows zips also hold `nanolathe-launcher.exe`. Beside them
are `Nanolathe-Setup.exe` (the x64 launcher, which Windows players download),
`build.json` (revision, version, build manifest digest) and `SHA256SUMS`. The
**native** job runs on every push and pull request and keeps the unsigned
Windows executables as their own artifact,
`windows-{amd64,arm64}/{nanolathe,nanolathe-launcher}.exe`, for signing.
**native-smoke** starts each packaged game on its own platform and checks that
it reports the stamp and revision recorded in `build.json`, and that the
Windows executables carry their version resources. On main,
**native-publish** publishes the assets as an unsigned `native-<run number>`
prerelease (the Latest channel), keeping the two newest, through the same
release code as the browser build.

To test unpushed changes locally, pass `--archive` a tar.gz of the tree whose
one top directory is `nanolathe-<revision>/`, with `--revision` and
`--version`. Builds that run on the host check their own stamp, so a Linux
x64 or Windows x64 host verifies that target.

## Windows launcher

`cmd/nanolathe-launcher` installs, updates and starts prebuilt builds
(`internal/launcher`). Players download `Nanolathe-Setup.exe`. Its first run
offers to install into `%LOCALAPPDATA%\Nanolathe` for the current user, with
no administrator rights. It copies itself there as `nanolathe-launcher.exe`,
adds the Start Menu shortcut (and a desktop one if chosen), registers a
per-user Apps & Features entry that uninstalls through `--uninstall`, and
starts the installed copy. An installed launcher always acts on the folder it
runs from.

At every start the launcher checks its channel. **Stable** is the newest
published `vX.Y.Z` release with native assets; **Latest** is the
highest-numbered complete `native-<n>` release. Until the first Stable
release exists, a player who has not chosen a channel follows Latest. One
button reads Install, Update or Play. An update downloads the platform's
package, verifies every file against `SHA256SUMS`, unpacks it into a staging
directory, checks that the game starts (`--help`), then selects it
atomically. The previous build is kept, older ones are pruned, and a newer
launcher shipped in the package replaces the installed one at its next start.
An offline or failed check leaves the installed build playable. On a PC with
Smart App Control turned on, choosing Latest shows a warning, because Latest
builds are unsigned.

The launcher finds Total Annihilation through the game's own discovery
(`--list-installs`) and validates a folder with `--check-install`. It
confirms a single result, offers a choice when several are found, and opens
the Explorer folder picker when none are. It starts the game with the same
`--root`, `--save-dir` and `NANOLATHE_SETTINGS` as the source installer's
launcher, and writes its output to `logs/game-*.log`.

It adopts a source installation in place, because the directory layout is
the same. The first update keeps saves, settings, logs and `root.txt`,
removes the private Go toolchain and build caches, replaces the Start Menu
shortcut, and rewrites `launch.ps1` so older pinned shortcuts open the
launcher. Set `NANOLATHE_RELEASES_API` to another repository's API root, such
as `https://api.github.com/repos/<owner>/nanolathe`, to try a fork's
releases.
