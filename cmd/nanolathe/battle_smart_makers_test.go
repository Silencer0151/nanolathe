package main

import (
	"image"
	"math"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

func smartPlanFixture() (*frame.Frame, *content.Catalog, frame.EconomyView) {
	cat := &content.Catalog{Units: map[string]*content.UnitDef{
		"small": {MakesMetal: 1, EnergyUse: 60, OnOffable: true},
		"moho":  {MakesMetal: 16, EnergyUse: 600, OnOffable: true},
	}}
	f := &frame.Frame{Units: []frame.UnitView{
		{Slot: 1, AllocationSerial: 11, DefName: "small"},
		{Slot: 2, AllocationSerial: 22, DefName: "moho"},
		{Slot: 3, AllocationSerial: 33, DefName: "small"},
	}}
	return f, cat, frame.EconomyView{Player: 0, Active: true, Energy: 1000, EnergyCapacity: 1000, ActivityTick: 30}
}

func planHandles(plan []smartMakerAction) []pool.Handle {
	var handles []pool.Handle
	for _, action := range plan {
		handles = append(handles, action.unit.Handle)
	}
	return handles
}

func TestSmartMakerBudgetAndHysteresis(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		stock, production, demand float32
		on                        bool
		want                      []pool.Handle
	}{
		{"high budget buys small after moho will not fit", 1000, 0, 0, true, []pool.Handle{1, 3}},
		{"surplus buys moho first", 1000, 600, 0, true, []pool.Handle{2, 1, 3}},
		{"exact upper boundary uses surplus", 800, 60, 0, true, []pool.Handle{1}},
		{"upper boundary without surplus", 800, 0, 0, true, nil},
		{"below upper boundary", 799, 1000, 0, true, nil},
		{"lower boundary holds", 400, 0, 1000, false, nil},
		{"shed least efficient first", 300, 0, 0, false, []pool.Handle{3, 1}},
		{"shed moho only after small makers", 300, 0, 100, false, []pool.Handle{3, 1, 2}},
		{"recovery covers deficit", 300, 100, 0, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cat, e := smartPlanFixture()
			e.Energy, e.EnergyProduced, e.EnergyConsumed = tc.stock, tc.production, tc.demand
			for i := range f.Units {
				f.Units[i].Activated = !tc.on
			}
			before := slices.Clone(f.Units)
			plan := smartMakerPlan(f, cat, e)
			if !slices.Equal(planHandles(plan), tc.want) {
				t.Fatalf("plan %v, want %v", planHandles(plan), tc.want)
			}
			for _, a := range plan {
				if a.on != tc.on {
					t.Fatal("inverted activation")
				}
			}
			for i, u := range f.Units {
				if u.Activated != before[i].Activated {
					t.Fatal("plan mutated committed frame")
				}
			}
		})
	}
}

func TestSmartMakerExclusionsAndAllocationOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*frame.Frame, *content.Catalog, *frame.EconomyView)
	}{
		{"other owner", func(f *frame.Frame, _ *content.Catalog, _ *frame.EconomyView) {
			for i := range f.Units {
				f.Units[i].Owner = 1
			}
		}},
		{"unfinished", func(f *frame.Frame, _ *content.Catalog, _ *frame.EconomyView) {
			for i := range f.Units {
				f.Units[i].BuildRemaining = 0.5
			}
		}},
		{"extractors", func(_ *frame.Frame, c *content.Catalog, _ *frame.EconomyView) {
			c.Units["small"].ExtractsMetal = 1
			c.Units["moho"].ExtractsMetal = 1
		}},
		{"unswitchable", func(_ *frame.Frame, c *content.Catalog, _ *frame.EconomyView) {
			c.Units["small"].OnOffable = false
			c.Units["moho"].OnOffable = false
		}},
		{"mobile", func(_ *frame.Frame, c *content.Catalog, _ *frame.EconomyView) {
			c.Units["small"].BMCode = 1
			c.Units["moho"].BMCode = 1
		}},
		{"no storage", func(_ *frame.Frame, _ *content.Catalog, e *frame.EconomyView) { e.EnergyCapacity = 0 }},
		{"nonfinite", func(_ *frame.Frame, _ *content.Catalog, e *frame.EconomyView) { e.Energy = float32(math.NaN()) }},
		{"nonfinite demand", func(_ *frame.Frame, _ *content.Catalog, e *frame.EconomyView) {
			e.EnergyConsumed = float32(math.Inf(1))
		}},
		{"inactive", func(_ *frame.Frame, _ *content.Catalog, e *frame.EconomyView) { e.Active = false }},
		{"upkeep hold", func(f *frame.Frame, _ *content.Catalog, _ *frame.EconomyView) {
			f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityUpkeep)}}
		}},
		{"generation hold", func(f *frame.Frame, _ *content.Catalog, _ *frame.EconomyView) {
			f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityGeneration)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, e := smartPlanFixture()
			tc.change(f, c, &e)
			if plan := smartMakerPlan(f, c, e); len(plan) != 0 {
				t.Fatalf("excluded case emitted %v", plan)
			}
		})
	}
	f, c, e := smartPlanFixture()
	slices.Reverse(f.Units)
	if got := planHandles(smartMakerPlan(f, c, e)); !slices.Equal(got, []pool.Handle{1, 3}) {
		t.Fatalf("presentation order changed ties: %v", got)
	}
	f.EconomyHolds = []frame.EconomyHoldView{{Kind: uint8(economy.ActivityGeneration)}}
	e.Energy = 300
	for i := range f.Units {
		f.Units[i].Activated = true
	}
	if got := planHandles(smartMakerPlan(f, c, e)); !slices.Equal(got, []pool.Handle{3, 1}) {
		t.Fatalf("generation hold suppressed shedding: %v", got)
	}
}

func TestSmartMakerToggleModesCaptureAndScaling(t *testing.T) {
	for _, mode := range []gameplay.Mode{gameplay.Modern, gameplay.Strict31, gameplay.Community39} {
		t.Run(string(mode), func(t *testing.T) {
			b, cl := economyPanelFixture(t)
			b.sess.Gameplay = mode
			if b.smartMakersEnabled() != (mode == gameplay.Modern) {
				t.Fatal("wrong mode default")
			}
			f, _ := b.currentSnapshot()
			// Smart is independent of Modern-only activity hold authorization.
			f.EconomyHoldsAllowed = false
			b.economyPanel = economyPanelState{open: true, resource: economy.Energy}
			for _, size := range [][3]int{{640, 480, 1}, {640, 480, 2}, {1280, 720, 2}, {1920, 1080, 2}} {
				cl.Resize(size[0], size[1])
				b.chromeK = int32(size[2])
				for i, row := range economyReport(f, economy.Energy).rows {
					if row.activity != economy.ActivityUpkeep {
						continue
					}
					r := economySmartRect(economyPanelRowRect(b.economyPanelRect(), i))
					p := image.Pt(r.Min.X+5, r.Min.Y+5)
					x, y := int32(p.X)*b.economyPanelRegion().Scale, int32(p.Y)*b.economyPanelRegion().Scale
					if hit := b.economyPanelHit(f, x, y); hit.kind != 6 {
						t.Fatalf("Smart hit %v: %+v", size, hit)
					}
					before := b.smartMakersEnabled()
					economyPointer(b, cl, x, y, "press")
					economyPointer(b, cl, x, y, "release")
					if b.smartMakersEnabled() == before || !b.economyPanel.open || len(b.sess.PendingHumanCommands()) != 0 {
						t.Fatal("toggle navigated, closed or sent simulation config")
					}
				}
			}
			before := b.smartMakersEnabled()
			b.activateEconomyPanel(f, economyPanelHit{kind: 2})
			b.activateEconomyPanel(f, economyPanelHit{kind: 1, resource: economy.Energy})
			if b.smartMakersEnabled() != before {
				t.Fatal("closing dropdown reset preference")
			}
			b.playback = &replayPlayback{}
			b.activateEconomyPanel(f, economyPanelHit{kind: 6, held: before})
			if b.smartMakersEnabled() != before {
				t.Fatal("playback accepted a toggle")
			}
		})
	}
}

func TestSmartMakerServiceOrdinaryCommandsOncePerSettlement(t *testing.T) {
	b, cl := economyPanelFixture(t)
	b.sess.Gameplay = gameplay.Modern
	b.cl = cl
	f, _ := b.currentSnapshot()
	f.Units[0].DefName = "testmaker"
	f.Units[0].Activated = false
	b.cat.Units["testmaker"] = &content.UnitDef{MakesMetal: 1, EnergyUse: 60, OnOffable: true}
	f.Economy = []frame.EconomyView{{Player: 0, Active: true, Energy: 1000, EnergyCapacity: 1000, ActivityTick: 30}}
	// Controller only proposes; the committed activation bit cannot change.
	b.serviceSmartMakers()
	commands := b.sess.PendingHumanCommands()
	if len(commands) != 1 || commands[0].Kind != session.HumanActivation || !commands[0].Activation.Activate || commands[0].Activation.Unit != f.Units[0].Slot || f.Units[0].Activated {
		t.Fatalf("not an ordinary activation proposal: %+v", commands)
	}
	for range 100 {
		b.serviceSmartMakers()
	}
	if len(b.sess.PendingHumanCommands()) != 1 {
		t.Fatal("repeated the same settlement")
	}
	// Later settlement permits another decision, not a per-host-frame loop.
	f.Economy[0].ActivityTick = 60
	b.serviceSmartMakers()
	if len(b.sess.PendingHumanCommands()) != 2 {
		t.Fatal("new settlement was not observed")
	}
	for _, guard := range []string{"other viewer", "watcher", "replay", "original", "paused", "off", "preview"} {
		t.Run(guard, func(t *testing.T) {
			f.Economy[0].ActivityTick++
			switch guard {
			case "other viewer":
				f.ViewingPlayer = 1
				defer func() { f.ViewingPlayer = 0 }()
			case "watcher":
				f.Players[0].Watcher = true
				defer func() { f.Players[0].Watcher = false }()
			case "replay":
				b.playback = &replayPlayback{}
				defer func() { b.playback = nil }()
			case "original":
				cl.SetEnhanced(false)
				defer cl.SetEnhanced(true)
			case "paused":
				b.sess.Clock.Paused = true
				defer func() { b.sess.Clock.Paused = false }()
			case "off":
				b.smartMakers.enabled = false
				defer func() { b.smartMakers.enabled = true }()
			case "preview":
				b.preview = true
				defer func() { b.preview = false }()
			}
			before := len(b.sess.PendingHumanCommands())
			b.serviceSmartMakers()
			if len(b.sess.PendingHumanCommands()) != before {
				t.Fatal("read-only host emitted commands")
			}
		})
	}
}

func TestSmartMakerServiceBoundsLargeFleet(t *testing.T) {
	b, cl := economyPanelFixture(t)
	b.cl = cl
	b.sess.Gameplay = gameplay.Modern
	f, _ := b.currentSnapshot()
	first := f.Units[0]
	f.Units = nil
	b.cat.Units["testmaker"] = &content.UnitDef{MakesMetal: 1, EnergyUse: 1, OnOffable: true}
	for i := 1; i <= 100; i++ {
		u := first
		u.Slot = pool.Handle(i)
		u.AllocationSerial = uint64(i)
		u.DefName = "testmaker"
		u.Activated = false
		f.Units = append(f.Units, u)
	}
	f.Economy = []frame.EconomyView{{Player: 0, Active: true, Energy: 1000, EnergyCapacity: 1000, ActivityTick: 30}}
	b.serviceSmartMakers()
	b.serviceSmartMakers()
	if n := len(b.sess.PendingHumanCommands()); n != 16 {
		t.Fatalf("unbounded relay burst: %d", n)
	}
}

func TestSmartMakerOnlineUsesOwnerCommandStream(t *testing.T) {
	b, cl := economyPanelFixture(t)
	b.cl, b.sess.Gameplay = cl, gameplay.Modern
	f, _ := b.currentSnapshot()
	f.Units[0].DefName = "testmaker"
	f.Units[0].Activated = false
	b.cat.Units["testmaker"] = &content.UnitDef{MakesMetal: 1, EnergyUse: 60, OnOffable: true}
	f.Economy = []frame.EconomyView{{Player: 0, Active: true, Energy: 1000, EnergyCapacity: 1000, ActivityTick: 30}}
	driver := &testLocalBattleDriver{}
	b.multiplayer = &battleMultiplayer{driver: driver}
	b.serviceSmartMakers()
	if len(driver.commands) != 1 || driver.commands[0].Kind != session.HumanActivation || len(b.sess.PendingHumanCommands()) != 0 || f.Units[0].Activated {
		t.Fatalf("automation bypassed relay or optimistically mutated a maker: %+v", driver.commands)
	}
	// A stalled network frame cannot resend its settlement. Viewing another
	// seat cannot issue commands, even when its economy has a newer pass.
	b.serviceSmartMakers()
	f.ViewingPlayer, f.Economy[0].Player, f.Economy[0].ActivityTick = 1, 1, 60
	b.serviceSmartMakers()
	if len(driver.commands) != 1 {
		t.Fatal("stalled or foreign-view frame submitted commands")
	}
}
