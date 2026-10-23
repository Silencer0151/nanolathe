package ai

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

type managerBindingFixture struct {
	m        *Manager
	c        *CheckpointContext
	inputs   *content.SimulationInputs
	a        *checkpoint.BindingAuthority
	stream   *rng.Simulation
	orders   *orders.QueueBinding
	survival *SurvivalInfo
}

func newManagerBindingFixture(t *testing.T) *managerBindingFixture {
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
	fs.Close() // Capture cannot rely on live content access.
	stream := rng.NewSimulation(17)
	f := &managerBindingFixture{inputs: inputs, a: checkpoint.NewBindingAuthority(), stream: &stream,
		orders: &orders.QueueBinding{}, survival: &SurvivalInfo{},
		c: NewCheckpointContext(units.NewCheckpointContext(keys), world.NewCheckpointContext(keys))}
	f.m = NewManager(ManagerConfig{Catalog: inputs.Catalog(), RNG: f.stream, OrderBinding: f.orders, Survival: f.survival})
	f.m.SetCatalog(inputs.Catalog())
	return f
}

func (f *managerBindingFixture) register() error {
	return f.c.SetBindings(f.m, f.inputs, f.stream, f.orders, f.survival, f.a)
}

func managerBindingRefusal(t *testing.T, m *Manager, c *CheckpointContext, field string) {
	t.Helper()
	if _, err := m.CollectCheckpointReferences(c); err == nil || !strings.Contains(err.Error(), field) {
		t.Fatalf("collect: %v, want %s", err, field)
	}
	var out bytes.Buffer
	if err := m.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || !strings.Contains(err.Error(), field) {
		t.Fatalf("write: %v, want %s", err, field)
	}
	if out.Len() != 0 {
		t.Fatal("refusal emitted bytes")
	}
}

type managerBindingSlot struct {
	name     string
	install  func(*Manager, *checkpoint.BindingAuthority)
	ordinary func(*Manager)
	clear    func(*Manager, *checkpoint.BindingAuthority)
}

func managerBindingSlots() []managerBindingSlot {
	return []managerBindingSlot{
		{"CanPursueAir",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetCanPursueAirWithCheckpointBinding(func(*units.Unit, *units.Unit) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetCanPursueAir(m.CanPursueAirHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetCanPursueAirWithCheckpointBinding(nil, a) },
		},
		{"IsAlliance",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetIsAllianceWithCheckpointBinding(func(uint8, uint8) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetIsAlliance(m.IsAllianceHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetIsAllianceWithCheckpointBinding(nil, a) },
		},
		{"JammerSuppresses",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetJammerSuppressesWithCheckpointBinding(func(uint8, uint8) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetJammerSuppresses(m.JammerSuppressesHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetJammerSuppressesWithCheckpointBinding(nil, a) },
		},
		{"QueueBuildTyped",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetQueueBuildTypedWithCheckpointBinding(func(BuildRequest) error { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetQueueBuildTyped(m.QueueBuildTypedHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetQueueBuildTypedWithCheckpointBinding(nil, a) },
		},
		{"RallyProbeKnown",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetRallyProbeKnownWithCheckpointBinding(func(uint8, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetRallyProbeKnown(m.RallyProbeKnownHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetRallyProbeKnownWithCheckpointBinding(nil, a) },
		},
		{"RallyShotTimeAdmits",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetRallyShotTimeAdmitsWithCheckpointBinding(func(*units.Unit, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetRallyShotTimeAdmits(m.RallyShotTimeAdmitsHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetRallyShotTimeAdmitsWithCheckpointBinding(nil, a)
			},
		},
		{"RallyVisible",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetRallyVisibleWithCheckpointBinding(func(uint8, *units.Unit) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetRallyVisible(m.RallyVisibleHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetRallyVisibleWithCheckpointBinding(nil, a) },
		},
		{"UnitVisible",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetUnitVisibleWithCheckpointBinding(func(uint8, *units.Unit) bool { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetUnitVisible(m.UnitVisibleHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetUnitVisibleWithCheckpointBinding(nil, a) },
		},
		{"WeaponMaintenance",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.SetWeaponMaintenanceWithCheckpointBinding(func(uint8) { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.SetWeaponMaintenance(m.WeaponMaintenanceHook()) },
			func(m *Manager, a *checkpoint.BindingAuthority) { m.SetWeaponMaintenanceWithCheckpointBinding(nil, a) },
		},
		{"Strategic.energyEnvironment",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.Strategic.BindEnergyEnvironmentWithCheckpointBinding(func() (float32, float32) { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.Strategic.BindEnergyEnvironment(m.Strategic.energyEnvironment) },
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.Strategic.BindEnergyEnvironmentWithCheckpointBinding(nil, a)
			},
		},
		{"Strategic.rebuildRegistry",
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.Strategic.BindTargetRegistryRebuildWithCheckpointBinding(func(uint32, uint8) { panic("callback invoked") }, a)
			},
			func(m *Manager) { m.Strategic.BindTargetRegistryRebuild(m.Strategic.rebuildRegistry) },
			func(m *Manager, a *checkpoint.BindingAuthority) {
				m.Strategic.BindTargetRegistryRebuildWithCheckpointBinding(nil, a)
			},
		},
	}
}

func TestCheckpointManagerBindingsVector(t *testing.T) {
	f := newManagerBindingFixture(t)
	for _, slot := range managerBindingSlots() {
		slot.install(f.m, f.a)
	}
	f.m.Profile = &Profile{appliedCatalog: f.inputs.Catalog()}
	f.m.Terrain = &world.Terrain{}
	f.c.World.Terrain = f.m.Terrain
	f.m.BattleSeed, f.m.OriginX, f.m.Strategic.Radius = 0x01020304, -1<<40, -7
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	// Independently authored complete record: all eleven callbacks and owner
	// aliases present; a present otherwise-empty profile; no discovery helpers.
	var want bytes.Buffer
	put := func(v any) {
		t.Helper()
		if err := binary.Write(&want, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	put(uint32(0x01020304))
	put([2]byte{1, 1}) // seed, CanPursueAir, Catalog
	put([144]byte{})   // Community
	put([2]byte{})
	put(uint32(0))
	put([10]uint32{}) // rules, controller, params, deadlines
	put(byte(0))
	put(uint16(1))
	put(uint32(0))
	put([9]uint32{}) // Ext, nil Factory, groups
	put([2]byte{1, 1})
	put(int32(0))
	put(byte(1)) // alliance, jammer, mission, orders
	put(int64(-1 << 40))
	put(int64(0))
	put([4]byte{0, 0, 0, 1}) // origins, passive, planner, player, profile
	put(byte(0))
	put(uint32(0))
	put([4]byte{0, 0, 0, 1}) // profile Limit, Plan, Weight/allLimits/allWeights/appliedCatalog
	put(uint32(0))
	put([3]byte{})
	put(uint32(0))
	put([3]byte{}) // directives, fixture maps, ID map, name, recordIDs/textLoaded/weightsByID
	put([6]byte{1, 1, 1, 1, 1, 0})
	put(uint32(0)) // queue/RNG/rally predicates, StartOwners, StartPositions
	put(int32(0))
	put(byte(1))
	put([3]int64{})
	put([3]uint32{}) // Strategic build count, catalog, centre, maps
	put([4]int16{})
	put(uint32(0))
	put(uint32(0))
	put(int32(-7))
	put(uint32(0))
	put([4]int16{}) // regions, refresh, metal spots, radius, vectors
	put(byte(1))
	put(uint16(0))
	put(int32(0))
	put([3]byte{0, 1, 0})
	put(uint16(0))
	put(byte(0)) // energy, live count, max wind, bound/rebuild/ready, unit limit/bound
	put(int32(0))
	put([6]byte{1, 1, 1, 1, 0, 0})
	put(int32(0)) // surface, Survival/Terrain/visible/maintenance/countdown/wave, rally score
	put([6]int64{})
	put(byte(0))
	put([3]int64{})
	put(uint32(0))
	put(uint32(0))
	put([2]byte{}) // rally vectors/latches
	beforeRNG, proof, strategicProof, registered := *f.stream, f.m.checkpointCallbacks, f.m.Strategic.checkpointCallbacks, f.c.bindings
	for i := 0; i < 3; i++ {
		if err := f.register(); err != nil {
			t.Fatal(err)
		}
		got := aiCheckpointBytes(t, f.m, f.c)
		if !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("got (%d) %x\nwant (%d) %x", len(got), got, want.Len(), want.Bytes())
		}
	}
	if *f.stream != beforeRNG || proof != f.m.checkpointCallbacks || strategicProof != f.m.Strategic.checkpointCallbacks || registered != f.c.bindings {
		t.Fatal("capture or repeated registration changed proof or RNG")
	}
	if f.m.Profile.recordIDs != nil || f.m.Strategic.Counts != nil || f.m.rallyInitialized {
		t.Fatal("capture initialized gameplay state")
	}
}

func TestCheckpointManagerBindingsIndependentSlots(t *testing.T) {
	for _, slot := range managerBindingSlots() {
		t.Run(slot.name, func(t *testing.T) {
			f := newManagerBindingFixture(t)
			for _, each := range managerBindingSlots() {
				each.install(f.m, f.a)
			}
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			baseline := aiCheckpointBytes(t, f.m, f.c)
			mp, sp, registered := f.m.checkpointCallbacks, f.m.Strategic.checkpointCallbacks, f.c.bindings
			slot.ordinary(f.m) // even the identical returned function invalidates its slot
			managerBindingRefusal(t, f.m, f.c, slot.name)
			if err := f.register(); err == nil || f.c.bindings != registered {
				t.Fatal("repeat repaired proof")
			}
			cleared := 0
			for i, p := range mp {
				if p != f.m.checkpointCallbacks[i] {
					cleared++
					if f.m.checkpointCallbacks[i] != (checkpointManagerCallbackProof{}) {
						t.Fatal("ordinary proof retained")
					}
				}
			}
			for i, p := range sp {
				if p != f.m.Strategic.checkpointCallbacks[i] {
					cleared++
					if f.m.Strategic.checkpointCallbacks[i] != (checkpointStrategicCallbackProof{}) {
						t.Fatal("ordinary strategic proof retained")
					}
				}
			}
			if cleared != 1 {
				t.Fatalf("cleared %d slots", cleared)
			}
			for _, a := range []*checkpoint.BindingAuthority{nil, checkpoint.NewBindingAuthority()} {
				slot.install(f.m, a)
				managerBindingRefusal(t, f.m, f.c, slot.name)
			}
			slot.install(f.m, f.a)
			if got := aiCheckpointBytes(t, f.m, f.c); !bytes.Equal(got, baseline) {
				t.Fatal("canonical reinstall changed payload")
			}
			slot.clear(f.m, f.a)
			absent := aiCheckpointBytes(t, f.m, f.c)
			diffs := 0
			for i, b := range baseline {
				if b != absent[i] {
					diffs++
					if b != 1 || absent[i] != 0 {
						t.Fatal("clear changed nonpresence data")
					}
				}
			}
			if diffs != 1 {
				t.Fatalf("clear changed %d bytes", diffs)
			}
			slot.ordinary(f.m)
			if !bytes.Equal(absent, aiCheckpointBytes(t, f.m, f.c)) {
				t.Fatal("ordinary nil changed absence")
			}
		})
	}
}

func TestCheckpointManagerBindingsAliasesAndAtomicity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*managerBindingFixture)
	}{
		{"Catalog", func(f *managerBindingFixture) { f.m.Catalog = &content.Catalog{} }},
		{"Strategic.Catalog", func(f *managerBindingFixture) { f.m.Strategic.Catalog = &content.Catalog{} }},
		{"Profile.appliedCatalog", func(f *managerBindingFixture) { f.m.Profile = &Profile{appliedCatalog: &content.Catalog{}} }},
		{"RNG", func(f *managerBindingFixture) { v := *f.stream; f.m.RNG = &v }},
		{"OrderBinding", func(f *managerBindingFixture) { f.m.OrderBinding = &orders.QueueBinding{} }},
		{"Survival", func(f *managerBindingFixture) { f.m.Survival = &SurvivalInfo{} }},
		{"Terrain", func(f *managerBindingFixture) { f.m.Terrain = &world.Terrain{} }},
		{"Ext", func(f *managerBindingFixture) { f.m.Ext = checkpointPanicController{} }},
		{"Planner", func(f *managerBindingFixture) { f.m.Planner = checkpointUnknownPlanner{} }},
		{"ConstructionRules", func(f *managerBindingFixture) { f.m.ConstructionRules = checkpointUnknownConstructionRules{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagerBindingFixture(t)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			registered := f.c.bindings
			tc.edit(f)
			managerBindingRefusal(t, f.m, f.c, tc.name)
			if err := f.register(); err == nil || f.c.bindings != registered {
				t.Fatal("failed repeat changed context")
			}
			fresh := NewCheckpointContext(f.c.Units, f.c.World)
			if err := fresh.SetBindings(f.m, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil || fresh.bindings != nil {
				t.Fatal("failed first registration changed context")
			}
		})
	}
	f := newManagerBindingFixture(t)
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	registered := f.c.bindings
	copyManager := *f.m
	other := newManagerBindingFixture(t)
	for _, next := range []checkpointManagerBindings{
		{&copyManager, f.inputs, f.inputs.Catalog(), f.stream, f.orders, f.survival, f.a},
		{f.m, other.inputs, other.inputs.Catalog(), f.stream, f.orders, f.survival, f.a},
		{f.m, f.inputs, f.inputs.Catalog(), other.stream, f.orders, f.survival, f.a},
		{f.m, f.inputs, f.inputs.Catalog(), f.stream, other.orders, f.survival, f.a},
		{f.m, f.inputs, f.inputs.Catalog(), f.stream, f.orders, other.survival, f.a},
		{f.m, f.inputs, f.inputs.Catalog(), f.stream, f.orders, f.survival, other.a},
	} {
		if err := f.c.SetBindings(next.manager, next.inputs, next.rng, next.orders, next.survival, next.authority); err == nil || f.c.bindings != registered {
			t.Fatal("conflicting tuple accepted")
		}
	}
	for _, args := range []struct {
		m  *Manager
		in *content.SimulationInputs
		a  *checkpoint.BindingAuthority
	}{
		{nil, f.inputs, f.a}, {f.m, nil, f.a}, {f.m, f.inputs, nil},
	} {
		fresh := NewCheckpointContext(f.c.Units, f.c.World)
		if err := fresh.SetBindings(args.m, args.in, f.stream, f.orders, f.survival, args.a); err == nil || fresh.bindings != nil {
			t.Fatal("missing required argument accepted")
		}
	}
	if err := (*CheckpointContext)(nil).SetBindings(f.m, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil {
		t.Fatal("nil context accepted")
	}
	*f.inputs = *other.inputs
	managerBindingRefusal(t, f.m, f.c, "Catalog")
	if err := f.register(); err == nil || f.c.bindings != registered {
		t.Fatal("overwritten input refreshed original alias")
	}
}

func TestCheckpointManagerBindingsCopies(t *testing.T) {
	f := newManagerBindingFixture(t)
	for _, slot := range managerBindingSlots() {
		slot.install(f.m, f.a)
	}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	copied := *f.m
	managerBindingRefusal(t, &copied, f.c, "ai.bindings")
	fresh := NewCheckpointContext(f.c.Units, f.c.World)
	if err := fresh.SetBindings(&copied, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil {
		t.Fatal("copied manager inherited callback ownership")
	}
	for _, slot := range managerBindingSlots()[:9] {
		slot.install(&copied, f.a)
	}
	if err := fresh.SetBindings(&copied, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil || !strings.Contains(err.Error(), "Strategic.energyEnvironment") {
		t.Fatalf("copied Strategic accepted: %v", err)
	}
	for _, slot := range managerBindingSlots()[9:] {
		slot.install(&copied, f.a)
	}
	if err := fresh.SetBindings(&copied, f.inputs, f.stream, f.orders, f.survival, f.a); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aiCheckpointBytes(t, f.m, f.c), aiCheckpointBytes(t, &copied, fresh)) {
		t.Fatal("owner address entered payload")
	}
	f.m.Strategic = copied.Strategic
	managerBindingRefusal(t, f.m, f.c, "Strategic.energyEnvironment")
	f.m.Strategic.BindEnergyEnvironmentWithCheckpointBinding(f.m.Strategic.energyEnvironment, f.a)
	managerBindingRefusal(t, f.m, f.c, "Strategic.rebuildRegistry")
	f.m.Strategic.BindTargetRegistryRebuildWithCheckpointBinding(f.m.Strategic.rebuildRegistry, f.a)
	aiCheckpointBytes(t, f.m, f.c)
}

func TestCheckpointManagerBindingsProfileAndPurity(t *testing.T) {
	f := newManagerBindingFixture(t)
	p := &Profile{Plan: DifficultyEasy, Weight: map[string]int32{"raw": -7}, appliedCatalog: f.inputs.Catalog()}
	f.m.Profile = p
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	baseline := aiCheckpointBytes(t, f.m, f.c)
	before := *f.stream
	for i := 0; i < 3; i++ {
		aiCheckpointBytes(t, f.m, f.c)
	}
	if *f.stream != before || p.Weight["raw"] != -7 || p.recordIDs != nil || f.m.Strategic.Counts != nil {
		t.Fatal("capture applied profile or initialized strategic state")
	}
	p.appliedCatalog = nil
	if bytes.Equal(baseline, aiCheckpointBytes(t, f.m, f.c)) {
		t.Fatal("applied catalog presence omitted")
	}
	p.appliedCatalog = f.inputs.Catalog()
	if !bytes.Equal(baseline, aiCheckpointBytes(t, f.m, f.c)) {
		t.Fatal("restored memo changed payload")
	}
	p.SetDifficulty(DifficultyHard)
	if p.appliedCatalog != nil {
		t.Fatal("difficulty did not clear memo")
	}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	aiCheckpointBytes(t, f.m, f.c)
	f.m.Profile = nil // Profile is mutable serialized state, not an expected pointer.
	aiCheckpointBytes(t, f.m, f.c)
	f.m.RNG, f.m.OrderBinding, f.m.Survival = nil, nil, nil
	c := NewCheckpointContext(f.c.Units, f.c.World)
	if err := c.SetBindings(f.m, f.inputs, nil, nil, nil, f.a); err != nil {
		t.Fatal(err)
	}
	aiCheckpointBytes(t, f.m, c)
}

func TestCheckpointManagerBindingsRallyOneShot(t *testing.T) {
	f := newManagerBindingFixture(t)
	b := RallyBattleBindings{
		Visible:        func(uint8, *units.Unit) bool { panic("visible invoked") },
		ProbeKnown:     func(uint8, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("known invoked") },
		ShotTimeAdmits: func(*units.Unit, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("shot invoked") },
	}
	terrain := &world.Terrain{CellW: 0x10000001, CellH: -3}
	f.c.World.Terrain = terrain
	if !f.m.InitializeBattleStateWithCheckpointBinding(terrain, b, f.a) {
		t.Fatal("first initialization refused")
	}
	// Existing int32 multiplication wraps before division, and coordinates
	// retain the signed high-word conversion [08 R-AI-02 §1].
	want := [3]numeric.Fixed{8 << 16, 0, -24 << 16}
	for _, got := range [][3]numeric.Fixed{{f.m.rallyBestX, f.m.rallyBestY, f.m.rallyBestZ}, {f.m.rallyProbeX, f.m.rallyProbeY, f.m.rallyProbeZ}, {f.m.rallyDriftX, f.m.rallyDriftY, f.m.rallyDriftZ}} {
		if got != want {
			t.Fatalf("rally vector %v want %v", got, want)
		}
	}
	if err := f.register(); err != nil {
		t.Fatal(err)
	}
	before := f.m.checkpointCallbacks
	payload := aiCheckpointBytes(t, f.m, f.c)
	if f.m.InitializeBattleStateWithCheckpointBinding(&world.Terrain{}, RallyBattleBindings{}, checkpoint.NewBindingAuthority()) || before != f.m.checkpointCallbacks || !bytes.Equal(payload, aiCheckpointBytes(t, f.m, f.c)) {
		t.Fatal("rejected repeat changed state/proof")
	}
	f.m.SetRallyVisible(b.Visible)
	invalidated := f.m.checkpointCallbacks
	if f.m.InitializeBattleStateWithCheckpointBinding(terrain, b, f.a) || invalidated != f.m.checkpointCallbacks {
		t.Fatal("rejected repeat repaired proof")
	}
	managerBindingRefusal(t, f.m, f.c, "RallyVisible")
	g := newManagerBindingFixture(t)
	g.c.World.Terrain = terrain
	if g.m.InitializeBattleStateWithCheckpointBinding(nil, b, g.a) || g.m.rallyInitialized || g.m.checkpointCallbacks != ([9]checkpointManagerCallbackProof{}) {
		t.Fatal("nil terrain changed initialization")
	}
	if (*Manager)(nil).InitializeBattleStateWithCheckpointBinding(terrain, b, f.a) {
		t.Fatal("nil manager accepted")
	}
	if !g.m.InitializeBattleStateWithCheckpointBinding(terrain, b, nil) {
		t.Fatal("nil authority changed gameplay initialization")
	}
	if err := g.register(); err == nil {
		t.Fatal("nil authority stamped rally")
	}
	h := newManagerBindingFixture(t)
	h.c.World.Terrain = terrain
	if !h.m.InitializeBattleStateWithCheckpointBinding(terrain, RallyBattleBindings{}, h.a) || h.m.checkpointCallbacks != ([9]checkpointManagerCallbackProof{}) {
		t.Fatal("nil rally callbacks retained proof")
	}
	if err := h.register(); err != nil {
		t.Fatal(err)
	}
	var nilStrategic *Strategic
	nilStrategic.BindEnergyEnvironmentWithCheckpointBinding(func() (float32, float32) { panic("nil receiver invoked") }, f.a)
	nilStrategic.BindTargetRegistryRebuildWithCheckpointBinding(func(uint32, uint8) { panic("nil receiver invoked") }, f.a)
}

// A single present slot pins its exact lexical position independently of the
// all-present vector. Optional Profile is absent in this 459-byte fixture.
func TestCheckpointManagerBindingsSlotPositions(t *testing.T) {
	positions := [...]int{4, 239, 240, 266, 268, 269, 270, 368, 369, 349, 357}
	for i, slot := range managerBindingSlots() {
		t.Run(slot.name, func(t *testing.T) {
			f := newManagerBindingFixture(t)
			slot.install(f.m, f.a)
			if err := f.register(); err != nil {
				t.Fatal(err)
			}
			want := make([]byte, 459)
			// Catalog, Factory's table ID, OrderBinding, RNG, Strategic.Catalog, Survival.
			for _, index := range []int{5, 197, 245, 267, 280, 366, positions[i]} {
				want[index] = 1
			}
			if got := aiCheckpointBytes(t, f.m, f.c); !bytes.Equal(got, want) {
				t.Fatalf("slot %s: got (%d) %x want (%d) %x", slot.name, len(got), got, len(want), want)
			}
		})
	}
}

func TestCheckpointManagerBindingsOrdinaryConstructionAndRally(t *testing.T) {
	f := newManagerBindingFixture(t)
	for _, slot := range managerBindingSlots() {
		slot.install(f.m, f.a)
	}
	// Config copies functions but never their Manager proof or invokes them.
	m := NewManager(ManagerConfig{
		Catalog: f.inputs.Catalog(), Strategic: f.m.Strategic,
		RNG: f.stream, OrderBinding: f.orders, Survival: f.survival,
		CanPursueAir: f.m.CanPursueAirHook(), IsAlliance: f.m.IsAllianceHook(),
		JammerSuppresses: f.m.JammerSuppressesHook(), QueueBuildTyped: f.m.QueueBuildTypedHook(),
		RallyProbeKnown: f.m.RallyProbeKnownHook(), RallyShotTimeAdmits: f.m.RallyShotTimeAdmitsHook(),
		RallyVisible: f.m.RallyVisibleHook(), UnitVisible: f.m.UnitVisibleHook(), WeaponMaintenance: f.m.WeaponMaintenanceHook(),
	})
	if m.checkpointCallbacks != ([9]checkpointManagerCallbackProof{}) {
		t.Fatal("ordinary config transferred proof")
	}
	if err := f.c.SetBindings(m, f.inputs, f.stream, f.orders, f.survival, f.a); err == nil {
		t.Fatal("ordinary config admitted")
	}
	before := f.m.checkpointCallbacks
	terrain := &world.Terrain{CellW: 3, CellH: 5}
	b := RallyBattleBindings{Visible: f.m.RallyVisibleHook(), ProbeKnown: f.m.RallyProbeKnownHook(), ShotTimeAdmits: f.m.RallyShotTimeAdmitsHook()}
	if !f.m.InitializeBattleStateWithCheckpointBinding(terrain, b, nil) {
		t.Fatal("nil authority rejected initialization")
	}
	for i, p := range f.m.checkpointCallbacks {
		if i >= checkpointRallyProbeKnown && i <= checkpointRallyVisible {
			if p != (checkpointManagerCallbackProof{}) {
				t.Fatal("rally slot kept stale proof")
			}
		} else if p != before[i] {
			t.Fatal("rally initialization changed another slot")
		}
	}
	f.c.World.Terrain = terrain
	if err := f.register(); err == nil || !strings.Contains(err.Error(), "RallyProbeKnown") {
		t.Fatalf("rally without authority admitted: %v", err)
	}
}
