package combat

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// An aircraft choosing its own target takes a visible unit that can shoot
// aircraft before a nearer harmless one, whatever the anti-air's range and
// readiness; a ground shooter, and anti-air that cannot reach aircraft, do not
// change the choice (DESIGN_MOVEMENT_PATH "Modern aircraft tactics").
func TestModernAircraftPrefersVisibleAntiAir(t *testing.T) {
	s, w, terrain, shooter, near, _ := modernCombatFixture(t)
	h, err := w.Create(near.Def, 1, cellCentre(9), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	far := w.Unit(h)
	// Short-ranged: it cannot engage now, so the ordinary threat score is the
	// same as the harmless unit's and the nearer target would win.
	aa := &content.WeaponDef{ID: 91, LineOfSight: true, Range: 64, WeaponVelocity: 20 << 16, DamageDefault: 40, ReloadTime: 30}
	far.InstallWeapon(0, aa)

	// A mobile one: a turret counts every armed enemy, in range or not.
	ground := *shooter.Def
	ground.CanMove = true
	shooter.Def = &ground
	q := modernTargetQuery(s, w, terrain, shooter, near, far)
	if got, _ := s.rules().SelectTarget(s, &q); got != near.Handle {
		t.Fatal("a ground shooter left the nearer target")
	}

	flyer := ground
	flyer.CanFly = true
	shooter.Def = &flyer
	shooter.Move.ModeMirror = airborneMoverMode
	q = modernTargetQuery(s, w, terrain, shooter, near, far)
	if got, _ := s.rules().SelectTarget(s, &q); got != far.Handle {
		t.Fatal("an aircraft did not take the anti-air first")
	}

	aa.Ballistic = true // a ground gun that cannot solve aircraft
	if got, _ := s.rules().SelectTarget(s, &q); got != near.Handle {
		t.Fatal("a ballistic ground weapon counted as anti-air")
	}
}

// An aircraft looking for work leaves a target other aircraft of its side are
// already expected to destroy, and takes whatever has been hurting its side's
// aircraft before anything else (DESIGN_MOVEMENT_PATH "Modern aircraft
// tactics"). Claims count only for the sight-distance scan an order makes;
// the weapon's own maintenance scan is unchanged.
func TestModernAircraftClaimsAndAttackers(t *testing.T) {
	s, w, terrain, shooter, near, _ := modernCombatFixture(t)
	s.AirBoard = &AirBoard{}
	s.rules().CombatTick(s, 100, false)
	h, err := w.Create(near.Def, 1, cellCentre(9), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	far := w.Unit(h)
	flyer := *shooter.Def
	flyer.CanFly = true
	shooter.Def = &flyer
	shooter.Move.ModeMirror = airborneMoverMode
	wh, err := w.Create(&flyer, 0, cellCentre(2), numeric.FixedFromInt(10), cellCentre(1))
	if err != nil {
		t.Fatal(err)
	}
	wing := w.Unit(wh)
	wing.InstallWeapon(0, shooter.SlotAt(0).Weapon)

	scan := func(sight bool) pool.Handle {
		q := modernTargetQuery(s, w, terrain, shooter, near, far)
		q.Acquisition.SightScan = sight
		got, _ := s.rules().SelectTarget(s, &q)
		return got
	}
	if scan(true) != near.Handle {
		t.Fatal("the nearer of two equal targets was not chosen")
	}
	// The wing's five shots in the claim horizon more than destroy it. Its
	// pilot posts during the movement phase, a tick ahead of combat's tick.
	s.AirBoard.Claim(wing, near, 101)
	if scan(false) != near.Handle {
		t.Fatal("a weapon maintenance scan read the claims")
	}
	if scan(true) != far.Handle {
		t.Fatal("an aircraft joined a target its wing is expected to destroy")
	}
	q := modernTargetQuery(s, w, terrain, shooter, near)
	q.Acquisition.SightScan = true
	if got, ok := s.rules().SelectTarget(s, &q); ok || got != 0 {
		t.Fatal("an aircraft piled onto the only target in reach, already covered")
	}
	s.rules().CombatTick(s, 101+airClaimHold+1, false)
	if scan(true) != near.Handle {
		t.Fatal("an unrenewed claim did not expire")
	}

	// Damage dealt to this side's aircraft puts the attacker first; damage
	// dealt to another player's aircraft does not.
	s.AirBoard.noteHit(far, 3, 50, 101+airClaimHold+1)
	if scan(true) != near.Handle {
		t.Fatal("damage to someone else's aircraft changed the choice")
	}
	s.AirBoard.noteHit(far, 0, 50, 101+airClaimHold+1)
	if scan(true) != far.Handle {
		t.Fatal("the unit hurting this side's aircraft was not taken first")
	}
	s.rules().CombatTick(s, 101+airClaimHold+1+airHitMemory, false)
	if scan(true) != near.Handle {
		t.Fatal("old damage was not forgotten")
	}
}
