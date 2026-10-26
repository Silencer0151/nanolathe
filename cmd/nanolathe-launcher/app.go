package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/launcher"
	"github.com/nanolathe-gg/nanolathe/internal/launcher/shell"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// view is what the window shows below the header.
type view int

const (
	viewSetup  view = iota // running from a download: offer to install
	viewMain               // the installed launcher
	viewFolder             // choosing the Total Annihilation folder
)

// app is the launcher window. Network, disk and child-process work runs on
// goroutines; they change state only under mu, and Draw reads it there.
type app struct {
	in      launcher.Install
	src     launcher.Source
	running string
	goos    string
	goarch  string

	fonts  screenkit.Fonts
	loaded bool
	hits   screenkit.Hits
	scale  float64
	last   time.Time

	mu         sync.Mutex
	view       view
	settings   launcher.Settings
	chosen     bool // the player has picked a channel before
	game       launcher.Installed
	hasGame    bool
	checking   bool
	checkGen   int
	offered    *launcher.Release
	offerErr   error
	busy       string  // what a running task is doing, "" when idle
	progress   float64 // 0..1, or <0 when no bar shows
	status     string
	failed     bool
	root       string // remembered, validated game folder
	appControl shell.AppControl
	dropdown   bool
	desktop    bool

	// The folder view.
	candidates []string
	searching  bool
	selected   int
	scroll     int  // the first candidate the list shows
	launchNext bool // start the game once a folder is chosen
	quit       bool

	// closeWhenIdle: the player closed the window during a task, which
	// finishes first. Only the game goroutine reads or writes it.
	closeWhenIdle bool
}

func newApp(in launcher.Install, running string) *app {
	a := &app{in: in, src: launcher.NewSource(), running: running, goos: runtime.GOOS, goarch: launcher.NativeArch(),
		progress: -1, desktop: true, appControl: shell.SmartAppControl()}
	a.settings, a.chosen = in.LoadSettings()
	if !installed(in, running) {
		a.view = viewSetup
		return a
	}
	a.view = viewMain
	in.RemoveOldLauncher()
	if runtime.GOOS == "windows" {
		adoptSourceInstall(in)
	}
	a.refreshInstalled()
	a.root = in.RememberedRoot()
	a.check()
	return a
}

// refreshInstalled re-reads the selected build, and keeps the desktop
// entries naming the installed version. The caller need not hold mu.
func (a *app) refreshInstalled() {
	game, ok := a.in.Selected()
	a.mu.Lock()
	a.game, a.hasGame = game, ok
	a.mu.Unlock()
	if ok && runtime.GOOS == "windows" {
		_ = shell.Register(shell.Entries{Base: a.in.Base, Launcher: a.in.InstalledLauncher(), Version: game.Version, Desktop: shell.HasDesktopShortcut()})
	}
}

// check asks the channel for its release. Stable has none until the first
// signed release; a player who has not chosen a channel then follows Latest
// for now, so a first install can play, and Stable once it has a release.
// A check the player overtook by switching channels is dropped.
func (a *app) check() {
	a.mu.Lock()
	a.checkGen++
	gen, ch, chosen := a.checkGen, a.settings.Channel, a.chosen
	if !chosen {
		ch = launcher.Stable
	}
	a.checking, a.offered, a.offerErr = true, nil, nil
	a.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := a.src.Find(ctx, ch, a.goos, a.goarch)
		if errors.Is(err, launcher.ErrNoRelease) && !chosen {
			ch = launcher.Latest
			rel, err = a.src.Find(ctx, ch, a.goos, a.goarch)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if gen != a.checkGen {
			return
		}
		a.checking = false
		if !chosen {
			a.settings.Channel = ch
		}
		if err != nil {
			a.offerErr = err
			return
		}
		a.offered = &rel
	}()
}

// setChannel saves the player's choice and checks it.
func (a *app) setChannel(ch launcher.Channel) {
	a.mu.Lock()
	a.dropdown = false
	if a.settings.Channel == ch && a.chosen {
		a.mu.Unlock()
		return
	}
	a.settings.Channel, a.chosen = ch, true
	settings := a.settings
	a.mu.Unlock()
	if err := a.in.SaveSettings(settings); err != nil {
		a.fail("Could not save the channel: " + err.Error())
	}
	a.check()
}

func (a *app) setStatus(text string) {
	a.mu.Lock()
	a.status, a.failed = text, false
	a.mu.Unlock()
}

func (a *app) fail(text string) {
	a.mu.Lock()
	a.status, a.failed = text, true
	a.mu.Unlock()
}

// task runs work in the background; only one runs at a time. doing is the
// action button's short label, detail the status line (default doing...).
func (a *app) task(doing, detail string, work func() error) {
	a.mu.Lock()
	if a.busy != "" {
		a.mu.Unlock()
		return
	}
	a.busy, a.progress, a.status, a.failed = doing, -1, detail, false
	a.mu.Unlock()
	go func() {
		err := work()
		a.mu.Lock()
		a.busy, a.progress = "", -1
		a.mu.Unlock()
		if err != nil {
			a.fail(err.Error())
		}
	}()
}

// install copies the downloaded launcher into place, registers it with
// the desktop and starts the installed copy.
func (a *app) install() {
	a.mu.Lock()
	desktop := a.desktop
	a.mu.Unlock()
	a.task("Installing", "", func() error {
		if err := a.in.InstallLauncher(a.running); err != nil {
			return fmt.Errorf("Could not install the launcher: %w", err)
		}
		version := ""
		if game, ok := a.in.Selected(); ok {
			version = game.Version
		}
		if err := shell.Register(shell.Entries{Base: a.in.Base, Launcher: a.in.InstalledLauncher(), Version: version, Desktop: desktop}); err != nil {
			return fmt.Errorf("Could not add the Start Menu shortcut: %w", err)
		}
		if err := startInstalled(a.in.InstalledLauncher(), a.in.Base); err != nil {
			return err
		}
		a.mu.Lock()
		a.quit = true
		a.mu.Unlock()
		return nil
	})
}

// update installs the offered release.
func (a *app) update() {
	a.mu.Lock()
	rel := a.offered
	a.mu.Unlock()
	if rel == nil {
		return
	}
	a.task("Downloading", "", func() error {
		err := a.in.Update(context.Background(), a.src, *rel, a.goos, a.goarch, func(done, total int64) {
			a.mu.Lock()
			if total > 0 {
				a.progress = float64(done) / float64(total)
			}
			a.mu.Unlock()
		})
		if err != nil {
			return fmt.Errorf("Update failed: %w", err)
		}
		_, launcherErr := a.in.UpdateLauncher()
		a.refreshInstalled()
		a.mu.Lock()
		version := a.game.Version
		a.mu.Unlock()
		if launcherErr != nil {
			return fmt.Errorf("Updated to %s, but the launcher could not update itself: %w", version, launcherErr)
		}
		a.setStatus("Updated to " + version + ".")
		return nil
	})
}

// play starts the game on the remembered folder after checking it still
// holds the game; otherwise the player chooses one first.
func (a *app) play() {
	a.mu.Lock()
	root := a.root
	a.mu.Unlock()
	if root == "" {
		a.chooseFolder(true)
		return
	}
	a.task("Checking", "Checking your game folder: "+root, func() error {
		if !a.checkRoot(root) {
			a.mu.Lock()
			a.root = ""
			a.mu.Unlock()
			a.chooseFolder(true)
			return errors.New("The remembered game folder is no longer usable. Choose it again.")
		}
		return a.start(root)
	})
}

func (a *app) start(root string) error {
	if _, logName, err := a.in.Start(root); err != nil {
		return fmt.Errorf("Could not start the game: %w (log %s)", err, logName)
	}
	a.mu.Lock()
	a.quit = true
	a.mu.Unlock()
	return nil
}

// checkRoot validates a folder with the game, logging why one is refused.
func (a *app) checkRoot(root string) bool {
	log, err := a.in.NewLog("folder-check")
	if err != nil {
		return false
	}
	defer log.Close()
	return launcher.CheckRoot(context.Background(), a.in.GamePath(), root, log)
}

// chooseFolder opens the folder view with the game's detected candidates.
func (a *app) chooseFolder(launchNext bool) {
	a.mu.Lock()
	a.view, a.launchNext, a.candidates, a.selected, a.searching, a.scroll = viewFolder, launchNext, nil, 0, true, 0
	a.mu.Unlock()
	go func() {
		roots, err := launcher.Candidates(context.Background(), a.in.GamePath())
		a.mu.Lock()
		defer a.mu.Unlock()
		a.candidates, a.searching = roots, false
		if err != nil {
			a.status, a.failed = err.Error(), true
		}
	}()
}

// useFolder validates and remembers a folder, then plays when asked.
func (a *app) useFolder(root string) {
	a.task("Checking", "Checking "+root, func() error {
		if !a.checkRoot(root) {
			return errors.New("That folder is not a usable Total Annihilation installation. Its log is in the logs folder.")
		}
		if err := a.in.Remember(root); err != nil {
			return err
		}
		a.mu.Lock()
		a.root, a.view = root, viewMain
		launch := a.launchNext
		a.mu.Unlock()
		if launch {
			return a.start(root)
		}
		a.setStatus("Game folder set.")
		return nil
	})
}

// browse asks for a folder with the desktop's own picker.
func (a *app) browse() {
	owner := shell.Owner()
	a.mu.Lock()
	start := a.root
	if len(a.candidates) > 0 {
		start = a.candidates[a.selected]
	}
	a.mu.Unlock()
	go func() {
		folder, ok, err := shell.PickFolder(owner, "Choose your Total Annihilation folder", start)
		if err != nil {
			a.fail(err.Error())
			return
		}
		if ok {
			a.useFolder(folder)
		}
	}()
}

func (a *app) openFolder(path string) {
	_ = os.MkdirAll(path, 0o755)
	if err := shell.Open(path); err != nil {
		a.fail(err.Error())
	}
}

// Update implements ebiten.Game.
func (a *app) Update() error {
	if !a.loaded {
		a.fonts, a.loaded = screenkit.LoadFonts(), true
	}
	now := time.Now()
	dt := 1.0 / 30
	if !a.last.IsZero() {
		dt = now.Sub(a.last).Seconds()
	}
	a.last = now
	a.mu.Lock()
	busy := a.busy
	a.mu.Unlock()
	if ebiten.IsWindowBeingClosed() || a.closeWhenIdle {
		if busy == "" {
			return ebiten.Termination
		}
		if !a.closeWhenIdle {
			a.setStatus("Nanolathe is " + strings.ToLower(busy) + "; the window closes when it is done.")
			a.closeWhenIdle = true
		}
	}
	in := screenkit.ReadInput()
	if in.WheelY != 0 {
		a.mu.Lock()
		if a.view == viewFolder {
			a.scrollCandidates(-int(math.Copysign(1, in.WheelY)))
		}
		a.mu.Unlock()
	}
	a.hits.Update(in, dt)
	a.mu.Lock()
	quit := a.quit
	a.mu.Unlock()
	if quit {
		return ebiten.Termination
	}
	return nil
}

// scrollCandidates moves the folder list by rows, within its bounds. The
// caller holds mu.
func (a *app) scrollCandidates(rows int) {
	a.scroll = max(0, min(a.scroll+rows, len(a.candidates)-candidateRows))
}

// Layout implements ebiten.Game: the window is drawn in device pixels.
func (a *app) Layout(w, h int) (int, int) {
	a.scale = ebiten.Monitor().DeviceScaleFactor()
	return int(float64(w) * a.scale), int(float64(h) * a.scale)
}
