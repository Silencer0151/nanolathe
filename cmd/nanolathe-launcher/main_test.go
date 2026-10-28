package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/launcher"
)

// TestInstallationIsWhereTheLauncherLives: an installed launcher, as its
// shortcut and its uninstall entry start it, acts on its own folder, never
// on the default one; a downloaded launcher installs into the default.
func TestInstallationIsWhereTheLauncherLives(t *testing.T) {
	defaultBase := filepath.Join(t.TempDir(), "default")
	t.Setenv("NANOLATHE_INSTALL_DIR", defaultBase)
	name := launcher.Open(t.TempDir()).LauncherExecutable()

	installed := t.TempDir()
	if err := os.WriteFile(filepath.Join(installed, "current.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	downloads := t.TempDir()
	for running, want := range map[string]string{
		filepath.Join(installed, name):                  installed,
		filepath.Join(downloads, name):                  defaultBase, // no installation files beside it
		filepath.Join(installed, "Nanolathe-Setup.exe"): defaultBase, // the download, wherever it is
	} {
		if got, err := installationOf(running); err != nil || got != want {
			t.Errorf("installationOf(%s) = %s, %v; want %s", running, got, err, want)
		}
	}
}
