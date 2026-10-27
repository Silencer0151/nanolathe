// Package launcher installs, updates and starts prebuilt Nanolathe releases
// for the launcher (cmd/nanolathe-launcher). This is Nanolathe host
// installation policy, not retail behavior. It keeps the source installers'
// directory layout (tools/installer/README.md "Files and shortcuts"), so a
// launcher adopts their installations in place and the game still finds the
// remembered game folder beside its release (internal/install):
//
//	<base>/releases/<release>/nanolathe[.exe]   one directory per build
//	<base>/current.txt (Windows) or current     the selected release
//	<base>/root.txt (Windows) or game-root      the remembered game folder
//	<base>/settings.json, saves/, logs/         the game's own files
//	<base>/launcher.json                        the launcher's settings
//
// Nothing here draws or depends on a window; the launcher's screen and the
// host shell integration live in cmd/nanolathe-launcher.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// byteOrderMark is the UTF-8 mark a Windows editor may put before text.
const byteOrderMark = string(rune(0xfeff))

// Install is one installation directory.
type Install struct {
	Base string
	// GOOS selects the platform's file names; tests set it.
	GOOS string
}

// Open returns the installation at base for the running platform.
func Open(base string) Install { return Install{Base: base, GOOS: runtime.GOOS} }

// DefaultBase is the source installers' default directory: LOCALAPPDATA on
// Windows, the XDG data directory on Linux, Application Support on Mac.
// NANOLATHE_INSTALL_DIR overrides it, as it does for the installers.
func DefaultBase() (string, error) {
	if dir := os.Getenv("NANOLATHE_INSTALL_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("nanolathe: NANOLATHE_INSTALL_DIR must be an absolute path")
		}
		return dir, nil
	}
	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "Nanolathe"), nil
		}
		return "", errors.New("nanolathe: LOCALAPPDATA is not set")
	case "darwin":
		home, err := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "Nanolathe"), err
	}
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "nanolathe"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "nanolathe"), err
}

func (in Install) windows() bool { return in.GOOS == "windows" }

// Path joins a name onto the installation directory.
func (in Install) Path(elem ...string) string {
	return filepath.Join(append([]string{in.Base}, elem...)...)
}

// Executable is the game's file name on this platform.
func (in Install) Executable() string {
	if in.windows() {
		return "nanolathe.exe"
	}
	return "nanolathe"
}

// LauncherExecutable is the launcher's file name on this platform.
func (in Install) LauncherExecutable() string {
	if in.windows() {
		return "nanolathe-launcher.exe"
	}
	return "nanolathe-launcher"
}

func (in Install) rootFile() string {
	if in.windows() {
		return in.Path("root.txt")
	}
	return in.Path("game-root")
}

// releaseName matches the installers' release directory names.
var releaseName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]+$`)

// Current is the selected release's directory name, or "" when none is.
func (in Install) Current() (string, error) {
	var name string
	if in.windows() {
		data, err := os.ReadFile(in.Path("current.txt"))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		} else if err != nil {
			return "", err
		}
		name = strings.TrimSpace(strings.TrimPrefix(string(data), byteOrderMark))
	} else {
		target, err := os.Readlink(in.Path("current"))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		} else if err != nil {
			return "", err
		}
		if filepath.Base(filepath.Dir(target)) != "releases" {
			return "", fmt.Errorf("nanolathe: current release link %s points outside releases", target)
		}
		name = filepath.Base(target)
	}
	if !releaseName.MatchString(name) {
		return "", fmt.Errorf("nanolathe: invalid current release pointer %q", name)
	}
	return name, nil
}

// select points the installation at an installed release, atomically, so a
// launch never sees a half-written pointer.
func (in Install) selectRelease(name string) error {
	if !releaseName.MatchString(name) {
		return fmt.Errorf("nanolathe: invalid release name %q", name)
	}
	if in.windows() {
		return writeAtomic(in.Path("current.txt"), []byte(name))
	}
	link := in.Path("current.next")
	_ = os.Remove(link)
	if err := os.Symlink(in.Path("releases", name), link); err != nil {
		return err
	}
	return os.Rename(link, in.Path("current"))
}

// GamePath is the selected release's executable, or "" when no complete
// release is selected.
func (in Install) GamePath() string {
	name, err := in.Current()
	if err != nil || name == "" {
		return ""
	}
	exe := in.Path("releases", name, in.Executable())
	if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return exe
}

// RememberedRoot is the game folder the launcher or an installer last
// validated, or "" when none is remembered or the folder has gone.
func (in Install) RememberedRoot() string {
	data, err := os.ReadFile(in.rootFile())
	if err != nil {
		return ""
	}
	root := strings.TrimRight(strings.TrimPrefix(string(data), byteOrderMark), "\r\n")
	if strings.ContainsAny(root, "\r\n") || !filepath.IsAbs(root) {
		return ""
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return ""
	}
	return root
}

// Remember records a validated game folder.
func (in Install) Remember(root string) error {
	data := root
	if !in.windows() {
		data += "\n"
	}
	return writeAtomic(in.rootFile(), []byte(data))
}

// playerFiles are the game's own files in the installation: saves, the
// settings.json the launcher names for it, and the Survival records the
// game keeps beside that file.
var playerFiles = []string{"saves", "settings.json", "survival-bests.json"}

// sourceInstallerFiles are what only the source installers use: their
// private Go toolchains, module and build caches, and lock. Their work-*
// and stage-* directories are matched by prefix.
var sourceInstallerFiles = []string{"toolchains", "cache", "install.lock", ".install-lock"}

// owned lists what the launcher and the source installers create in the
// installation directory, by this platform's names.
func (in Install) owned() []string {
	names := append([]string{"releases", "logs", "launcher.json", in.LauncherExecutable(), in.LauncherExecutable() + ".old"},
		sourceInstallerFiles...)
	if in.windows() {
		return append(names, "current.txt", "root.txt", "launch.ps1")
	}
	return append(names, "current", "current.next", "game-root", "launch.sh")
}

// RemoveFiles deletes what the launcher, the source installers and the game
// made in the installation directory, and nothing else: a player may have
// chosen a folder that holds other things. The player's files go only with
// removePlayerFiles. running, the launcher doing this, cannot delete itself
// while it runs and is left to its caller.
func (in Install) RemoveFiles(removePlayerFiles bool, running string) error {
	entries, err := os.ReadDir(in.Base)
	if err != nil {
		return err
	}
	owned := in.owned()
	if removePlayerFiles {
		owned = append(owned, playerFiles...)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if in.windows() {
			name = strings.ToLower(name)
		}
		if strings.EqualFold(in.Path(e.Name()), running) || !ownedEntry(owned, name, e.IsDir()) {
			continue
		}
		errs = append(errs, os.RemoveAll(in.Path(e.Name())))
	}
	return errors.Join(errs...)
}

// ownedEntry matches an owned name, a source installer's work-* or stage-*
// directory, or an owned file's interrupted atomic write (<name>.*.tmp).
func ownedEntry(owned []string, name string, dir bool) bool {
	if dir && (strings.HasPrefix(name, "work-") || strings.HasPrefix(name, "stage-")) {
		return true
	}
	for _, o := range owned {
		if name == o || !dir && strings.HasPrefix(name, o+".") && strings.HasSuffix(name, ".tmp") {
			return true
		}
	}
	return false
}

// writeAtomic replaces a small file by renaming a complete temporary copy
// over it.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
