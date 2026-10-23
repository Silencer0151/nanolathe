package ai

import (
	"bytes"
	"errors"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// These methods panic so registration/capture cannot accidentally ask the
// planner for its policy. The zero-length array also prevents interface equality.
type checkpointModernStep struct{ _ [0]func() }

func (checkpointModernStep) Step(*Manager, uint32, *units.World, *economy.Service) {
	panic("planner invoked")
}
func (checkpointModernStep) ControlsModernAI(*Manager) bool { panic("marker invoked") }

type checkpointOtherModernStep struct{ checkpointModernStep }
type checkpointStatefulModernStep struct {
	checkpointModernStep
	_ int
}

type checkpointBridgeOwner struct {
	self                                   *checkpointBridgeOwner
	manager                                *Manager
	history                                *ApplicationHistory
	validateCalls, writeCalls, enableCalls int
	reject                                 bool
}

func newCheckpointBridgeOwner(m *Manager) *checkpointBridgeOwner {
	h := &checkpointBridgeOwner{manager: m, history: m.checkpointHistory}
	h.self = h
	return h
}
func (*checkpointBridgeOwner) ControllerCheckpoint() ControllerCheckpoint {
	panic("unselected provider method invoked")
}
func (h *checkpointBridgeOwner) EnableCheckpointApplications(id checkpoint.Identity, keys *content.CheckpointKeys) error {
	h.enableCalls++
	if h.self != h {
		return errors.New("copied controller")
	}
	if err := EnableControllerCheckpointApplications(h.manager, h, id, keys); err != nil {
		return err
	}
	h.history = h.manager.checkpointHistory
	return nil
}
func (h *checkpointBridgeOwner) ValidateCheckpointBindings(m *Manager, c *CheckpointContext) error {
	h.validateCalls++
	if h.reject || h.self != h || h.manager != m || h.history != m.checkpointHistory {
		return errors.New("controller provenance changed")
	}
	// This must not recursively invoke this validator.
	return c.ValidateModernManager(m)
}
func (h *checkpointBridgeOwner) WriteControllerCheckpoint(e *checkpoint.Encoder, _ *CheckpointContext) error {
	h.writeCalls++
	e.U8(1)
	e.U32(0x12345678)
	return e.Err()
}
func (*checkpointBridgeOwner) AppendControllerCheckpointSummary(s *checkpoint.Summary) error {
	s.Word(1)
	s.Word(0x12345678)
	return nil
}

// Embedding does not grant the exact concrete pointer's admission. A promoted
// call through the nil interface would panic, as would a provider-only call.
type checkpointForeignController struct {
	CheckpointControllerOwner
	_ []int
}
type checkpointProviderOnly struct{ ControllerCheckpointProvider }

type checkpointModernFixture struct {
	m     *Manager
	c     *CheckpointContext
	w     CheckpointModernPlanner
	s     CheckpointControllerSource
	a     *checkpoint.BindingAuthority
	owner *checkpointBridgeOwner
}

func newCheckpointModernFixture(t *testing.T, present bool) *checkpointModernFixture {
	t.Helper()
	f := &checkpointModernFixture{
		m: &Manager{Controller: ControllerModern, Planner: checkpointModernStep{}}, c: aiCheckpointContext(t),
		w: NewCheckpointModernPlanner(checkpointModernStep{}),
		s: NewCheckpointControllerSource[checkpointBridgeOwner, *checkpointBridgeOwner](),
		a: checkpoint.NewBindingAuthority(),
	}
	if err := f.s.EnableApplications(f.m, checkpoint.Identity{}, f.c.Units.Keys); err != nil {
		t.Fatal(err)
	}
	if present {
		f.owner = newCheckpointBridgeOwner(f.m)
		f.m.Ext = f.owner
	}
	return f
}
func (f *checkpointModernFixture) register() error {
	return f.c.SetModernBindings(f.m, checkpointModernStep{}, f.w, f.s, f.a)
}

func TestCheckpointModernPlannerWitness(t *testing.T) {
	w := NewCheckpointModernPlanner(checkpointModernStep{})
	if !w.Matches(checkpointModernStep{}) || (CheckpointModernPlanner{}).Matches(checkpointModernStep{}) {
		t.Fatal("generated/absent witness mismatch")
	}
	for _, p := range []Planner{nil, &checkpointModernStep{}, (*checkpointModernStep)(nil), checkpointOtherModernStep{}, checkpointStatefulModernStep{}, checkpointUnknownPlanner{}, RetailPlanner{}, ModernPlanner{}} {
		if w.Matches(p) {
			t.Fatalf("admitted %T", p)
		}
	}
	for name, construct := range map[string]func(){
		"pointer":   func() { NewCheckpointModernPlanner(&checkpointModernStep{}) },
		"typed nil": func() { NewCheckpointModernPlanner((*checkpointModernStep)(nil)) },
		"state":     func() { NewCheckpointModernPlanner(checkpointStatefulModernStep{}) },
		"interface": func() { NewCheckpointModernPlanner[ModernAIStep](checkpointModernStep{}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("registration accepted non-concrete/zero-size struct")
				}
			}()
			construct()
		})
	}
}

func TestCheckpointModernBindingsVectorsAndPurity(t *testing.T) {
	for _, present := range []bool{false, true} {
		f := newCheckpointModernFixture(t, present)
		historyBefore, err := f.m.checkpointHistory.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.register(); err != nil {
			t.Fatal(err)
		}
		// Independent 459-byte empty manager layout: Controller at 151,
		// Ext at 196, table-1 Factory ref at 197, Planner at 263.
		want := make([]byte, 459)
		want[151], want[197], want[263] = 1, 1, 3
		if present {
			want[196] = 1
		}
		if got := aiCheckpointBytes(t, f.m, f.c); !bytes.Equal(got, want) {
			t.Fatalf("present %v manager vector\ngot  %x\nwant %x", present, got, want)
		}
		var out bytes.Buffer
		err = f.s.WriteCheckpoint(f.m, checkpoint.NewEncoder(&out), f.c)
		if !present {
			if err == nil || out.Len() != 0 {
				t.Fatalf("absent fragment: %v, %x", err, out.Bytes())
			}
		} else {
			if err != nil || !bytes.Equal(out.Bytes(), []byte{1, 0x78, 0x56, 0x34, 0x12}) {
				t.Fatalf("controller fragment: %v, %x", err, out.Bytes())
			}
			if f.owner.writeCalls != 1 || f.owner.enableCalls != 0 {
				t.Fatal("unexpected controller operation")
			}
		}
		after, err := f.m.checkpointHistory.Snapshot()
		if err != nil || after != historyBefore {
			t.Fatal("capture changed application history")
		}
		registered := f.c.modern
		if err := f.register(); err != nil || f.c.modern != registered {
			t.Fatalf("repeat replaced registration: %v", err)
		}
	}
}

func TestCheckpointModernBindingsRegistrationOrders(t *testing.T) {
	for _, modernFirst := range []bool{false, true} {
		f := newManagerBindingFixture(t)
		f.m.Controller = ControllerModern
		w := NewCheckpointModernPlanner(checkpointModernStep{})
		s := NewCheckpointControllerSource[checkpointBridgeOwner, *checkpointBridgeOwner]()
		if !modernFirst {
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
		}
		f.m.Planner = checkpointModernStep{}
		if err := s.EnableApplications(f.m, checkpoint.Identity{}, f.c.Units.Keys); err != nil {
			t.Fatal(err)
		}
		f.m.Ext = newCheckpointBridgeOwner(f.m)
		if !modernFirst {
			if err := f.c.SetModernBindings(f.m, f.m.Planner, w, s, checkpoint.NewBindingAuthority()); err == nil || f.c.modern != nil {
				t.Fatal("Modern registration accepted foreign Classic authority")
			}
			copyManager := *f.m
			if err := f.c.SetModernBindings(&copyManager, f.m.Planner, w, s, f.a); err == nil || f.c.modern != nil {
				t.Fatal("Modern registration accepted different Classic manager")
			}
		}
		if err := f.c.SetModernBindings(f.m, f.m.Planner, w, s, f.a); err != nil {
			t.Fatal(err)
		}
		if modernFirst {
			if err := f.c.SetBindings(f.m, f.inputs, f.stream, f.orders, f.survival, checkpoint.NewBindingAuthority()); err == nil || f.c.bindings != nil {
				t.Fatal("Classic registration accepted foreign Modern authority")
			}
			copyManager := *f.m
			if err := f.c.SetBindings(&copyManager, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil || f.c.bindings != nil {
				t.Fatal("Classic registration accepted different Modern manager")
			}
		}
		if err := f.register(); err != nil {
			t.Fatal(err)
		}
		aiCheckpointBytes(t, f.m, f.c)
	}
}

func TestCheckpointModernBindingsRefuseChangedTuple(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*checkpointModernFixture)
	}{
		{"controller kind", func(f *checkpointModernFixture) { f.m.Controller = ControllerClassic }},
		{"player", func(f *checkpointModernFixture) { f.m.Player = 10 }},
		{"history player", func(f *checkpointModernFixture) { f.m.Player = 1 }},
		{"planner", func(f *checkpointModernFixture) { f.m.Planner = checkpointOtherModernStep{} }},
		{"typed nil planner", func(f *checkpointModernFixture) { f.m.Planner = (*checkpointModernStep)(nil) }},
		{"nil history", func(f *checkpointModernFixture) { f.m.checkpointHistory = nil }},
		{"new history", func(f *checkpointModernFixture) {
			f.m.checkpointHistory, _ = NewApplicationHistory(checkpoint.Identity{}, 0, 2)
		}},
		{"history kind", func(f *checkpointModernFixture) { f.m.checkpointHistory.kind = 1 }},
		{"failed history", func(f *checkpointModernFixture) { f.m.checkpointHistory.Fail(errors.New("fixture failure")) }},
		{"active history", func(f *checkpointModernFixture) {
			h := f.m.checkpointHistory
			h.BeginAttempt(0, h.NextSerial(), 0, func(*checkpoint.Encoder) error { return nil })
		}},
		{"manager keys", func(f *checkpointModernFixture) { f.m.checkpointKeys = nil }},
		{"world keys", func(f *checkpointModernFixture) { f.c.World.Keys = nil }},
		{"unit keys", func(f *checkpointModernFixture) { f.c.Units.Keys = nil }},
		{"all keys", func(f *checkpointModernFixture) {
			other := aiCheckpointContext(t)
			f.c.Units.Keys, f.c.World.Keys, f.m.checkpointKeys = other.Units.Keys, other.Units.Keys, other.Units.Keys
		}},
		{"absent owner", func(f *checkpointModernFixture) { f.m.Ext = nil }},
		{"typed nil owner", func(f *checkpointModernFixture) { f.m.Ext = (*checkpointBridgeOwner)(nil) }},
		{"new owner", func(f *checkpointModernFixture) { f.m.Ext = newCheckpointBridgeOwner(f.m) }},
		{"copied owner", func(f *checkpointModernFixture) { copied := *f.owner; f.m.Ext = &copied }},
		{"noncomparable owner", func(f *checkpointModernFixture) { f.m.Ext = checkpointForeignController{} }},
		{"owner validation", func(f *checkpointModernFixture) { f.owner.reject = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCheckpointModernFixture(t, true)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			registered := f.c.modern
			tc.change(f)
			if err := f.register(); err == nil || f.c.modern != registered {
				t.Fatalf("changed tuple accepted/replaced: %v", err)
			}
			if _, err := f.m.CollectCheckpointReferences(f.c); err == nil {
				t.Fatal("collector accepted changed tuple")
			}
			for _, write := range []func(*checkpoint.Encoder) error{
				func(e *checkpoint.Encoder) error { return f.m.WriteCheckpoint(e, f.c) },
				func(e *checkpoint.Encoder) error { return f.s.WriteCheckpoint(f.m, e, f.c) },
			} {
				var out bytes.Buffer
				if err := write(checkpoint.NewEncoder(&out)); err == nil || out.Len() != 0 {
					t.Fatalf("changed tuple emitted bytes: %v, %x", err, out.Bytes())
				}
			}
		})
	}
}

func TestCheckpointModernBindingsAtomicRefusals(t *testing.T) {
	f := newCheckpointModernFixture(t, true)
	f.owner.reject = true
	if err := f.register(); err == nil || f.c.modern != nil {
		t.Fatal("failed validator installed tuple")
	}
	f.owner.reject = false
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		w       CheckpointModernPlanner
		s       CheckpointControllerSource
		a       *checkpoint.BindingAuthority
		planner Planner
	}{
		{"absent witness", CheckpointModernPlanner{}, f.s, f.a, f.m.Planner},
		{"new witness", NewCheckpointModernPlanner(checkpointModernStep{}), f.s, f.a, f.m.Planner},
		{"absent source", f.w, CheckpointControllerSource{}, f.a, f.m.Planner},
		{"new source", f.w, NewCheckpointControllerSource[checkpointBridgeOwner, *checkpointBridgeOwner](), f.a, f.m.Planner},
		{"nil authority", f.w, f.s, nil, f.m.Planner},
		{"foreign authority", f.w, f.s, checkpoint.NewBindingAuthority(), f.m.Planner},
		{"registered planner", f.w, f.s, f.a, checkpointOtherModernStep{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := f.c.modern
			if err := f.c.SetModernBindings(f.m, tc.planner, tc.w, tc.s, tc.a); err == nil || f.c.modern != registered {
				t.Fatal("conflicting tuple accepted/replaced")
			}
		})
	}
	copyManager := *f.m
	copyContext := *f.c
	if err := copyContext.ValidateModernManager(&copyManager); err == nil {
		t.Fatal("context copy admitted manager copy")
	}
	if err := f.c.SetModernBindings(&copyManager, f.m.Planner, f.w, f.s, f.a); err == nil {
		t.Fatal("repeat admitted manager copy")
	}
	// A capture context with an absent owner cannot be reused after lazy creation.
	absent := newCheckpointModernFixture(t, false)
	if err := absent.register(); err != nil {
		t.Fatal(err)
	}
	absent.m.Ext = newCheckpointBridgeOwner(absent.m)
	if err := absent.register(); err == nil {
		t.Fatal("refreshed absent owner expectation")
	}
}

func TestCheckpointModernSourceRejectsUnknownWithoutCalls(t *testing.T) {
	f := newCheckpointModernFixture(t, false)
	for _, ext := range []any{
		nil, (*checkpointBridgeOwner)(nil), checkpointForeignController{}, &checkpointForeignController{},
		struct{ *checkpointBridgeOwner }{}, checkpointProviderOnly{}, []int{1}, RetailPlanner{},
	} {
		m := &Manager{Controller: ControllerModern, Planner: checkpointModernStep{}, Ext: ext}
		c := aiCheckpointContext(t)
		m.checkpointHistory, _ = NewApplicationHistory(checkpoint.Identity{}, 0, 2)
		m.checkpointKeys = c.Units.Keys
		if ext != nil {
			if err := f.s.EnableApplications(m, checkpoint.Identity{}, c.Units.Keys); err == nil {
				t.Fatalf("enabled unknown %T", ext)
			}
			if err := c.SetModernBindings(m, m.Planner, f.w, f.s, f.a); err == nil || c.modern != nil {
				t.Fatalf("registered unknown %T", ext)
			}
		}
		var sum checkpoint.Summary
		sum.Word(9)
		if err := f.s.AppendSummary(m, &sum); err == nil {
			t.Fatalf("summarized unknown %T", ext)
		}
		if n, v := sum.Result(); n != 1 || v != 9 {
			t.Fatal("failed summary changed prefix")
		}
	}
}

func TestCheckpointModernSourceEnable(t *testing.T) {
	c := aiCheckpointContext(t)
	s := NewCheckpointControllerSource[checkpointBridgeOwner, *checkpointBridgeOwner]()
	for _, present := range []bool{false, true} {
		m := &Manager{Controller: ControllerModern, Player: 3}
		var h *checkpointBridgeOwner
		if present {
			h = newCheckpointBridgeOwner(m)
			m.Ext = h
		}
		if err := s.EnableApplications(m, checkpoint.Identity{}, c.Units.Keys); err != nil {
			t.Fatal(err)
		}
		state, err := m.checkpointHistory.Snapshot()
		if err != nil || !state.Enabled || state.Player != 3 || state.Kind != 2 || state.NextSerial != 1 {
			t.Fatalf("enabled history %+v, %v", state, err)
		}
		if present && (h.enableCalls != 1 || h.history != m.checkpointHistory) {
			t.Fatal("present owner not attached once")
		}
		old := m.checkpointHistory
		if err := s.EnableApplications(m, checkpoint.Identity{}, c.Units.Keys); err == nil || m.checkpointHistory != old {
			t.Fatal("reset enabled history")
		}
	}
	for _, controller := range []Controller{ControllerClassic, Controller(255)} {
		for _, present := range []bool{false, true} {
			m := &Manager{Controller: controller}
			h := newCheckpointBridgeOwner(m)
			if present {
				m.Ext = h
			}
			if err := s.EnableApplications(m, checkpoint.Identity{}, c.Units.Keys); err == nil || m.checkpointHistory != nil || h.enableCalls != 0 {
				t.Fatal("non-Modern enabling crossed owner boundary")
			}
		}
	}
	m := &Manager{Controller: ControllerModern}
	if err := s.EnableApplications(nil, checkpoint.Identity{}, c.Units.Keys); err == nil {
		t.Fatal("nil manager enabled")
	}
	if err := s.EnableApplications(m, checkpoint.Identity{}, nil); err == nil {
		t.Fatal("nil keys enabled")
	}
	if err := (CheckpointControllerSource{}).EnableApplications(m, checkpoint.Identity{}, c.Units.Keys); err == nil {
		t.Fatal("absent source enabled")
	}
}

func TestCheckpointModernSourceSummaryBoundary(t *testing.T) {
	f := newCheckpointModernFixture(t, true)
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	// Summary must not inspect bindings, tables, history, or call full validation.
	f.owner.reject = true
	f.m.checkpointHistory.Fail(errors.New("failed full capture"))
	f.c.Units, f.c.World = nil, nil
	calls := f.owner.validateCalls
	var sum checkpoint.Summary
	sum.Word(7)
	if err := f.s.AppendSummary(f.m, &sum); err != nil {
		t.Fatal(err)
	}
	if n, v := sum.Result(); n != 3 || v != 7+2+3*0x12345678 {
		t.Fatalf("summary = %d, %d", n, v)
	}
	if f.owner.validateCalls != calls {
		t.Fatal("summary invoked full validator")
	}
	// The accumulator belongs to the caller; declaring it inside the indirect
	// call loop would measure its interface-induced escape instead.
	var summary checkpoint.Summary
	if allocs := testing.AllocsPerRun(100, func() {
		summary = checkpoint.Summary{}
		if err := f.s.AppendSummary(f.m, &summary); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("summary allocations = %v", allocs)
	}
	if err := f.s.AppendSummary(f.m, nil); err == nil {
		t.Fatal("nil summary accepted")
	}
	if err := f.s.WriteCheckpoint(f.m, nil, f.c); err == nil {
		t.Fatal("nil encoder accepted")
	}
	var out bytes.Buffer
	if err := (CheckpointControllerSource{}).WriteCheckpoint(f.m, checkpoint.NewEncoder(&out), f.c); err == nil || out.Len() != 0 {
		t.Fatal("foreign source wrote payload")
	}
}

func TestCheckpointModernBindingsInitialGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*checkpointModernFixture)
	}{
		{"nil context", func(f *checkpointModernFixture) { f.c = nil }},
		{"nil manager", func(f *checkpointModernFixture) { f.m = nil }},
		{"no unit context", func(f *checkpointModernFixture) { f.c.Units = nil }},
		{"no world context", func(f *checkpointModernFixture) { f.c.World = nil }},
		{"no history", func(f *checkpointModernFixture) { f.m.checkpointHistory = nil }},
		{"copied controller", func(f *checkpointModernFixture) { copied := *f.owner; f.m.Ext = &copied }},
		{"wrong controller manager", func(f *checkpointModernFixture) { f.owner.manager = &Manager{} }},
		{"wrong controller history", func(f *checkpointModernFixture) { f.owner.history = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCheckpointModernFixture(t, true)
			tc.change(f)
			if err := f.register(); err == nil || f.c != nil && f.c.modern != nil {
				t.Fatal("invalid initial tuple admitted")
			}
		})
	}
	f := newCheckpointModernFixture(t, true)
	if err := f.c.ValidateModernManager(f.m); err == nil {
		t.Fatal("unregistered manager admitted")
	}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	calls := f.owner.validateCalls
	if n := testing.AllocsPerRun(100, func() {
		if err := f.c.ValidateModernManager(f.m); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("direct validation allocations = %v", n)
	}
	if f.owner.validateCalls != calls {
		t.Fatal("direct validation dispatched to owner")
	}
	if (CheckpointControllerSource{}).Valid() || !f.s.Valid() {
		t.Fatal("source validity")
	}
}
