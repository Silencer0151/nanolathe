//go:build retail

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// This is a running retail-asset battle, not a fabricated EconomyView. Storage,
// production, billing and activation come from the real session. Optional PNGs
// are the production Enhanced HUD and world rendered from those same frames.
func TestRetailSmartMakersHUD(t *testing.T) {
	opts, err := parseFlags([]string{"--root=" + testsupport.RetailRoot(t), "--map=ashap plateau", "--seed=1234", "--gameplay=modern"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := openContent(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	s, cat, err := newBattleSession(opts, cs)
	if err != nil {
		t.Fatal(err)
	}
	driver := int32(1 << 20)
	step := func() { s.Step(driver); driver++ }
	for range 60 {
		if s.State == session.StateBattle {
			break
		}
		step()
	}
	var commander *units.Unit
	for _, u := range s.Units.IterSliced() {
		if u != nil && u.Def != nil && u.Def.Commander && u.Owner == s.LocalOwner {
			commander = u
			break
		}
	}
	if commander == nil {
		t.Fatal("no local commander")
	}
	create := func(key string, dx, dz int32) *units.Unit {
		d, ok := cat.Unit(key)
		if !ok {
			t.Fatalf("missing stock unit %s", key)
		}
		x, z := commander.X+world.CellToWorld(dx), commander.Z+world.CellToWorld(dz)
		u, err := s.Units.Create(d, s.LocalOwner, x, s.World.HeightAt(x, z), z)
		if err != nil {
			t.Fatal(err)
		}
		s.CompleteUnit(u)
		return s.Units.Unit(u)
	}
	create("armestor", 12, -12)
	create("armestor", 18, -12)
	create("armfus", 12, 12)
	mohos := []*units.Unit{create("armmmkr", -12, -12), create("armmmkr", -18, -12)}
	makers := append([]*units.Unit(nil), mohos...)
	for i := int32(0); i < 4; i++ {
		makers = append(makers, create("armmakr", -18+6*i, 14))
	}
	for _, u := range makers {
		if u.COBBinding() == nil {
			t.Fatal("maker lacks production script")
		}
		u.SetActivationEdge(false)
	}
	cl, err := client.New(client.Options{Buffer: s.Snapshot, Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	cl.SetModelFS(cs.unmappedMount)
	b, err := composeBattleEntry(s, cat, cs, cl, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.teardown(cl)
	cl.SetEnhanced(true)
	cl.SetFocused(true)
	b.chromeFixed = true
	b.cam.JumpToBattleViewCenter(int32(commander.X.Int()), int32(commander.Z.Int()))
	p := &s.Econ.Players[s.LocalOwner]
	// Count both new storage buildings before the free-running experiment.
	for passes := 0; passes < 2; {
		deadline := p.UpdateTime
		p.Stock[economy.Energy] = max(p.Capacity[economy.Energy], 7000)
		step()
		if p.UpdateTime != deadline {
			passes++
		}
	}
	if !b.smartMakersEnabled() {
		t.Fatal("Modern did not default to Smart on")
	}
	b.serviceSmartMakers()
	pending := s.PendingHumanCommands()
	if len(pending) < 2 || pending[0].Kind != session.HumanActivation || pending[0].Activation.Unit != mohos[0].Handle || pending[1].Activation.Unit != mohos[1].Handle {
		t.Fatalf("Moho efficiency ordering: %+v", pending)
	}
	// Planning did not write live activation bits. Normal order dispatch does.
	if mohos[0].Activated || mohos[1].Activated {
		t.Fatal("host mutated a live maker")
	}
	step()
	if !mohos[0].Activated || !mohos[1].Activated {
		t.Fatal("ordinary activation commands did not run")
	}
	b.commitSelection([]pool.Handle{mohos[0].Handle}, false)
	dir := os.Getenv("NANOLATHE_SMART_CAPTURE_DIR")
	shot := func(name string) {
		if dir == "" {
			return
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		b.economyPanel = economyPanelState{open: true, resource: economy.Energy, viewer: s.LocalOwner}
		// Let the HUD's stock-counter easing converge as it would over live
		// presentations. This does not advance or alter the simulation.
		for range 64 {
			cl.BeginPresentationFrame()
			cl.ComposeFrame()
		}
		cl.BeginPresentationFrame()
		shareShotPNG(t, filepath.Join(dir, name+".png"), cl.ComposeFrame())
	}
	shot("01-smart-on-high-energy")
	capacity, lowest := p.Capacity[economy.Energy], p.Stock[economy.Energy]
	metal := p.TotalProduced[economy.Metal]
	previous := make([]bool, len(makers))
	for i, u := range makers {
		previous[i] = u.Activated
	}
	switches, shed := 0, false
	for range 2400 {
		b.serviceSmartMakers()
		step()
		lowest = min(lowest, p.Stock[economy.Energy])
		for i, u := range makers {
			if u.Activated != previous[i] {
				switches++
			}
			if i < 2 && previous[i] && !u.Activated {
				for _, small := range makers[2:] {
					if small.Activated {
						t.Fatal("Moho shed before inefficient small makers")
					}
				}
				if !shed {
					shot("02-smart-on-low-energy-shedding")
					shed = true
				}
			}
			previous[i] = u.Activated
		}
	}
	if !shed || switches == 0 || p.TotalProduced[economy.Metal] <= metal || lowest <= 0 || lowest < capacity*2/5-1840 {
		t.Fatalf("free run: shed=%t switches=%d lowest=%v/%v metal made=%v", shed, switches, lowest, capacity, p.TotalProduced[economy.Metal]-metal)
	}
	// Exercise the actual Smart control, then prove an observed deficit no
	// longer results in activation commands. The dropdown can be closed too.
	f, _ := b.currentSnapshot()
	b.economyPanel = economyPanelState{open: true, resource: economy.Energy, viewer: s.LocalOwner}
	for i, row := range economyReport(f, economy.Energy).rows {
		if row.activity == economy.ActivityUpkeep {
			r := economySmartRect(economyPanelRowRect(b.economyPanelRect(), i))
			economyPointer(b, cl, int32(r.Min.X+5), int32(r.Min.Y+5), "press")
			economyPointer(b, cl, int32(r.Min.X+5), int32(r.Min.Y+5), "release")
		}
	}
	if b.smartMakersEnabled() {
		t.Fatal("Smart button did not turn assistance off")
	}
	shot("03-smart-off-manual-control")
	b.economyPanel.open = false
	for range 60 {
		b.serviceSmartMakers()
		if len(s.PendingHumanCommands()) != 0 {
			t.Fatal("disabled Smart submitted commands")
		}
		step()
	}
	t.Logf("capacity=%v lowest=%v switches=%d metal made=%v; all changes through ordinary commands", capacity, lowest, switches, p.TotalProduced[economy.Metal]-metal)
}
