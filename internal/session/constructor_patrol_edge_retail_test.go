//go:build retail

package session

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

func newConstructorPatrolEdgeRuntime(t *testing.T, mode gameplay.Mode) *constructorPatrolRuntime {
	t.Helper()
	fixture := loadRetailFixture(t)
	fixture.cfg.Gameplay = mode
	fixture.cfg.MapName = "plains and passes"
	s, err := NewSkirmishWithEntryOptions(fixture.fs, fixture.cat, fixture.cfg, SkirmishEntryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := &constructorPatrolRuntime{t: t, s: s, patrol: "VTOL_RepairPatrol", energyStock: 100000, metalStock: 100000}
	for i := 0; i < 5; i++ {
		f.step()
	}
	return f
}

func (f *constructorPatrolRuntime) edgeAircraft(x, y, z numeric.Fixed, mode uint8) *units.Unit {
	f.t.Helper()
	u := placeCompleteRetailUnit(f.t, f.s, "armaca", 0, x, z)
	f.s.Movement.EnsureUnit(u)
	f.s.bindOrderQueue(u)
	orders.QueueForUnit(u).CancelAll()
	f.s.Movement.SetMoverMode(u, mode)
	if !f.s.Movement.PlaceUnit(orders.PlaceRequest{Unit: u.Handle, X: x, Y: y, Z: z}) {
		f.t.Fatal("aircraft placement failed")
	}
	return u
}

// An ordinary group command near the map edge creates an off-map formation
// waypoint [04 R-STANCE-01 §5]. Modern must keep flying both retained legs,
// without rewriting that command or removing the retail vertical bypass.
// DESIGN_MOVEMENT_PATH "Modern construction aircraft patrol recovery".
func TestConstructionAircraftGroupPatrolAtMapEdgeRetail(t *testing.T) {
	at := numeric.FixedFromInt
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Modern} {
		t.Run(string(mode), func(t *testing.T) {
			f := newConstructorPatrolEdgeRuntime(t, mode)
			first := f.edgeAircraft(at(20), at(173), at(323), 2)
			second := f.edgeAircraft(at(60), at(173), at(323), 2)
			if err := f.s.EnqueueHumanCommand(HumanCommand{Kind: HumanOrder, Order: HumanOrderCommand{
				Handles: []pool.Handle{first.Handle, second.Handle}, Code: 9,
				Position: orders.ResolvePos{X: at(1), Y: at(86), Z: at(323)},
			}}); err != nil {
				t.Fatal(err)
			}
			f.step()
			q := orders.QueueForUnit(first)
			route := q.Primary()
			if len(route) != 2 || route[0].GoalX != at(-19) || orders.QueueForUnit(second).Head().GoalX != at(21) {
				t.Fatal("group command did not retain the expected formation offsets")
			}
			leg, back := route[0], route[1]
			marker := f.s.Movement.AirMarkerState(first.Handle)
			if mode == gameplay.Strict31 && marker.Goal.X != at(-19) || mode == gameplay.Modern && marker.Goal.X < 0 {
				t.Fatalf("wrong bound policy: mode=%s marker=%+v", mode, marker)
			}
			rotations, last := 0, q.Head()
			for i := 0; i < 1800; i++ {
				f.step()
				if head := q.Head(); head != last {
					rotations++
					last = head
				}
				if q.LenPrimary() != 2 || leg.GoalX != at(-19) || back.GoalX != at(20) {
					t.Fatal("patrol recovery changed the retained route")
				}
			}
			if mode == gameplay.Modern && (rotations < 3 || first.Y < f.s.World.HeightAt(first.X, first.Z)) {
				t.Fatalf("Modern patrol stopped: rotations=%d position=(%d,%d,%d)", rotations, first.X.Int(), first.Y.Int(), first.Z.Int())
			}
		})
	}
}

// Rounded diagnostic positions reconstruct the two retained-leg conditions,
// not the original battle's unavailable command history or velocity vectors.
func TestConstructionAircraftRetainedPatrolRecoveryRetail(t *testing.T) {
	at := numeric.FixedFromInt
	for _, tc := range []struct {
		name                                string
		x, y, z, goalX, goalZ, backX, backZ int64
		mode                                uint8
	}{
		{"off-map-low-altitude", -16, -9, 323, -16, 323, 768, 352, 2},
		{"grounded", 800, 86, 272, 15, 243, 800, 272, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructorPatrolEdgeRuntime(t, gameplay.Modern)
			u := f.edgeAircraft(at(tc.x), at(tc.y), at(tc.z), tc.mode)
			q := orders.QueueForUnit(u)
			id := orders.Lookup("VTOL_RepairPatrol")
			q.Push(id, orders.Node{Owner: u.Handle, GoalX: at(tc.goalX), GoalY: at(86), GoalZ: at(tc.goalZ), Phase: 1, GoalSupplied: true})
			leg := q.Head()
			leg.DynamicGate, leg.Deadline = 0xe1, int32(f.s.Clock.GlobalTick+14)
			q.Push(id, orders.Node{Owner: u.Handle, GoalX: at(tc.backX), GoalY: at(86), GoalZ: at(tc.backZ), Phase: 1, GoalSupplied: true})
			back := q.Primary()[1]
			rotations, last := 0, leg
			for i := 0; i < 1500; i++ {
				f.step()
				if head := q.Head(); head != last {
					rotations++
					last = head
				}
				if q.LenPrimary() != 2 || leg.GoalX != at(tc.goalX) || back.GoalX != at(tc.backX) {
					t.Fatal("recovery changed the retained patrol")
				}
			}
			if rotations < 3 || u.Move.ModeMirror != 2 || u.Y <= at(tc.y) {
				t.Fatalf("retained patrol did not recover: rotations=%d mode=%d position=(%d,%d,%d)", rotations, u.Move.ModeMirror, u.X.Int(), u.Y.Int(), u.Z.Int())
			}
		})
	}
}

func TestConstructionAircraftOffMapCompletionResumesPatrolRetail(t *testing.T) {
	at := numeric.FixedFromInt
	f := newConstructorPatrolEdgeRuntime(t, gameplay.Modern)
	f.helper = f.edgeAircraft(at(-48), at(166), at(323), 2)
	f.q = orders.QueueForUnit(f.helper)
	id := orders.Lookup("VTOL_RepairPatrol")
	f.q.Push(id, orders.Node{Owner: f.helper.Handle, GoalX: at(-48), GoalY: at(86), GoalZ: at(323), Phase: 1, GoalSupplied: true})
	leg := f.q.Head()
	f.q.Push(id, orders.Node{Owner: f.helper.Handle, GoalX: at(768), GoalY: at(86), GoalZ: at(352), Phase: 1, GoalSupplied: true})
	back := f.q.Primary()[1]
	def, ok := f.s.Catalog.Unit("armfus")
	if !ok {
		t.Fatal("armfus absent")
	}
	h, err := f.s.Units.CreateNanoframe(def, 0, at(56), f.s.World.HeightAt(at(56), at(288)), at(288))
	if err != nil {
		t.Fatal(err)
	}
	target := f.s.Units.Unit(h)
	target.Remaining = .00002
	target.Health = int32(float32(def.MaxDamage) * (1 - target.Remaining))
	f.s.Movement.EnsureUnit(target)
	paid := f.observeWork(target)
	completed, resumed := false, false
	rotations := 0
	var last *orders.Node
	for i := 0; i < 1500; i++ {
		f.step()
		if !completed && target.Remaining == 0 {
			completed = true
			if !f.s.Movement.Flights[f.helper.Handle].OffMap {
				t.Fatalf("fixture did not finish work with its footprint off-map: X=%d", f.helper.X.Int())
			}
		}
		if completed && (f.q.Head() == leg || f.q.Head() == back) {
			resumed = true
			if last != nil && f.q.Head() != last {
				rotations++
			}
			last = f.q.Head()
		}
	}
	if !completed || !resumed || rotations < 2 || paid.assist == 0 || paid.metal <= 0 || paid.energy <= 0 {
		t.Fatalf("off-map completion failed: completed=%v resumed=%v rotations=%d paid=%+v", completed, resumed, rotations, paid)
	}
	if f.q.LenPrimary() != 2 || leg.GoalX != at(-48) || back.GoalX != at(768) {
		t.Fatal("completed work changed the retained route")
	}
}
