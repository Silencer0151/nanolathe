//go:build retail

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

// Exercise the actual window lifecycle, HUD replacement, the carried-over
// selection and camera, live clock and orders, the Modern AI taking the
// recording's other human, and a save and load of the continuation,
// including a threaded Modern computer in Strict.
// Optional captures use the normal software composer, with no window server.
func TestReplayTakeoverWindowRetail(t *testing.T) {
	for _, name := range []string{"modern", "strict-3.1", "community-3.9", "survival"} {
		t.Run(name, func(t *testing.T) {
			replayTestSettings(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "takeover.nlreplay")
			mode := name
			var extra []string
			if name == "survival" {
				mode, extra = "modern", []string{"--survival", "--survival-buddies", "1"}
			}
			args := []string{"--map", "ashap plateau", "--seed", "7", "--gameplay", mode,
				"--ai-player", "all=modern", "--headless", "--ticks", "150", "--record-replay", path, "--report", filepath.Join(dir, "report.json")}
			opts := replayTestOptions(t, append(args, extra...)...)
			cs := replayTestContent(t, opts)
			if err := runHeadless(opts, cs, io.Discard); !errors.Is(err, errHeadlessTickLimit) {
				t.Fatal(err)
			}
			for _, owner := range []uint8{0, 1} {
				t.Run(fmt.Sprintf("player-%d", owner+1), func(t *testing.T) {
					w := openReplayWindow(t, replayTestOptions(t, "--replay", path), cs)
					// Exercise the modern window's worker handoff without a
					// graphics device; the same client still composes captures.
					async := name == "modern" && owner == 1
					w.cl.SetAsyncSimulation(async)
					b, p := w.shell.battle, w.shell.battle.playback
					for range 60 {
						w.step(1)
					}
					if err := p.SetPerspective(int(owner)); err != nil {
						t.Fatal(err)
					}
					p.SetPaused(true)
					w.step(1)
					at := p.Tick()
					// Watching the seat, select its commander and move the
					// camera; both must survive taking that seat over.
					var watched pool.Handle
					for _, u := range b.sess.Units.IterSliced() {
						if u.Alive && u.Owner == owner && u.Def != nil && u.Def.Commander {
							watched = u.Handle
						}
					}
					if _, err := b.submitHumanCommand(session.HumanCommand{Kind: session.HumanSelectionReplace, Selection: session.HumanSelectionCommand{Handles: []pool.Handle{watched}}}); err != nil {
						t.Fatal(err)
					}
					if got := b.selectedHandlesInSlotOrder(); watched == 0 || len(got) != 1 || got[0] != watched {
						t.Fatalf("the viewed seat's commander %d cannot be selected while watching: %v", watched, got)
					}
					b.cam.JumpTo(b.cam.X+64, b.cam.Z+32)
					camX, camZ := b.cam.X, b.cam.Z
					capture := func(shot string) {
						t.Helper()
						if dest := os.Getenv("NANOLATHE_TAKEOVER_SHOTS"); dest != "" && name == "modern" && owner == 1 {
							if err := os.MkdirAll(dest, 0o755); err != nil {
								t.Fatal(err)
							}
							if err := encodeShotPNG(filepath.Join(dest, shot+".png"), w.cl.ComposeFrame()); err != nil {
								t.Fatal(err)
							}
							data, err := os.ReadFile(path)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(dest, "demo.nlreplay"), data, 0o644); err != nil {
								t.Fatal(err)
							}
						}
					}
					capture("before")
					p.apply(replayKeyTakeover)
					w.step(1)
					if b.playback != nil || b.takeover == nil || b.sess.LocalOwner != owner || b.hud.owner != owner || b.sess.Clock.GlobalTick != at || b.sess.Clock.Paused || b.sess.Clock.Active != 10 {
						t.Fatalf("takeover did not transfer at tick %d: playback %v, takeover %v, owner %d, HUD %d, clock %+v; notice %s", at, b.playback != nil, b.takeover != nil, b.sess.LocalOwner, b.hud.owner, b.sess.Clock, p.overlay.notice)
					}
					if got := b.selectedHandlesInSlotOrder(); len(got) != 1 || got[0] != watched {
						t.Fatalf("the watched selection did not carry over: %v, want %v", got, watched)
					}
					if b.cam.X != camX || b.cam.Z != camZ {
						t.Fatalf("the camera moved from (%d,%d) to (%d,%d)", camX, camZ, b.cam.X, b.cam.Z)
					}
					// The recording's other human is the Modern AI's now.
					for seat := uint8(0); seat < 2; seat++ {
						if seat != owner && !b.sess.ModernAIPlayer(seat) {
							t.Fatalf("player %d is not a Modern AI computer player", seat+1)
						}
					}
					w.shell.opts.SaveDir = t.TempDir()
					_, canRestart := b.battleRestartRequest()
					if !w.shell.autosaveAllowed() || canRestart || b.replay != nil || b.survivalBest != nil {
						t.Fatal("a single-player continuation must save, and must neither restart, record nor score")
					}
					u := localCommanderOf(t, b.sess)
					x, z := u.X, u.Z
					if _, err := b.submitHumanCommand(session.HumanCommand{Kind: session.HumanSelectionReplace, Selection: session.HumanSelectionCommand{Handles: []pool.Handle{u.Handle}}}); err != nil {
						t.Fatal(err)
					}
					if got := b.selectedHandlesInSlotOrder(); len(got) != 1 || got[0] != u.Handle {
						t.Fatalf("new owner's commander cannot be selected: %v", got)
					}
					// No explicit handles: exercise the new owner's local selection.
					if _, err := b.submitHumanCommand(session.HumanCommand{Kind: session.HumanOrder, Order: session.HumanOrderCommand{Code: 2,
						Position: orders.ResolvePos{X: x + 96<<16, Y: u.Y, Z: z + 32<<16}}}); err != nil {
						t.Fatal(err)
					}
					for range 120 {
						w.step(1)
					}
					b.joinSimulation(w.cl)
					if async && b.sim == nil {
						t.Fatal("takeover did not start the modern window's simulation worker")
					}
					if u.X == x && u.Z == z || b.sess.Clock.GlobalTick <= at || p.Tick() != at || b.sess.LocalOwner != owner {
						t.Fatal("the live clock/command did not advance independently of the replay")
					}
					if side, ok := b.sess.SideForOwner(int(owner)); !ok || b.hud.side != b.cat.Sides[side] || !b.sess.Snapshot.Current().Fog.Valid {
						t.Fatal("the chosen faction HUD or ordinary fog was not restored")
					}
					if (b.sess.Survival != nil) != (name == "survival") {
						t.Fatal("takeover changed the scenario")
					}
					capture("after")
					// The continuation saves and loads as an ordinary battle,
					// still played by the chosen seat.
					live := b.sess.Units.LiveCountForPlayer(int(owner))
					if err := w.shell.writeSlotSave(quicksaveName); err != nil {
						t.Fatal(err)
					}
					if err := w.shell.loadRetailSavePath(session.RetailSavePath(w.shell.slotSaveDir(), quicksaveName)); err != nil {
						t.Fatal(err)
					}
					loaded := w.shell.battle
					if loaded == nil || loaded == b || loaded.takeover != nil || loaded.sess.LocalOwner != owner ||
						loaded.sess.Units.LiveCountForPlayer(int(owner)) != live || loaded.sess.IsSurvival() != (name == "survival") {
						t.Fatal("the continuation's save did not load as the chosen player's battle")
					}
					for seat := uint8(0); seat < 2; seat++ {
						if seat != owner && !loaded.sess.ModernAIPlayer(seat) {
							t.Fatalf("the loaded game lost player %d's Modern AI", seat+1)
						}
					}
				})
			}
		})
	}
}
