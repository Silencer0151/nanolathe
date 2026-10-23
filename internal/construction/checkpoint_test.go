package construction

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/internal/movement"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// Two independently admitted same-name objects ensure that placement identity
// is its frozen catalog ordinal, never a name-based lookup or pointer address.
func constructionCheckpointFixture(t *testing.T) (*CheckpointContext, [2]*content.UnitDef) {
	t.Helper()
	defs := [2]*content.UnitDef{}
	for i := range defs {
		defs[i] = &content.UnitDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "same"}, UnitName: "same", ObjectName: "fixture"}
	}
	cat := &content.Catalog{Units: map[string]*content.UnitDef{"a": defs[0], "b": defs[1]}}
	data, err := formats.EncodeThreeDO(&formats.ThreeDO{Root: 0, Objects: []formats.ThreeDOObject{{
		Version: 1, Name: "base", Selection: -1, Parent: -1, FirstChild: -1, NextSibling: -1,
		Vertices: []formats.ThreeDOVertex{{}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "objects3d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "objects3d", "fixture.3do"), data, 0o644); err != nil {
		t.Fatal(err)
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
	return NewCheckpointContext(orders.NewCheckpointContext(units.NewCheckpointContext(keys)), world.NewCheckpointContext(keys)), defs
}

func constructionCheckpointBytes(t *testing.T, s *Service, c *CheckpointContext) []byte {
	t.Helper()
	if n, err := s.CollectCheckpointReferences(c); err != nil || n != 0 {
		t.Fatalf("collect = %d, %v", n, err)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func checkpointRect(t *testing.T, x, z, width, depth int32) world.FootprintRect {
	t.Helper()
	extent, err := world.NewFootprintExtent(width, depth)
	if err != nil {
		t.Fatal(err)
	}
	rect, err := world.NewFootprintRect(world.NewFootprintAnchor(x, z), extent)
	if err != nil {
		t.Fatal(err)
	}
	return rect
}

// Independent authored payload vector: lexical source fields, u32 raw handles,
// i64 Go int/fixed positions, full physical arrays, absent bindings, and exact
// creation yard bytes. The two equal-name definitions have distinct ordinals;
// the zero rectangle remains distinct from an initialized empty rectangle.
func TestCheckpointConstructionVector(t *testing.T) {
	c, defs := constructionCheckpointFixture(t)
	terrain := &world.Terrain{}
	c.World.Terrain = terrain
	s := &Service{ModeSelector: 0x0102030405060708, Terrain: terrain,
		builderLinks: map[pool.Handle]pool.Handle{256: 3, 0: 65535, 2: 258},
		kickRecords:  []kickRecord{{x: -1, y: 1 << 40, z: 3}, {}, {x: 0x0102030405060708, y: -2, z: -(1 << 42), valid: true}},
		placements: map[pool.Handle]placementRecord{
			256: {def: defs[1], rect: checkpointRect(t, 0, 0, 0, 0)},
			2:   {def: defs[0], rect: checkpointRect(t, -3, 4, 2, 3), yard: []world.YardCell{0x80, 3, 0xfe}},
			0:   {},
		},
		repairBanks: [][2]repairBank{{{target: 65535, remainder: -2}, {remainder: 42}}, {}, {{target: 258, remainder: 0x01020304}, {target: 3, remainder: -0x01020304}}},
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// Four absent ports, shared Community zero value (144 bytes), three
	// absent ports, ModeSelector, six absent ports, terrain present, world absent.
	want := make([]byte, 4+144+3)
	want = append(want, decode("08070605040302010000000000000100")...)
	want = append(want, decode("03000000"+"00000000ffff0000"+"0200000002010000"+"0001000003000000")...) // sorted links
	want = append(want, decode("03000000"+"00ffffffffffffffff00000000000100000300000000000000")...)
	want = append(want, make([]byte, 25)...)
	want = append(want, decode("010807060504030201feffffffffffffff0000000000fcffff")...)
	want = append(want, decode("03000000"+"0000000000")...) // placements, key zero, absent def
	want = append(want, make([]byte, 25+4)...)
	want = append(want, decode("0200000001010100000009000000756e69742f73616d65"+
		"fdffffff04000000030000000102000000ffffffff07000000"+"030000008003fe")...)
	want = append(want, decode("0001000001010200000009000000756e69742f73616d65"+
		"00000000000000000000000001000000000000000000000000"+"00000000")...)
	want = append(want, decode("03000000")...) // bank row count
	want = append(want, decode("feffffffffff00002a00000000000000")...)
	want = append(want, make([]byte, 16)...)
	want = append(want, decode("0403020102010000fcfcfdfe03000000"+"00")...)
	if got := constructionCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatalf("payload\ngot  %x\nwant %x", got, want)
	}
}

func TestCheckpointConstructionRetainedMutations(t *testing.T) {
	c, defs := constructionCheckpointFixture(t)
	fresh := func() *Service {
		return &Service{builderLinks: map[pool.Handle]pool.Handle{2: 3}, placements: map[pool.Handle]placementRecord{
			2: {def: defs[0], rect: checkpointRect(t, 1, 2, 3, 4), yard: []world.YardCell{1, 2}},
		}, repairBanks: make([][2]repairBank, 3), kickRecords: make([]kickRecord, 3)}
	}
	base := constructionCheckpointBytes(t, fresh(), c)
	for _, test := range []struct {
		name string
		edit func(*Service)
	}{
		{"mode", func(s *Service) { s.ModeSelector = -(1 << 40) }},
		{"community", func(s *Service) { s.Community.StructureRotation = true }},
		{"link value", func(s *Service) { s.builderLinks[2] = 65535 }},
		{"link key", func(s *Service) { delete(s.builderLinks, 2); s.builderLinks[1] = 3 }},
		{"placement key", func(s *Service) { s.placements[3] = s.placements[2]; delete(s.placements, 2) }},
		{"definition ordinal", func(s *Service) { row := s.placements[2]; row.def = defs[1]; s.placements[2] = row }},
		{"definition presence", func(s *Service) { row := s.placements[2]; row.def = nil; s.placements[2] = row }},
		{"rectangle", func(s *Service) {
			row := s.placements[2]
			row.rect = checkpointRect(t, -1, 2, 3, 4)
			s.placements[2] = row
		}},
		{"creation yard", func(s *Service) { s.placements[2].yard[1] = 0xff }},
		{"yard length", func(s *Service) { row := s.placements[2]; row.yard = append(row.yard, 0); s.placements[2] = row }},
		{"first bank target", func(s *Service) { s.repairBanks[0][0].target = 65535 }},
		{"second bank remainder", func(s *Service) { s.repairBanks[2][1].remainder = -1 }},
		{"bank row count", func(s *Service) { s.repairBanks = append(s.repairBanks, [2]repairBank{}) }},
		{"invalid kick x", func(s *Service) { s.kickRecords[2].x = 1 << 40 }},
		{"invalid kick y", func(s *Service) { s.kickRecords[2].y = -(1 << 40) }},
		{"invalid kick z", func(s *Service) { s.kickRecords[0].z = 1 }},
		{"kick validity", func(s *Service) { s.kickRecords[2].valid = true }},
		{"kick row count", func(s *Service) { s.kickRecords = append(s.kickRecords, kickRecord{}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := fresh()
			test.edit(s)
			if got := constructionCheckpointBytes(t, s, c); bytes.Equal(got, base) {
				t.Fatal("retained mutation did not change payload")
			}
		})
	}
}

func TestCheckpointConstructionMapOrderAndIdentity(t *testing.T) {
	c, defs := constructionCheckpointFixture(t)
	makeState := func(keys []pool.Handle) *Service {
		s := &Service{builderLinks: make(map[pool.Handle]pool.Handle), placements: make(map[pool.Handle]placementRecord)}
		for _, key := range keys {
			s.builderLinks[key] = key + 1
			s.placements[key] = placementRecord{def: defs[key%2], yard: []world.YardCell{world.YardCell(key)}}
		}
		return s
	}
	a, b := makeState([]pool.Handle{256, 2, 1}), makeState([]pool.Handle{1, 2, 256})
	want := constructionCheckpointBytes(t, a, c)
	for i := 0; i < 25; i++ {
		if got := constructionCheckpointBytes(t, b, c); !bytes.Equal(got, want) {
			t.Fatal("map insertion or iteration changed bytes")
		}
	}
	foreign := *defs[0]
	b.placements[256] = placementRecord{def: &foreign}
	b.placements[2] = placementRecord{def: &foreign}
	for i := 0; i < 25; i++ {
		if _, err := b.CollectCheckpointReferences(c); err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint capture failed: logical path construction.Service.placements[2].def") {
			t.Fatalf("foreign same-name identity / deterministic failure = %v", err)
		}
	}
	var out bytes.Buffer
	e := checkpoint.NewEncoder(&out)
	err := b.WriteCheckpoint(e, c)
	if err == nil || !strings.Contains(err.Error(), "placements[2].def") {
		t.Fatalf("writer accepted foreign definition: %v", err)
	}
	n := out.Len()
	e.U8(1)
	if out.Len() != n || e.Err() != err {
		t.Fatal("failure was not sticky")
	}
}

func TestCheckpointConstructionExclusionsAndPurity(t *testing.T) {
	c, defs := constructionCheckpointFixture(t)
	s := &Service{
		builderLinks: map[pool.Handle]pool.Handle{3: 2},
		placements:   map[pool.Handle]placementRecord{3: {def: defs[0], rect: checkpointRect(t, 1, 2, 3, 4), yard: []world.YardCell{3, 4}}},
		repairBanks:  make([][2]repairBank, 3, 8), kickRecords: make([]kickRecord, 3, 8),
	}
	s.repairBanks[2][1] = repairBank{target: 3, remainder: -7}
	s.kickRecords[2] = kickRecord{x: -5, y: 7, z: 9}
	want := constructionCheckpointBytes(t, s, c)
	called := false
	s.OnRefresh = func(*units.Unit) { called = true }
	s.StatusText = func(string) { called = true }
	s.DebugBuilderIdentity = func(*units.Unit) uint64 { called = true; return 0 }
	s.RepairBankFallbacks = 99
	s.rotationCache = map[rotationCacheKey]StructureGeometry{{def: defs[0], facing: units.FacingSouth}: {Yard: []world.YardCell{0xff}}}
	s.rowsResolved = true
	s.stepDrivenRows, s.mobileWakeRows, s.getBuiltRow = []orders.ID{99, 98}, []orders.ID{97}, 96
	s.boundConstructionWake = func(*units.Unit, *orders.Node, uint32, uint32) (orders.Code, bool) { called = true; return 0, false }
	s.boundGetBuilt = s.boundConstructionWake
	s.messages = []string{"message"}
	s.admissions, s.admissionsStart, s.admissionsTotal = []AdmissionDiagnostic{{Reason: "reason"}}, 1, 2
	s.commands = []CommandDiagnostic{{Reason: "command"}}
	s.lastPermanent, s.hasPermanent = AdmissionDiagnostic{Reason: "permanent"}, true
	s.lastKill = KillInfo{Damage: 65535, Severity: -1, NoCorpse: true}
	banks := append([][2]repairBank(nil), s.repairBanks...)
	kicks := append([]kickRecord(nil), s.kickRecords...)
	yards := append([]world.YardCell(nil), s.placements[3].yard...)
	cache := s.rotationCache[rotationCacheKey{def: defs[0], facing: units.FacingSouth}]
	for i := 0; i < 3; i++ {
		if got := constructionCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
			t.Fatal("excluded state changed payload")
		}
	}
	if called || !reflect.DeepEqual(banks, s.repairBanks) || !reflect.DeepEqual(kicks, s.kickRecords) || !reflect.DeepEqual(yards, s.placements[3].yard) ||
		!reflect.DeepEqual(cache, s.rotationCache[rotationCacheKey{def: defs[0], facing: units.FacingSouth}]) || cap(s.repairBanks) != 8 || cap(s.kickRecords) != 8 || !s.rowsResolved || s.getBuiltRow != 96 {
		t.Fatal("capture called a sink or changed retained/derived storage")
	}
	if len(c.Orders.Units.Allocations.Values()) != 0 || len(c.Orders.Nodes.Values()) != 0 || len(c.Orders.Queues.Values()) != 0 {
		t.Fatal("construction introduced graph objects")
	}
	// Allocated-empty sequences have the same logical sequence as nil.
	a, b := &Service{}, &Service{builderLinks: map[pool.Handle]pool.Handle{}, placements: map[pool.Handle]placementRecord{}, repairBanks: make([][2]repairBank, 0, 5), kickRecords: make([]kickRecord, 0, 5)}
	if !bytes.Equal(constructionCheckpointBytes(t, a, c), constructionCheckpointBytes(t, b, c)) {
		t.Fatal("empty storage capacity leaked into payload")
	}
}

type checkpointConstructionPresentation struct{}

func (*checkpointConstructionPresentation) EmitNanolathe(frame.Event) bool {
	panic("checkpoint called presentation binding")
}

type checkpointConstructionRules struct{ Rules }

func constructionCheckpointBindings() []struct {
	field string
	set   func(*Service)
} {
	return []struct {
		field string
		set   func(*Service)
	}{
		{"Allocator", func(s *Service) {
			s.Allocator = func(uint8, *content.UnitDef, numeric.Fixed, numeric.Fixed, numeric.Fixed) (*units.Unit, error) {
				panic("allocator")
			}
		}},
		{"CRTRandom", func(s *Service) { s.SetCRTRandom(func(uint32) uint32 { panic("rng") }) }},
		{"Catalog", func(s *Service) { s.Catalog = &content.Catalog{} }},
		{"Combat", func(s *Service) { s.Combat = &combat.Service{} }},
		{"Economy", func(s *Service) { s.Economy = &economy.Service{} }},
		{"IsSpecialSecondState", func(s *Service) { s.SetIsSpecialSecondState(func(uint8) bool { panic("special") }) }},
		{"LimitChecker", func(s *Service) { s.LimitChecker = func(*units.Unit, string) bool { panic("limit") } }},
		{"ModelForFactory", func(s *Service) { s.SetModelForFactory(func(*units.Unit) *model.Model { panic("model") }) }},
		{"ModelForUnit", func(s *Service) { s.SetModelForUnit(func(*units.Unit) *model.Model { panic("model") }) }},
		{"Movement", func(s *Service) { s.Movement = &movement.System{} }},
		{"OrderBinding", func(s *Service) { s.OrderBinding = &orders.QueueBinding{} }},
		{"Presentation", func(s *Service) { s.Presentation = &checkpointConstructionPresentation{} }},
		{"Presentation", func(s *Service) { s.Presentation = (*checkpointConstructionPresentation)(nil) }},
		{"Rules", func(s *Service) { s.Rules = checkpointConstructionRules{} }},
		{"Rules", func(s *Service) { s.Rules = (*checkpointConstructionRules)(nil) }},
		{"World", func(s *Service) { s.World = &units.World{} }},
		{"repairWorld", func(s *Service) { s.repairWorld = &units.World{} }},
		{"completedInPump", func(s *Service) { s.completedInPump = 65535 }},
		{"reclaimStepNode", func(s *Service) { s.reclaimStepNode = &orders.Node{} }},
		{"vtolBuildStepOwner", func(s *Service) { s.vtolBuildStepOwner = &units.Unit{} }},
	}
}

func TestCheckpointConstructionRefusesBindingsAndActiveContexts(t *testing.T) {
	c, _ := constructionCheckpointFixture(t)
	for _, test := range constructionCheckpointBindings() {
		t.Run(test.field, func(t *testing.T) {
			s := &Service{}
			test.set(s)
			constructionCheckpointRefused(t, s, c, "construction.Service."+test.field)
		})
	}
	constructionCheckpointRefused(t, nil, c, "construction.Service")
	for _, invalid := range []*CheckpointContext{nil, {}, {Orders: c.Orders}, {World: c.World}, {Orders: &orders.CheckpointContext{}, World: c.World}, {Orders: orders.NewCheckpointContext(&units.CheckpointContext{}), World: c.World}} {
		constructionCheckpointRefused(t, &Service{}, invalid, "construction.context")
	}
	terrain := &world.Terrain{}
	constructionCheckpointRefused(t, &Service{Terrain: terrain}, c, "construction.Service.Terrain")
	c.World.Terrain = terrain
	constructionCheckpointRefused(t, &Service{}, c, "construction.Service.Terrain")
	constructionCheckpointRefused(t, &Service{Terrain: &world.Terrain{}}, c, "construction.Service.Terrain")
	if got := constructionCheckpointBytes(t, &Service{Terrain: terrain}, c); len(got) == 0 {
		t.Fatal("matched singleton terrain rejected")
	}
}

func constructionCheckpointRefused(t *testing.T, s *Service, c *CheckpointContext, path string) {
	t.Helper()
	n, err := s.CollectCheckpointReferences(c)
	if n != 0 || err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint capture failed: logical path "+path+",") {
		t.Fatalf("collect = %d, %v; want %s", n, err, path)
	}
	var out bytes.Buffer
	e := checkpoint.NewEncoder(&out)
	if err := s.WriteCheckpoint(e, c); err == nil || !strings.Contains(err.Error(), path) || out.Len() != 0 {
		t.Fatalf("write = %v, %d bytes; want pre-write refusal at %s", err, out.Len(), path)
	}
}

// Summary vector is independently listed in selected semantic order, which
// deliberately differs from the full payload's lexical order.
func TestCheckpointConstructionSummaryVector(t *testing.T) {
	s := &Service{
		repairBanks: [][2]repairBank{{{target: 65535, remainder: -2}, {remainder: 42}}, {}, {{target: 258, remainder: 0x01020304}, {target: 3, remainder: -0x01020304}}},
		kickRecords: []kickRecord{{x: -1, y: 1 << 40, z: 3}, {}, {x: 0x0102030405060708, y: -2, z: -(1 << 42), valid: true}},
		placements:  map[pool.Handle]placementRecord{0: {}, 2: {}, 256: {}}, builderLinks: map[pool.Handle]pool.Handle{2: 3},
	}
	words := []uint64{3, 65535, 0xfffffffffffffffe, 0, 42, 0, 0, 0, 0, 258, 0x01020304, 3, 0xfffffffffefdfcfc,
		3, 0xffffffffffffffff, 1 << 40, 3, 0, 0, 0, 0, 0, 0x0102030405060708, 0xfffffffffffffffe, 0xfffffc0000000000, 1, 3, 1}
	for _, prefix := range []uint64{0, 7} {
		var summary checkpoint.Summary
		var count, sum uint64
		if prefix != 0 {
			summary.Word(prefix)
			count, sum = 1, prefix
		}
		for _, word := range words {
			count++
			sum += count * word
		}
		if err := s.AppendCheckpointSummary(&summary); err != nil {
			t.Fatal(err)
		}
		if n, got := summary.Result(); n != count || got != sum {
			t.Fatalf("summary = (%d,%x); want (%d,%x)", n, got, count, sum)
		}
	}
}

func TestCheckpointConstructionSummaryRelationshipsAndPurity(t *testing.T) {
	c, defs := constructionCheckpointFixture(t)
	s := &Service{repairBanks: make([][2]repairBank, 2), kickRecords: make([]kickRecord, 2), placements: map[pool.Handle]placementRecord{1: {def: defs[0]}}, builderLinks: map[pool.Handle]pool.Handle{1: 2}}
	read := func() checkpoint.Summary {
		t.Helper()
		var summary checkpoint.Summary
		if err := s.AppendCheckpointSummary(&summary); err != nil {
			t.Fatal(err)
		}
		return summary
	}
	baseline, full := read(), constructionCheckpointBytes(t, s, c)
	s.repairBanks[1][1].remainder = -3
	if read() == baseline || bytes.Equal(full, constructionCheckpointBytes(t, s, c)) {
		t.Fatal("selected bank mutation must change summary and full payload")
	}
	s.repairBanks[1][1].remainder = 0
	// Equal selected words at different positions can deliberately collide.
	s.repairBanks[0][0].target = 3
	a, fullA := read(), constructionCheckpointBytes(t, s, c)
	s.repairBanks[0][0] = repairBank{remainder: 2}
	if a != read() || bytes.Equal(fullA, constructionCheckpointBytes(t, s, c)) {
		t.Fatal("weighted summary collision must not imply equal full payload")
	}
	s.repairBanks[0][0] = repairBank{}
	s.ModeSelector = 99
	s.builderLinks[1] = 99
	row := s.placements[1]
	row.def, row.yard = defs[1], []world.YardCell{0xff}
	s.placements[1] = row
	if read() != baseline || bytes.Equal(full, constructionCheckpointBytes(t, s, c)) {
		t.Fatal("configuration/map-value blind spots must still change full payload")
	}
	// The selected-only summary does not need admitted keys, valid bindings,
	// graph traversal, or an inactive pump; U6 owns its tick-end boundary.
	for _, binding := range constructionCheckpointBindings() {
		binding.set(s)
	}
	foreign := *defs[0]
	s.placements[1] = placementRecord{def: &foreign}
	s.RepairBankFallbacks = 123
	banks, kicks := append([][2]repairBank(nil), s.repairBanks...), append([]kickRecord(nil), s.kickRecords...)
	if read() != baseline || !reflect.DeepEqual(banks, s.repairBanks) || !reflect.DeepEqual(kicks, s.kickRecords) {
		t.Fatal("summary inspected unselected state or mutated rows")
	}
	var err error
	if allocs := testing.AllocsPerRun(50, func() { var result checkpoint.Summary; err = s.AppendCheckpointSummary(&result) }); allocs != 0 || err != nil {
		t.Fatalf("summary allocations = %g, error %v", allocs, err)
	}
	var summary checkpoint.Summary
	summary.Word(7)
	before := summary
	if err := (*Service)(nil).AppendCheckpointSummary(&summary); err == nil || summary != before {
		t.Fatal("nil service must fail atomically")
	}
	if err := s.AppendCheckpointSummary(nil); err == nil {
		t.Fatal("nil summary accepted")
	}
}
