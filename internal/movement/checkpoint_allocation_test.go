package movement

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Most physical per-handle rows are empty. Adding empty slots must not format
// a nested diagnostic path for each one (DESIGN_MULTIPLAYER §16.3.81).
func TestMovementCheckpointNullableRowsAllocation(t *testing.T) {
	profile := &Profile{BadSlope: 1, BadWaterSlope: 2, FootPrintX: -3, FootPrintZ: 4, MaxSlope: 5, MaxWaterDepth: -6, MaxWaterSlope: 7, MinWaterDepth: -8}
	measure := func(size int) float64 {
		rows := make([]*Profile, size)
		rows[2] = profile
		e := checkpoint.NewEncoder(io.Discard)
		return testing.AllocsPerRun(10, func() {
			writeMovementRows(e, nil, nil, "movement.profiles", rows, writeMovementProfile)
			if err := e.Err(); err != nil {
				panic(err)
			}
		})
	}
	small, large := measure(16), measure(4096)
	t.Logf("nullable rows: 16 slots %.0f allocations; 4096 slots %.0f", small, large)
	if large > small+1 || large > 4 {
		t.Fatalf("empty rows allocate paths: small=%g large=%g", small, large)
	}
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) {
		writeMovementRows(e, nil, nil, "movement.profiles", []*Profile{nil, profile, nil}, writeMovementProfile)
	})
	// The existing Profile vector, enclosed by the nullable row schema.
	want := movementCheckpointVector(t, uint32(3), uint8(0), uint8(1), uint8(1), uint8(2), int16(-3), int16(4), uint8(5), int32(-6), uint8(7), int32(-8), uint8(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("nullable profile bytes\ngot  %x\nwant %x", got, want)
	}
}

func TestMovementCheckpointDenseOccupancyAllocation(t *testing.T) {
	measure := func(size int) float64 {
		g := &OccupancyGrid{planeW: int32(size), planeH: 1, air: make([]int32, size), cells: make([]int32, size), sectorHead: make([]int32, size)}
		s, c := &System{Grid: g}, movementCheckpointContext()
		e := checkpoint.NewEncoder(io.Discard)
		return testing.AllocsPerRun(10, func() {
			writeMovementGrid(e, c, s, g, "movement.Grid")
			if err := e.Err(); err != nil {
				panic(err)
			}
		})
	}
	small, large := measure(16), measure(4096)
	t.Logf("occupancy: 16 cells/plane %.0f allocations; 4096 cells/plane %.0f", small, large)
	if large > small+1 || large > 6 {
		t.Fatalf("empty occupancy cells allocate paths: small=%g large=%g", small, large)
	}
	g := &OccupancyGrid{planeW: 2, planeH: 1, air: []int32{0, 0}, cells: []int32{0, 0}, sectorHead: []int32{0, 0}}
	s, c := &System{Grid: g}, movementCheckpointContext()
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { writeMovementGrid(e, c, s, g, "movement.Grid") })
	want := movementCheckpointVector(t, uint32(2), [2]uint8{}, uint32(2), [2]uint8{},
		uint8(0), uint64(0), uint32(0), uint8(0), uint8(0), uint8(0), int32(1), int32(2), uint8(0), int32(0), uint32(2), [2]uint8{}, int32(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("empty occupancy bytes\ngot  %x\nwant %x", got, want)
	}
}

type movementCheckpointOffsetFailure struct {
	remaining int
	err       error
}

func (w *movementCheckpointOffsetFailure) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, w.err
	}
	w.remaining -= len(p)
	return len(p), nil
}

// Lazy paths must still identify the exact failing presence or payload word,
// including logical occupant zero and widened negative storage values.
func TestMovementCheckpointDenseErrorPaths(t *testing.T) {
	profile := &Profile{}
	g := &OccupancyGrid{planeW: 2, planeH: 1, air: []int32{0, 1}, cells: []int32{-2147483648, 0}, airCount: 1, cellCount: 1, sectorHead: []int32{0, 3}}
	s, c := &System{Grid: g}, movementCheckpointContext()
	rows := func(e *checkpoint.Encoder) {
		writeMovementRows(e, nil, nil, "movement.profiles", []*Profile{nil, profile, nil}, writeMovementProfile)
	}
	grid := func(e *checkpoint.Encoder) { writeMovementGrid(e, c, s, g, "movement.Grid") }
	for _, tc := range []struct {
		name   string
		write  func(*checkpoint.Encoder)
		offset int
		path   string
	}{
		{"nil row", rows, 4, "movement.profiles[0]"},
		{"present row", rows, 5, "movement.profiles[1]"},
		{"row field", rows, 6, "movement.profiles[1].BadSlope"},
		{"air absent", grid, 4, "movement.Grid.air[0]"},
		{"air present", grid, 5, "movement.Grid.air[1]"},
		{"air identity", grid, 6, "movement.Grid.air[1]"},
		{"ground present", grid, 18, "movement.Grid.cells[0]"},
		{"ground identity", grid, 19, "movement.Grid.cells[0]"},
		{"ground absent", grid, 27, "movement.Grid.cells[1]"},
		{"sector absent", grid, 61, "movement.Grid.sectorHead[0]"},
		{"sector present", grid, 62, "movement.Grid.sectorHead[1]"},
		{"sector identity", grid, 63, "movement.Grid.sectorHead[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := errors.New("authored sink failure")
			w := &movementCheckpointOffsetFailure{remaining: tc.offset, err: failure}
			e := checkpoint.NewEncoder(w)
			tc.write(e)
			if !errors.Is(e.Err(), failure) || !strings.Contains(e.Err().Error(), "logical path "+tc.path+",") {
				t.Fatalf("offset %d error=%v, want %s", tc.offset, e.Err(), tc.path)
			}
		})
	}
	got := movementCheckpointWrite(t, grid)
	want := movementCheckpointVector(t, uint32(2), uint8(0), uint8(1), int64(0), uint32(2), uint8(1), int64(-2147483649), uint8(0),
		uint8(0), uint64(0), uint32(0), uint8(0), uint8(0), uint8(0), int32(1), int32(2), uint8(0), int32(0), uint32(2), uint8(0), uint8(1), int64(2), int32(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("occupant presence/width changed\ngot  %x\nwant %x", got, want)
	}
}

// These capacity-sized rows retain their values, but successful writes need
// no per-slot diagnostic strings. Nonempty names still copy their byte payload.
func TestMovementCheckpointIndexedRowsAllocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payloads int
		build    func(int) func(*checkpoint.Encoder)
	}{
		{"system sparse rows", 0, func(size int) func(*checkpoint.Encoder) {
			s := &System{moveGoals: make([]*moveGoal, size), profileNames: make([]string, size), recordGoals: make([][]recordGoal, size)}
			c := movementCheckpointContext()
			collectMovementCheckpoint(t, s, c)
			return func(e *checkpoint.Encoder) { _ = s.WriteCheckpoint(e, c) }
		}},
		{"occupancy links", 0, func(size int) func(*checkpoint.Encoder) {
			g := &OccupancyGrid{links: make([]sectorLink, size)}
			s, c := &System{Grid: g}, movementCheckpointContext()
			return func(e *checkpoint.Encoder) { writeMovementGrid(e, c, s, g, "movement.Grid") }
		}},
		{"layer commits", 0, func(size int) func(*checkpoint.Encoder) {
			v := &ClassLayer{commits: make([]commitWord, size)}
			s, c := &System{}, movementCheckpointContext()
			return func(e *checkpoint.Encoder) { writeMovementLayer(e, c, s, v, "movement.layers[0]") }
		}},
		{"registry names", 0, func(size int) func(*checkpoint.Encoder) {
			r := &ClassLayers{names: make([]string, size)}
			s, c := &System{}, movementCheckpointContext()
			return func(e *checkpoint.Encoder) { writeMovementLayers(e, c, s, r, "movement.layerRegistry") }
		}},
		{"registry membership", 1, func(size int) func(*checkpoint.Encoder) {
			r := &ClassLayers{byName: make(map[string]*ClassLayer, size)}
			for i := 0; i < size; i++ {
				r.byName[fmt.Sprintf("class%d", i)] = nil
			}
			s, c := &System{}, movementCheckpointContext()
			return func(e *checkpoint.Encoder) { writeMovementLayers(e, c, s, r, "movement.layerRegistry") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			measure := func(size int) float64 {
				write := tc.build(size)
				e := checkpoint.NewEncoder(io.Discard)
				return testing.AllocsPerRun(10, func() {
					write(e)
					if err := e.Err(); err != nil {
						panic(err)
					}
				})
			}
			small, large := measure(16), measure(4096)
			t.Logf("16 rows %.0f allocations; 4096 rows %.0f", small, large)
			if large > small+float64(tc.payloads*(4096-16))+1 {
				t.Fatalf("indexed rows allocate diagnostic paths: small=%g large=%g", small, large)
			}
		})
	}
}

// Fixed air-base diagnostic names must preserve the ten row counts, handle
// payloads and exact sink-error paths (DESIGN_MULTIPLAYER §16.3.81).
func TestMovementCheckpointAirBaseRowsVectorAndErrors(t *testing.T) {
	s := &System{}
	empty := movementCheckpointBytes(t, s)
	pads := make([]*units.Unit, 10)
	var rows [20]uint32
	for owner := range pads {
		handle := pool.Handle(100 - owner)
		pads[owner] = &units.Unit{Handle: handle, Owner: uint8(owner), Alive: true, Activated: true,
			Def: &content.UnitDef{Builder: true, IsAirBase: true}}
		rows[owner*2], rows[owner*2+1] = 1, uint32(handle)
	}
	s.airBases.Rebuild(0, pads, nil)
	c := movementCheckpointContext()
	collectMovementCheckpoint(t, s, c)
	write := func(e *checkpoint.Encoder) { _ = s.WriteCheckpoint(e, c) }
	got := movementCheckpointWrite(t, write)
	// The existing System vector has 207 bytes before the ten air-base rows.
	// Empty rows are ten u32 counts; each authored row adds one u32 handle.
	const prefix, emptyRows = 207, 40
	wantRows := movementCheckpointVector(t, rows)
	if len(got) != len(empty)+emptyRows || !bytes.Equal(got[:prefix], empty[:prefix]) ||
		!bytes.Equal(got[prefix:prefix+len(wantRows)], wantRows) ||
		!bytes.Equal(got[prefix+len(wantRows):], empty[prefix+emptyRows:]) {
		t.Fatal("air-base row order, framing, handles or surrounding System bytes changed")
	}
	for owner := range pads {
		path := fmt.Sprintf("movement.airBases.lists[%d]", owner)
		assertMovementCheckpointOffsetError(t, write, prefix+owner*8, path)
		assertMovementCheckpointOffsetError(t, write, prefix+owner*8+4, path)
	}
}

func TestMovementCheckpointIndexedSystemVectorAndErrors(t *testing.T) {
	s := &System{moveGoals: []*moveGoal{nil, nil}, profileNames: []string{"", "x"}, recordGoals: [][]recordGoal{nil, {}}}
	c := movementCheckpointContext()
	collectMovementCheckpoint(t, s, c)
	write := func(e *checkpoint.Encoder) { _ = s.WriteCheckpoint(e, c) }
	got := movementCheckpointWrite(t, write)
	want := movementCheckpointVector(t,
		uint8(0), uint8(0), uint32(0), [144]uint8{}, uint8(0), [16]uint8{}, uint32(0), uint8(0), uint8(1), int64(0), int32(0), uint16(6), uint32(0), uint8(0),
		uint32(0), uint8(0), uint8(0), uint32(0), uint8(0), uint32(0), [10]uint32{}, uint8(0), uint32(0), uint32(0),
		uint32(0), uint32(0), uint8(0), uint8(0), uint32(2), uint16(11), uint32(0), uint16(11), uint32(0),
		uint64(0), uint8(0), uint32(0), uint32(0), uint32(0), int64(0), uint32(0), uint32(0), uint32(0),
		uint32(2), uint32(0), uint32(1), uint8('x'), uint32(0), uint32(2), uint32(0), uint32(0),
		uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), int64(0), int64(0), int64(0), uint8(0),
		uint16(5), uint32(0), uint16(6), uint32(0), uint16(7), uint32(0), uint16(8), uint32(0), uint16(11), uint32(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("indexed system rows\ngot  %x\nwant %x", got, want)
	}
	for _, tc := range []struct {
		offset int
		path   string
	}{
		{270, "movement.moveGoals[0]"},
		{272, "movement.moveGoals[0]"},
		{276, "movement.moveGoals[1]"},
		{327, "movement.profileNames[0]"},
		{331, "movement.profileNames[1]"},
		{335, "movement.profileNames[1]"},
		{344, "movement.recordGoals[0]"},
		{348, "movement.recordGoals[1]"},
	} {
		assertMovementCheckpointOffsetError(t, write, tc.offset, tc.path)
	}
	// Present payloads still use the original nested row path.
	s.recordGoals[1] = []recordGoal{{}}
	assertMovementCheckpointOffsetError(t, write, 352, "movement.recordGoals[1][0].air")
	assertMovementCheckpointOffsetError(t, write, 358, "movement.recordGoals[1][0].ground")
	// A reference refusal must retain the same indexed path even without a
	// sink failure, and writing must not silently discover the missing object.
	s.moveGoals[1] = &moveGoal{}
	e := checkpoint.NewEncoder(io.Discard)
	if err := s.WriteCheckpoint(e, c); err == nil || !strings.Contains(err.Error(), "logical path movement.moveGoals[1],") || !strings.Contains(err.Error(), "undiscovered reference") {
		t.Fatalf("undiscovered goal: %v", err)
	}
	if _, ok := c.moveGoals.Find(s.moveGoals[1]); ok {
		t.Fatal("writer registered a missing goal")
	}
}

func assertMovementCheckpointOffsetError(t *testing.T, write func(*checkpoint.Encoder), offset int, path string) {
	t.Helper()
	failure := errors.New("authored indexed sink failure")
	e := checkpoint.NewEncoder(&movementCheckpointOffsetFailure{remaining: offset, err: failure})
	write(e)
	if !errors.Is(e.Err(), failure) || !strings.Contains(e.Err().Error(), "logical path "+path+",") {
		t.Fatalf("offset %d error=%v, want %s", offset, e.Err(), path)
	}
}

func TestMovementCheckpointIndexedPayloadErrors(t *testing.T) {
	s, c := &System{}, movementCheckpointContext()
	g := &OccupancyGrid{links: []sectorLink{{linked: true, next: 1, offMap: true, prev: -2147483648, sx: -5, sz: 6}, {}}}
	grid := func(e *checkpoint.Encoder) { writeMovementGrid(e, c, s, g, "grid") }
	layer := &ClassLayer{commits: []commitWord{{set: true, tick: 6}, {tick: 9}}}
	layerWrite := func(e *checkpoint.Encoder) { writeMovementLayer(e, c, s, layer, "layer") }
	r := &ClassLayers{byName: map[string]*ClassLayer{"z": nil, "a": nil}, names: []string{"", "z"}}
	registry := func(e *checkpoint.Encoder) { writeMovementLayers(e, c, s, r, "registry") }
	for _, tc := range []struct {
		write  func(*checkpoint.Encoder)
		offset int
		path   string
	}{
		{grid, 21, "grid.links[0].linked"},
		{grid, 22, "grid.links[0].next"},
		{grid, 23, "grid.links[0].next"},
		{grid, 31, "grid.links[0].offMap"},
		{grid, 32, "grid.links[0].prev"},
		{grid, 33, "grid.links[0].prev"},
		{grid, 41, "grid.links[0].sx"},
		{grid, 45, "grid.links[0].sz"},
		{grid, 50, "grid.links[1].next"},
		{grid, 52, "grid.links[1].prev"},
		{layerWrite, 34, "layer.commits[0].set"},
		{layerWrite, 35, "layer.commits[0].tick"},
		{layerWrite, 39, "layer.commits[1].set"},
		{layerWrite, 40, "layer.commits[1].tick"},
		{registry, 5, "registry.byName[0].key"},
		{registry, 9, "registry.byName[0].key"},
		{registry, 10, "registry.byName[0].value"},
		{registry, 12, "registry.byName[0].value"},
		{registry, 21, "registry.byName[1].value"},
		{registry, 34, "registry.names[0]"},
		{registry, 38, "registry.names[1]"},
		{registry, 42, "registry.names[1]"},
	} {
		assertMovementCheckpointOffsetError(t, tc.write, tc.offset, tc.path)
	}
	got := movementCheckpointWrite(t, grid)
	want := movementCheckpointVector(t, uint32(0), uint32(0), uint8(0), uint64(0), uint32(2),
		uint8(1), uint8(1), int64(0), uint8(1), uint8(1), int64(-2147483649), int32(-5), int32(6),
		uint8(0), uint8(0), uint8(0), uint8(0), int32(0), int32(0),
		uint8(0), uint8(0), uint8(0), int32(0), int32(0), uint8(0), int32(0), uint32(0), int32(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("link identities and empty row\ngot  %x\nwant %x", got, want)
	}
	// An unregistered registry member fails on its value slot, not its key.
	r.byName["z"] = &ClassLayer{}
	e := checkpoint.NewEncoder(io.Discard)
	registry(e)
	if err := e.Err(); err == nil || !strings.Contains(err.Error(), "logical path registry.byName[1].value,") || !strings.Contains(err.Error(), "undiscovered reference") {
		t.Fatalf("undiscovered registry layer: %v", err)
	}
}
