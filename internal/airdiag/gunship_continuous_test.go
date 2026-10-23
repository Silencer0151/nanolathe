package airdiag

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Eight ticks at at most a quarter world unit per tick permits a short
// reversal, while rejecting the former 31-tick Modern dwell. This is an
// acceptance bound for continuous gunship travel, not a retail constant:
// DESIGN_MOVEMENT_PATH "Modern aircraft tactics", issue 121.
const gunshipMaxNearStop = 8

// Relate emission to the stock weapon instead of freezing a pellet census.
// Allow one burst span across a window boundary, then require three quarters
// of the remaining complete bursts. The margin permits obstruction avoidance
// and aim reacquisition, but catches losing two fifths of one wingman's fire
// behind the crystal when the dwell is merely removed (issue 121).
func assertGunshipEmission(t *testing.T, label string, u *units.Unit, minHealth int32, windowTicks int, pellets []int, maxDry int) {
	t.Helper()
	w := u.SlotAt(0).Weapon
	if w == nil || w.ReloadTime <= 0 {
		t.Fatal("gunship fixture requires an authored positive reload")
	}
	if minHealth <= 0 || minHealth > u.Def.MaxDamage || u.Kills != 0 {
		t.Fatal("gunship fixture requires living aircraft without kill experience")
	}
	// Targets never die, so experience cannot shorten reload. Account for
	// any ordinary friendly damage using the slowest observed health factor,
	// with the authored integer truncation [06 §4.2]. No health is restored.
	reload := int((120 - 20*minHealth/u.Def.MaxDamage) * w.ReloadTime / 100)
	burst := max(1, int(w.Burst))
	span := 0
	if w.Burst > 0 {
		span = burst * max(1, int(w.BurstRate))
	}
	// Parked anchors are not pellets; only emitted clones count [06 §4.3].
	capacity := (windowTicks - span) / reload * burst
	minimum := (3*capacity + 3) / 4
	for window, count := range pellets {
		if count < minimum {
			t.Errorf("%s emitted %d pellets in window%d, want at least %d from reload=%d burst=%d span=%d over %d ticks", label, count, window, minimum, reload, burst, span, windowTicks)
		}
	}
	if limit := 3*reload + span; maxDry > limit {
		t.Errorf("%s emitted no pellet for %d ticks, limit %d (three reloads plus a burst span)", label, maxDry, limit)
	}
}

func gunshipPelletsThisTick(h *Harness, u *units.Unit) int {
	pellets := 0
	for _, pr := range h.Session.Combat.Records[:h.Session.Combat.Count()] {
		if pr.Shooter == u.Handle && pr.CreationTick == h.Session.Clock.GlobalTick && pr.BurstRemaining == 0 && !pr.Dead {
			pellets++
		}
	}
	return pellets
}

// An unarmed vehicle plant isolates the pause without obstruction or damage.
// Switching the live attack to Strict must return it to the
// retail orbit; a new Modern attack then exercises replacement of that target.
func TestGunshipContinuousFlightThroughRuleSwitchAndRetarget(t *testing.T) {
	root := testsupport.RetailRoot(t)
	t.Setenv("NANOLATHE_AIRLAB_LOS", "0")
	h := labHarness(t, root, diagMap, 11)
	w := h.Session.World
	c := labVec{float64(w.CellW) * 8, float64(w.CellH) * 8}
	s := newLabScene(h, "continuous-switch", c)
	target := s.holdPosition(s.spawn(t, 1, "ARMVP", c))
	u := s.fireAtWill(s.spawn(t, 0, "ARMBRAWL", labVec{c.X - 640, c.Z}))
	h.Step(30)
	targets := []*units.Unit{target}
	for _, phase := range []struct {
		name, rules string
		settle      int
	}{
		{"modern", "modern", 300},
		{"live-strict", "strict-3.1", 60},
		{"modern-new-target", "modern", 300},
	} {
		if err := h.Session.SetRules(phase.rules); err != nil {
			t.Fatal(err)
		}
		if phase.name == "modern-new-target" {
			target = s.holdPosition(s.spawn(t, 1, "ARMVP", labVec{c.X + 320, c.Z - 320}))
			targets = append(targets, target)
		}
		if phase.name != "live-strict" {
			s.order(t, u, input.LatchAttack, target, labVec{})
		}
		const windowTicks = 240
		prev := labPos(u)
		stopped, maxStopped, dry, maxDry, pellets := 0, 0, 0, 0, 0
		for tick := 0; tick < phase.settle+windowTicks; tick++ {
			for _, target := range targets {
				target.Health = target.Def.MaxDamage
			}
			h.Step(1)
			pos := labPos(u)
			if tick >= phase.settle {
				if pos.dist(prev) <= 0.25 {
					stopped++
					maxStopped = max(maxStopped, stopped)
				} else {
					stopped = 0
				}
				if emitted := gunshipPelletsThisTick(h, u); emitted > 0 {
					pellets += emitted
					dry = 0
				} else {
					dry++
					maxDry = max(maxDry, dry)
				}
			}
			prev = pos
		}
		t.Logf("%s pellets=%d longestStop=%d longestDry=%d", phase.name, pellets, maxStopped, maxDry)
		if !u.Alive || u.Dying || u.Health != u.Def.MaxDamage {
			t.Fatal("passive firing fixture damaged its aircraft")
		}
		if maxStopped > gunshipMaxNearStop {
			t.Errorf("%s gunship stayed still for %d ticks, limit %d", phase.name, maxStopped, gunshipMaxNearStop)
		}
		assertGunshipEmission(t, phase.name, u, u.Health, windowTicks, []int{pellets}, maxDry)
		if u.SlotAt(0).Target.Unit != target.Handle {
			t.Error("live attack did not retain its current target")
		}
	}
}

// Keep real incoming missiles and health loss separate from emission sampling.
// Never restore aircraft health: damage must reach the pilot's next visit and
// retain its ordinary reload effect. This checks movement under fire, not a
// survivability ranking between rules or a reconstruction of the report.
func TestGunshipKeepsMovingUnderRealMissileFire(t *testing.T) {
	root := testsupport.RetailRoot(t)
	t.Setenv("NANOLATHE_AIRLAB_LOS", "0")
	h := labHarness(t, root, diagMap, 11)
	if err := h.Session.SetRules("modern"); err != nil {
		t.Fatal(err)
	}
	w := h.Session.World
	c := labVec{float64(w.CellW) * 8, float64(w.CellH) * 8}
	s := newLabScene(h, "missile-damage", c)
	target := s.fireAtWill(s.holdPosition(s.spawn(t, 1, "CORRL", c)))
	u := s.fireAtWill(s.spawn(t, 0, "ARMBRAWL", labVec{c.X - 640, c.Z}))
	h.Step(30)
	s.order(t, u, input.LatchAttack, target, labVec{})
	prev := labPos(u)
	stopped, maxStopped, damagedTicks, incoming, pellets := 0, 0, 0, 0, 0
	for tick := 0; tick < 600; tick++ {
		target.Health = target.Def.MaxDamage
		h.Step(1)
		if !u.Alive || u.Dying {
			break
		}
		pos := labPos(u)
		if tick >= 300 && u.Health < u.Def.MaxDamage {
			damagedTicks++
			if pos.dist(prev) <= 0.25 {
				stopped++
				maxStopped = max(maxStopped, stopped)
			} else {
				stopped = 0
			}
			pellets += gunshipPelletsThisTick(h, u)
		}
		incoming += gunshipPelletsThisTick(h, target)
		prev = pos
	}
	t.Logf("damagedTicks=%d incoming=%d pellets=%d longestStop=%d finalHealth=%d", damagedTicks, incoming, pellets, maxStopped, u.Health)
	if damagedTicks < 90 || incoming == 0 || pellets == 0 {
		t.Fatal("missile fixture did not exercise sustained, damaged combat")
	}
	if maxStopped > gunshipMaxNearStop {
		t.Errorf("damaged gunship stayed still for %d ticks, limit %d", maxStopped, gunshipMaxNearStop)
	}
}
