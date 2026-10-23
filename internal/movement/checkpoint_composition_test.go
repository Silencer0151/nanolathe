package movement

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func movementCompositionKeys(t *testing.T, classes map[string]*content.MovementClass) *content.CheckpointKeys {
	t.Helper()
	fs := vfs.New()
	t.Cleanup(func() { _ = fs.Close() })
	sources, err := content.CaptureSimulationSources(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	in, err := content.FreezeSimulationInputs(sources, content.SimulationInputRequest{Catalog: &content.Catalog{Movement: classes}})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := in.CheckpointKeys()
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func movementCompositionFixture(t *testing.T, classes map[string]*content.MovementClass) (*System, *CheckpointContext, *checkpoint.BindingAuthority) {
	t.Helper()
	a := checkpoint.NewBindingAuthority()
	s := NewSystemWithCheckpointBindings(nil, Profile{}, nil, a)
	s.SetClasses(classes)
	c := movementCheckpointContext()
	c.Orders.Units.Keys = movementCompositionKeys(t, classes)
	return s, c, a
}

func movementCompositionPanicCallbacks(s *System, a *checkpoint.BindingAuthority) {
	s.SetDamageWithCheckpointBinding(func(uint32, combat.DamageInput) combat.DamageResult { panic("capture called damage") }, a)
	s.SetProductFootprintWithCheckpointBinding(func(uint32) (int32, int32, bool) { panic("capture called footprint") }, a)
}

func movementCompositionRefused(t *testing.T, s *System, c *CheckpointContext) {
	t.Helper()
	movementAuxiliaryRefused(t, s, c)
	var out bytes.Buffer
	if err := s.WritePathProviderCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || out.Len() != 0 {
		t.Fatalf("provider writer error=%v bytes=%d", err, out.Len())
	}
}

func TestMovementCheckpointCompositionPresenceVector(t *testing.T) {
	for mask := uint8(0); mask < 8; mask++ {
		s, c, a := movementCompositionFixture(t, nil)
		// Isolate the lexical slots on the original admitted allocation. No
		// proof is transferred to a different System; unrelated runtime rows
		// are empty so the full record below is independently authored.
		owner := s.checkpointOrderHandlers
		*s = System{checkpointOrderHandlers: owner}
		if mask&1 != 0 {
			s.SetClasses(map[string]*content.MovementClass{})
		}
		if mask&2 != 0 {
			s.SetDamageWithCheckpointBinding(func(uint32, combat.DamageInput) combat.DamageResult { panic("damage") }, a)
		}
		if mask&4 != 0 {
			s.SetProductFootprintWithCheckpointBinding(func(uint32) (int32, int32, bool) { panic("footprint") }, a)
		}
		if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
			t.Fatal(err)
		}
		// Empty maps remain present. Community is 32 bools + 14 i64s;
		// Profile is 16 bytes. Proofs add no bytes or graph references.
		want := movementCheckpointVector(t,
			uint8(0), mask&1, uint32(0), [144]byte{}, (mask>>1)&1, [16]byte{},
			uint32(0), uint8(0), uint8(1), int64(0), int32(0), uint16(6), uint32(0),
			(mask>>2)&1, uint32(0), uint8(0), uint8(0), uint32(0), uint8(0),
			uint32(0), [10]uint32{}, uint8(0),
			[4]uint32{}, [2]uint8{}, uint32(0), uint64(0), uint8(0),
			[3]uint32{}, int64(0),
			[11]uint32{}, int64(0), int64(0), int64(0), uint8(0),
			uint16(5), uint32(0), uint16(6), uint32(0), uint16(7), uint32(0),
			uint16(8), uint32(0), uint16(11), uint32(0))
		got := movementAuxiliaryBytes(t, s, c)
		if !bytes.Equal(got, want) {
			t.Fatalf("mask %d\ngot  %x\nwant %x", mask, got, want)
		}
		if mask == 0 && !bytes.Equal(got, movementCheckpointBytes(t, &System{})) {
			t.Fatal("admitted absence changed unregistered fixture bytes")
		}
	}
}

func TestMovementCheckpointCompositionPurityAndClassAliases(t *testing.T) {
	class := &content.MovementClass{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "walk"}, FootprintX: 3, FootprintZ: 5, MaxSlope: 7}
	s, c, a := movementCompositionFixture(t, map[string]*content.MovementClass{"walk": class, "absent": nil})
	movementCompositionPanicCallbacks(s, a)
	if err := c.SetPathBindings(s, nil, a); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
		t.Fatal(err)
	}
	before := movementAuxiliaryBytes(t, s, c)
	proof, damage, footprint, value := c.compositionBindings, s.checkpointDamage, s.checkpointProductFootprint, *class
	// Equal map containers over the original objects are allowed, including
	// authored nil entries. No rebuild or canonicalization is needed.
	s.SetClasses(map[string]*content.MovementClass{"absent": nil, "walk": class})
	if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
		t.Fatal(err)
	}
	if got := movementAuxiliaryBytes(t, s, c); !bytes.Equal(got, before) {
		t.Fatal("pure capture or equal class container changed bytes")
	}
	if c.compositionBindings != proof || s.checkpointDamage != damage || s.checkpointProductFootprint != footprint || !reflect.DeepEqual(*class, value) || s.layerRegistry != nil || s.learned != nil {
		t.Fatal("capture changed values, proof or lazy runtime owners")
	}
	if n := testing.AllocsPerRun(25, func() {
		if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("revalidation allocated %g times", n)
	}
}

func TestMovementCheckpointCompositionCallbackReplacement(t *testing.T) {
	for _, slot := range []string{"damage", "footprint"} {
		for _, mode := range []string{"ordinary", "nil authority", "foreign authority", "missing proof", "foreign owner", "stale proof"} {
			t.Run(slot+"/"+mode, func(t *testing.T) {
				s, c, a := movementCompositionFixture(t, nil)
				movementCompositionPanicCallbacks(s, a)
				if err := c.SetPathBindings(s, nil, a); err != nil {
					t.Fatal(err)
				}
				if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
					t.Fatal(err)
				}
				other := s.checkpointProductFootprint
				proof := &s.checkpointDamage
				ordinary := func() { s.SetDamage(s.DamageHook()) }
				admitted := func(a *checkpoint.BindingAuthority) { s.SetDamageWithCheckpointBinding(s.DamageHook(), a) }
				clearValue := func() { s.damage = nil }
				clear := func() { s.SetDamage(nil) }
				if slot == "footprint" {
					other, proof = s.checkpointDamage, &s.checkpointProductFootprint
					ordinary = func() { s.SetProductFootprint(s.ProductFootprintHook()) }
					admitted = func(a *checkpoint.BindingAuthority) {
						s.SetProductFootprintWithCheckpointBinding(s.ProductFootprintHook(), a)
					}
					clearValue = func() { s.productFootprint = nil }
					clear = func() { s.SetProductFootprint(nil) }
				}
				switch mode {
				case "ordinary":
					ordinary()
				case "nil authority":
					admitted(nil)
				case "foreign authority":
					admitted(checkpoint.NewBindingAuthority())
				case "missing proof":
					*proof = checkpointCompositionProof{}
				case "foreign owner":
					proof.system = &System{}
				case "stale proof":
					clearValue()
				}
				unchanged := s.checkpointProductFootprint
				if slot == "footprint" {
					unchanged = s.checkpointDamage
				}
				if unchanged != other {
					t.Fatal("replacement invalidated the other slot")
				}
				before := c.compositionBindings
				if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err == nil || c.compositionBindings != before {
					t.Fatal("repeat blessed replacement or changed registration")
				}
				movementCompositionRefused(t, s, c)
				clear()
				if *proof != (checkpointCompositionProof{}) {
					t.Fatal("ordinary nil retained proof")
				}
				movementAuxiliaryBytes(t, s, c)
			})
		}
	}
}

func TestMovementCheckpointCompositionClassMutationRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*System)
	}{
		{"missing", func(s *System) { delete(s.Classes, "walk") }},
		{"extra nil", func(s *System) { s.Classes["extra"] = nil }},
		{"removed nil", func(s *System) { delete(s.Classes, "absent") }},
		{"renamed", func(s *System) { s.Classes["WALK"] = s.Classes["walk"]; delete(s.Classes, "walk") }},
		{"equal copy", func(s *System) { v := *s.Classes["walk"]; s.Classes["walk"] = &v }},
		{"value", func(s *System) { s.Classes["walk"].MaxSlope++ }},
		{"stale header", func(s *System) { s.Classes["walk"].CanonicalKey = "different" }},
		{"nil table", func(s *System) { s.SetClasses(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, a := movementCompositionFixture(t, map[string]*content.MovementClass{"walk": {DefinitionHeader: content.DefinitionHeader{CanonicalKey: "walk"}, MaxSlope: 3}, "absent": nil})
			if err := c.SetPathBindings(s, nil, a); err != nil {
				t.Fatal(err)
			}
			if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
				t.Fatal(err)
			}
			tc.edit(s)
			before := c.compositionBindings
			if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err == nil || c.compositionBindings != before {
				t.Fatal("repeat accepted changed classes or rewrote registration")
			}
			movementCompositionRefused(t, s, c)
		})
	}
}

func TestMovementCheckpointCompositionRegistrationRefusal(t *testing.T) {
	s, c, a := movementCompositionFixture(t, nil)
	keys := c.Orders.Units.Keys
	copySystem := *s
	copyKeys := *keys
	for _, tc := range []struct {
		name string
		c    *CheckpointContext
		s    *System
		keys *content.CheckpointKeys
		a    *checkpoint.BindingAuthority
	}{
		{"nil context", nil, s, keys, a}, {"missing shared", &CheckpointContext{}, s, keys, a},
		{"nil system", c, nil, keys, a}, {"nil keys", c, s, nil, a}, {"nil authority", c, s, keys, nil},
		{"wrong keys", c, s, &copyKeys, a}, {"wrong authority", c, s, keys, checkpoint.NewBindingAuthority()},
		{"ordinary constructor", c, NewSystem(nil, Profile{}, nil), keys, a},
		{"copied constructor", c, &copySystem, keys, a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before CheckpointContext
			if tc.c != nil {
				before = *tc.c
			}
			if err := tc.c.SetCompositionBindings(tc.s, tc.keys, tc.a); err == nil {
				t.Fatal("invalid registration accepted")
			}
			if tc.c != nil && !reflect.DeepEqual(before, *tc.c) {
				t.Fatal("refusal changed context")
			}
		})
	}
	movementCompositionPanicCallbacks(s, a)
	// Installing proof cannot substitute for capture-local registration.
	if err := c.SetPathBindings(s, nil, a); err != nil {
		t.Fatal(err)
	}
	movementCompositionRefused(t, s, c)
	if err := c.SetCompositionBindings(s, keys, a); err != nil {
		t.Fatal(err)
	}
	// The public shared keys are re-read at capture and at repeated registration.
	c.Orders.Units.Keys = &copyKeys
	movementCompositionRefused(t, s, c)
	if err := c.SetCompositionBindings(s, keys, a); err == nil {
		t.Fatal("changed shared keys accepted")
	}
	if err := c.SetCompositionBindings(s, &copyKeys, a); err == nil {
		t.Fatal("registration replaced original keys")
	}
	c.Orders.Units.Keys = keys
	copySystem = *s
	movementCompositionRefused(t, &copySystem, c)
}

func TestMovementCheckpointCompositionAuthorityAgreement(t *testing.T) {
	for _, kind := range []string{"terrain", "path", "auxiliary"} {
		for _, compositionFirst := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/owner first", true: "/composition first"}[compositionFirst], func(t *testing.T) {
				s, c, a := movementCompositionFixture(t, nil)
				other := checkpoint.NewBindingAuthority()
				lower := world.NewCheckpointContext(nil)
				register := func(authority *checkpoint.BindingAuthority) error {
					switch kind {
					case "terrain":
						if s.Terrain == nil {
							s.Terrain = &world.Terrain{}
							s.Terrain.SetMovers(gridOccupancy{})
						}
						s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, authority)
						return c.SetTerrainBindings(s, lower, nil, authority)
					case "path":
						s.ConfigurePathWithCheckpointBinding(1, 1, nil, authority)
						return c.SetPathBindings(s, nil, authority)
					default:
						return c.SetAuxiliaryBindings(s, nil, authority)
					}
				}
				if compositionFirst {
					if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); err != nil {
						t.Fatal(err)
					}
				} else if err := register(other); err != nil {
					t.Fatal(err)
				}
				before, beforePaths, beforeWorld := *c, *c.Paths, *lower
				var err error
				if compositionFirst {
					err = register(other)
				} else {
					err = c.SetCompositionBindings(s, c.Orders.Units.Keys, a)
				}
				if err == nil || !strings.Contains(err.Error(), "composition authority differs") {
					t.Fatalf("missing symmetric authority refusal: %v", err)
				}
				if !reflect.DeepEqual(before, *c) || !reflect.DeepEqual(beforePaths, *c.Paths) || beforeWorld != *lower {
					t.Fatal("failed registration changed a context")
				}
				if compositionFirst {
					if err := register(a); err != nil {
						t.Fatal("matching authority refused", err)
					}
				} else {
					// A fresh context can register the matching pair in this order.
					keys := c.Orders.Units.Keys
					c, lower = movementCheckpointContext(), world.NewCheckpointContext(nil)
					c.Orders.Units.Keys = keys
					if err := register(a); err != nil {
						t.Fatal(err)
					}
					if err := c.SetCompositionBindings(s, keys, a); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestMovementCheckpointCompositionSettersPreserveCallbacks(t *testing.T) {
	in := combat.DamageInput{Nominal: -7, Direction: 253, Kind: 11}
	want := combat.DamageResult{Accepted: true, Amount: 65531, DeathLatched: true}
	for _, mode := range []string{"ordinary", "admitted", "failed proof"} {
		s, c, a := movementCompositionFixture(t, nil)
		calls := 0
		damage := func(tick uint32, got combat.DamageInput) combat.DamageResult {
			if tick != 0xfedcba98 || got != in {
				t.Fatal("damage operands changed")
			}
			calls++
			return want
		}
		footprint := func(index uint32) (int32, int32, bool) {
			if index != 0xfedcba98 {
				t.Fatal("footprint operand changed")
			}
			calls++
			return -3, 17, false
		}
		if mode == "ordinary" {
			s.SetDamage(damage)
			s.SetProductFootprint(footprint)
		} else {
			s.SetDamageWithCheckpointBinding(damage, a)
			s.SetProductFootprintWithCheckpointBinding(footprint, a)
			if mode == "failed proof" {
				s.SetDamage(s.DamageHook())
			}
		}
		if err := c.SetCompositionBindings(s, c.Orders.Units.Keys, a); (err == nil) != (mode == "admitted") {
			t.Fatalf("%s registration: %v", mode, err)
		}
		if calls != 0 {
			t.Fatal("installation invoked callback")
		}
		if got := s.DamageHook()(0xfedcba98, in); got != want {
			t.Fatal("damage result changed")
		}
		if x, z, ok := s.ProductFootprintHook()(0xfedcba98); x != -3 || z != 17 || ok {
			t.Fatal("footprint result changed")
		}
		if calls != 2 {
			t.Fatal("callback count changed")
		}
		s.SetDamageWithCheckpointBinding(nil, a)
		s.SetProductFootprintWithCheckpointBinding(nil, a)
		if s.DamageHook() != nil || s.ProductFootprintHook() != nil || s.checkpointDamage != (checkpointCompositionProof{}) || s.checkpointProductFootprint != (checkpointCompositionProof{}) {
			t.Fatal("nil callbacks retained proof")
		}
	}
}

func TestMovementCheckpointCompositionNilReceiverKeepsOrdinaryPanic(t *testing.T) {
	var s *System
	for _, install := range []func(){
		func() { s.SetDamage(nil) },
		func() { s.SetProductFootprint(nil) },
		func() { s.SetDamageWithCheckpointBinding(nil, nil) },
		func() { s.SetProductFootprintWithCheckpointBinding(nil, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("installation on nil receiver became a no-op")
				}
			}()
			install()
		}()
	}
}
