//go:build !windows

package main

import "os/exec"

// scheduleRemoval is unused outside Windows, where the launcher does not
// install itself.
func scheduleRemoval(string, string) {}

func startInstalled(path, dir string) error {
	cmd := exec.Command(path)
	cmd.Dir = dir
	return cmd.Start()
}
