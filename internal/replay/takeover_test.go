package replay_test

import (
	"errors"
	"io"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/replay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	// A takeover hands the recording's other humans to the Modern AI
	// (DESIGN_MULTIPLAYER §10.1), so this test binary links it.
	_ "github.com/nanolathe-gg/nanolathe/mods/aikit"
)

// A completed pump is a usable live world even when the original input
// stream still has many commands. A fork permanently retires that reader
// (DESIGN_MULTIPLAYER §10.1), while reopening it remains ordinary playback.
func TestPlayerTakeoverStopsRecordedInputAndContinuesLive(t *testing.T) {
	fs := portableFS(t)
	cfg := session.DirectSkirmishConfig(portableMap)
	cfg.UnitLimit, cfg.Location = 20, 1
	rec := recordSinglePlayer(t, headless.FreshBattleRequest{Kind: headless.ScenarioSkirmish, Map: portableMap,
		Skirmish: cfg, Gameplay: gameplay.Modern, SimulationSeed: 7, CRTSeed: 11, FS: fs, LocalOwner: -1}, 80)
	for _, owner := range []uint8{0, 1} {
		r, err := replay.NewReader(rec.data)
		if err != nil {
			t.Fatal(err)
		}
		s, err := replay.Compose(r.Header(), replay.Content{FS: fs})
		if err != nil {
			t.Fatal(err)
		}
		p, err := replay.NewPlayer(s, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Step(47); err != nil {
			t.Fatal(err)
		}
		at := p.Tick()
		if at == 0 || at >= rec.ticks {
			t.Fatalf("no interior branch point at %d of %d", at, rec.ticks)
		}
		if _, err := p.TakeOver(owner); err != nil {
			t.Fatal(err)
		}
		if n, err := p.Step(10000); n != 0 || err != io.EOF || s.Clock.GlobalTick != at {
			t.Fatalf("retired playback advanced: %d ticks, %v", n, err)
		}
		u := localCommander(t, s)
		if err := s.EnqueueHumanCommand(session.HumanCommand{Kind: session.HumanOrder,
			Order: session.HumanOrderCommand{Handles: []pool.Handle{u.Handle}, Code: 2,
				Position: orders.ResolvePos{X: 256 << 16, Y: u.Y, Z: 256 << 16}}}); err != nil {
			t.Fatal(err)
		}
		x, z := u.X, u.Z
		for range 60 {
			s.ExecuteStep(s.PrepareStep(s.Clock.ScaledAnchor + 1))
		}
		if u.X == x && u.Z == z || s.LocalOwner != owner || p.Tick() != at || p.CanTakeOver(owner) == nil {
			t.Fatal("the fork did not move under live human control with retired playback")
		}
	}
	checkSinglePlayerPlayback(t, rec, replay.Content{FS: fs})
}

func TestPlayerTakeoverAllowsStoppedRecordingButRefusesDivergence(t *testing.T) {
	fs := portableFS(t)
	rec := recordOnline(t, fs, 90)
	for _, corrupt := range []bool{false, true} {
		data := rewrite(t, rec.data, func(e *replay.Entry) bool {
			if corrupt && e.Kind == replay.EntryChecksum && e.Tick == 60 {
				e.Sum[0] ^= 1
			}
			if e.Kind == replay.EntryEnd {
				e.Reason = replay.EndLeft
			}
			return true
		})
		p, s, _, err := play(t, fs, data, 1)
		if corrupt {
			if !errors.Is(err, replay.ErrMismatch) || p.CanTakeOver(0) == nil {
				t.Fatalf("diverged playback admitted: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.TakeOver(0); err != nil {
			t.Fatal(err)
		}
		if err := s.StepGranted(91); err != nil || s.LocalOwner != 0 {
			t.Fatalf("stopped online recording did not continue locally: %v", err)
		}
	}
}
