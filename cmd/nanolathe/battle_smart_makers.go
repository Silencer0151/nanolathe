package main

// Smart makers is local input assistance, not a simulation rule. It reads the
// committed economy and submits the same per-unit intent as the ON/OFF button
// (DESIGN_INTERFACE_HUD_INPUT "Smart makers input assistance").

import (
	"cmp"
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

type smartMakerAction struct {
	unit pool.UnitRef
	on   bool
}

type smartMakersState struct {
	initialized bool
	enabled     bool
	observed    bool
	lastPass    uint32
}

func (b *battleSession) smartMakersEnabled() bool {
	if b == nil || b.sess == nil {
		return false
	}
	if !b.smartMakers.initialized {
		b.smartMakers.initialized = true
		b.smartMakers.enabled = b.sess.Gameplay.Normalize() == gameplay.Modern
	}
	return b.smartMakers.enabled
}

func (b *battleSession) smartMakersCanControl(f *frame.Frame) bool {
	return b != nil && b.sess != nil && !b.preview && b.playback == nil && !b.ended && f != nil &&
		f.ViewingPlayer == b.sess.LocalOwner && !localSlotWatching(f)
}

// Preserve the original whole-energy arithmetic and efficiency ordering. Bound
// conversions so cross-products fit in int64, and refuse non-finite reports.
func smartMakerEnergy(v float32) int64 {
	return int64(numeric.TruncateFloat32ToLow32(min(float32(1<<30), max(float32(-1<<30), v))))
}

func smartMakerCandidate(d *content.UnitDef) bool {
	return d != nil && d.BMCode == 0 && uint8(d.MakesMetal) != 0 && d.ExtractsMetal == 0 &&
		d.EnergyUse > 0 && d.OnOffable
}

func smartMakerHeld(f *frame.Frame, a economy.Activity) bool {
	for _, hold := range f.EconomyHolds {
		if hold.Player == f.ViewingPlayer && hold.Kind == uint8(a) {
			return true
		}
	}
	return false
}

// The plan uses only committed observations plus immutable catalog definitions.
// Exact ties use allocation order, independent of the frame's presentation order.
func smartMakerPlan(f *frame.Frame, cat *content.Catalog, e frame.EconomyView) []smartMakerAction {
	if f == nil || cat == nil || !e.Active || e.Player != f.ViewingPlayer ||
		!economyFinite(e.Energy) || !economyFinite(e.EnergyCapacity) ||
		!economyFinite(e.EnergyProduced) || !economyFinite(e.EnergyConsumed) ||
		smartMakerHeld(f, economy.ActivityUpkeep) {
		return nil
	}
	capacity := smartMakerEnergy(e.EnergyCapacity)
	if capacity <= 0 {
		return nil
	}
	stock := smartMakerEnergy(e.Energy)
	net := smartMakerEnergy(e.EnergyProduced) - smartMakerEnergy(e.EnergyConsumed)
	var budget, need int64
	switch {
	case 5*stock >= 4*capacity:
		budget = 5*net + 5*stock - 4*capacity
		if budget <= 0 || smartMakerHeld(f, economy.ActivityGeneration) {
			return nil
		}
	case 5*stock < 2*capacity:
		need = 2*capacity - 5*stock - 5*net
		if need <= 0 {
			return nil
		}
	default:
		return nil
	}
	type maker struct {
		unit          frame.UnitView
		makes, upkeep int64
	}
	var makers []maker
	for _, u := range f.Units {
		if u.Owner != f.ViewingPlayer || u.BuildRemaining != 0 {
			continue
		}
		d, ok := cat.Unit(u.DefName)
		if !ok || !smartMakerCandidate(d) {
			continue
		}
		makers = append(makers, maker{u, int64(uint8(d.MakesMetal)), max(1, smartMakerEnergy(float32(d.EnergyUse)))})
	}
	slices.SortFunc(makers, func(a, b maker) int {
		if n := cmp.Compare(b.makes*a.upkeep, a.makes*b.upkeep); n != 0 {
			return n
		}
		if n := cmp.Compare(a.unit.Slot, b.unit.Slot); n != 0 {
			return n
		}
		return cmp.Compare(a.unit.AllocationSerial, b.unit.AllocationSerial)
	})
	var plan []smartMakerAction
	if budget > 0 {
		for _, m := range makers {
			if cost := 5 * m.upkeep; !m.unit.Activated && cost <= budget {
				plan = append(plan, smartMakerAction{unitViewRef(m.unit), true})
				budget -= cost
			}
		}
	} else {
		for i := len(makers) - 1; i >= 0 && need > 0; i-- {
			m := makers[i]
			if m.unit.Activated {
				plan = append(plan, smartMakerAction{unitViewRef(m.unit), false})
				need -= 5 * m.upkeep
			}
		}
	}
	return plan
}

func (b *battleSession) serviceSmartMakers() {
	if b == nil || b.cl == nil || !b.cl.Enhanced() || !b.smartMakersEnabled() {
		return
	}
	f, ok := b.currentSnapshot()
	if !ok || !b.smartMakersCanControl(f) || b.sess.Clock == nil || b.sess.Clock.Paused {
		return
	}
	for _, e := range f.Economy {
		if e.Player != f.ViewingPlayer || !e.Active || e.ActivityTick == 0 {
			continue
		}
		s := &b.smartMakers
		if s.observed && s.lastPass == e.ActivityTick {
			return
		}
		s.observed, s.lastPass = true, e.ActivityTick
		// Bound automation to 16 commands per settlement (normally one second),
		// leaving room for clicks in the relay's 64-command pending grant.
		// Larger fleets converge over subsequent settlements, not host frames.
		for i, a := range smartMakerPlan(f, b.cat, e) {
			if i == 16 {
				break
			}
			if err := b.DispatchActivation(session.HumanActivationCommand{Unit: a.unit.Handle, Activate: a.on}); err != nil {
				b.economyPanel.message = "Smart makers command could not be sent."
				return
			}
		}
		return
	}
}
