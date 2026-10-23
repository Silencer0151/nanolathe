package orders

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/community"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

func checkpointBindingBytes(t *testing.T, b *QueueBinding, c *CheckpointContext) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := b.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func checkpointBindingRefusal(t *testing.T, b *QueueBinding, c *CheckpointContext, path string) {
	t.Helper()
	if err := c.ValidateBinding(b); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("validation = %v; want %s", err, path)
	}
	var out bytes.Buffer
	e := checkpoint.NewEncoder(&out)
	err := b.WriteCheckpoint(e, c)
	if err == nil || !strings.Contains(err.Error(), path) || out.Len() != 0 {
		t.Fatalf("write = %v, %d bytes; want %s before bytes", err, out.Len(), path)
	}
	e.U8(99)
	if e.Err() != err || out.Len() != 0 {
		t.Fatal("refusal was not sticky")
	}
}

// This oracle uses literal schema positions and the separately specified
// Community width (32 bools and fourteen i64s). No production writer or
// reflection supplies the expected field order. Adapter nil means absent.
func checkpointBindingVector(q [14]byte, movement, presentation, weapons, work, world []byte, economyTag, rules, simTag byte, features []byte) []byte {
	out := []byte{1, q[0], q[1]}
	if features == nil {
		features = make([]byte, 144)
	}
	out = append(out, features...)
	out = append(out, q[2], q[3], q[4], q[5], q[6], q[7], economyTag, q[8], q[9], q[10])
	adapter := func(v []byte) {
		if v == nil {
			out = append(out, 0)
		} else {
			out = append(out, 1)
			out = append(out, v...)
		}
	}
	adapter(movement)
	adapter(presentation)
	out = append(out, q[11], q[12], rules, simTag, q[13])
	adapter(weapons)
	adapter(work)
	adapter(world)
	return out
}

// Reflection is confined to this test: it exercises every exported setter
// using its declared callback type, including named AirLegRunner. No function
// address is inspected or compared. Every installed callback panics if called.
func checkpointBindingSlotSuite[C any, T any](t *testing.T, ordinary func(C) *T, admitted func(C, *checkpoint.BindingAuthority) *T, wrap func(*T) *QueueBinding, names string, vector func([]byte) []byte) {
	t.Helper()
	var config C
	fields := reflect.ValueOf(&config).Elem()
	expectedNames := strings.Fields(names)
	for _, name := range expectedNames {
		f := fields.FieldByName(name)
		if !f.IsValid() || f.Kind() != reflect.Func {
			t.Fatalf("missing callback config %s", name)
		}
		f.Set(reflect.MakeFunc(f.Type(), func([]reflect.Value) []reflect.Value { panic("checkpoint invoked callback") }))
	}
	callbackCount := 0
	for i := range fields.NumField() {
		if fields.Field(i).Kind() == reflect.Func {
			callbackCount++
		}
	}
	if callbackCount != len(expectedNames) {
		t.Fatal("callback inventory changed without schema review")
	}
	a := checkpoint.NewBindingAuthority()
	foreign := checkpoint.NewBindingAuthority()
	owner := admitted(config, a)
	b := wrap(owner)
	c := NewCheckpointContext(nil)
	if err := c.SetBindings(b, nil, nil, a); err != nil {
		t.Fatal(err)
	}
	all := bytes.Repeat([]byte{1}, len(expectedNames))
	baseline := checkpointBindingBytes(t, b, c)
	if !bytes.Equal(baseline, vector(all)) {
		t.Fatal("all-present vector differs")
	}
	if err := NewCheckpointContext(nil).SetBindings(wrap(ordinary(config)), nil, nil, a); err == nil {
		t.Fatal("ordinary construction acquired proof")
	}
	if err := NewCheckpointContext(nil).SetBindings(wrap(admitted(config, nil)), nil, nil, a); err == nil {
		t.Fatal("nil-authority construction acquired proof")
	}
	if err := NewCheckpointContext(nil).SetBindings(wrap(admitted(config, foreign)), nil, nil, a); err == nil {
		t.Fatal("foreign-authority construction acquired proof")
	}
	copied := *owner
	if err := NewCheckpointContext(nil).SetBindings(wrap(&copied), nil, nil, a); err == nil {
		t.Fatal("copied owner borrowed proof")
	}
	receiver := reflect.ValueOf(owner)
	for i, name := range expectedNames {
		t.Run(name, func(t *testing.T) {
			getter := receiver.MethodByName(name + "Hook")
			fn := getter.Call(nil)[0]
			ordinarySetter := receiver.MethodByName("Set" + name)
			canonicalSetter := receiver.MethodByName("Set" + name + "WithCheckpointBinding")
			// The configuration is copied: overwriting it cannot replace this slot.
			fields.FieldByName(name).SetZero()
			if !bytes.Equal(baseline, checkpointBindingBytes(t, b, c)) {
				t.Fatal("configuration mutation reached owner")
			}
			ordinarySetter.Call([]reflect.Value{fn})
			checkpointBindingRefusal(t, b, c, name)
			canonicalSetter.Call([]reflect.Value{fn, reflect.ValueOf(foreign)})
			checkpointBindingRefusal(t, b, c, name)
			canonicalSetter.Call([]reflect.Value{fn, reflect.Zero(reflect.TypeFor[*checkpoint.BindingAuthority]())})
			checkpointBindingRefusal(t, b, c, name)
			canonicalSetter.Call([]reflect.Value{fn, reflect.ValueOf(a)})
			if !bytes.Equal(baseline, checkpointBindingBytes(t, b, c)) {
				t.Fatal("exact reinstall changed another slot")
			}
			// One absent slot independently pins every lexical byte position.
			ordinarySetter.Call([]reflect.Value{reflect.Zero(fn.Type())})
			oneAbsent := append([]byte(nil), all...)
			oneAbsent[i] = 0
			if !bytes.Equal(checkpointBindingBytes(t, b, c), vector(oneAbsent)) {
				t.Fatal("slot payload is out of lexical order")
			}
			// Nil canonical installation remains absent and carries no proof.
			canonicalSetter.Call([]reflect.Value{reflect.Zero(fn.Type()), reflect.ValueOf(a)})
			if !bytes.Equal(checkpointBindingBytes(t, b, c), vector(oneAbsent)) {
				t.Fatal("nil canonical slot differs")
			}
			canonicalSetter.Call([]reflect.Value{fn, reflect.ValueOf(a)})
		})
	}
	// A nil receiver preserves ordinary panic behavior through both forms.
	nilOwner := reflect.ValueOf((*T)(nil))
	for _, suffix := range []string{"", "WithCheckpointBinding"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("nil setter stopped panicking")
				}
			}()
			fnType := receiver.MethodByName(expectedNames[0] + "Hook").Call(nil)[0].Type()
			args := []reflect.Value{reflect.Zero(fnType)}
			if suffix != "" {
				args = append(args, reflect.ValueOf(a))
			}
			nilOwner.MethodByName("Set" + expectedNames[0] + suffix).Call(args)
		}()
	}
}

func TestCheckpointBindingEveryCallbackSlot(t *testing.T) {
	t.Run("QueueBinding", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewQueueBinding, NewQueueBindingWithCheckpointBinding, func(b *QueueBinding) *QueueBinding { return b },
			"BuildList BuilderOptions CurrentTick Damage DangerCanRespond DangerRouteFeasible DangerStepFeasible DangerVisible Hostility Lookup ModernAIPlayer ReclaimFeature Resources TransportAdmission",
			func(v []byte) []byte {
				return checkpointBindingVector([14]byte(v), nil, nil, nil, nil, nil, 0, 0, 0, nil)
			})
	})
	t.Run("Movement", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewMovementGoalAdapter, NewMovementGoalAdapterWithCheckpointBinding, func(v *MovementGoalAdapter) *QueueBinding { return &QueueBinding{Movement: v} },
			"AirBases CrowdedMoveBlocked Destroy DetachTakeoff InstallAir InstallAnnulus InstallPoint InstallRectangle PlaceUnit Ready Release RunAir",
			func(v []byte) []byte { return checkpointBindingVector([14]byte{}, v, nil, nil, nil, nil, 0, 0, 0, nil) })
	})
	t.Run("Presentation", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewPresentationAdapter, NewPresentationAdapterWithCheckpointBinding, func(v *PresentationAdapter) *QueueBinding { return &QueueBinding{Presentation: v} },
			"Nanolathe NanolatheFeature Ready Status Teleport",
			func(v []byte) []byte { return checkpointBindingVector([14]byte{}, nil, v, nil, nil, nil, 0, 0, 0, nil) })
	})
	t.Run("Weapons", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewWeaponAdapter, NewWeaponAdapterWithCheckpointBinding, func(v *WeaponAdapter) *QueueBinding { return &QueueBinding{Weapons: v} },
			"Acquire CanEngage Engaged FirePoint FireTarget FiringPositionBlocked FiringPositionClear InhibitSlot Ready ReleaseSlot SetManualTarget StopFiring TargetsInRadius",
			func(v []byte) []byte { return checkpointBindingVector([14]byte{}, nil, nil, v, nil, nil, 0, 0, 0, nil) })
	})
	t.Run("Work", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewWorkAdapter, NewWorkAdapterWithCheckpointBinding, func(v *WorkAdapter) *QueueBinding { return &QueueBinding{Work: v} },
			"Assist CanResurrectFeature CancelNotice Capture Ready Repair Resurrect",
			func(v []byte) []byte { return checkpointBindingVector([14]byte{}, nil, nil, nil, v, nil, 0, 0, 0, nil) })
	})
	t.Run("World", func(t *testing.T) {
		checkpointBindingSlotSuite(t, NewWorldQueryAdapter, NewWorldQueryAdapterWithCheckpointBinding, func(v *WorldQueryAdapter) *QueueBinding { return &QueueBinding{World: v} },
			"DeclaresAlliance ForEachFeature ForEachUnit ForEachUnitInRadius Hostile LookupFeature LookupUnit MappingWord SeaLevel TerrainHeight",
			func(v []byte) []byte { return checkpointBindingVector([14]byte{}, nil, nil, nil, nil, v, 0, 0, 0, nil) })
	})
}

type checkpointHostileEconomy []int

func (checkpointHostileEconomy) UnitBuckets(pool.Handle) *[2]economy.Bucket {
	panic("checkpoint queried economy")
}

func TestCheckpointBindingExactContextAndAliases(t *testing.T) {
	a := checkpoint.NewBindingAuthority()
	sim := rng.NewSimulation(7)
	ec := &economy.Service{}
	b := &QueueBinding{Economy: ec, SimRNG: &sim, Movement: &MovementGoalAdapter{}, Presentation: &PresentationAdapter{}, Weapons: &WeaponAdapter{}, Work: &WorkAdapter{}, World: &WorldQueryAdapter{}}
	c := NewCheckpointContext(nil)
	if (*CheckpointContext)(nil).SetBindings(b, ec, &sim, a) == nil || c.SetBindings(nil, ec, &sim, a) == nil || c.SetBindings(b, ec, &sim, nil) == nil {
		t.Fatal("missing registration input accepted")
	}
	if c.bindings != nil {
		t.Fatal("failed registration changed context")
	}
	if err := c.SetBindings(b, ec, &sim, a); err != nil {
		t.Fatal(err)
	}
	before := c.bindings
	if c.SetBindings(b, ec, &sim, a) != nil || c.bindings != before {
		t.Fatal("repeat registration changed context")
	}
	for _, tc := range []struct {
		b   *QueueBinding
		ec  *economy.Service
		sim *rng.Simulation
		a   *checkpoint.BindingAuthority
	}{
		{&QueueBinding{}, ec, &sim, a}, {b, &economy.Service{}, &sim, a}, {b, ec, nil, a}, {b, ec, &sim, checkpoint.NewBindingAuthority()},
	} {
		if err := c.SetBindings(tc.b, tc.ec, tc.sim, tc.a); err == nil || c.bindings != before {
			t.Fatal("conflicting tuple changed context")
		}
	}
	baseline := checkpointBindingBytes(t, b, c)
	if !bytes.Equal(baseline, checkpointBindingVector([14]byte{}, make([]byte, 12), make([]byte, 5), make([]byte, 13), make([]byte, 7), make([]byte, 10), 1, 0, 1, nil)) {
		t.Fatal("empty adapters/service presence differs")
	}
	for _, mutate := range []func(){
		func() { b.Movement = &MovementGoalAdapter{} }, func() { b.Presentation = &PresentationAdapter{} }, func() { b.Weapons = &WeaponAdapter{} }, func() { b.Work = &WorkAdapter{} }, func() { b.World = &WorldQueryAdapter{} },
	} {
		original := *b
		mutate()
		checkpointBindingRefusal(t, b, c, "adapters")
		if c.SetBindings(b, ec, &sim, a) == nil || c.bindings != before {
			t.Fatal("changed adapter reregistered")
		}
		*b = original
	}
	for _, actual := range []interface {
		UnitBuckets(pool.Handle) *[2]economy.Bucket
	}{nil, (*economy.Service)(nil), &economy.Service{}, checkpointHostileEconomy{1}} {
		b.Economy = actual
		checkpointBindingRefusal(t, b, c, "Economy")
	}
	b.Economy = ec
	b.SimRNG = &rng.Simulation{}
	checkpointBindingRefusal(t, b, c, "SimRNG")
	b.SimRNG = &sim
	if !bytes.Equal(baseline, checkpointBindingBytes(t, b, c)) {
		t.Fatal("restored aliases changed bytes")
	}
	// A custom or typed-nil interface is not absence even with expected nil.
	for _, actual := range []interface {
		UnitBuckets(pool.Handle) *[2]economy.Bucket
	}{(*economy.Service)(nil), checkpointHostileEconomy{}} {
		fresh := NewCheckpointContext(nil)
		if fresh.SetBindings(&QueueBinding{Economy: actual}, nil, nil, a) == nil || fresh.bindings != nil {
			t.Fatal("unregistered economy admitted")
		}
	}
	empty := &QueueBinding{}
	if c.ValidateBinding(empty) == nil {
		t.Fatal("foreign empty binding admitted")
	}
	if err := c.ValidateBinding(nil); err != nil {
		t.Fatal("absent fixture rejected")
	}
	if !bytes.Equal(checkpointBindingBytes(t, nil, c), []byte{0}) {
		t.Fatal("absent binding framing changed")
	}
	checkpointBindingRefusal(t, empty, NewCheckpointContext(nil), "QueueBinding")
	checkpointBindingRefusal(t, empty, nil, "QueueBinding")
}

func TestCheckpointBindingRulesCommunityAndPurity(t *testing.T) {
	a := checkpoint.NewBindingAuthority()
	sim := rng.NewSimulation(7)
	beforeRNG := sim
	calls := 0
	config := QueueBindingConfig{SimRNG: &sim, CurrentTick: func() uint32 { calls++; return 42 }, Community: community.Features{
		AIApplianceEnergy: true, AIBuilderPlacementLimit: 1 << 40, AIDifficultyIncome: true, DebrisCapacity: -2,
		RepairRate: community.RepairRate{Enabled: true, RepairMultiplier: -3, SelfHealMultiplier: 1 << 41}, WreckSnapRadiusMax: 1 << 42,
	}}
	b := NewQueueBindingWithCheckpointBinding(config, a)
	c := NewCheckpointContext(nil)
	if err := c.SetBindings(b, nil, &sim, a); err != nil {
		t.Fatal(err)
	}
	// Literal complete nested record, retaining high/negative Go int bits.
	features := checkpointHex(t, "01"+"0000000000010000"+"0001"+"00"+"000000000000000000"+
		"feffffffffffffff"+"0000000000000000"+"0000000000"+
		"0000000000000000000000000000000000000000000000000000000000000000"+
		"00"+"0000000000000000"+"00"+"01fdffffffffffffff0000000000020000"+
		"00000000"+"0000000000000000"+"000000"+"0000000000000000"+"00000000"+"0000000000000000"+"0000000000040000")
	var callbacks [14]byte
	callbacks[2] = 1
	for _, tc := range []struct {
		rules Rules
		tag   byte
	}{{nil, 0}, {StrictRules{}, 1}, {&StrictRules{}, 1}, {CommunityRules{}, 2}, {&CommunityRules{}, 2}, {&ModernRules{}, 3}} {
		b.Rules = tc.rules
		got := checkpointBindingBytes(t, b, c)
		if !bytes.Equal(got, checkpointBindingVector(callbacks, nil, nil, nil, nil, nil, 0, tc.tag, 1, features)) {
			t.Fatalf("rules %T vector differs", tc.rules)
		}
	}
	for _, bad := range []Rules{(*StrictRules)(nil), (*CommunityRules)(nil), (*ModernRules)(nil), checkpointUnknownRules{}, (*checkpointUnknownRules)(nil)} {
		b.Rules = bad
		checkpointBindingRefusal(t, b, c, "Rules")
		fresh := NewCheckpointContext(nil)
		if fresh.SetBindings(b, nil, &sim, a) == nil || fresh.bindings != nil {
			t.Fatal("invalid rules registered")
		}
	}
	b.Rules = nil
	before := checkpointBindingBytes(t, b, c)
	b.Community.WorkingWeaponsAutonomous = true
	if bytes.Equal(before, checkpointBindingBytes(t, b, c)) {
		t.Fatal("live Community mutation excluded")
	}
	if sim != beforeRNG || calls != 0 {
		t.Fatal("capture invoked binding or changed RNG")
	}
	config.CurrentTick = func() uint32 { return 99 }
	config.Community.AIApplianceEnergy = false
	if b.CurrentTickHook()() != 42 || !b.Community.AIApplianceEnergy {
		t.Fatal("configuration mutation reached copied owner")
	}
}

func TestCheckpointBindingQueuePayloadAndEmptyGraphValidation(t *testing.T) {
	q := &Queue{}
	u := &units.Unit{Orders: q}
	c := orderCheckpointContext(t, u)
	collectOrdersCheckpoint(t, c)
	before := orderCheckpointBytes(t, c)
	b := &QueueBinding{}
	a := checkpoint.NewBindingAuthority()
	if err := c.SetBindings(b, nil, nil, a); err != nil {
		t.Fatal(err)
	}
	q.binding = b
	collectOrdersCheckpoint(t, c)
	got := orderCheckpointBytes(t, c)
	payload := checkpointBindingBytes(t, b, c)
	// One root (4+6+6 bytes), table-2 header (2+4), then binding payload.
	want := append([]byte(nil), before[:22]...)
	want = append(want, payload...)
	want = append(want, before[23:]...)
	if !bytes.Equal(got, want) {
		t.Fatal("queue binding payload moved another field")
	}
	// Binding proof must never admit unrelated row handlers.
	q.ownedHandlers = make([]OwnedHandler, len(table))
	q.ownedHandlers[0] = func(*units.Unit, *Node, uint32, uint32) (Code, bool) { panic("capture invoked owned handler") }
	if _, err := (&Pump{}).CollectCheckpointReferences(c); err == nil {
		t.Fatal("owned handler admitted")
	}
	// Even with no graph roots, registered live adapter replacements are checked.
	c = orderCheckpointContext(t)
	if err := c.SetBindings(b, nil, nil, a); err != nil {
		t.Fatal(err)
	}
	b.World = &WorldQueryAdapter{}
	if _, err := (&Pump{}).CollectCheckpointReferences(c); err == nil {
		t.Fatal("empty graph hid changed binding")
	}
	var out bytes.Buffer
	if err := (&Pump{}).WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || out.Len() != 0 {
		t.Fatal("empty graph writer hid changed binding")
	}
}

type checkpointBindingFailWriter struct{}

func (checkpointBindingFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCheckpointBindingOutputFailureIsSticky(t *testing.T) {
	b := &QueueBinding{}
	c := NewCheckpointContext(nil)
	if err := c.SetBindings(b, nil, nil, checkpoint.NewBindingAuthority()); err != nil {
		t.Fatal(err)
	}
	e := checkpoint.NewEncoder(checkpointBindingFailWriter{})
	err := b.WriteCheckpoint(e, c)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer error = %v", err)
	}
	if again := b.WriteCheckpoint(e, c); again != err {
		t.Fatal("writer failure was replaced")
	}
}
