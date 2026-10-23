package session

import (
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// A replay fork changes control, never reconstructs the world. These are
// prototype session policies, including in Strict (DESIGN_MULTIPLAYER §10.1).
func TestReplayTakeoverPreservesWorldAndRetiresOnlyChosenController(t *testing.T) {
	fs := replayFixtureFS(t)
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Modern, gameplay.Community39} {
		t.Run(string(mode), func(t *testing.T) {
			s := replayFixtureBattle(t, fs, replayFixtureConfig(t, mode), true)
			t.Cleanup(s.closeAIControllers)
			for range 12 {
				if err := s.StepRecordedPump(s.Clock.GlobalTick + 5); err != nil {
					t.Fatal(err)
				}
			}
			chosen, other := s.AI[1], s.AI[0]
			if err := s.setAIController(chosen, ai.ControllerModern); err != nil {
				t.Fatal(err)
			}
			closed, retained := &closeCountingExt{}, &closeCountingExt{}
			chosen.Ext, other.Ext = closed, retained
			beforeUnits := make(map[pool.Handle]units.Unit)
			for _, u := range s.Units.Iter() {
				beforeUnits[u.Handle] = *u
			}
			beforePlayers, sim, crt := s.Econ.Players, *s.SimRNG(), *s.CrtRNG()
			beforeClock, beforeRules := *s.Clock, s.Rules
			beforeSum := s.UnitStateChecksum()
			actor := replayUnits(s, 1)[0]
			// Read-ahead must not leak an order into the continuation, and
			// its accepted sequence must not alias a new local order.
			if err := s.EnqueueSeatCommand(CommandStamp{Seat: 1, Tick: 61, Position: 41}, moveTo([]pool.UnitRef{s.Units.Reference(actor)}, 200)); err != nil {
				t.Fatal(err)
			}
			_ = s.SetPresentationPerspective(PresentationPerspective{Override: true, RevealAll: true})
			floor, err := s.TakeOverReplay(1)
			if err != nil {
				t.Fatal(err)
			}
			if floor != 41 || len(s.pendingHuman) != 0 || s.seatCommands.replay.recorder != nil {
				t.Fatalf("future replay input survived: floor %d, queued %d", floor, len(s.pendingHuman))
			}
			if *s.SimRNG() != sim || *s.CrtRNG() != crt || *s.Clock != beforeClock || !reflect.DeepEqual(s.Rules, beforeRules) || s.UnitStateChecksum() != beforeSum {
				t.Fatal("takeover changed the world, clock, rules or a random stream")
			}
			for i := range beforePlayers {
				if s.Econ.Players[i].Stock != beforePlayers[i].Stock {
					t.Fatalf("takeover changed player %d's resources", i)
				}
			}
			for _, u := range s.Units.Iter() {
				before, after := beforeUnits[u.Handle], *u
				// The one local sensor bank is switched, with no extra pass
				// or cloak deadline write. Every other unit field survives.
				before.Flags &^= visibility.FriendlyMask | visibility.JammedBit
				after.Flags &^= visibility.FriendlyMask | visibility.JammedBit
				if before.Flags != after.Flags || before.Script != after.Script || before.Orders != after.Orders || before.Remaining != after.Remaining || before.RevealDeadline != after.RevealDeadline || !reflect.DeepEqual(before.Move, after.Move) {
					t.Fatalf("unit %d lost script, order, build, movement or cloak state", u.Handle)
				}
			}
			if closed.closed != 1 || retained.closed != 0 || s.AI[1] != chosen || s.AI[0] != other || chosen.Ext != nil || other.Ext != retained || chosen.Planner != s.Rules.Planner || s.ModernAIPlayer(1) {
				t.Fatal("takeover replaced a manager or stopped the wrong controller")
			}
			if s.LocalOwner != 1 || s.ViewingOwner != 1 || s.Econ.Players[1].ControllerState != 1 || s.seatCommands.replay.perspective != (PresentationPerspective{}) {
				t.Fatal("the chosen player did not acquire ordinary local control")
			}
			seq, err := s.EnqueueHumanCommandWithSequence(HumanCommand{Kind: HumanStop, Stop: HumanStopCommand{Handles: []pool.Handle{actor}}})
			if err != nil || seq <= floor {
				t.Fatalf("new command identity %d after %d: %v", seq, floor, err)
			}
			s.ExecuteStep(s.PrepareStep(s.Clock.ScaledAnchor + 1))
			if q := orders.QueueOfUnit(s.Units.Unit(actor)); q != nil && q.LenPrimary() != 0 {
				t.Fatal("the new human stop lost to recorded input or computer planning")
			}
			if _, err := s.TakeOverReplay(0); err == nil {
				t.Fatal("a second takeover was accepted")
			}
		})
	}
}

func TestReplayTakeoverPinsSelectedHumanAndRejectsUnplayableSeats(t *testing.T) {
	f := newSeatFixture(t, false, false)
	s := f.s
	s.State, s.Latch = StateBattle, NewEndLatch()
	if local, _ := s.triggerLocalPlayer(); local == 0 {
		t.Fatal("fixture does not exercise a later human row")
	}
	for _, owner := range []uint8{3, 4, 255} {
		if _, err := s.TakeOverReplay(owner); err == nil {
			t.Fatalf("unplayable seat %d accepted", owner)
		}
	}
	s.seatCommands.removed[1] = true
	if _, err := s.TakeOverReplay(1); err == nil {
		t.Fatal("removed seat accepted")
	}
	s.Survival = &survivalState{attacker: 2}
	if _, err := s.TakeOverReplay(2); err == nil {
		t.Fatal("scenario attacker accepted")
	}
	s.Survival = nil
	s.Econ.Players[2].GameEnded = true
	if _, err := s.TakeOverReplay(2); err == nil {
		t.Fatal("ended computer accepted")
	}
	if _, err := s.TakeOverReplay(0); err != nil {
		t.Fatal(err)
	}
	if local, ok := s.triggerLocalPlayer(); !ok || local != 0 || s.Econ.Players[1].ControllerState != 1 {
		t.Fatal("old human rows overrode the selected local owner or were deactivated")
	}
}

func TestOnlineReplayTakeoverPromotesComputerAndAcceptsLocalCommands(t *testing.T) {
	inputs, config := onlineComputerFixtureMatch(t, onlineComputerTestSetup(false))
	s, err := NewPlaytestSkirmish(inputs, config, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeAIControllers)
	if err := s.PrepareGrantedBattle(); err != nil {
		t.Fatal(err)
	}
	for tick := uint32(1); tick <= 30; tick++ {
		if err := s.StepGranted(tick); err != nil {
			t.Fatal(err)
		}
	}
	chosen, other := s.AI[3], s.AI[2]
	closed, retained := &closeCountingExt{}, &closeCountingExt{}
	chosen.Ext, other.Ext = closed, retained
	sum, sim, crt := s.UnitStateChecksum(), *s.SimRNG(), *s.CrtRNG()
	floor, err := s.TakeOverReplay(3)
	if err != nil {
		t.Fatal(err)
	}
	if s.UnitStateChecksum() != sum || *s.SimRNG() != sim || *s.CrtRNG() != crt {
		t.Fatal("online handoff changed the world or a random stream")
	}
	if !s.OnlineCommandContext() || !s.onlineResults.seats[3].present || s.onlineResults.hosted[3] != -1 || s.Vis.PerspectiveOf(3) != 3 || s.Vis.PerspectiveOf(2) != 0 {
		t.Fatal("online handoff lost the online kernel or failed to detach the chosen seat's perspective")
	}
	if closed.closed != 1 || retained.closed != 0 || s.AI[3] != chosen || s.AI[2] != other {
		t.Fatal("the wrong controller was retired")
	}
	actor := replayUnits(s, 3)[0]
	c, err := s.CaptureOnlineCommand(HumanCommand{Kind: HumanStop, Stop: HumanStopCommand{Handles: []pool.Handle{actor}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSeatCommand(CommandStamp{Seat: 3, Tick: 31, Position: floor + 1}, c); err != nil {
		t.Fatal(err)
	}
	if err := s.StepGranted(31); err != nil {
		t.Fatal(err)
	}
	rs := s.DrainCommandReceipts()
	if len(rs) != 1 || rs[0].Outcome != CommandApplied || s.LocalOwner != 3 {
		t.Fatalf("promoted computer's live command: %+v; local %d", rs, s.LocalOwner)
	}
}
