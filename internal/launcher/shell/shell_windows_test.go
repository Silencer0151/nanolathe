package shell

import (
	"os"
	"path/filepath"
	"testing"

	windowsicon "github.com/nanolathe-gg/nanolathe/tools/installer/windows"
)

// TestShortcutKeepsUnicodePaths: the shell link interface the launcher uses
// stores paths outside the ANSI code page, which the source installer had
// to work around (tools/installer/README.md).
func TestShortcutKeepsUnicodePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Spiel 遊戲 Ω")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "nanolathe-launcher.exe")
	if err := os.WriteFile(target, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "Nanolathe ☆.lnk")
	if err := SaveShortcut(link, target, dir); err != nil {
		t.Fatal(err)
	}
	got, err := ShortcutTarget(link)
	if err != nil || got != target {
		t.Fatalf("shortcut target %q, %v; want %q", got, err, target)
	}
}

func TestIconImagesDecode(t *testing.T) {
	images := windowsicon.Images()
	if len(images) < 3 || images[len(images)-1].Bounds().Dx() != 256 {
		t.Fatalf("%d icon images", len(images))
	}
}
