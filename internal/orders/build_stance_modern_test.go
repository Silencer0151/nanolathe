package orders

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/cob"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// buildStanceFixture is a scripted builder whose queue carries rules, with a
// probe row that runs the shared INBUILDSTANCE wait toward a work position 64
// world units along +X. It counts StartBuilding arrangements on the unit's own
// callback bridge and the probe's dispatches.
type buildStanceFixture struct {
	q          *Queue
	u          *units.Unit
	n          *Node
	sim        *rng.Simulation
	starts     int
	dispatched int
	restore    func()
}

func newBuildStanceFixture(t *testing.T, rules Rules) *buildStanceFixture {
	t.Helper()
	moveID := Lookup("Move_Ground")
	if moveID == 0 {
		t.Fatal("descriptor table is unavailable")
	}
	rng.SeedGlobal(1, 0)
	f := &buildStanceFixture{sim: rng.Global.Sim}
	f.u, _ = cbUnit(cbProgram("StartBuilding", "StopBuilding"))
	f.u.ScriptState.Binding.Callbacks.SetLifecycleSink(func(e cob.LifecycleEvent) {
		if e.Name == "StartBuilding" && e.Phase == "start" {
			f.starts++
		}
	})
	f.q = &Queue{binding: &QueueBinding{SimRNG: f.sim, Rules: rules}}
	BindQueue(f.u, f.q)
	workX := numeric.Fixed(64 << 16)
	f.restore = setHandler(moveID, func(u *units.Unit, n *Node, satisfied uint32, tick uint32) Code {
		f.dispatched++
		if code := inBuildStanceWait(u, n, 0, satisfied, tick, workX, 0); code != 1 {
			return code
		}
		return Code(5)
	})
	t.Cleanup(f.restore)
	f.q.Push(moveID, Node{Owner: f.u.Handle})
	f.n = f.q.Primary()[0]
	return f
}

func (f *buildStanceFixture) pump(from, to uint32) {
	for tick := from; tick <= to; tick++ {
		f.q.Pump(f.u, tick)
	}
}

// Strict 3.1 and Community keep retail's wait: the gate and nothing else, so a
// silent script parks the record for as long as it stays silent [04 R-COB-06].
// Modern arms a long retry deadline beside it and arranges StartBuilding again
// each time that deadline expires, without drawing from the simulation stream
// (DESIGN_UNITS_ORDERS_COB "Modern build-stance retry").
func TestBuildStanceWaitRetryIsModernOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules Rules
		retry bool
	}{
		{"strict", StrictRules{}, false},
		{"community", CommunityRules{}, false},
		{"modern", &ModernRules{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBuildStanceFixture(t, tc.rules)
			draws := f.sim.Draws()
			f.pump(1000, 1000)
			if f.dispatched != 1 || f.n.Phase != 0 {
				t.Fatalf("first visit dispatched %d times, phase %d", f.dispatched, f.n.Phase)
			}
			if !tc.retry {
				if f.n.DynamicGate != gateBuildStance || f.n.Deadline != -1 {
					t.Fatalf("gate %#x deadline %d, want exactly 0x4 and no deadline [04 R-COB-06]", f.n.DynamicGate, f.n.Deadline)
				}
				f.pump(1001, 1000+3*modernBuildStanceRetryTicks)
				if f.dispatched != 1 || f.starts != 0 {
					t.Fatalf("silent script: %d dispatches, %d StartBuilding arrangements; want 1 and 0", f.dispatched, f.starts)
				}
				return
			}
			due := int32(1000 + modernBuildStanceRetryTicks)
			if f.n.DynamicGate != gateBuildStance|gateDeadline || f.n.Deadline != due {
				t.Fatalf("gate %#x deadline %d, want 0x5 and %d", f.n.DynamicGate, f.n.Deadline, due)
			}
			f.pump(1001, uint32(due)-1)
			if f.dispatched != 1 || f.starts != 0 {
				t.Fatalf("before the retry deadline: %d dispatches, %d arrangements; want 1 and 0", f.dispatched, f.starts)
			}
			f.pump(uint32(due), uint32(due))
			if f.dispatched != 2 || f.starts != 1 {
				t.Fatalf("at the retry deadline: %d dispatches, %d arrangements; want 2 and 1", f.dispatched, f.starts)
			}
			if f.n.Deadline != due+int32(modernBuildStanceRetryTicks) || f.n.DynamicGate != gateBuildStance|gateDeadline {
				t.Fatalf("after a retry: gate %#x deadline %d, want 0x5 and %d", f.n.DynamicGate, f.n.Deadline, due+int32(modernBuildStanceRetryTicks))
			}
			if f.n.Flags&FlagStopBuildingPending == 0 {
				t.Fatal("a retried StartBuilding must leave the StopBuilding counterpart pending")
			}
			if got := f.sim.Draws(); got != draws {
				t.Fatalf("the retry drew %d simulation numbers, want none", got-draws)
			}
		})
	}
}

// Any engine-port write raises the script-touched marker, so a stance wait is
// re-polled by unrelated script activity. Those wakes keep the pending retry
// deadline instead of pushing it back, and a ready stance releases it.
func TestModernBuildStanceMarkerKeepsRetryAndReadyReleasesIt(t *testing.T) {
	f := newBuildStanceFixture(t, &ModernRules{})
	f.pump(1000, 1000)
	due := f.n.Deadline
	f.u.Pending |= units.PendingScriptTouched
	f.pump(1100, 1100)
	if f.dispatched != 2 || f.n.Deadline != due || f.starts != 0 {
		t.Fatalf("marker wake: %d dispatches, deadline %d (want %d), %d arrangements", f.dispatched, f.n.Deadline, due, f.starts)
	}
	f.u.InBuildStance = true
	f.u.Pending |= units.PendingScriptTouched
	f.pump(1101, 1101)
	if len(f.q.Primary()) != 0 {
		t.Fatal("a ready stance must advance the record")
	}
	if f.n.Deadline != -1 {
		t.Fatalf("ready stance left deadline %d, want the retry released", f.n.Deadline)
	}
}

// A rule-set switch takes effect at the record's next visit and unwinds
// nothing (DESIGN_GAMEPLAY_RULES §5). A retry deadline armed under Modern still
// wakes the record once after a switch to Strict 3.1, which re-parks it on the
// gate alone and arranges nothing. A record parked under Strict is not visited
// until its script writes a port; under Modern that visit arms the retry.
func TestBuildStanceRetryFollowsARuleSwitch(t *testing.T) {
	t.Run("modern-to-strict", func(t *testing.T) {
		f := newBuildStanceFixture(t, &ModernRules{})
		f.pump(1000, 1000)
		due := uint32(f.n.Deadline)
		f.q.Binding().Rules = StrictRules{}
		f.pump(1001, due+3*modernBuildStanceRetryTicks)
		if f.dispatched != 2 || f.starts != 0 {
			t.Fatalf("after the switch: %d dispatches, %d arrangements; want 2 and 0", f.dispatched, f.starts)
		}
		if f.n.DynamicGate != gateBuildStance || f.n.Deadline != -1 {
			t.Fatalf("gate %#x deadline %d, want exactly 0x4 and no deadline [04 R-COB-06]", f.n.DynamicGate, f.n.Deadline)
		}
	})
	t.Run("strict-to-modern", func(t *testing.T) {
		f := newBuildStanceFixture(t, StrictRules{})
		f.pump(1000, 1000)
		f.q.Binding().Rules = &ModernRules{}
		f.pump(1001, 2000)
		if f.dispatched != 1 || f.starts != 0 {
			t.Fatalf("silent script after the switch: %d dispatches, %d arrangements; want 1 and 0", f.dispatched, f.starts)
		}
		f.u.Pending |= units.PendingScriptTouched
		f.pump(2001, 2001)
		due := int32(2001 + modernBuildStanceRetryTicks)
		if f.dispatched != 2 || f.n.Deadline != due || f.n.DynamicGate != gateBuildStance|gateDeadline {
			t.Fatalf("marker wake: %d dispatches, gate %#x deadline %d; want 2, 0x5 and %d", f.dispatched, f.n.DynamicGate, f.n.Deadline, due)
		}
		f.pump(2002, uint32(due))
		if f.dispatched != 3 || f.starts != 1 {
			t.Fatalf("at the retry deadline: %d dispatches, %d arrangements; want 3 and 1", f.dispatched, f.starts)
		}
	})
}
