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
	plan        []smartMakerAction
	pending     []smartMakerAction
	overridden  []pool.UnitRef // manual activation retires provenance, not transport credit
	sequence    uint64
	sentTick    uint32
	holds       [2]smartMakerHoldIntent
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

func (b *battleSession) noteSmartMakerManualActivation(unit pool.Handle) {
	s := &b.smartMakers
	// An explicit human switch owns this allocation from now on. Keep the
	// outstanding command's credit until its grant, but retire its provenance.
	for _, a := range s.pending {
		if a.unit.Handle == unit && !slices.Contains(s.overridden, a.unit) {
			s.overridden = append(s.overridden, a.unit)
		}
	}
	s.plan = slices.DeleteFunc(s.plan, func(a smartMakerAction) bool { return a.unit.Handle == unit })
}

type smartMakerHoldIntent struct {
	active, pause bool
	sequence      uint64
	tick          uint32
}

func (b *battleSession) noteSmartMakerHold(c session.HumanEconomyActivityCommand, sequence uint64) {
	index := 0
	if c.Activity == economy.ActivityGeneration {
		index = 1
	} else if c.Activity != economy.ActivityUpkeep {
		return
	}
	f, ok := b.currentSnapshot()
	if ok {
		b.smartMakers.holds[index] = smartMakerHoldIntent{true, c.Pause, sequence, f.Tick}
	}
}

func (b *battleSession) smartMakerCommandObserved(sequence uint64, tick uint32, f *frame.Frame) bool {
	if b.multiplayer != nil {
		// A room's stream positions and a client's submission sequences are
		// different. The driver exposes only the latter's applied watermark.
		ack, ok := b.multiplayer.driver.(interface{ AppliedLocalSequence() uint64 })
		return ok && sequence <= ack.AppliedLocalSequence()
	}
	return f.Tick > tick
}

func (b *battleSession) smartMakerHoldPending(index int) bool {
	h := b.smartMakers.holds[index]
	return h.active && h.pause
}
func (b *battleSession) serviceSmartMakers() {
	if b == nil || b.cl == nil {
		return
	}
	planning := b.cl.Enhanced() && b.smartMakersEnabled()
	f, ok := b.currentSnapshot()
	if !ok || !b.smartMakersCanControl(f) || b.sess.Clock == nil || b.sess.Clock.Paused {
		return
	}
	s := &b.smartMakers
	for i := range s.holds {
		h := &s.holds[i]
		if h.active && b.smartMakerCommandObserved(h.sequence, h.tick, f) {
			h.active = false
		}
	}
	upkeepHeld := smartMakerHeld(f, economy.ActivityUpkeep) || b.smartMakerHoldPending(0)
	generationHeld := smartMakerHeld(f, economy.ActivityGeneration) || b.smartMakerHoldPending(1)
	var corrections []smartMakerAction
	if len(s.pending) != 0 {
		if !b.smartMakerCommandObserved(s.sequence, s.sentTick, f) || b.smartMakerHoldPending(0) || b.smartMakerHoldPending(1) {
			return
		}
		// A previously submitted ON can reach its order visit after an
		// Upkeep/Generation pause. Undo only our own in-flight ON intent,
		// not a manual activation or an unrelated switchable structure.
		if upkeepHeld || generationHeld {
			for _, a := range s.pending {
				if a.on && !slices.Contains(s.overridden, a.unit) {
					corrections = append(corrections, smartMakerAction{a.unit, false})
				}
			}
		}
		s.pending = nil
		s.overridden = nil
	}
	if planning {
		for _, e := range f.Economy {
			if e.Player != f.ViewingPlayer || !e.Active || e.ActivityTick == 0 {
				continue
			}
			if !s.observed || s.lastPass != e.ActivityTick {
				s.observed, s.lastPass = true, e.ActivityTick
				s.plan = smartMakerPlan(f, b.cat, e)
			}
			break
		}
	} else {
		// Disabling new decisions cannot withdraw an ON already submitted.
		// Finish its hold correction through the ordinary command path even
		// after switching presentation (DESIGN_INTERFACE_HUD_INPUT "Smart
		// makers input assistance"). Discard the remaining budget instead.
		s.plan = nil
	}
	if upkeepHeld {
		s.plan = nil
	}
	s.plan = append(corrections, s.plan...)
	for len(s.plan) > 0 {
		a := s.plan[0]
		s.plan = s.plan[1:]
		u, exists := snapshotUnitByHandle(f, a.unit.Handle)
		if !exists || unitViewRef(u) != a.unit || u.Owner != f.ViewingPlayer ||
			u.BuildRemaining != 0 || u.Activated == a.on || a.on && (upkeepHeld || generationHeld) {
			continue
		}
		sequence, err := b.submitHumanCommand(session.HumanCommand{Kind: session.HumanActivation,
			Activation: session.HumanActivationCommand{Unit: a.unit.Handle, Activate: a.on}})
		if err != nil {
			b.economyPanel.message = "Smart makers command could not be sent."
			s.plan = nil
			return
		}
		s.pending = append(s.pending, a)
		s.sequence, s.sentTick = sequence, f.Tick
		if b.multiplayer != nil {
			// One outstanding Smart command per client bounds ten synchronized
			// humans to ten entries in the room-wide 64-entry pending grant,
			// even if grants stall. No optimistic activation or timer retry.
			return
		}
	}
}
