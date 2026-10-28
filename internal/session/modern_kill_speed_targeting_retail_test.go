//go:build retail

package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// quietRetailTurretScene disarms the boot units, stops the AI and clears the
// features around the scene so only the placed units fight.
func quietRetailTurretScene(t *testing.T, f *retailFixture, mode gameplay.Mode) *Session {
	t.Helper()
	s := f.session(t)
	s.SetGameplay(mode)
	stepRetail(s, 2)
	for player := 0; player < 2; player++ {
		s.Units.ForEachPlayerSliceLive(player, func(u *units.Unit) {
			u.Flags &^= units.StandingFieldMask << units.StandingFireShift
			for i := 0; i < combat.NumSlots; i++ {
				u.SlotAt(i).Flags &^= units.SlotFlagEnabled
			}
		})
	}
	for _, ai := range s.AI {
		if ai != nil {
			for i := range ai.Deadlines {
				ai.Deadlines[i] = ^uint32(0)
			}
		}
	}
	for cz := int32(300 / 16); cz <= 1200/16; cz++ {
		for cx := int32(300 / 16); cx <= 1000/16; cx++ {
			cell := s.World.PlotAt(cx, cz)
			if cell != nil && !cell.IsEmpty() {
				cell.SetFeature(world.PlotFeatureNone)
				cell.SetOccupied(false)
				s.Features.ReleaseRemovedAt(int(cx), int(cz))
				s.World.NoteFootprintRestamp(cx, cz, 1, 1)
			}
		}
	}
	s.World.BumpStaticObstacleRevision()
	return s
}

// An LLT with an Instigator and a Weasel both firing at it takes the Weasel
// first under Modern: it dies to fewer shots, so its fire stops soonest
// (DESIGN_WEAPONS_PROJECTILES "Modern threat targeting", kill speed; issue
// #168). The LLT then returns to the Instigator. Strict 3.1 keeps its retail
// sampled acquisition, recorded here before the Modern change.
func TestModernTurretTakesQuickestKillFirstRetail(t *testing.T) {
	f := loadRetailFixture(t)
	for _, tc := range []struct {
		mode gameplay.Mode
		want string
	}{
		{gameplay.Modern, "Weasel Instigator"},
		{gameplay.Strict31, strictQuickestKillTrace},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s := quietRetailTurretScene(t, f, tc.mode)
			fx := numeric.FixedFromInt
			tower := placeCompleteRetailUnit(t, s, "ARMLLT", 0, fx(600), fx(600))
			inst := placeCompleteRetailUnit(t, s, "CORGATOR", 1, fx(770), fx(600))
			weasel := placeCompleteRetailUnit(t, s, "CORFAV", 1, fx(450), fx(600))
			names := map[pool.Handle]string{inst.Handle: "Instigator", weasel.Handle: "Weasel"}
			for _, u := range []*units.Unit{inst, weasel} {
				u.Flags &^= units.StandingFieldMask << units.StandingMoveShift // hold position
			}
			eligible, observers := visibilityModeRefreshInputs(s, 3)
			s.Vis.RefreshMode(3, true, eligible, observers)
			var order []string
			last := pool.Handle(0)
			for i := 0; i < 600 && (inst.Alive || weasel.Alive); i++ {
				tower.Health = tower.MaxHealth
				s.Step(s.Clock.ScaledAnchor + 1)
				slot := tower.SlotAt(0)
				if slot.Target.Kind == units.TargetUnit && slot.Target.Unit != last {
					last = slot.Target.Unit
					name, ok := names[last]
					if !ok {
						name = fmt.Sprintf("h%d", last)
					}
					if tc.mode == gameplay.Strict31 {
						name += fmt.Sprintf("@%d", s.Clock.GlobalTick)
					}
					order = append(order, name)
				}
			}
			if got := strings.Join(order, " "); got != tc.want {
				t.Fatalf("LLT targets %q, want %q", got, tc.want)
			}
		})
	}
}

// strictQuickestKillTrace is the Strict 3.1 target order and switch ticks of
// the test above, recorded on the code before Modern's kill-speed ranking.
const strictQuickestKillTrace = "Weasel@58 Instigator@94"

// A damaged AK driving past an LLT toward the base, never inside its own
// weapon's reach of the tower, is still a threat to what the tower defends:
// Modern finishes it before turning to a fresh AK holding position and firing
// at the tower (DESIGN_WEAPONS_PROJECTILES "Modern threat targeting"; issue
// #168). Ranked by what could reach the tower, the runner scored as harmless
// and drove through. Strict 3.1 keeps its retail acquisition, recorded here.
func TestModernTurretFinishesAnEnemyDrivingPastRetail(t *testing.T) {
	f := loadRetailFixture(t)
	for _, tc := range []struct {
		mode gameplay.Mode
		want string
	}{
		{gameplay.Modern, "Runner Holder"},
		{gameplay.Strict31, strictDrivingPastTrace},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s := quietRetailTurretScene(t, f, tc.mode)
			fx := numeric.FixedFromInt
			tower := placeCompleteRetailUnit(t, s, "ARMLLT", 0, fx(600), fx(600))
			// The AK laser reaches 180, the LLT 300: the runner's lane keeps it
			// 220 from the tower.
			runner := placeCompleteRetailUnit(t, s, "CORAK", 1, fx(820), fx(420))
			holder := placeCompleteRetailUnit(t, s, "CORAK", 1, fx(450), fx(600))
			runner.Health = runner.MaxHealth / 2
			holder.Flags &^= units.StandingFieldMask << units.StandingMoveShift // hold position
			s.Movement.EnsureUnit(runner)
			s.BindStagedOrderQueue(runner)
			q := orders.QueueOfUnit(runner)
			id := orders.Lookup("Move_Ground")
			if q == nil || id == 0 {
				t.Fatal("runner lacks an ordinary move queue")
			}
			gx, gz := fx(820), fx(1150)
			q.Push(id, orders.NewNodeForOrder(id, 0, gx, s.World.HeightAt(gx, gz), gz, s.Clock.GlobalTick, runner.Handle, false))
			names := map[pool.Handle]string{runner.Handle: "Runner", holder.Handle: "Holder"}
			eligible, observers := visibilityModeRefreshInputs(s, 3)
			s.Vis.RefreshMode(3, true, eligible, observers)
			var deaths []string
			alive := map[pool.Handle]bool{runner.Handle: true, holder.Handle: true}
			for i := 0; i < 900 && len(deaths) < 2; i++ {
				tower.Health = tower.MaxHealth
				s.Step(s.Clock.ScaledAnchor + 1)
				for _, u := range []*units.Unit{runner, holder} {
					if alive[u.Handle] && !u.Alive {
						alive[u.Handle] = false
						name := names[u.Handle]
						if tc.mode == gameplay.Strict31 {
							name += fmt.Sprintf("@%d", s.Clock.GlobalTick)
						}
						deaths = append(deaths, name)
					}
				}
			}
			if got := strings.Join(deaths, " "); got != tc.want {
				t.Fatalf("deaths %q, want %q", got, tc.want)
			}
		})
	}
}

// strictDrivingPastTrace is the Strict 3.1 death order and ticks of the test
// above, recorded on the code before the Modern turret threat rule.
const strictDrivingPastTrace = "Holder@138 Runner@208"
