//go:build !windows

package shell

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Register does nothing: the source installer's script makes the
// shortcut on these systems.
func Register(Entries) error { return nil }

// Unregister does nothing; see Register.
func Unregister(Entries) error { return nil }

// HasDesktopShortcut reports false; see Register.
func HasDesktopShortcut() bool { return false }

// Owner has no meaning outside Windows.
func Owner() uintptr { return 0 }

// PickFolder uses the desktop's own chooser through zenity or kdialog, or
// AppleScript on Mac. ok is false when the player cancels.
func PickFolder(_ uintptr, title, start string) (string, bool, error) {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "`+strings.ReplaceAll(title, `"`, `'`)+`")`)
	case found("zenity"):
		cmd = exec.Command("zenity", "--file-selection", "--directory", "--title="+title, "--filename="+start+"/")
	case found("kdialog"):
		cmd = exec.Command("kdialog", "--getexistingdirectory", start, "--title", title)
	default:
		return "", false, errors.New("nanolathe: no folder chooser is available; install zenity or kdialog")
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	folder := strings.TrimRight(string(out), "\r\n")
	return folder, folder != "", nil
}

func found(name string) bool { _, err := exec.LookPath(name); return err == nil }

// Open shows a folder in the desktop's file manager.
func Open(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}

// Confirm has no dialog outside Windows; the launcher only asks there.
func Confirm(uintptr, string, string) bool { return false }

// Notify has no dialog outside Windows.
func Notify(uintptr, string, string) {}

// SmartAppControl is a Windows feature.
func SmartAppControl() AppControl { return AppControlOff }
