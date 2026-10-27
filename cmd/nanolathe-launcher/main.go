// Command nanolathe-launcher installs, updates and starts Nanolathe. This is
// Nanolathe host installation policy, not retail behavior.
//
// Downloaded on its own, it is the Windows setup program: its first run
// copies it into the installation directory, adds the Start Menu shortcut
// and the Apps & Features entry, and starts the installed copy. From there
// it checks the chosen channel (Stable or Latest) at every start, offers
// the update, finds or asks for the Total Annihilation folder, and starts
// the game. It adopts a source installer's directory in place
// (internal/launcher). With --uninstall it removes the installation.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/launcher"
	"github.com/nanolathe-gg/nanolathe/internal/launcher/shell"
	windowsicon "github.com/nanolathe-gg/nanolathe/tools/installer/windows"
)

func main() {
	uninstallFlag := flag.Bool("uninstall", false, "remove Nanolathe from this computer")
	flag.Parse()
	if err := run(*uninstallFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		shell.Notify(0, "Nanolathe", err.Error())
		os.Exit(1)
	}
}

func run(uninstallRequested bool) error {
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
