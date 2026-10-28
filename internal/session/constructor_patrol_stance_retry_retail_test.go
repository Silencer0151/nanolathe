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

// Issue #171. A CORACK chaining Modern patrol reclaims through Great Divide's
// forest often starts the next job while the last job's close animation still
// holds its threads; StartBuilding's `start-script RequestState(0)` then finds
// no free slot, the stance never rises and retail's deadline-free wait would
// keep the Reclaim forever. Every one of these builders used to park within
// about two thousand ticks. DESIGN_UNITS_ORDERS_COB "Modern build-stance
// retry": each wait now ends within the retry interval plus an ordinary
// opening, and the builders keep reclaiming.
func TestModernPatrolBuildStanceRetryUnparksReclaimRetail(t *testing.T) {
	s := aiE2ESkirmishAtMode(t, "Great Divide", aiE2ESeed, SkirmishDefaultDifficulty, gameplay.Modern)
	if _, ok := s.Catalog.Unit("corack"); !ok {
		t.Skip("corack absent from reference install")
	}
	driver := int32(1 << 20)
	step := func() {
		p := &s.Econ.Players[s.LocalOwner]
		p.Capacity, p.Stock = [2]float32{10000, 10000}, [2]float32{5000, 5000}
		s.Step(driver)
		driver++
		if s.State != StateBattle {
			t.Fatalf("battle ended at tick %d", s.Clock.GlobalTick)
		}
	}
	for i := 0; i < 60 && s.State != StateBattle; i++ {
		step()
	}
	at := func(v int64) numeric.Fixed { return numeric.FixedFromInt(v) }
	legs := [][4]int64{
		{352, 2825, 26, 2489}, {300, 2700, 40, 2300}, {600, 2500, 100, 2200},
		{20, 2300, 20, 2700}, {80, 2600, 450, 2450}, {60, 2400, 10, 2480},
	}
	type builder struct {
		u        *units.Unit
		q        *orders.Queue
		waiting  *orders.Node
		since    uint32
		finished int
	}
	var builders []*builder
	for _, l := range legs {
		u := placeCompleteRetailUnit(t, s, "corack", uint8(s.LocalOwner), at(l[0]), at(l[1]))
		s.Movement.EnsureUnit(u)
		s.bindOrderQueue(u)
		q := orders.QueueForUnit(u)
		q.CancelAll()
		if err := s.EnqueueHumanCommand(HumanCommand{Kind: HumanOrder, Order: HumanOrderCommand{
			Handles: []pool.Handle{u.Handle}, Code: 9, AssignedPosition: true,
			Position: orders.ResolvePos{X: at(l[2]), Y: s.World.HeightAt(at(l[2]), at(l[3])), Z: at(l[3]), InterfaceType: orders.InterfaceTypeRightClick},
		}}); err != nil {
			t.Fatal(err)
		}
		builders = append(builders, &builder{u: u, q: q})
	}
	// 450 retry ticks plus a CORACK opening, with margin.
	const bound = 450 + 300
	for i := 0; i < 4000; i++ {
		step()
		for _, b := range builders {
			head := b.q.Head()
			waiting := head != nil && head.DynamicGate&0x4 != 0 && !b.u.InBuildStance
			switch {
			case waiting && head != b.waiting:
				b.waiting, b.since = head, s.Clock.GlobalTick
			case !waiting && b.waiting != nil:
				b.waiting = nil
				b.finished++
			}
			if b.waiting != nil && s.Clock.GlobalTick-b.since > bound {
				t.Fatalf("CORACK %d has waited on its build stance since tick %d (head %s)", b.u.Handle, b.since, orders.DescriptorFor(head.ID).Name)
			}
		}
	}
	for _, b := range builders {
		if b.finished < 3 {
			t.Fatalf("CORACK %d finished only %d stance waits; the patrol stopped working", b.u.Handle, b.finished)
		}
	}
}
