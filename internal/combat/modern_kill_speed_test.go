package combat

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Modern ranks an enemy by the fire it removes per second of this slot's
// fire: its threat over the whole shots it needs. Of two equal threats the
// one that dies sooner wins, and a target this slot has worn down is kept
// against a fresh one (DESIGN_WEAPONS_PROJECTILES "Modern threat targeting").
func TestModernKillSpeedRanksEqualThreats(t *testing.T) {
	s, w, terrain, shooter, a, weapon := modernCombatFixture(t)
	h, err := w.Create(a.Def, 1, cellCentre(8), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	b := w.Unit(h)
	enemy := *weapon
	a.InstallWeapon(0, &enemy)
	b.InstallWeapon(0, &enemy)
	a.Health, b.Health = 300, 100 // five shots against two
	q := modernTargetQuery(s, w, terrain, shooter, a, b)
	if got, _ := s.rules().SelectTarget(s, &q); got != b.Handle {
		t.Fatal("the nearer equal threat won over the one that dies sooner")
	}
	// Worn down to one shot, the retained target holds against a fresh
	// threat half again as dangerous.
	stronger := enemy
	stronger.DamageDefault = 150
	b.SlotAt(0).Weapon = &stronger
	a.Health = 60
	b.Health = 100
	shooter.SlotAt(0).Target = units.Target{Kind: units.TargetUnit, Unit: a.Handle}
	if got, _ := s.rules().SelectTarget(s, &q); got != a.Handle {
		t.Fatal("a fresh threat took the slot from a target one shot from death")
	}
	a.Health = 300
	if got, _ := s.rules().SelectTarget(s, &q); got != b.Handle {
		t.Fatal("a healthy retained target pinned a deadlier one that dies sooner")
	}
	// No randomness is drawn by the ranking.
	random := rng.NewSimulation(7)
	q.Acquisition.RNG = &random
	before := random
	s.rules().SelectTarget(s, &q)
	if random != before {
		t.Fatal("kill-speed ranking consumed RNG")
	}
}

// A turret defends what lies behind it, so an armed enemy outside its own
// weapon's reach still threatens: an Instigator driving past a line of
// turrets toward the commander. A mobile shooter keeps ranking by what can
// reach it now, and a weapon that could never engage the turret's medium
// is no threat to either (DESIGN_WEAPONS_PROJECTILES "Modern threat
// targeting", issue #168).
func TestModernTurretCountsArmedEnemiesOutOfReach(t *testing.T) {
	s, w, terrain, shooter, harmless, weapon := modernCombatFixture(t)
	h, err := w.Create(harmless.Def, 1, cellCentre(8), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	passing := w.Unit(h)
	short := *weapon
	short.Range = 32 // the turret is 112 away
	passing.InstallWeapon(0, &short)
	q := modernTargetQuery(s, w, terrain, shooter, harmless, passing)
	if got, _ := s.rules().SelectTarget(s, &q); got != passing.Handle {
		t.Fatal("a turret preferred a nearer harmless unit to an armed one passing out of reach")
	}
	mobile := *shooter.Def
	mobile.CanMove = true
	shooter.Def = &mobile
	if got, _ := s.rules().SelectTarget(s, &q); got != harmless.Handle {
		t.Fatal("a mobile shooter counted an enemy that cannot reach it")
	}
	fixed := mobile
	fixed.CanMove = false
	shooter.Def = &fixed
	short.ToAirWeapon = true
	if got, _ := s.rules().SelectTarget(s, &q); got != harmless.Handle {
		t.Fatal("a weapon that cannot engage the turret's medium counted as a threat")
	}
}

// An unguided shot loses hits against a target that moves during its flight,
// so of two equal threats a cannon takes the one standing still; a beam
// weapon, which keeps every hit, takes the nearer one whatever it is doing.
func TestModernHitChanceFavoursATargetTheShotCanHit(t *testing.T) {
	s, w, terrain, shooter, moving, weapon := modernCombatFixture(t)
	h, err := w.Create(moving.Def, 1, cellCentre(8), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	still := w.Unit(h)
	enemy := *weapon
	moving.InstallWeapon(0, &enemy)
	still.InstallWeapon(0, &enemy)
	q := modernTargetQuery(s, w, terrain, shooter, moving, still)
	if got, _ := s.rules().SelectTarget(s, &q); got != moving.Handle {
		t.Fatal("equal standing threats did not choose the nearer")
	}
	moving.Move.Speed = numeric.FixedFromInt(4)
	if got, _ := s.rules().SelectTarget(s, &q); got != still.Handle {
		t.Fatal("an unguided shot preferred a target that moves out of its flight path")
	}
	weapon.BeamWeapon = true
	if got, _ := s.rules().SelectTarget(s, &q); got != moving.Handle {
		t.Fatal("a beam weapon discounted a moving target")
	}
	weapon.BeamWeapon, weapon.Tracks = false, true
	if got, _ := s.rules().SelectTarget(s, &q); got != moving.Handle {
		t.Fatal("a tracking weapon discounted a moving target")
	}
}
