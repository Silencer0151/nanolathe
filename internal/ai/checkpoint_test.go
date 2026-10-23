package ai

import (
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func aiCheckpointContext(t *testing.T, defs ...*content.UnitDef) *CheckpointContext {
	t.Helper()
	cat := &content.Catalog{Units: make(map[string]*content.UnitDef)}
	for i, d := range defs {
		d.ObjectName = "fixture"
		cat.Units[string(rune('a'+i))] = d
	}
	root := t.TempDir()
	if len(defs) > 0 {
		data, err := formats.EncodeThreeDO(&formats.ThreeDO{Root: 0, Objects: []formats.ThreeDOObject{{Version: 1, Name: "base", Selection: -1, Parent: -1, FirstChild: -1, NextSibling: -1, Vertices: []formats.ThreeDOVertex{{}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "objects3d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "objects3d", "fixture.3do"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs := vfs.New()
	if err := fs.MountDirectory(root, 10); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	sources, err := content.CaptureSimulationSources(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := content.FreezeSimulationInputs(sources, content.SimulationInputRequest{Catalog: cat})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := inputs.CheckpointKeys()
	if err != nil {
		t.Fatal(err)
	}
	return NewCheckpointContext(units.NewCheckpointContext(keys), world.NewCheckpointContext(keys))
}
func aiCheckpointBytes(t *testing.T, m *Manager, c *CheckpointContext) []byte {
	t.Helper()
	if _, err := m.CollectCheckpointReferences(c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := m.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func aiCheckpointHex(t *testing.T, s string) []byte {
	t.Helper()
	v, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Independent full-owner vector: absent binding tags, definition-independent
// factory reference, lexical group order, optional public start assignment,
// source widths and the complete zero Strategic and resolved Community leaves.
func TestCheckpointManagerVector(t *testing.T) {
	m := &Manager{BattleSeed: 0x01020304, Controller: ControllerModern, ControllerParams: "p!", Factory: &units.Unit{Handle: 9, AllocationSerial: 1},
		GroupConstruction: []pool.Handle{0xf001}, GroupExplore: []pool.Handle{0xf002}, GroupNull: []pool.Handle{0xf003}, GroupRally: []pool.Handle{0xf004},
		GroupRegroupA: []pool.Handle{0xf005}, GroupRegroupB: []pool.Handle{0xf006}, GroupResource: []pool.Handle{0xf007}, GroupWaveA: []pool.Handle{0xf008}, GroupWaveB: []pool.Handle{0xf009},
		MissionGateFlag: -1, OriginX: -2, OriginZ: 1<<40 + 3, Passive: true, Player: 7, StartOwners: []int8{-1, 7}, StartPositions: [][2]int32{{-3, 4}}, SurfaceMetal: -4, countdown: 10,
		modernWaveAir: true, rallyBestScore: -5, rallyBestX: -6, rallyBestY: 7, rallyBestZ: 8, rallyDriftX: 9, rallyDriftY: -10, rallyDriftZ: 11, rallyInitialized: true,
		rallyProbeX: 12, rallyProbeY: 13, rallyProbeZ: -14, rallyTargets: []pool.Handle{0xffff, 0}, unitLossDeadline: 15, waveAEngaged: true}
	for i := range m.Deadlines {
		m.Deadlines[i] = uint32(i)
	}
	got := aiCheckpointBytes(t, m, aiCheckpointContext(t))
	want := aiCheckpointHex(t, "040302010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000102000000702100000000010000000200000003000000040000000500000006000000070000000800000009000000000100010000000100000001f000000100000002f000000100000003f000000100000004f000000100000005f000000100000006f000000100000007f000000100000008f000000100000009f000000000ffffffff00feffffffffffffff03000000000100000100070000000000000102000000ff0701000000fdffffff040000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000fcffffff000000000a01fbfffffffaffffffffffffff070000000000000008000000000000000900000000000000f6ffffffffffffff0b00000000000000010c000000000000000d00000000000000f2ffffffffffffff02000000ffff0000000000000f0000000100")
	if !bytes.Equal(got, want) {
		t.Fatalf("got (%d) %x\nwant (%d) %x", len(got), got, len(want), want)
	}
}

func TestCheckpointManagerRetainedState(t *testing.T) {
	c := aiCheckpointContext(t)
	baseline := aiCheckpointBytes(t, &Manager{}, c)
	for name, edit := range map[string]func(*Manager){
		"player": func(m *Manager) { m.Player = 2 }, "passive": func(m *Manager) { m.Passive = true }, "controller": func(m *Manager) { m.Controller = ControllerModern },
		"parameters": func(m *Manager) { m.ControllerParams = "raw" }, "seed": func(m *Manager) { m.BattleSeed = 1 }, "community": func(m *Manager) { m.Community.AIStockpileProducts = true },
		"difficulty reversed": func(m *Manager) { m.Community.AIDifficultyReversed = true },
		"deadline":            func(m *Manager) { m.Deadlines[9] = 1 }, "countdown": func(m *Manager) { m.countdown = 1 }, "loss": func(m *Manager) { m.unitLossDeadline = 1 },
		"placement origin": func(m *Manager) { m.OriginZ = 1 << 40 }, "surface": func(m *Manager) { m.SurfaceMetal = -1 }, "mission": func(m *Manager) { m.MissionGateFlag = -1 },
		"wave air": func(m *Manager) { m.modernWaveAir = true }, "wave A": func(m *Manager) { m.waveAEngaged = true }, "wave B": func(m *Manager) { m.waveBEngaged = true },
		"rally ready": func(m *Manager) { m.rallyInitialized = true }, "rally score": func(m *Manager) { m.rallyBestScore = -1 }, "rally coordinate": func(m *Manager) { m.rallyDriftY = -1 },
		"rally raw": func(m *Manager) { m.rallyTargets = []pool.Handle{0xffff} }, "starts": func(m *Manager) { m.StartPositions = [][2]int32{{-1, 2}} },
		"empty public assignments": func(m *Manager) { m.StartOwners = []int8{} }, "profile presence": func(m *Manager) { m.Profile = &Profile{} },
	} {
		t.Run(name, func(t *testing.T) {
			m := &Manager{}
			edit(m)
			if bytes.Equal(baseline, aiCheckpointBytes(t, m, c)) {
				t.Fatal("retained mutation vanished")
			}
		})
	}
	m := &Manager{GroupResource: []pool.Handle{9, 2, 9}}
	first := aiCheckpointBytes(t, m, c)
	m.GroupResource = []pool.Handle{2, 9, 9}
	if bytes.Equal(first, aiCheckpointBytes(t, m, c)) {
		t.Fatal("group order collapsed")
	}
}

func TestCheckpointManagerReferencesAndPurity(t *testing.T) {
	c := aiCheckpointContext(t)
	u := &units.Unit{Handle: 9, AllocationSerial: 1}
	m := &Manager{Factory: u, GroupWaveA: []pool.Handle{0xffff, 0}, rallyTargets: []pool.Handle{9}}
	if n, err := m.CollectCheckpointReferences(c); n != 1 || err != nil {
		t.Fatalf("collect %d %v", n, err)
	}
	if n, err := m.CollectCheckpointReferences(c); n != 0 || err != nil {
		t.Fatalf("recollect %d %v", n, err)
	}
	if got := c.Units.Allocations.Values(); !reflect.DeepEqual(got, []*units.Unit{u}) {
		t.Fatal("raw handles became graph roots")
	}
	first := aiCheckpointBytes(t, m, c)
	copyUnit := *u
	m.Factory = &copyUnit
	if n, err := m.CollectCheckpointReferences(c); n != 1 || err != nil {
		t.Fatal("same-handle allocation collapsed")
	}
	if bytes.Equal(first, aiCheckpointBytes(t, m, c)) {
		t.Fatal("factory identity omitted")
	}
	relocated := *m
	if !bytes.Equal(first, aiCheckpointBytes(t, &relocated, aiCheckpointContext(t))) {
		t.Fatal("pointer address entered bytes")
	}
	fresh := NewCheckpointContext(units.NewCheckpointContext(c.Units.Keys), c.World)
	if err := m.WriteCheckpoint(checkpoint.NewEncoder(io.Discard), fresh); err == nil {
		t.Fatal("writer discovered factory")
	}
	before := *m
	before.GroupWaveA = append([]pool.Handle(nil), m.GroupWaveA...)
	before.rallyTargets = append([]pool.Handle(nil), m.rallyTargets...)
	aiCheckpointBytes(t, m, c)
	if !reflect.DeepEqual(before, *m) {
		t.Fatal("capture changed manager")
	}
	baseline := aiCheckpointBytes(t, m, c)
	m.broadcastWalk = []*units.Unit{u}
	m.hostileWalk = []*units.Unit{u}
	m.rallyWalk = []*units.Unit{u}
	m.classifyWalk = []*units.Unit{u}
	m.strategicTypes = []string{"foreign"}
	m.strategicTypesFor = &content.Catalog{}
	resume := uint64(99)
	m.ResumeGenerator = &resume
	m.Shared = &BattleShared{v: func() { panic("worker accessed") }}
	if !bytes.Equal(baseline, aiCheckpointBytes(t, m, c)) {
		t.Fatal("excluded scratch or worker material encoded")
	}
	terrain := &world.Terrain{}
	c.World.Terrain = terrain
	m.Terrain = terrain
	if bytes.Equal(baseline, aiCheckpointBytes(t, m, c)) {
		t.Fatal("terrain presence omitted")
	}
	m.Terrain = &world.Terrain{CellW: 1}
	if _, err := m.CollectCheckpointReferences(c); err == nil {
		t.Fatal("foreign terrain accepted")
	}
}

type checkpointPanicController struct{}

func (checkpointPanicController) ControllerCheckpoint() ControllerCheckpoint {
	panic("controller called")
}
func (checkpointPanicController) WriteControllerCheckpoint(*checkpoint.Encoder, *CheckpointContext) error {
	panic("controller called")
}

func TestCheckpointManagerBindingRefusals(t *testing.T) {
	c := aiCheckpointContext(t)
	for name, edit := range map[string]func(*Manager){
		"CanPursueAir":      func(m *Manager) { m.SetCanPursueAir(func(*units.Unit, *units.Unit) bool { panic("called") }) },
		"Catalog":           func(m *Manager) { m.Catalog = &content.Catalog{} },
		"ConstructionRules": func(m *Manager) { m.ConstructionRules = checkpointUnknownConstructionRules{} },
		"Ext":               func(m *Manager) { m.Ext = checkpointPanicController{} },
		"IsAlliance":        func(m *Manager) { m.SetIsAlliance(func(uint8, uint8) bool { panic("called") }) },
		"JammerSuppresses":  func(m *Manager) { m.SetJammerSuppresses(func(uint8, uint8) bool { panic("called") }) },
		"OrderBinding":      func(m *Manager) { m.OrderBinding = &orders.QueueBinding{} },
		"Planner":           func(m *Manager) { m.Planner = checkpointUnknownPlanner{} },
		"QueueBuildTyped":   func(m *Manager) { m.SetQueueBuildTyped(func(BuildRequest) error { panic("called") }) },
		"RNG":               func(m *Manager) { m.RNG = &rng.Simulation{} },
		"RallyProbeKnown": func(m *Manager) {
			m.SetRallyProbeKnown(func(uint8, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("called") })
		},
		"RallyShotTimeAdmits": func(m *Manager) {
			m.SetRallyShotTimeAdmits(func(*units.Unit, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool { panic("called") })
		},
		"RallyVisible":                func(m *Manager) { m.SetRallyVisible(func(uint8, *units.Unit) bool { panic("called") }) },
		"Survival":                    func(m *Manager) { m.Survival = &SurvivalInfo{} },
		"UnitVisible":                 func(m *Manager) { m.SetUnitVisible(func(uint8, *units.Unit) bool { panic("called") }) },
		"WeaponMaintenance":           func(m *Manager) { m.SetWeaponMaintenance(func(uint8) { panic("called") }) },
		"Strategic.Catalog":           func(m *Manager) { m.Strategic.Catalog = &content.Catalog{} },
		"Strategic.energyEnvironment": func(m *Manager) { m.Strategic.energyEnvironment = func() (float32, float32) { panic("called") } },
		"Strategic.rebuildRegistry":   func(m *Manager) { m.Strategic.rebuildRegistry = func(uint32, uint8) { panic("called") } },
		"Profile.appliedCatalog":      func(m *Manager) { m.Profile = &Profile{appliedCatalog: &content.Catalog{}} },
	} {
		t.Run(name, func(t *testing.T) {
			m := &Manager{}
			edit(m)
			if _, err := m.CollectCheckpointReferences(c); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("collect %v", err)
			}
			var out bytes.Buffer
			if err := m.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("write %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("binding refusal wrote bytes")
			}
			if err := m.AppendCheckpointSummary(&checkpoint.Summary{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	var typedNil *RetailPlanner
	m := &Manager{Planner: typedNil}
	if _, err := m.CollectCheckpointReferences(c); err == nil {
		t.Fatal("typed nil planner accepted")
	}
	for _, bad := range []*CheckpointContext{nil, {}, NewCheckpointContext(c.Units, nil), NewCheckpointContext(units.NewCheckpointContext(nil), c.World)} {
		if _, err := (&Manager{}).CollectCheckpointReferences(bad); err == nil {
			t.Fatal("missing context accepted")
		}
	}
	if _, err := (*Manager)(nil).CollectCheckpointReferences(c); err == nil {
		t.Fatal("nil manager accepted")
	}
	if err := (&Manager{}).WriteCheckpoint(nil, c); err == nil {
		t.Fatal("nil encoder accepted")
	}
}
