package session

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// The playback perspective is presentation only: a battle whose perspective
// changes between pumps — another owner, the whole map, both, back — and is
// republished while paused runs exactly as its twin that never changes it,
// with the same random streams, unit checksums and canonical checkpoints, and
// its local and viewing owners never move (DESIGN_MULTIPLAYER §10).
func TestPresentationPerspectiveChangesNoSimulation(t *testing.T) {
	fs := replayFixtureFS(t)
	config := replayFixtureConfig(t, gameplay.Modern)
	viewed := replayFixtureBattle(t, fs, config, false)
	twin := replayFixtureBattle(t, fs, config, false)
	perspectives := []PresentationPerspective{
		{Owner: 1, Override: true},
		{RevealAll: true},
		{Owner: 1, Override: true, RevealAll: true},
		{Owner: 0, Override: true},
		{},
	}
	spawn := HumanCommand{Kind: HumanDeveloperSpawn, DeveloperSpawn: HumanDeveloperSpawnCommand{Pattern: "portscout", X: 140 << 16, Y: 20 << 16, Z: 160 << 16}}
	for i := range 40 {
		for _, s := range []*Session{viewed, twin} {
			own := replayUnits(s, s.LocalOwner)
			var batch []HumanCommand
			switch i {
			case 0:
				batch = []HumanCommand{spawn, spawn}
			case 3, 17, 29:
				batch = []HumanCommand{{Kind: HumanOrder, Order: HumanOrderCommand{Handles: own, Code: 2, Position: replayPoint(int32(60+i*10), int32(400-i*8))}}}
			case 9:
				// A death the viewing player sees: temporary sight, which
				// the viewing owner decides.
				s.Units.Destroy(own[len(own)-1], units.DeathKilled)
			case 12:
				batch = []HumanCommand{{Kind: HumanVisibility, Visibility: HumanVisibilityCommand{ToggleMask: visibility.ModeCurrentEnabled}}}
			}
			for _, c := range batch {
				if err := s.EnqueueHumanCommand(c); err != nil {
					t.Fatal(err)
				}
			}
			replayRequestCheckpoint(t, s)
		}
		if i%3 == 0 {
			p := perspectives[(i/3)%len(perspectives)]
			if err := viewed.SetPresentationPerspective(p); err != nil {
				t.Fatal(err)
			}
			if !viewed.RepublishPresentation() {
				t.Fatal("the committed tick was not republished")
			}
			replayExpectPerspective(t, viewed, p)
		}
		viewed.ExecuteStep(StepPlan{run: true, ticks: 1 + i%3})
		twin.ExecuteStep(StepPlan{run: true, ticks: 1 + i%3})
		replayCompare(t, "a perspective change", viewed, twin)
		if viewed.LocalOwner != twin.LocalOwner || viewed.ViewingOwner != twin.ViewingOwner || viewed.ViewingOwner != 0 {
			t.Fatal("the perspective moved a simulation owner")
		}
		p := viewed.seatCommands.replay.perspective
		replayExpectPerspective(t, viewed, p)
	}
	replayCompareHistories(t, viewed, twin)
	if err := viewed.SetPresentationPerspective(PresentationPerspective{Owner: 10, Override: true}); err == nil {
		t.Fatal("a perspective of player 11 was accepted")
	}
}

// replayExpectPerspective checks the committed frame draws the perspective.
func replayExpectPerspective(t *testing.T, s *Session, p PresentationPerspective) {
	t.Helper()
	f := s.Snapshot.Current()
	viewer := s.ViewingOwner
	if p.Override {
		viewer = p.Owner
	}
	if f.ViewingPlayer != viewer {
		t.Fatalf("frame viewer %d, want %d", f.ViewingPlayer, viewer)
	}
	if p.RevealAll {
		if f.Fog.Valid || !f.Visibility.Valid || len(f.Visibility.Visible) == 0 {
			t.Fatal("a revealed frame draws fog or no coverage")
		}
		for _, v := range f.Visibility.Visible {
			if v == 0 {
				t.Fatal("a revealed frame hides a cell")
			}
		}
		for _, u := range f.Units {
			if !u.DirectVisibilityKnown || !u.DirectlyVisible {
				t.Fatalf("a revealed frame hides unit %d", u.Slot)
			}
		}
		for _, c := range f.Radar.Contacts {
			if !c.Visible {
				t.Fatalf("a revealed frame hides contact %d", c.Handle)
			}
		}
		return
	}
	if want := s.Vis.ByteGrid(visibility.PlayerID(viewer)); s.Vis.CurrentEnabled() && string(f.Visibility.Visible) != string(want) {
		t.Fatalf("the frame publishes another player's coverage than %d's", viewer)
	}
	if viewer == s.ViewingOwner {
		if f.Fog.Valid != s.Vis.FogCacheValid() || f.Fog.Valid && f.Fog.Source != s.Vis.PresentationIdentity() {
			t.Fatalf("viewer %d's frame does not carry the fog cache", viewer)
		}
	} else {
		// Another viewer's own fog, under an identity no real fog has.
		var want visibility.FogCache
		if _, ok := s.Vis.DeriveFogFor(&want, visibility.PlayerID(viewer)); !ok || !f.Fog.Valid || f.Fog.Version == 0 || f.Fog.Source != ^s.Vis.PresentationIdentity() {
			t.Fatalf("viewer %d's frame carries no fog of its own", viewer)
		}
		if ch0, ch1 := want.CopyChannelsInto(nil, nil); !bytes.Equal(f.Fog.Ch0, ch0) || !bytes.Equal(f.Fog.Ch1, ch1) {
			t.Fatalf("viewer %d's frame carries another player's fog", viewer)
		}
	}
	for _, u := range f.Units {
		real := s.Units.Unit(u.Slot)
		if real == nil {
			continue
		}
		if viewer == s.ViewingOwner && u.Flags != s.sensorStatus(viewer, real) {
			t.Fatalf("unit %d publishes another status word than the simulation's", u.Slot)
		}
		if u.Owner != viewer && u.DirectlyVisible != s.Vis.IsVisible(visibility.PlayerID(viewer), unitVisibilityTarget(real, u.Flags)) {
			t.Fatalf("unit %d's visibility is not player %d's", u.Slot, viewer)
		}
	}
}

// A playback watching the computer seat draws that seat's own fog and
// contacts and still reproduces its recording exactly: played with and
// without the override, the recording has the same unit checksum, random
// streams and canonical checkpoint after every pump, and the visibility
// service's own fog cache and sensor diagnostics stay the viewing owner's
// (DESIGN_MULTIPLAYER §10). The fixture's computer has no radar, so its
// contacts are its own units, friendly, and the human units in its line of
// sight: a scout spawned beside its commander is one, the human commander
// across the map is not — and the human's own frames show both as friendly.
func TestPresentationPerspectiveDrawsTheComputerSeat(t *testing.T) {
	fs := replayFixtureFS(t)
	config := replayFixtureConfig(t, gameplay.Modern)
	recorded := replayFixtureBattle(t, fs, config, false)
	tape := &replayTape{t: t}
	if err := recorded.SetReplayRecorder(tape); err != nil {
		t.Fatal(err)
	}
	spawn := func(x, z int32) HumanCommand {
		return HumanCommand{Kind: HumanDeveloperSpawn, DeveloperSpawn: HumanDeveloperSpawnCommand{Pattern: "portscout", X: numeric.Fixed(x) << 16, Y: 20 << 16, Z: numeric.Fixed(z) << 16}}
	}
	for i := range 60 {
		var batch []HumanCommand
		switch i {
		case 0:
			batch = []HumanCommand{spawn(400, 315), spawn(140, 160)}
		case 20, 41:
			// The commander, created first, walks about its own corner.
			own := replayUnits(recorded, recorded.LocalOwner)
			batch = []HumanCommand{{Kind: HumanOrder, Order: HumanOrderCommand{Handles: own[:1], Code: 2, Position: replayPoint(int32(90+i), int32(150-i))}}}
		}
		for _, c := range batch {
			if err := recorded.EnqueueHumanCommand(c); err != nil {
				t.Fatal(err)
			}
		}
		recorded.ExecuteStep(StepPlan{run: true, ticks: 1 + i%3})
	}
	if len(tape.failures) != 0 || len(tape.commands) == 0 {
		t.Fatalf("recording failed: %v, %d commands", tape.failures, len(tape.commands))
	}

	const computer = 1
	viewing := PresentationPerspective{Owner: computer, Override: true}
	plain := replayFixtureBattle(t, fs, config, true)
	viewed := replayFixtureBattle(t, fs, config, true)
	if err := viewed.SetPresentationPerspective(viewing); err != nil {
		t.Fatal(err)
	}
	if !viewed.RepublishPresentation() {
		t.Fatal("the committed tick was not republished")
	}
	replayExpectPerspective(t, viewed, viewing)
	next, exact := 0, 0
	var seen, unseen, fogDiffers bool
	for i, p := range tape.pumps {
		end := next
		for end < len(tape.commands) && tape.commands[end].stamp.Tick <= p.lastTick {
			end++
		}
		for _, s := range []*Session{plain, viewed} {
			for _, e := range tape.commands[next:end] {
				if err := s.EnqueueSeatCommand(e.stamp, e.command); err != nil {
					t.Fatal(err)
				}
			}
			replayRequestCheckpoint(t, s)
			if err := s.StepRecordedPump(p.lastTick); err != nil {
				t.Fatal(err)
			}
		}
		next = end
		if plain.UnitStateChecksum() != viewed.UnitStateChecksum() {
			t.Fatalf("pump %d: unit checksums differ at tick %d", i, p.lastTick)
		}
		replayCompare(t, "the computer's view", plain, viewed)
		ch0, ch1 := viewed.Vis.Fog().CopyChannelsInto(nil, nil)
		plain0, plain1 := plain.Vis.Fog().CopyChannelsInto(nil, nil)
		if viewed.Vis.FogVersion() != plain.Vis.FogVersion() || !bytes.Equal(ch0, plain0) || !bytes.Equal(ch1, plain1) ||
			viewed.Vis.FogCacheValid() != plain.Vis.FogCacheValid() || !reflect.DeepEqual(viewed.Vis.SensorInputs(), plain.Vis.SensorInputs()) {
			t.Fatalf("pump %d: the computer's view changed the service's fog cache or sensor diagnostics", i)
		}
		replayExpectPerspective(t, plain, PresentationPerspective{})
		replayExpectPerspective(t, viewed, viewing)
		v := &viewed.unitChannel.perspective
		if !v.sensorsValid || v.sensorViewer != computer || v.sensorsAt != v.realPasses {
			t.Fatalf("pump %d: the computer's bank is not current with the real passes", i)
		}
		if i%9 == 4 {
			// Derive the bank afresh from this tick's state, as a view
			// change while paused does, so it can be checked exactly.
			v.sensorsValid = false
			if !viewed.RepublishPresentation() {
				t.Fatal("the committed tick was not republished")
			}
			exact++
		}
		fogDiffers = fogDiffers || !bytes.Equal(viewed.Snapshot.Current().Fog.Ch0, plain.Snapshot.Current().Fog.Ch0) ||
			!bytes.Equal(viewed.Snapshot.Current().Fog.Ch1, plain.Snapshot.Current().Fog.Ch1)
		ownContacts := map[pool.Handle]frame.RadarContactView{}
		for _, c := range plain.Snapshot.Current().Radar.Contacts {
			if c.Kind == frame.RadarContactUnit {
				ownContacts[c.Handle] = c
			}
		}
		for _, c := range viewed.Snapshot.Current().Radar.Contacts {
			if c.Kind != frame.RadarContactUnit {
				continue
			}
			real := viewed.Units.Unit(c.Handle)
			if real == nil {
				t.Fatalf("pump %d: contact %d has no unit", i, c.Handle)
			}
			const sensorBits = visibility.FriendlyMask | visibility.JammedBit | visibility.DecloakBit
			if c.Status&^sensorBits != real.Flags&^units.SelectedStatus&^sensorBits {
				t.Fatalf("pump %d: contact %d's other status bits are not the unit's", i, c.Handle)
			}
			if c.Owner == computer {
				if !c.Friendly || !c.Visible {
					t.Fatalf("pump %d: the computer's own unit %d is not a friendly contact", i, c.Handle)
				}
				continue
			}
			if c.Status&visibility.SonarBit != 0 || c.Friendly != c.Seen || c.Visible != c.Seen {
				t.Fatalf("pump %d: human unit %d reads %#x, which no radar-less computer pass leaves", i, c.Handle, c.Status)
			}
			if mine := ownContacts[c.Handle]; !mine.Friendly || !mine.Visible {
				t.Fatalf("pump %d: the human's own frame does not show its unit %d friendly", i, c.Handle)
			}
			if i%9 != 4 {
				continue
			}
			want := !real.Hidden && viewed.Vis.VisiblePoint(computer, real.X, real.Y, real.Z)
			if c.Seen != want {
				t.Fatalf("pump %d: human unit %d seen %v, the computer's line of sight says %v", i, c.Handle, c.Seen, want)
			}
			seen, unseen = seen || want, unseen || !want
		}
	}
	if exact == 0 || !seen || !unseen || !fogDiffers || viewed.unitChannel.perspective.realPasses == 0 {
		t.Fatalf("exact checks %d, seen %v, unseen %v, fog differs %v, real passes %d: the fixture proves nothing",
			exact, seen, unseen, fogDiffers, viewed.unitChannel.perspective.realPasses)
	}
	if p := &plain.unitChannel.perspective; p.sensorsValid || p.fogVersion != 0 {
		t.Fatal("a playback watching the recorded seat derived another viewer's picture")
	}
}
