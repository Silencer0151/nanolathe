package launcher

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Candidates lists the Total Annihilation installations the game's own
// discovery finds (--list-installs), so the launcher never repeats that
// policy. The game exits 1 when it finds none.
func Candidates(ctx context.Context, exe string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--list-installs")
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	hideWindow(cmd)
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("nanolathe: game discovery failed: %w: %s", err, strings.TrimSpace(diag.String()))
	}
	var roots []string
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			roots = append(roots, line)
		}
	}
	return roots, scanner.Err()
}

// CheckRoot validates a game folder with the game's --check-install, which
// mounts its content without opening a window. Its diagnostics go to log.
func CheckRoot(ctx context.Context, exe, root string, log io.Writer) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--check-install", "--root", root)
	cmd.Stdout, cmd.Stderr = log, log
	hideWindow(cmd)
	return cmd.Run() == nil
}

// NewLog opens a timestamped log under logs/, named as the source
// installers name theirs.
func (in Install) NewLog(kind string) (*os.File, error) {
	if err := os.MkdirAll(in.Path("logs"), 0o755); err != nil {
		return nil, err
	}
	return os.Create(in.Path("logs", kind+"-"+time.Now().Format("20060102-150405")+".log"))
}

// Start launches the selected game on root, as the source installers'
// launcher does: saves and settings live in the installation directory,
// and the game's output goes to a log. The game runs on after the launcher
// exits.
func (in Install) Start(root string) (*exec.Cmd, string, error) {
	exe := in.GamePath()
	if exe == "" {
		return nil, "", errors.New("nanolathe: no installed game is selected")
	}
	log, err := in.NewLog("game")
	if err != nil {
		return nil, "", err
	}
	defer log.Close()
	cmd := exec.Command(exe, "--root", root, "--save-dir", in.Path("saves"))
	cmd.Env = append(os.Environ(), "NANOLATHE_SETTINGS="+in.Path("settings.json"))
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Dir = in.Base
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, log.Name(), err
	}
	return cmd, log.Name(), nil
}
