//go:build !windows

package launcher

import (
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// detach starts the game in its own session, so closing the launcher's
// terminal does not end it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// NativeArch is the running architecture.
func NativeArch() string { return runtimeArch() }
