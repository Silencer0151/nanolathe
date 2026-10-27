package main

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

type smartAckDriver struct {
	testLocalBattleDriver
	applied uint64
}

func (d *smartAckDriver) AppliedLocalSequence() uint64 { return d.applied }

func TestSmartMakerControllerSubmitsBeforeTick(t *testing.T) {
	b, cl, f := smartServiceFixture(t)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	c := NewBattleController(b, &scriptedMillisSource{samples: []uint32{0, 0}})
	c.Step(BattleInputFrame{}, cl)
	if len(d.commands) != 1 || d.commands[0].Kind != session.HumanActivation || f.Units[0].Activated {
		t.Fatal("controller did not submit an ordinary activation from its committed frame")
	}
}
func smartServiceFixture(t *testing.T) (*battleSession, *client.Client, *frame.Frame) {
	t.Helper()
	b, cl := economyPanelFixture(t)
	b.cl, b.sess.Gameplay = cl, gameplay.Modern
	f, _ := b.currentSnapshot()
	b.cat.Units["testmaker"] = &content.UnitDef{MakesMetal: 1, EnergyUse: 1, OnOffable: true}
	f.Units[0].DefName, f.Units[0].Activated = "testmaker", false
	f.Economy = []frame.EconomyView{{Player: 0, Active: true, Energy: 1000, EnergyCapacity: 1000, ActivityTick: 30}}
	return b, cl, f
}

func TestSmartMakerTenSeatsBoundOutstandingGrant(t *testing.T) {
	// The relay has ONE 64-entry queue for the whole room. Ten full fleets
	// must not issue ten independent bursts, even across stalled settlements.
	total := 0
	for range 10 {
		b, _, f := smartServiceFixture(t)
		first := f.Units[0]
		f.Units = nil
		for i := 1; i <= 100; i++ {
			u := first
			u.Slot = pool.Handle(i)
			u.AllocationSerial = uint64(i)
			f.Units = append(f.Units, u)
		}
		d := &smartAckDriver{}
		b.multiplayer = &battleMultiplayer{driver: d}
		for i := 0; i < 100; i++ {
			f.Economy[0].ActivityTick++
			f.Tick++
			b.serviceSmartMakers()
			b.economyPanel.resource = economy.Energy
			b.activateEconomyPanel(f, economyPanelHit{kind: 6, held: true})
			b.activateEconomyPanel(f, economyPanelHit{kind: 6, held: false})
		}
		if len(d.commands) != 1 {
			t.Fatalf("unacknowledged seat issued %d commands", len(d.commands))
		}
		total += len(d.commands)
		// An applied local sequence, not an unrelated global position or
		// elapsed time, permits the next ordinary command from this plan.
		f.Units[0].Activated = true
		d.applied = 1
		b.serviceSmartMakers()
		if len(d.commands) != 2 || d.commands[1].Activation.Unit != 2 {
			t.Fatalf("ack did not release next maker: %+v", d.commands)
		}
	}
	if total != 10 {
		t.Fatalf("room-wide automation backlog=%d, want ten with 54 entries spare", total)
	}
}

func TestSmartMakerPendingPauseAndDelayedEcho(t *testing.T) {
	for _, activity := range []economy.Activity{economy.ActivityUpkeep, economy.ActivityGeneration} {
		b, _, f := smartServiceFixture(t)
		d := &smartAckDriver{}
		b.multiplayer = &battleMultiplayer{driver: d}
		b.economyPanel.resource = economy.Energy
		b.activateEconomyPanel(f, economyPanelHit{kind: 4, activity: activity})
		for range 10 {
			b.serviceSmartMakers()
			f.Tick++
			f.Economy[0].ActivityTick++
		}
		if len(d.commands) != 1 || d.commands[0].Kind != session.HumanEconomyActivity {
			t.Fatal("pending pause was followed by an automatic ON")
		}
		f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(activity)}}
		d.applied = 1
		b.serviceSmartMakers()
		if len(d.commands) != 1 {
			t.Fatal("committed pause was overridden")
		}
	}
}

func TestSmartMakerInFlightOnCannotOutliveUpkeepPause(t *testing.T) {
	b, _, f := smartServiceFixture(t)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	b.serviceSmartMakers() // ON already in flight when the human pauses.
	if len(d.commands) != 1 || !d.commands[0].Activation.Activate {
		t.Fatal("missing initial ON")
	}
	if err := b.enqueueHumanCommand(session.HumanCommand{Kind: session.HumanEconomyActivity, EconomyActivity: session.HumanEconomyActivityCommand{Activity: economy.ActivityUpkeep, Pause: true}}); err != nil {
		t.Fatal(err)
	}
	// Both requests have now run, but the old ON's queued order visited
	// after the hold. Only this assistant's own ON needs a corrective OFF.
	f.Tick++
	f.Units[0].Activated = true
	f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityUpkeep)}}
	d.applied = 2
	b.serviceSmartMakers()
	if len(d.commands) != 3 || d.commands[2].Kind != session.HumanActivation || d.commands[2].Activation.Activate {
		t.Fatalf("in-flight ON escaped pause: %+v", d.commands)
	}
	for range 100 {
		b.serviceSmartMakers()
	}
	if len(d.commands) != 3 {
		t.Fatal("repeated correction before acknowledgement")
	}
}

func TestSmartMakerRunsBehindOnlineOptions(t *testing.T) {
	b, cl, f := smartServiceFixture(t)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	b.openBattleMenu()
	b.viewerStep(0, cl)
	if len(d.commands) != 1 || d.commands[0].Kind != session.HumanActivation {
		t.Fatal("non-pausing online options suspended Smart")
	}
	if b.sess.Clock.Paused || f.Units[0].Activated {
		t.Fatal("modal test paused or optimistically activated")
	}
}

func TestSmartMakerStalePlanNeverTargetsReusedOrCapturedUnit(t *testing.T) {
	b, _, f := smartServiceFixture(t)
	first := f.Units[0]
	first.Slot = 40
	first.AllocationSerial = 400
	f.Units = append(f.Units, first)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	b.serviceSmartMakers()
	f.Units[0].Activated = true
	d.applied = 1
	f.Units[len(f.Units)-1].AllocationSerial++
	b.serviceSmartMakers()
	if len(d.commands) != 1 {
		t.Fatal("remaining plan targeted a replacement allocation")
	}
}

func TestSmartMakerWaitsForPauseNotJustItsOwnOn(t *testing.T) {
	b, _, f := smartServiceFixture(t)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	b.serviceSmartMakers()
	f.Tick++
	f.Units[0].Activated = true
	d.applied = 1
	if err := b.enqueueHumanCommand(session.HumanCommand{Kind: session.HumanEconomyActivity, EconomyActivity: session.HumanEconomyActivityCommand{Activity: economy.ActivityUpkeep, Pause: true}}); err != nil {
		t.Fatal(err)
	}
	b.serviceSmartMakers()
	if len(d.commands) != 2 {
		t.Fatal("corrected before the pause's grant; this OFF would erase its restoration record")
	}
	// The later pause correctly sees the ON and remembers it for resume.
	f.Tick++
	f.Units[0].Activated = false
	f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityUpkeep)}}
	d.applied = 2
	b.serviceSmartMakers()
	if len(d.commands) != 2 {
		t.Fatal("sent an unnecessary OFF after a correctly applied pause")
	}
}

func TestSmartMakerManualActivationRetiresCorrectionButNotTransportCredit(t *testing.T) {
	b, _, f := smartServiceFixture(t)
	d := &smartAckDriver{}
	b.multiplayer = &battleMultiplayer{driver: d}
	b.serviceSmartMakers()
	if err := b.enqueueHumanCommand(session.HumanCommand{Kind: session.HumanEconomyActivity, EconomyActivity: session.HumanEconomyActivityCommand{Activity: economy.ActivityUpkeep, Pause: true}}); err != nil {
		t.Fatal(err)
	}
	if err := b.DispatchActivation(session.HumanActivationCommand{Unit: f.Units[0].Slot, Activate: true}); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		b.serviceSmartMakers()
	}
	if len(d.commands) != 3 {
		t.Fatal("manual override released outstanding Smart transport credit")
	}
	// This ON is now the human's explicit override, not Smart's to undo.
	f.Tick++
	f.Units[0].Activated = true
	d.applied = 3
	f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityUpkeep)}}
	b.serviceSmartMakers()
	if len(d.commands) != 3 {
		t.Fatal("Smart undid a manual ON under the hold")
	}
}
