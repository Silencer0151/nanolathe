package construction

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/internal/movement"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

type constructionBindingFixture struct {
	s      *Service
	c      *CheckpointContext
	inputs *content.SimulationInputs
	a      *checkpoint.BindingAuthority
}

func newConstructionBindingFixture(t *testing.T) constructionBindingFixture {
	t.Helper()
	fs := vfs.New()
	sources, err := content.CaptureSimulationSources(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := content.FreezeSimulationInputs(sources, content.SimulationInputRequest{Catalog: &content.Catalog{}})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := inputs.CheckpointKeys()
	if err != nil {
		t.Fatal(err)
	}
	// Capture must not need the filesystem after admission.
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	a := checkpoint.NewBindingAuthority()
	return constructionBindingFixture{
		s:      NewServiceWithCheckpointBinding(nil, inputs.Catalog(), nil, nil, a),
		c:      NewCheckpointContext(orders.NewCheckpointContext(units.NewCheckpointContext(keys)), world.NewCheckpointContext(keys)),
		inputs: inputs, a: a,
	}
}

func (f constructionBindingFixture) register() error {
	return f.c.SetBindings(f.s, f.inputs, f.s.World, f.s.Economy, f.s.Combat, f.s.Movement, f.s.OrderBinding, f.a)
}

func constructionBindingPanicCRT(uint32) uint32              { panic("capture called CRT") }
func constructionBindingPanicSpecial(uint8) bool             { panic("capture called special-state predicate") }
func constructionBindingPanicModel(*units.Unit) *model.Model { panic("capture queried model") }

func installConstructionBindingCallbacks(s *Service, a *checkpoint.BindingAuthority) {
	s.SetCRTRandomWithCheckpointBinding(constructionBindingPanicCRT, a)
	s.SetIsSpecialSecondStateWithCheckpointBinding(constructionBindingPanicSpecial, a)
	s.SetModelForFactoryWithCheckpointBinding(constructionBindingPanicModel, a)
	s.SetModelForUnitWithCheckpointBinding(constructionBindingPanicModel, a)
}

type constructionBindingSink struct {
	crt    *rng.CRT
	strips int
}

func (s *constructionBindingSink) EmitNanolathe(frame.Event) bool {
	s.strips++
	s.crt.Rand()
	return false
}

type constructionNoncomparableSink []int

func (constructionNoncomparableSink) EmitNanolathe(frame.Event) bool { panic("foreign sink called") }

type constructionOtherSink struct{}

func (*constructionOtherSink) EmitNanolathe(frame.Event) bool { panic("other sink called") }

func TestCheckpointConstructionBindingPresenceVectorAndPurity(t *testing.T) {
	f := newConstructionBindingFixture(t)
	s, c := f.s, f.c
	installConstructionBindingCallbacks(s, f.a)
	s.World, s.Economy, s.Combat, s.Movement, s.OrderBinding = &units.World{}, &economy.Service{}, &combat.Service{}, &movement.System{}, &orders.QueueBinding{}
	s.repairWorld = s.World
	s.Terrain = &world.Terrain{}
	c.World.Terrain = s.Terrain
	s.ModeSelector = -0x0102030405060708
	crt := rng.NewCRT(17)
	sink := &constructionBindingSink{crt: &crt, strips: 9}
	s.Presentation = sink
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	if err := SetCheckpointPresentationSink(c, sink); err != nil {
		t.Fatal(err)
	}
	// Fixed schema vector, authored independently of Service: four bindings,
	// Community, three bindings, signed mode, five bindings, rules/terrain/world,
	// four empty collection counts and repair-world presence (§16.3.15/.67).
	var want bytes.Buffer
	for _, value := range []any{[4]byte{0, 1, 1, 1}, [144]byte{}, [3]byte{1, 1, 0}, int64(-0x0102030405060708), [5]byte{1, 1, 1, 1, 1}, [3]byte{0, 1, 1}, [4]uint32{}, byte(1)} {
		if err := binary.Write(&want, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	beforeCRT, beforeProof, beforeBindings := crt, s.checkpointCallbacks, c.bindings
	for range 3 {
		if err := f.register(); err != nil {
			t.Fatal(err)
		}
		if err := SetCheckpointPresentationSink(c, sink); err != nil {
			t.Fatal(err)
		}
		got := constructionCheckpointBytes(t, s, c)
		if !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("binding vector\ngot  %x\nwant %x", got, want.Bytes())
		}
	}
	if crt != beforeCRT || sink.strips != 9 || s.checkpointCallbacks != beforeProof || c.bindings != beforeBindings || s.rowsResolved || s.boundConstructionWake != nil || s.boundGetBuilt != nil {
		t.Fatal("capture invoked a producer or changed lazy/proof state")
	}
}

func TestCheckpointConstructionBindingIndividualPresencePositions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offset  int
		install func(constructionBindingFixture)
	}{
		{"CRT", 1, func(f constructionBindingFixture) {
			f.s.SetCRTRandomWithCheckpointBinding(constructionBindingPanicCRT, f.a)
		}},
		{"special", 149, func(f constructionBindingFixture) {
			f.s.SetIsSpecialSecondStateWithCheckpointBinding(constructionBindingPanicSpecial, f.a)
		}},
		{"factory model", 159, func(f constructionBindingFixture) {
			f.s.SetModelForFactoryWithCheckpointBinding(constructionBindingPanicModel, f.a)
		}},
		{"unit model", 160, func(f constructionBindingFixture) {
			f.s.SetModelForUnitWithCheckpointBinding(constructionBindingPanicModel, f.a)
		}},
		{"combat", 3, func(f constructionBindingFixture) { f.s.Combat = &combat.Service{} }},
		{"economy", 148, func(f constructionBindingFixture) { f.s.Economy = &economy.Service{} }},
		{"movement", 161, func(f constructionBindingFixture) { f.s.Movement = &movement.System{} }},
		{"order binding", 162, func(f constructionBindingFixture) { f.s.OrderBinding = &orders.QueueBinding{} }},
		{"presentation", 163, func(f constructionBindingFixture) {
			sink := &constructionOtherSink{}
			f.s.Presentation = sink
			if err := SetCheckpointPresentationSink(f.c, sink); err != nil {
				t.Fatal(err)
			}
		}},
		{"world", 166, func(f constructionBindingFixture) { f.s.World = &units.World{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionBindingFixture(t)
			tc.install(f)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			want := make([]byte, 184)
			want[2], want[tc.offset] = 1, 1 // the frozen catalog is always present
			if got := constructionCheckpointBytes(t, f.s, f.c); !bytes.Equal(got, want) {
				t.Fatalf("presence at %d\ngot %x\nwant %x", tc.offset, got, want)
			}
		})
	}
}

func TestCheckpointConstructionBindingIndependentCallbackProofs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		slot     int
		ordinary func(*Service)
		admitted func(*Service, *checkpoint.BindingAuthority)
		clear    func(*Service)
	}{
		{"CRTRandom", 0, func(s *Service) { s.SetCRTRandom(s.CRTRandomHook()) }, func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetCRTRandomWithCheckpointBinding(constructionBindingPanicCRT, a)
		}, func(s *Service) { s.SetCRTRandomWithCheckpointBinding(nil, checkpoint.NewBindingAuthority()) }},
		{"IsSpecialSecondState", 1, func(s *Service) { s.SetIsSpecialSecondState(s.IsSpecialSecondStateHook()) }, func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetIsSpecialSecondStateWithCheckpointBinding(constructionBindingPanicSpecial, a)
		}, func(s *Service) { s.SetIsSpecialSecondState(nil) }},
		{"ModelForFactory", 2, func(s *Service) { s.SetModelForFactory(s.ModelForFactoryHook()) }, func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetModelForFactoryWithCheckpointBinding(constructionBindingPanicModel, a)
		}, func(s *Service) { s.SetModelForFactoryWithCheckpointBinding(nil, nil) }},
		{"ModelForUnit", 3, func(s *Service) { s.SetModelForUnit(s.ModelForUnitHook()) }, func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetModelForUnitWithCheckpointBinding(constructionBindingPanicModel, a)
		}, func(s *Service) { s.SetModelForUnit(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionBindingFixture(t)
			installConstructionBindingCallbacks(f.s, f.a)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			baseline := constructionCheckpointBytes(t, f.s, f.c)
			proofs := f.s.checkpointCallbacks
			tc.ordinary(f.s)
			for i, p := range f.s.checkpointCallbacks {
				if i == tc.slot {
					if p != (checkpointCallbackProof{}) {
						t.Fatal("ordinary replacement retained proof")
					}
				} else if p != proofs[i] {
					t.Fatal("replacement cleared another slot")
				}
			}
			constructionCheckpointRefused(t, f.s, f.c, "construction.Service."+tc.name)
			old := f.c.bindings
			if f.register() == nil || f.c.bindings != old {
				t.Fatal("repeat blessed ordinary callback or changed context")
			}
			for _, a := range []*checkpoint.BindingAuthority{nil, checkpoint.NewBindingAuthority()} {
				tc.admitted(f.s, a)
				constructionCheckpointRefused(t, f.s, f.c, "construction.Service."+tc.name)
			}
			tc.admitted(f.s, f.a)
			if got := constructionCheckpointBytes(t, f.s, f.c); !bytes.Equal(got, baseline) {
				t.Fatal("canonical reinstall changed bytes")
			}
			tc.clear(f.s)
			if f.s.checkpointCallbacks[tc.slot] != (checkpointCallbackProof{}) {
				t.Fatal("nil callback retained proof")
			}
			constructionCheckpointBytes(t, f.s, f.c)
		})
	}
}

func TestCheckpointConstructionBindingOwnerAliasesAndAtomicConflicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service)
	}{
		{"Catalog", func(s *Service) { v := *s.Catalog; s.Catalog = &v }},
		{"World", func(s *Service) { v := *s.World; s.World = &v }},
		{"Economy", func(s *Service) { v := *s.Economy; s.Economy = &v }},
		{"Combat", func(s *Service) { v := *s.Combat; s.Combat = &v }},
		{"Movement", func(s *Service) { v := *s.Movement; s.Movement = &v }},
		{"OrderBinding", func(s *Service) { v := *s.OrderBinding; s.OrderBinding = &v }},
		{"repairWorld", func(s *Service) { s.repairWorld = &units.World{} }},
		{"Terrain", func(s *Service) { s.Terrain = &world.Terrain{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionBindingFixture(t)
			f.s.World, f.s.Economy, f.s.Combat, f.s.Movement, f.s.OrderBinding = &units.World{}, &economy.Service{}, &combat.Service{}, &movement.System{}, &orders.QueueBinding{}
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			prior := f.c.bindings
			tc.change(f.s)
			constructionCheckpointRefused(t, f.s, f.c, "construction.Service."+tc.name)
			if f.register() == nil || f.c.bindings != prior {
				t.Fatal("conflict replaced context")
			}
		})
	}
	f := newConstructionBindingFixture(t)
	copyService := *f.s
	for _, s := range []*Service{nil, &copyService, NewService(nil, f.inputs.Catalog(), nil, nil)} {
		if f.c.SetBindings(s, f.inputs, nil, nil, nil, nil, nil, f.a) == nil || f.c.bindings != nil {
			t.Fatal("invalid constructor changed context")
		}
	}
	for _, a := range []*checkpoint.BindingAuthority{nil, checkpoint.NewBindingAuthority()} {
		if f.c.SetBindings(f.s, f.inputs, nil, nil, nil, nil, nil, a) == nil || f.c.bindings != nil {
			t.Fatal("invalid authority changed context")
		}
	}
	if f.c.SetBindings(f.s, nil, nil, nil, nil, nil, nil, f.a) == nil || f.c.bindings != nil {
		t.Fatal("nil inputs admitted")
	}
	for _, c := range []*CheckpointContext{nil, {}} {
		if c.SetBindings(f.s, f.inputs, nil, nil, nil, nil, nil, f.a) == nil {
			t.Fatal("missing context admitted")
		}
	}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	copyService = *f.s
	installConstructionBindingCallbacks(&copyService, f.a)
	if f.c.SetBindings(&copyService, f.inputs, nil, nil, nil, nil, nil, f.a) == nil {
		t.Fatal("copy replaced constructor")
	}
	constructionCheckpointRefused(t, &copyService, f.c, "construction.bindings")
	// Retained repair banks may still refer to this same world after a mode
	// switch; nil is also a valid not-yet-prepared bank binding.
	f = newConstructionBindingFixture(t)
	f.s.World = &units.World{}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	f.s.repairWorld = f.s.World
	constructionCheckpointBytes(t, f.s, f.c)
	f.s.repairWorld = nil
	constructionCheckpointBytes(t, f.s, f.c)
}

func TestCheckpointConstructionBindingSinkIdentity(t *testing.T) {
	for _, before := range []bool{false, true} {
		f := newConstructionBindingFixture(t)
		crt := rng.NewCRT(31)
		sink := &constructionBindingSink{crt: &crt}
		f.s.Presentation = sink
		if before {
			if err := SetCheckpointPresentationSink(f.c, sink); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.register(); err != nil {
			t.Fatal(err)
		}
		if !before {
			constructionCheckpointRefused(t, f.s, f.c, "construction.Service.Presentation")
			if err := SetCheckpointPresentationSink(f.c, sink); err != nil {
				t.Fatal(err)
			}
		}
		baseline := constructionCheckpointBytes(t, f.s, f.c)
		prior := f.c.presentation
		copySink := *sink
		if SetCheckpointPresentationSink(f.c, &copySink) == nil || f.c.presentation != prior {
			t.Fatal("equal-valued sink replaced expectation")
		}
		if SetCheckpointPresentationSink(f.c, (*constructionBindingSink)(nil)) == nil || SetCheckpointPresentationSink(nil, sink) == nil {
			t.Fatal("nil sink/context admitted")
		}
		for _, replacement := range []interface{ EmitNanolathe(frame.Event) bool }{nil, (*constructionBindingSink)(nil), &copySink, &constructionOtherSink{}, constructionNoncomparableSink{1}} {
			f.s.Presentation = replacement
			constructionCheckpointRefused(t, f.s, f.c, "construction.Service.Presentation")
		}
		f.s.Presentation = sink
		if got := constructionCheckpointBytes(t, f.s, f.c); !bytes.Equal(got, baseline) {
			t.Fatal("restored exact sink changed bytes")
		}
	}
}

func TestCheckpointConstructionBindingRetainsOtherRefusals(t *testing.T) {
	for _, tc := range constructionCheckpointBindings() {
		switch tc.field {
		case "Allocator", "LimitChecker", "Rules", "completedInPump", "reclaimStepNode", "vtolBuildStepOwner":
		default:
			continue
		}
		t.Run(tc.field, func(t *testing.T) {
			f := newConstructionBindingFixture(t)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			tc.set(f.s)
			constructionCheckpointRefused(t, f.s, f.c, "construction.Service."+tc.field)
		})
	}
	f := newConstructionBindingFixture(t)
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	other := newConstructionBindingFixture(t)
	*f.inputs = *other.inputs
	constructionCheckpointRefused(t, f.s, f.c, "construction.Service.Catalog")
	if f.register() == nil {
		t.Fatal("overwritten frozen-input object refreshed catalog expectation")
	}
}

func TestCheckpointConstructionBindingRejectsBorrowedProofAndChangedSource(t *testing.T) {
	f := newConstructionBindingFixture(t)
	installConstructionBindingCallbacks(f.s, f.a)
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	other := NewServiceWithCheckpointBinding(nil, f.inputs.Catalog(), nil, nil, f.a)
	installConstructionBindingCallbacks(other, f.a)
	proof := f.s.checkpointCallbacks[checkpointCRTRandom]
	f.s.checkpointCallbacks[checkpointCRTRandom] = other.checkpointCallbacks[checkpointCRTRandom]
	constructionCheckpointRefused(t, f.s, f.c, "construction.Service.CRTRandom")
	f.s.checkpointCallbacks[checkpointCRTRandom] = proof
	original := f.s.checkpointHandlers
	for _, alter := range []func(){
		func() { f.s.checkpointHandlers.authority = checkpoint.NewBindingAuthority() },
		func() { f.s.checkpointHandlers.source = nil },
		func() { f.s.checkpointHandlers.source = other.checkpointHandlers.source },
	} {
		alter()
		constructionCheckpointRefused(t, f.s, f.c, "construction.bindings")
		prior := f.c.bindings
		if f.register() == nil || f.c.bindings != prior {
			t.Fatal("changed source refreshed context")
		}
		f.s.checkpointHandlers = original
	}
	constructionCheckpointBytes(t, f.s, f.c)
}
