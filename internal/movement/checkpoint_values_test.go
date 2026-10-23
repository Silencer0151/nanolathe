package movement

import (
	"bytes"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/path"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

func TestMovementCheckpointSystemFramingVector(t *testing.T) {
	s := &System{PathPlayers: -1, PathUnitLimit: 2, firstRequests: []pool.Handle{5, 6}, nextActivation: 7, pendingFiled: []bool{true, false},
		pendingFilings: []pool.Handle{8}, pendingLayers: []pool.Handle{9}, pocketLive: -10, prevMoveTier: []int{-11}, prevSFXBand: []int{-12},
		tick: 13, unreachableLive: -14, workSmooth: -15, workTick: -16}
	got := movementCheckpointBytes(t, s)
	want := movementCheckpointVector(t,
		uint8(0), uint8(0), uint32(0), [144]uint8{}, uint8(0), [16]uint8{}, uint32(0), uint8(0), uint8(1), int64(-1), int32(2), uint16(6), uint32(0), uint8(0),
		uint32(0), uint8(0), uint8(0), uint32(0), uint8(0), uint32(0), [10]uint32{}, uint8(0), uint32(0), uint32(0),
		uint32(2), uint32(5), uint32(6), uint32(0), uint8(0), uint8(0), uint32(0), uint64(7), uint8(0), uint32(2), uint8(1), uint8(0),
		uint32(1), uint32(8), uint32(1), uint32(9), int64(-10), uint32(0), uint32(1), int64(-11), uint32(1), int64(-12),
		uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(13), uint32(0), uint32(0), int64(-14), int64(-15), int64(-16), uint8(0),
		uint16(5), uint32(0), uint16(6), uint32(0), uint16(7), uint32(0), uint16(8), uint32(0), uint16(11), uint32(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("system framing\ngot  %x\nwant %x", got, want)
	}
}

func TestMovementCheckpointAirBaseRowsRemainStale(t *testing.T) {
	s := &System{}
	before := movementCheckpointBytes(t, s)
	pad := &units.Unit{Handle: 23, Owner: 2, Alive: true, Activated: true, Def: &content.UnitDef{Builder: true, IsAirBase: true}}
	s.airBases.Rebuild(30, []*units.Unit{pad}, nil)
	stale := movementCheckpointBytes(t, s)
	if bytes.Equal(before, stale) {
		t.Fatal("air-base list absent from checkpoint")
	}
	pad.Dying = true
	s.tick = 60
	// Keep the selected tick fixed across captures; capture itself must not
	// rebuild the list even when a live cadence check would permit rebuilding.
	stale = movementCheckpointBytes(t, s)
	if after := movementCheckpointBytes(t, s); !bytes.Equal(stale, after) || len(s.airBases.List(2)) != 1 {
		t.Fatal("capture refreshed stale list")
	}
	s.airBases.Rebuild(60, []*units.Unit{pad}, nil)
	if after := movementCheckpointBytes(t, s); bytes.Equal(stale, after) {
		t.Fatal("ordinary list refresh invisible")
	}
}

func TestMovementCheckpointProfileAndRouteVectors(t *testing.T) {
	c := movementCheckpointContext()
	s := &System{}
	p := Profile{BadSlope: 1, BadWaterSlope: 2, FootPrintX: -3, FootPrintZ: 4, MaxSlope: 5, MaxWaterDepth: -6, MaxWaterSlope: 7, MinWaterDepth: -8}
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { writeMovementProfile(e, c, s, &p, "profile") })
	want := movementCheckpointVector(t, uint8(1), uint8(2), int16(-3), int16(4), uint8(5), int32(-6), uint8(7), int32(-8))
	if !bytes.Equal(got, want) {
		t.Fatalf("profile %x != %x", got, want)
	}
	r := Route{Active: true, Count: 1, Dirty: true, LastRequestTick: 2, Status: 3, WantsRepath: true, firstHold: 4, firstPending: true}
	r.Points[0] = Point{X: -5, Z: 6}
	r.Points[19] = Point{X: 7, Z: -8}
	got = movementCheckpointWrite(t, func(e *checkpoint.Encoder) { writeMovementRoute(e, c, s, &r, "route") })
	want = movementCheckpointVector(t, uint8(1), uint8(1), uint8(1), uint32(2), [2]int32{-5, 6}, [36]int32{}, [2]int32{7, -8}, uint32(3), uint8(1), uint32(4), uint8(1))
	if !bytes.Equal(got, want) {
		t.Fatalf("route %x != %x", got, want)
	}
}

func TestMovementCheckpointOccupancyVectorAndDerivedRefusal(t *testing.T) {
	s := &System{}
	g := &OccupancyGrid{planeW: 2, planeH: 1, cells: []int32{1, 0}, air: []int32{0, 3}, cellCount: 1, airCount: 1, linkSeq: 4,
		links: []sectorLink{{linked: true, next: 3, offMap: true, prev: 1, sx: -5, sz: 6}}, offMapHead: 1, sectorH: 7, sectorW: 8, sectorHead: []int32{0, 3}}
	s.Grid = g
	g.overlap = s
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { writeMovementGrid(e, movementCheckpointContext(), s, g, "grid") })
	want := movementCheckpointVector(t,
		uint32(2), uint8(0), uint8(1), int64(2), // air, identity two
		uint32(2), uint8(1), int64(0), uint8(0), // ground, identity zero
		uint8(0), uint64(4), uint32(1), uint8(1), uint8(1), int64(2), uint8(1), uint8(1), int64(0), int32(-5), int32(6),
		uint8(1), int64(0), uint8(1), uint8(0), int32(1), int32(2), uint8(0), int32(7), uint32(2), uint8(0), uint8(1), int64(2), int32(8))
	if !bytes.Equal(got, want) {
		t.Fatalf("occupancy\ngot  %x\nwant %x", got, want)
	}
	g.cellCount++
	if _, err := s.CollectCheckpointReferences(movementCheckpointContext()); err == nil {
		t.Fatal("accepted inconsistent derived count")
	}
}

func TestMovementCheckpointAirSectorIdentity(t *testing.T) {
	s := &System{AirSectors: &AirSectorGrid{records: make([]airSector, 2)}}
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) {
		for _, v := range []*airSector{nil, &s.AirSectors.records[1], &s.AirSectors.sentinel} {
			writeMovementAirSector(e, s, v, "sector")
		}
	})
	want := movementCheckpointVector(t, uint8(0), uint8(1), uint32(1), uint8(2))
	if !bytes.Equal(got, want) {
		t.Fatalf("air-sector %x != %x", got, want)
	}
	var b bytes.Buffer
	e := checkpoint.NewEncoder(&b)
	writeMovementAirSector(e, s, &airSector{}, "sector")
	if e.Err() == nil {
		t.Fatal("equal-valued foreign sector accepted")
	}
}

func TestMovementCheckpointProviderVectorAndActualKeys(t *testing.T) {
	s := &System{}
	p := &pathProvider{system: s, players: 2, limit: 3, tick: 4, staged: []uint64{(1 << 5) | (1 << 9)}}
	p.cursor[0] = -1
	p.started[9] = true
	p.requests[2] = map[pool.Handle]path.Request{9: {Activation: 10, Player: 6, Start: path.Cell{X: -11, Z: 12}, Unit: 13}, 5: {Activation: 14, Player: 7, Start: path.Cell{X: 15, Z: -16}, Unit: 17}}
	s.pathProvider = p
	c := movementCheckpointContext()
	collectMovementCheckpoint(t, s, c)
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { _ = s.WritePathProviderCheckpoint(e, c) })
	want := movementCheckpointVector(t, uint8(1), [10]int64{-1}, uint8(0), int32(3), int64(2), uint32(0), uint32(0), uint32(2),
		uint32(5), uint64(14), uint16(9), uint32(0), uint8(7), int32(15), int32(-16), uint32(17),
		uint32(9), uint64(10), uint16(9), uint32(0), uint8(6), int32(-11), int32(12), uint32(13),
		[7]uint32{}, [10]uint8{0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, uint8(1), uint32(4), uint8(0))
	if !bytes.Equal(got, want) {
		t.Fatalf("provider\ngot  %x\nwant %x", got, want)
	}
	p.requests[2] = map[pool.Handle]path.Request{5: p.requests[2][5], 9: p.requests[2][9]}
	p.eligibleNow[3] = true
	if again := movementCheckpointWrite(t, func(e *checkpoint.Encoder) { _ = s.WritePathProviderCheckpoint(e, c) }); !bytes.Equal(got, again) {
		t.Fatal("map construction or inactive cache changed bytes")
	}
	p.staged[0] ^= 1
	if err := validateMovementProvider(s, p, c); err == nil {
		t.Fatal("inconsistent staged index accepted")
	}
	p.staged[0] ^= 1
	p.eligible = func(int) bool { panic("eligible called") }
	if err := validateMovementProvider(s, p, c); err == nil {
		t.Fatal("unattested eligibility accepted")
	}
	p.eligible = nil
	s.world = &units.World{}
	p.world = s.world
	var b bytes.Buffer
	if err := s.WritePathProviderCheckpoint(checkpoint.NewEncoder(&b), c); err == nil {
		t.Fatal("provider writer admitted unattested upper world")
	}
}

func TestMovementCheckpointPayloadVectors(t *testing.T) {
	s := &System{}
	c := movementCheckpointContext()
	marker := &airMarker{altOffset: -1, attachPiece: 2, flags: 3, goal: Vec3{X: -4, Y: 5, Z: 6}, heading: 7, radial: -8, radius: 9, sys: s, target: 10}
	velocity := &airVelocityMarker{commanded: 11, pos: Vec3{X: 12, Y: -13, Z: 14}, savedAux: 15, savedFlags: 16, savedTrailing: 17, steer: true, sys: s, vel: Vec3{X: 18, Y: 19, Z: -20}}
	got := movementCheckpointWrite(t, func(e *checkpoint.Encoder) {
		writeMovementPayload(e, c, s, marker, "marker")
		writeMovementPayload(e, c, s, velocity, "velocity")
	})
	want := movementCheckpointVector(t, uint8(1), int16(-1), uint16(2), uint16(3), int64(-4), int64(5), int64(6), uint16(7), int64(-8), uint16(9), uint8(1), uint32(10), uint16(1), uint32(0),
		uint8(2), uint16(11), int64(12), int64(-13), int64(14), uint16(15), uint16(16), uint16(17), uint8(1), uint8(1), uint16(1), uint32(0), int64(18), int64(19), int64(-20))
	if !bytes.Equal(got, want) {
		t.Fatalf("payloads\ngot  %x\nwant %x", got, want)
	}
}
