package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// scheduleRemoval deletes the running launcher, then its folder when that
// is empty, after this process exits.
func scheduleRemoval(self, base string) {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	// cmd.exe parses its own command line, so it is passed verbatim.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `/d /c ping -n 3 127.0.0.1 >nul & del /f /q "` + self + `" & rmdir "` + base + `"`,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	_ = cmd.Start()
}

// startInstalled starts the installed launcher and detaches from it.
func startInstalled(path, dir string) error {
	cmd := exec.Command(path)
	cmd.Dir = dir
	return cmd.Start()
}
