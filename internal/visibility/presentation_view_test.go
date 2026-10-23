package visibility

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// fogState is everything of the service's own fog cache and its bookkeeping
// that a derivation into caller storage must leave alone, deep-copied.
type fogState struct {
	ch0, ch1, in               []uint8
	mode                       Mode
	tracked                    bool
	dirty                      []uint64
	fogVersion, mappingVersion uint64
	local                      PlayerID
	wordMask                   []uint16
	byteGrids                  [10][]uint8
	inValid, inCurrent         bool
	inLocal                    PlayerID
	inKey                      fogWindowKey
	originX, originZ, w, h     int32
}

func captureFogState(s *Service) fogState {
	st := fogState{
		mode: s.mode, tracked: s.fogTracked, dirty: slices.Clone(s.fogDirty),
		fogVersion: s.fogVersion, mappingVersion: s.mappingVersion, local: s.local,
		wordMask: slices.Clone(s.wordMask), inValid: s.fog.inValid, inCurrent: s.fog.inCurrent,
		inLocal: s.fog.inLocal, inKey: s.fog.inKey, originX: s.fog.originX, originZ: s.fog.originZ,
		w: s.fog.w, h: s.fog.h,
		ch0: slices.Clone(s.fog.ch0), ch1: slices.Clone(s.fog.ch1), in: slices.Clone(s.fog.in),
	}
	for p := range s.byteGrids {
		st.byteGrids[p] = slices.Clone(s.byteGrids[p])
	}
	return st
}

// referenceFog derives player's fog the way the service derives its own: a
// separate service holding copies of s's grids and mode, with player as its
// local player, rebuilt over the full map. history, when it differs from
// player, is the explored bit copied into player's bit.
func referenceFog(s *Service, terrain *world.Terrain, player, history PlayerID) *FogCache {
	ref := New(terrain, s.Mode()&^ModeFogCacheValid)
	for i, word := range s.wordMask {
		ref.wordMask[i] = word&^cellBit(player) | (word>>history&1)<<player
	}
	for p := range s.byteGrids {
		copy(ref.byteGrids[p], s.byteGrids[p])
	}
	ref.local = player
	ref.RebuildFog(0, 0)
	return ref.Fog()
}

func sameFog(a, b *FogCache) bool {
	return a.w == b.w && a.h == b.h && a.originX == b.originX && a.originZ == b.originZ &&
		bytes.Equal(a.ch0, b.ch0) && bytes.Equal(a.ch1, b.ch1)
}

// A playback's fog for another seat (DESIGN_MULTIPLAYER §10) is the fog the
// service derives for its local player, derived from that seat's own grids:
// for the local player it is the service's own cache, byte for byte, and for
// any other player it is what a service whose local player that was would
// derive. Its re-derivation reports a change exactly when a byte changes, and
// it leaves the service's own cache, mode, cell marks, revisions and grids as
// they were, while coverage moves, history accumulates, the current-coverage
// mode toggles and the local player changes.
func TestDeriveFogForIsThatPlayersFogAndTouchesNothing(t *testing.T) {
	const cells = 24
	terrain := &world.Terrain{CellW: cells * 2, CellH: cells * 2}
	s := New(terrain, ModeHistoryEnabled|ModeCurrentEnabled)
	seed := uint32(7)
	next := func(n int32) int32 {
		seed = seed*1664525 + 1013904223
		return int32((seed >> 8) % uint32(n))
	}
	type mover struct {
		x, z, r int32
		owner   PlayerID
	}
	movers := []mover{{2, 2, 3, 0}, {20, 5, 4, 1}, {10, 18, 2, 0}, {0, 23, 3, 2}, {23, 0, 2, 1}, {12, 12, 3, 2}}
	var stamped []struct {
		idx   int
		owner PlayerID
	}
	var caches [3]FogCache
	changes, steady := 0, 0
	for tick := range 120 {
		for _, st := range stamped {
			s.decByteGrid(st.idx, st.owner, false)
		}
		stamped = stamped[:0]
		for m := range movers {
			mv := &movers[m]
			mv.x = min(max(mv.x+next(3)-1, -2), cells+1)
			mv.z = min(max(mv.z+next(3)-1, -2), cells+1)
			if tick%5 == 4 {
				continue // hold still now and then, so some rebuilds change nothing
			}
			for z := mv.z - mv.r; z <= mv.z+mv.r; z++ {
				for x := mv.x - mv.r; x <= mv.x+mv.r; x++ {
					if x < 0 || z < 0 || x >= cells || z >= cells {
						continue
					}
					i := int(z*cells + x)
					s.setWordBit(i, mv.owner)
					s.incByteGrid(i, mv.owner, false)
					stamped = append(stamped, struct {
						idx   int
						owner PlayerID
					}{i, mv.owner})
				}
			}
		}
		switch {
		case tick%37 == 20:
			s.SetMode(s.Mode() ^ ModeCurrentEnabled)
		case tick%53 == 40:
			s.SetLocal((s.local + 1) % 3)
		}
		s.mode &^= ModeFogCacheValid
		s.RebuildFog(0, 0)
		for p := range PlayerID(3) {
			before := captureFogState(s)
			prev0, prev1 := slices.Clone(caches[p].ch0), slices.Clone(caches[p].ch1)
			changed, ok := s.DeriveFogFor(&caches[p], p)
			if !ok {
				t.Fatalf("tick %d: player %d's fog was not derived", tick, p)
			}
			if after := captureFogState(s); !reflect.DeepEqual(before, after) {
				t.Fatalf("tick %d: deriving player %d's fog changed the service's own fog state", tick, p)
			}
			want := referenceFog(s, terrain, p, p)
			if p == s.local {
				want = s.Fog()
			}
			if !sameFog(&caches[p], want) {
				t.Fatalf("tick %d: player %d's fog differs from its own full rebuild", tick, p)
			}
			same := bytes.Equal(prev0, caches[p].ch0) && bytes.Equal(prev1, caches[p].ch1)
			if changed == same {
				t.Fatalf("tick %d: player %d's fog reported changed %v with bytes unchanged %v", tick, p, changed, same)
			}
			if changed {
				changes++
			} else {
				steady++
			}
		}
	}
	if changes == 0 || steady == 0 {
		t.Fatalf("%d changed and %d steady derivations; the comparison proves nothing", changes, steady)
	}
	if _, ok := s.DeriveFogFor(s.Fog(), 1); ok {
		t.Fatal("the service's own cache was accepted as caller storage")
	}
	if _, ok := s.DeriveFogFor(&caches[0], 10); ok {
		t.Fatal("player 11's fog was derived")
	}
}

// Online, a hosted computer's picture reads its host's explored history and
// its own coverage, as its visibility queries do (historyPlayer).
func TestDeriveFogForHostedComputerReadsItsHostsHistory(t *testing.T) {
	terrain := &world.Terrain{CellW: 32, CellH: 32}
	s := New(terrain, ModeHistoryEnabled|ModeCurrentEnabled)
	s.EnableOwnerPerspectives()
	if !s.SetPerspectiveHost(2, 0) {
		t.Fatal("perspective host refused")
	}
	for i := range s.wordMask {
		switch {
		case i%3 == 0:
			s.setWordBit(i, 0)
		case i%5 == 0:
			s.setWordBit(i, 2)
		}
		if i%7 == 0 {
			s.incByteGrid(i, 2, false)
		}
	}
	var got FogCache
	if _, ok := s.DeriveFogFor(&got, 2); !ok {
		t.Fatal("the computer's fog was not derived")
	}
	if want := referenceFog(s, terrain, 2, 0); !sameFog(&got, want) {
		t.Fatal("the hosted computer's fog does not read its host's history and its own coverage")
	}
	if own := referenceFog(s, terrain, 2, 2); sameFog(&got, own) {
		t.Fatal("the fixture cannot tell the host's history from the computer's own")
	}
}

func cloneSensorIndex(x sensorCandidateIndex) sensorCandidateIndex {
	x.heads, x.counts, x.next = slices.Clone(x.heads), slices.Clone(x.counts), slices.Clone(x.next)
	x.order, x.candidates = slices.Clone(x.order), slices.Clone(x.candidates)
	return x
}

// The presentation sensor pass is the five sensor passes run for another
// viewer [03 R-VIS-01 §4]: each banked unit reads exactly the status the
// ordinary pass would leave on it were that viewer the viewing player,
// seeded from its current word. It writes no unit status word and no cloak
// deadline, and the real pass's diagnostics, candidate index, viewer and
// defeated flag are as the real pass left them.
func TestSensorTickForPresentationIsThatViewersPass(t *testing.T) {
	terrain := &world.Terrain{CellW: 64, CellH: 64}
	s := New(terrain, ModeHistoryEnabled|ModeCurrentEnabled)
	// The third player's unit stands in the viewer's line of sight only.
	s.incByteGrid(int(28*s.W+28), 1, false)
	type spec struct {
		id      uint16
		serial  uint64
		owner   PlayerID
		radar   int32
		jam     int32
		cloak   bool
		primary uint16
		hidden  bool
	}
	specs := []spec{
		{id: 1, serial: 11, owner: 0, radar: 300},
		{id: 2, serial: 12, owner: 0, cloak: true, primary: 1 << 1},
		{id: 3, serial: 13, owner: 1, radar: 300, primary: 1 << 0},
		{id: 4, serial: 14, owner: 1, cloak: true, primary: 1 << 0},
		{id: 5, serial: 15, owner: 1, jam: 100},
		{id: 6, serial: 16, owner: 0, hidden: true},
		{id: 9, serial: 19, owner: 2},
		{id: 7, serial: 0, owner: 0}, // still inside creation
	}
	pos := [][2]int64{{4, 4}, {20, 20}, {6, 4}, {21, 20}, {10, 10}, {12, 12}, {28, 28}, {5, 5}}
	flags := make([]uint32, len(specs))
	deadlines := make([]uint32, len(specs))
	units := make([]SensorUnit, len(specs))
	for i, sp := range specs {
		x, z := pos[i][0], pos[i][1]
		flags[i] = 0x40000000 | uint32(i)<<20
		deadlines[i] = uint32(100 + i)
		units[i] = SensorUnit{ID: sp.id, AllocationSerial: sp.serial, Owner: sp.owner, Status: &flags[i],
			X: tileWorld(x) + 16<<16, Z: tileWorld(z) + 16<<16, Alive: true, Active: true, Hidden: sp.hidden,
			RadarDistance: sp.radar, RadarJam: sp.jam, CanCloak: sp.cloak, MinCloakDistance: 100,
			OwnerLocallySimulated: true, PrimaryCandidateOf: sp.primary, DecloakDeadline: &deadlines[i]}
	}
	s.SetViewerDefeated(false)
	s.SensorTick(30, 3, units) // the viewing player's real pass
	for i := range deadlines {
		deadlines[i] = 7 // so a deadline write would show
	}

	realFlags, realDeadlines, realUnits := slices.Clone(flags), slices.Clone(deadlines), slices.Clone(units)
	inputs, byID, index := s.SensorInputs(), slices.Clone(s.sensorStatusByID), cloneSensorIndex(s.sensorIndex)
	var bank PresentationSensors
	for range 2 { // the second pass reuses the bank's storage
		s.SensorTickForPresentation(&bank, 1, false, 3, units)
	}
	if !slices.Equal(flags, realFlags) || !slices.Equal(deadlines, realDeadlines) || !slices.Equal(units, realUnits) {
		t.Fatal("the presentation pass wrote a unit's status word, cloak deadline or input")
	}
	if !reflect.DeepEqual(s.SensorInputs(), inputs) || !reflect.DeepEqual(s.sensorStatusByID, byID) ||
		!reflect.DeepEqual(s.sensorIndex, index) || s.local != 0 || s.viewerDefeated {
		t.Fatal("the presentation pass changed the real pass's diagnostics, index, viewer or defeated flag")
	}

	// The reference: the ordinary pass, with viewer 1 as the viewing player,
	// on copies of the same words.
	ref := New(terrain, s.Mode())
	copy(ref.byteGrids[1], s.byteGrids[1])
	ref.SetLocal(1)
	refFlags, refDeadlines := slices.Clone(flags), slices.Clone(deadlines)
	refUnits := slices.Clone(units)
	for i := range refUnits {
		refUnits[i].Status, refUnits[i].DecloakDeadline = &refFlags[i], &refDeadlines[i]
	}
	ref.SensorTick(30, 3, refUnits)
	differs := false
	for i, sp := range specs {
		got, ok := bank.Status(sp.id, sp.serial, flags[i])
		if sp.serial == 0 {
			if ok {
				t.Fatal("a unit still inside creation read a bank row")
			}
			continue
		}
		if !ok || got != refFlags[i] {
			t.Fatalf("unit %d reads %#x (%v), the viewer's own pass leaves %#x", sp.id, got, ok, refFlags[i])
		}
		if _, ok := bank.Status(sp.id, sp.serial+100, flags[i]); ok {
			t.Fatalf("unit %d's row was read by a later allocation of its slot", sp.id)
		}
		differs = differs || got != flags[i]
	}
	if !differs || slices.Equal(refDeadlines, realDeadlines) {
		t.Fatal("the fixture cannot tell the viewers' passes apart")
	}
	if got, _ := bank.Status(9, 19, flags[6]); got&SeenBit == 0 {
		t.Fatal("the third player's unit in the viewer's sight is not seen")
	}
	bank.Clear()
	if _, ok := bank.Status(1, 11, flags[0]); ok {
		t.Fatal("a cleared bank kept a row")
	}
}
