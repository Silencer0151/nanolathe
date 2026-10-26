// Command nanolathe-launcher installs, updates and starts Nanolathe. This is
// Nanolathe host installation policy, not retail behavior.
//
// Downloaded on its own, it is the Windows setup program: its first run
// copies it into the installation directory, adds the Start Menu shortcut
// and the Apps & Features entry, and starts the installed copy. From there
// it checks the chosen channel (Stable or Latest) at every start, offers
// the update, finds or asks for the Total Annihilation folder, and starts
// the game. It adopts a source installer's directory in place
// (internal/launcher). With --uninstall it removes the installation; with
// --update it installs or updates the game without a window, for
// tools/installer/install.ps1.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/launcher"
	"github.com/nanolathe-gg/nanolathe/internal/launcher/shell"
	windowsicon "github.com/nanolathe-gg/nanolathe/tools/installer/windows"
)

func main() {
	uninstallFlag := flag.Bool("uninstall", false, "remove Nanolathe from this computer")
	updateFlag := flag.Bool("update", false, "install or update the game from its channel without a window, then exit")
	flag.Parse()
	if err := run(*uninstallFlag, *updateFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// A windowless update reports to its caller, never in a dialog
		// that would stop a script.
		if !*updateFlag {
			shell.Notify(0, "Nanolathe", err.Error())
		}
		os.Exit(1)
	}
}

func run(uninstallRequested, updateRequested bool) error {
	running, err := os.Executable()
	if err != nil {
		return err
	}
	base, err := installationOf(running)
	if err != nil {
		return err
	}
	in := launcher.Open(base)
	if uninstallRequested {
		return uninstall(in, running)
	}
	if updateRequested {
		return updateNow(in, running)
	}
	a := newApp(in, running)
	ebiten.SetWindowTitle("Nanolathe")
	ebiten.SetWindowSize(windowW, windowH)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeDisabled)
	ebiten.SetWindowIcon(windowsicon.Images())
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetTPS(30)
	// A download keeps reporting, and a click into the window counts, while
	// another window has focus.
	ebiten.SetRunnableOnUnfocused(true)
	return ebiten.RunGame(a)
}

// updateNow is --update: what the window's Install or Update button does,
// without a window, so tools/installer/install.ps1 can finish an
// installation, and move a source installation to the launcher, from a
// script. It installs the channel's release unless it is already selected,
// takes the launcher that release ships, and refreshes the Start Menu entry.
// Progress goes to standard error and to logs/launcher-update-*.log.
func updateNow(in launcher.Install, running string) error {
	if !installed(in, running) {
		return fmt.Errorf("nanolathe: --update runs only from an installed launcher, %s", in.InstalledLauncher())
	}
	in.RemoveOldLauncher()
	if runtime.GOOS == "windows" {
		adoptSourceInstall(in)
	}
	out := io.Writer(os.Stderr)
	if log, err := in.NewLog("launcher-update"); err == nil {
		defer log.Close()
		out = io.MultiWriter(os.Stderr, log)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	src, goarch := launcher.NewSource(), launcher.NativeArch()
	settings, chosen := in.LoadSettings()
	rel, ch, err := launcher.Offer(ctx, src, settings, chosen, runtime.GOOS, goarch)
	if err != nil {
		return fmt.Errorf("nanolathe: no %s release to install: %w", settings.Channel, err)
	}
	fmt.Fprintf(out, "nanolathe: the %s channel offers %s for %s/%s\n", ch, rel.Tag, runtime.GOOS, goarch)
	if game, ok := in.Selected(); ok && game.Current(rel) {
		fmt.Fprintf(out, "nanolathe: %s is already installed\n", game.Version)
	} else if err := in.Update(ctx, src, rel, runtime.GOOS, goarch, nil); err != nil {
		return err
	}
	if replaced, err := in.UpdateLauncher(); err != nil {
		return fmt.Errorf("nanolathe: the game is installed, but the launcher could not update itself: %w", err)
	} else if replaced {
		fmt.Fprintln(out, "nanolathe: the launcher was updated")
	}
	game, _ := in.Selected()
	if runtime.GOOS == "windows" {
		if err := shell.Register(shell.Entries{Base: in.Base, Launcher: in.InstalledLauncher(), Version: game.Version, Desktop: shell.HasDesktopShortcut()}); err != nil {
			return fmt.Errorf("nanolathe: could not add the Start Menu shortcut: %w", err)
		}
	}
	fmt.Fprintf(out, "nanolathe: installed %s in %s\n", game.Version, in.Base)
	return nil
}

// installationOf is the installation a launcher belongs to: the folder it
// runs from when that holds an installed launcher's files, so its shortcut
// and its uninstall entry always act on their own installation, whatever
// NANOLATHE_INSTALL_DIR says when they run. A downloaded launcher installs
// into the default directory.
func installationOf(running string) (string, error) {
	dir := filepath.Dir(running)
	if strings.EqualFold(filepath.Base(running), launcher.Open(dir).LauncherExecutable()) {
		for _, marker := range []string{"launcher.json", "current.txt", "current", "releases"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
	}
	return launcher.DefaultBase()
}

// installed reports whether the running launcher is the installation's own
// copy. Outside Windows the source installer's script installs the game, so
// the launcher always runs as installed.
func installed(in launcher.Install, running string) bool {
	if runtime.GOOS != "windows" {
		return true
	}
	a, err := os.Stat(running)
	if err != nil {
		return false
	}
	b, err := os.Stat(in.InstalledLauncher())
	return err == nil && os.SameFile(a, b)
}

// bootstrapScript replaces the source installer's PowerShell loader, which
// its Start Menu shortcut ran. A copy of that shortcut pinned elsewhere
// still runs the loader; it now starts the launcher instead.
const bootstrapScript = `param([Parameter(Mandatory=$true)][string]$Base)
# The Nanolathe launcher replaced the source installer's loader.
Start-Process -FilePath (Join-Path $Base 'nanolathe-launcher.exe') -WorkingDirectory $Base
`

// adoptSourceInstall points an older shortcut's loader at the launcher.
func adoptSourceInstall(in launcher.Install) {
	path := in.Path("launch.ps1")
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "nanolathe-launcher.exe") {
		return
	}
	_ = os.WriteFile(path, []byte(bootstrapScript), 0o644)
}

// uninstall removes the shortcuts, the Apps & Features entry and the
// installation, keeping saves and settings unless the player asks.
func uninstall(in launcher.Install, running string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("nanolathe: to uninstall, delete %s and the Nanolathe shortcut", in.Base)
	}
	if !shell.Confirm(0, "Uninstall Nanolathe", "Remove Nanolathe from this computer?\n\nYour Total Annihilation files are not changed.") {
		return nil
	}
	removeAll := shell.Confirm(0, "Uninstall Nanolathe", "Also delete your Nanolathe saves and settings?\n\nChoose No to keep them in "+in.Base+".")
	if err := shell.Unregister(shell.Entries{Base: in.Base, Launcher: in.InstalledLauncher()}); err != nil {
		return err
	}
	self, _ := filepath.Abs(running)
	entries, err := os.ReadDir(in.Base)
	if err != nil {
		return err
	}
	keep := map[string]bool{"saves": true, "settings.json": true}
	for _, e := range entries {
		path := in.Path(e.Name())
		if strings.EqualFold(path, self) || (!removeAll && keep[strings.ToLower(e.Name())]) {
			continue
		}
		_ = os.RemoveAll(path)
	}
	// A running program cannot delete itself; a short-lived command removes
	// the launcher, and the folder if nothing was kept, once it has exited.
	scheduleRemoval(self, in.Base)
	message := "Nanolathe was removed."
	if !removeAll {
		message += "\n\nYour saves and settings are still in " + in.Base + "."
	}
	shell.Notify(0, "Uninstall Nanolathe", message)
	return nil
}
