package movement

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

type checkpointHostileTerrainPort []byte

func (checkpointHostileTerrainPort) CellOccupant(int32, int32) uint16 {
	panic("capture called occupancy adapter")
}
func (checkpointHostileTerrainPort) NoteFeatureFootprint(int32, int32, int16, int16) {
	panic("capture called restamp owner")
}

func movementTerrainFixture(grid *OccupancyGrid) (*System, *CheckpointContext, *world.CheckpointContext, *checkpoint.BindingAuthority) {
	t := &world.Terrain{CellW: 1, CellH: 1, Plot: make([]world.PlotCell, 1)}
	s := &System{Terrain: t, Grid: grid}
	a := checkpoint.NewBindingAuthority()
	t.SetMovers(gridOccupancy{grid: grid})
	grid.AttachPlot(t)
	t.SetClassRestampOwnerWithCheckpointBinding(s, a)
	return s, movementCheckpointContext(), world.NewCheckpointContext(nil), a
}

func requireMovementTerrainRefusal(t *testing.T, s *System, c *CheckpointContext, w *world.CheckpointContext) {
	t.Helper()
	beforeSystem, beforeBinding, beforeWorld := c.system, c.terrainBindings, *w
	if _, err := s.CollectCheckpointReferences(c); err == nil {
		t.Fatal("collector accepted invalid terrain binding")
	}
	for _, write := range []func(*checkpoint.Encoder, *CheckpointContext) error{s.WriteCheckpoint, s.WritePathProviderCheckpoint} {
		var out bytes.Buffer
		if err := write(checkpoint.NewEncoder(&out), c); err == nil || out.Len() != 0 {
			t.Fatalf("writer error = %v, bytes = %d", err, out.Len())
		}
	}
	if c.system != beforeSystem || c.terrainBindings != beforeBinding || *w != beforeWorld {
		t.Fatal("refusal changed registered contexts")
	}
}

// Independent empty-System framing pins the Terrain byte between Steers and
// activeOrders. Community is 32 bools + 14 i64s; Profile is 16 bytes. Neither
// installation metadata nor a world payload is added (DESIGN_MULTIPLAYER §16.3.57).
func movementTerrainVector(t *testing.T, terrain uint8) []byte {
	return movementCheckpointVector(t,
		[2]uint8{}, uint32(0), [144]byte{}, uint8(0), [16]byte{},
		uint32(0), uint8(0), uint8(1), int64(0), int32(0), uint16(6), uint32(0),
		uint8(0), uint32(0), uint8(0), uint8(0), uint32(0), terrain,
		uint32(0), [10]uint32{}, uint8(0), // activeOrders, airBases, airLegHandler
		[4]uint32{}, [2]uint8{}, uint32(0), uint64(0), uint8(0), // arrival through provider
		[3]uint32{}, int64(0), // pending arrays, pocketLive
		[11]uint32{}, int64(0), int64(0), int64(0), uint8(0), // pockets through world
		uint16(5), uint32(0), uint16(6), uint32(0), uint16(7), uint32(0),
		uint16(8), uint32(0), uint16(11), uint32(0))
}

func TestMovementTerrainBindingVectorAndPurity(t *testing.T) {
	if got, want := movementCheckpointBytes(t, &System{}), movementTerrainVector(t, 0); !bytes.Equal(got, want) {
		t.Fatalf("absent vector\ngot  %x\nwant %x", got, want)
	}
	s, c, w, a := movementTerrainFixture(nil)
	if err := c.SetTerrainBindings(s, w, nil, a); err != nil {
		t.Fatal(err)
	}
	beforeWorld, beforeBinding := *w, c.terrainBindings
	beforePlot := append([]world.PlotCell(nil), s.Terrain.Plot...)
	for range 2 {
		collectMovementCheckpoint(t, s, c)
		got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { _ = s.WriteCheckpoint(e, c) })
		if want := movementTerrainVector(t, 1); !bytes.Equal(got, want) {
			t.Fatalf("present vector\ngot  %x\nwant %x", got, want)
		}
	}
	if *w != beforeWorld || c.terrainBindings != beforeBinding || !reflect.DeepEqual(s.Terrain.Plot, beforePlot) || s.layerRegistry != nil {
		t.Fatal("capture changed contexts, plot or allocated layers")
	}
	if allocs := testing.AllocsPerRun(10, func() {
		if err := c.terrainBindings.validate(s); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("terrain preflight allocated %v", allocs)
	}
}

func TestMovementTerrainBindingRegistrationRejectsActualOwners(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*System, *checkpoint.BindingAuthority)
	}{
		{"foreign restamp owner", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(&System{}, a)
		}},
		{"typed nil owner", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding((*System)(nil), a)
		}},
		{"hostile owner", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(checkpointHostileTerrainPort{1}, a)
		}},
		{"absent movers", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers(nil)
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"pointer adapter", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers(&gridOccupancy{grid: s.Grid})
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"typed nil adapter", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers((*gridOccupancy)(nil))
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"hostile adapter", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers(checkpointHostileTerrainPort{2})
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"foreign grid adapter", func(s *System, a *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers(gridOccupancy{grid: NewOccupancyGrid()})
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"missing plot alias", func(s *System, _ *checkpoint.BindingAuthority) { s.Grid.plot = nil }},
		{"foreign plot alias", func(s *System, _ *checkpoint.BindingAuthority) { s.Grid.plot = &world.Terrain{} }},
		{"copied terrain", func(s *System, _ *checkpoint.BindingAuthority) {
			terrain := *s.Terrain
			s.Terrain = &terrain
			s.Grid.plot = &terrain
		}},
		{"absent terrain", func(s *System, _ *checkpoint.BindingAuthority) { s.Terrain = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, w, a := movementTerrainFixture(NewOccupancyGrid())
			tc.edit(s, a)
			beforeWorld := *w
			if err := c.SetTerrainBindings(s, w, s.Grid, a); err == nil {
				t.Fatal("invalid actual owners admitted")
			}
			if c.system != nil || c.terrainBindings != (checkpointTerrainBindings{}) || *w != beforeWorld {
				t.Fatal("failed registration changed either context")
			}
		})
	}
	s, c, w, a := movementTerrainFixture(nil)
	copy := *s
	if err := c.SetTerrainBindings(&copy, w, nil, a); err == nil || c.system != nil || w.Terrain != nil {
		t.Fatal("copied system borrowed original restamp proof")
	}
}

func TestMovementTerrainBindingReplacementRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*System, *world.CheckpointContext, *checkpoint.BindingAuthority)
	}{
		{"system grid", func(s *System, _ *world.CheckpointContext, _ *checkpoint.BindingAuthority) {
			s.Grid = NewOccupancyGrid()
		}},
		{"system terrain", func(s *System, _ *world.CheckpointContext, _ *checkpoint.BindingAuthority) {
			s.Terrain = &world.Terrain{}
		}},
		{"plot alias", func(s *System, _ *world.CheckpointContext, _ *checkpoint.BindingAuthority) { s.Grid.plot = nil }},
		{"ordinary restamp reinstall", func(s *System, _ *world.CheckpointContext, _ *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestamp(s.Terrain.ClassRestamp())
		}},
		{"ordinary movers reinstall", func(s *System, _ *world.CheckpointContext, _ *checkpoint.BindingAuthority) {
			s.Terrain.SetMovers(s.Terrain.Movers())
		}},
		{"canonical reinstall", func(s *System, _ *world.CheckpointContext, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
		}},
		{"foreign owner same authority", func(s *System, _ *world.CheckpointContext, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(&System{}, a)
		}},
		{"context terrain", func(_ *System, w *world.CheckpointContext, _ *checkpoint.BindingAuthority) { w.Terrain = nil }},
		{"world context replacement", func(s *System, w *world.CheckpointContext, a *checkpoint.BindingAuthority) {
			s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
			*w = *world.NewCheckpointContext(nil)
			if err := w.SetMovementBindings(s.Terrain, a); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, w, a := movementTerrainFixture(NewOccupancyGrid())
			if err := c.SetTerrainBindings(s, w, s.Grid, a); err != nil {
				t.Fatal(err)
			}
			tc.edit(s, w, a)
			requireMovementTerrainRefusal(t, s, c, w)
		})
	}
	s, c, w, a := movementTerrainFixture(nil)
	if err := c.SetTerrainBindings(s, w, nil, a); err != nil {
		t.Fatal(err)
	}
	copy := *s
	requireMovementTerrainRefusal(t, &copy, c, w)
}

func TestMovementTerrainBindingRegistrationConflictsAreAtomic(t *testing.T) {
	s, c, w, a := movementTerrainFixture(NewOccupancyGrid())
	if err := c.SetTerrainBindings(s, w, s.Grid, a); err != nil {
		t.Fatal(err)
	}
	beforeBinding, beforeWorld := c.terrainBindings, *w
	if err := c.SetTerrainBindings(s, w, s.Grid, a); err != nil || c.terrainBindings != beforeBinding || *w != beforeWorld {
		t.Fatal("identical registration changed context")
	}
	for _, tc := range []struct {
		c    *CheckpointContext
		s    *System
		w    *world.CheckpointContext
		grid *OccupancyGrid
		a    *checkpoint.BindingAuthority
	}{
		{nil, s, w, s.Grid, a}, {c, nil, w, s.Grid, a}, {c, s, nil, s.Grid, a}, {c, s, w, s.Grid, nil},
		{c, s, w, s.Grid, checkpoint.NewBindingAuthority()}, {c, s, w, nil, a},
		{c, s, w, NewOccupancyGrid(), a}, {c, s, world.NewCheckpointContext(nil), s.Grid, a},
	} {
		var candidateWorld world.CheckpointContext
		if tc.w != nil {
			candidateWorld = *tc.w
		}
		if err := tc.c.SetTerrainBindings(tc.s, tc.w, tc.grid, tc.a); err == nil {
			t.Fatal("conflict admitted")
		}
		if c.system != s || c.terrainBindings != beforeBinding || *w != beforeWorld || tc.w != nil && *tc.w != candidateWorld {
			t.Fatal("conflict changed either context")
		}
	}
	fresh := movementCheckpointContext()
	fresh.system = &System{}
	unset := world.NewCheckpointContext(nil)
	if err := fresh.SetTerrainBindings(s, unset, s.Grid, a); err == nil || unset.Terrain != nil {
		t.Fatal("foreign movement context changed world registration")
	}
	foreign := world.NewCheckpointContext(nil)
	foreign.Terrain = &world.Terrain{}
	previous := *foreign
	fresh = movementCheckpointContext()
	if err := fresh.SetTerrainBindings(s, foreign, s.Grid, a); err == nil || *foreign != previous || fresh.system != nil {
		t.Fatal("foreign world singleton changed movement registration")
	}
	// A same-owner reinstall changes only opaque receipt identity. Neither
	// registration may be refreshed to silently bless it for an old capture.
	s.Terrain.SetClassRestampOwnerWithCheckpointBinding(s, a)
	if err := c.SetTerrainBindings(s, w, s.Grid, a); err == nil || c.terrainBindings != beforeBinding || *w != beforeWorld {
		t.Fatal("reinstall refreshed an existing capture")
	}
}

func TestMovementTerrainConstructorAndPathComposition(t *testing.T) {
	terrain, grid, a := syntheticTerrainForIntegrate(), NewOccupancyGrid(), checkpoint.NewBindingAuthority()
	s := NewSystemWithCheckpointBindings(terrain, wiringProfile, grid, a)
	owner, ok := terrain.CheckpointMovementOwner(a)
	if !ok || owner != s || grid.plot != terrain || terrain.Movers().(gridOccupancy).grid != grid || s.layerRegistry != nil {
		t.Fatal("constructor changed wiring or lost exact owner")
	}
	ordinaryTerrain, ordinaryGrid := syntheticTerrainForIntegrate(), NewOccupancyGrid()
	ordinary := NewSystem(ordinaryTerrain, wiringProfile, ordinaryGrid)
	if _, ok := ordinaryTerrain.CheckpointMovementOwner(a); ok {
		t.Fatal("ordinary constructor gained proof")
	}
	if !reflect.DeepEqual(ordinary.AirSectors, s.AirSectors) || !reflect.DeepEqual(ordinary.Scheduler.TraceState(), s.Scheduler.TraceState()) || !reflect.DeepEqual(ordinaryTerrain.Plot, terrain.Plot) {
		t.Fatal("constructor changed simulation values")
	}
	// Existing occupancy capture still requires both physical planes. Populate
	// this authored fixture before capture; admission must never do this work.
	grid.planeRow(PlaneGround)
	grid.planeRow(PlaneAir)
	for _, pathFirst := range []bool{false, true} {
		c, w := movementCheckpointContext(), world.NewCheckpointContext(nil)
		if pathFirst {
			if err := c.SetPathBindings(s, nil, a); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.SetTerrainBindings(s, w, grid, a); err != nil {
			t.Fatal(err)
		}
		if !pathFirst {
			if err := c.SetPathBindings(s, nil, a); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.CollectCheckpointReferences(c); err == nil {
			t.Fatal("terrain proof admitted AirSectors")
		}
		// Isolate this unit from the still-unsupported immutable air snapshot.
		air := s.AirSectors
		s.AirSectors = nil
		movementPathBindingBytes(t, s, c)
		s.AirSectors = air
	}
	c := movementCheckpointContext()
	if err := c.SetTerrainBindings(ordinary, world.NewCheckpointContext(nil), ordinaryGrid, a); err == nil {
		t.Fatal("ordinary constructor admitted")
	}
}
