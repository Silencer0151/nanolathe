// Fog presentation: C13, C14, C15 [PLAN_05 WU-05-5].

package visibility

import (
	"math/bits"

	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// FogCache is presentation-only fog [03 §3.3] C13. Its validity is solely
// Service.mode bit 3; the cache itself never carries a second validity flag.
type FogCache struct {
	w, h int32
	ch0  []uint8 // channel zero: values 0..15
	ch1  []uint8 // channel one: values 0..15
	// originX/originZ identify the first cache cell in a viewport-aligned window.
	originX, originZ int32
	// in is the input the channels were last derived from: one byte per
	// visibility cell, bit 0 set when the cell is currently unseen (channel
	// one's source) and bit 1 when it is unexplored (channel zero's). inKey
	// is the window and map they were derived for. A rebuild over the same
	// window compares the live grids against in and re-derives only the
	// cells whose corners changed; see RebuildFogWindow. inValid is cleared
	// by every write that does not come from a rebuild.
	in      []uint8
	inKey   fogWindowKey
	inValid bool
	// inLocal and inCurrent are the local player and the current-coverage
	// mode bit the recorded input was read for. The cell marks of
	// Service.fogDirty describe changes against these alone.
	inLocal   PlayerID
	inCurrent bool
}

// fogWindowKey is everything besides the per-cell input that the derived
// bytes depend on: the cache window and the map it is clipped against.
type fogWindowKey struct {
	startX, startZ, endX, endZ int32
	mapW, mapH                 int32
}

// Fog returns the presentation fog cache [PLAN_05 Public API].
func (s *Service) Fog() *FogCache { return &s.fog }

// Dimensions returns the fog grid dimensions [03 §3.1] C1.
func (f *FogCache) Dimensions() (int32, int32) {
	if f == nil {
		return 0, 0
	}
	return f.w, f.h
}

// Origin returns the map-cell origin of a viewport-aligned cache window.
// Map-sized snapshots return (0,0). [03 §3.3]
func (f *FogCache) Origin() (int32, int32) {
	if f == nil {
		return 0, 0
	}
	return f.originX, f.originZ
}

// CopyChannelsInto copies the two channels into the caller's buffers, growing
// them only when they are too small, and returns them resized to the cache.
// The caller owns the result: writing through it never affects the cache, and
// a later rebuild never changes it (I6). It is the only way the channels leave
// the cache, and it copies once, straight into the storage that is published.
func (f *FogCache) CopyChannelsInto(dst0, dst1 []uint8) ([]uint8, []uint8) {
	if f == nil || f.ch0 == nil {
		return dst0[:0], dst1[:0]
	}
	return copyChannel(dst0, f.ch0), copyChannel(dst1, f.ch1)
}

func copyChannel(dst, src []uint8) []uint8 {
	if cap(dst) < len(src) {
		dst = make([]uint8, len(src))
	}
	dst = dst[:len(src)]
	copy(dst, src)
	return dst
}

// NewFogCacheFromChannelsAt reconstructs the detached cache with its
// viewport origin preserved across the snapshot boundary [03 §3.3].
func NewFogCacheFromChannelsAt(w, h, originX, originZ int32, ch0, ch1 []uint8) *FogCache {
	if w <= 0 || h <= 0 {
		return &FogCache{w: w, h: h, originX: originX, originZ: originZ}
	}
	n := int(w * h)
	fc := &FogCache{w: w, h: h, originX: originX, originZ: originZ, ch0: make([]uint8, n), ch1: make([]uint8, n)}
	if len(ch0) >= n {
		copy(fc.ch0, ch0[:n])
	} else if len(ch0) > 0 {
		copy(fc.ch0, ch0)
	}
	if len(ch1) >= n {
		copy(fc.ch1, ch1[:n])
	} else if len(ch1) > 0 {
		copy(fc.ch1, ch1)
	}
	for i := range fc.ch0 {
		fc.ch0[i] &= 0x0F
	}
	for i := range fc.ch1 {
		fc.ch1[i] &= 0x0F
	}
	return fc
}

// ReplaceChannelsAt refreshes a detached presentation cache in place. It
// retains channel storage when dimensions are unchanged so frame presentation
// does not allocate a new cache on every publication [03 §3.3][I6].
func (f *FogCache) ReplaceChannelsAt(w, h, originX, originZ int32, ch0, ch1 []uint8) bool {
	if f == nil || w <= 0 || h <= 0 {
		return false
	}
	product := int64(w) * int64(h)
	maxInt := int64(^uint(0) >> 1)
	if product <= 0 || product > maxInt {
		return false
	}
	n := int(product)
	if len(ch0) != n || len(ch1) != n {
		return false
	}
	if f.w != w || f.h != h || cap(f.ch0) < n || cap(f.ch1) < n {
		f.ch0 = make([]uint8, n)
		f.ch1 = make([]uint8, n)
	} else {
		f.ch0 = f.ch0[:n]
		f.ch1 = f.ch1[:n]
	}
	f.w, f.h, f.originX, f.originZ = w, h, originX, originZ
	f.inValid = false
	copy(f.ch0, ch0)
	copy(f.ch1, ch1)
	for i := 0; i < n; i++ {
		f.ch0[i] &= 0x0F
		f.ch1[i] &= 0x0F
	}
	return true
}

// Channel returns the two channel values for cell (x,y) for tests.
func (f *FogCache) Channel(x, y int32) (uint8, uint8) {
	if f == nil || f.ch0 == nil {
		return 0, 0
	}
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return 0, 0
	}
	idx := int(y*f.w + x)
	return f.ch0[idx], f.ch1[idx]
}

// SetChannel sets channel values for tests (presentation only).
func (f *FogCache) SetChannel(x, y int32, c0, c1 uint8) {
	if f == nil || f.ch0 == nil {
		return
	}
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return
	}
	idx := int(y*f.w + x)
	f.inValid = false
	f.ch0[idx] = c0 & 0x0F
	f.ch1[idx] = c1 & 0x0F
}

// RebuildFog lazily rebuilds the two-channel cache [03 §3.3] C13 [03 §3.3].
// The cache never writes word mask; values 15 are solid dark (channel0 Black/history) or patterned fill (channel1 Gray/current).
// Values 1..14 select GAF frame value-1 from variant families keyed by cell parity plus camera phase.
// Channel one (hi/Gray/current) renders first, then channel zero (lo/Black/history) [03 §3.3] [03 §3.3].
//
// Established: hi accumulates per-player byte-grid (cur==0) only when mode bit 1 is set else zeroed [03 §3.3],
// lo accumulates word-grid history mask 1<<player regardless [03 §3.3]; each holds a 4-bit nibble 0..15 via four bounded
// OR 1,2,4,8 sites (0 transparent, 15 solid dark, 1..14 index value-1 into Gray=hi/current and Black=lo/history four-way variant families
// with variant=(col+row+camPhase)&3 deterministically from floorMod(camera,32) residues [03 §3.3]), the map-border lines reached by the
// four conditional border fixups when the window crosses a map edge — never an unconditional 15 store [03 §3.3] "Map-edge propagation".
// Corner→bit 1=NW,2=NE,4=SW,8=SE remains supported inference pending asymmetric probe [03 §3.3].
// Camera residues/offX are used for viewport-sized cache alignment; for Nanolathe's map-sized cache we generate for the whole map and
// let BuildFogOpsWindowWithArtInto handle viewport clipping via hard 32 edges [03 §3.3] C13 — viewport edge forcing is therefore a render-time concern
// and the cache remains map-aligned. That is a deliberate layout divergence, not
// an open retail question: [03 §3.3] establishes the residues and the hard-32
// edge forcing, and BuildFogOpsWindowWithArtInto applies both at viewport clip time, so a
// map-aligned cache produces the same ops a viewport-aligned one would.
func (s *Service) RebuildFog(cameraX, cameraY int32) {
	if s == nil || s.fog.ch0 == nil || s.mode.FogCacheValid() {
		return
	}
	// Keep the historical entry point as a compatibility adapter. The only
	// producer is the viewport-aligned builder; a full-map view preserves the
	// old snapshot call while retaining edge residues and fixups.
	s.RebuildFogWindow(cameraX, cameraY, s.W*32, s.H*32)
}

// RebuildFogWindow rebuilds a cache sized to the camera viewport plus the
// one-cell border.  The cache is aligned to visibility-cell corners and is
// derived from the authoritative stores without modifying them. [03 §3.3]
//
// The cache is always window-addressed, including a zero-origin window.
func (s *Service) RebuildFogWindow(cameraX, cameraZ, viewW, viewH int32) {
	if s == nil || viewW <= 0 || viewH <= 0 || s.W <= 0 || s.H <= 0 {
		return
	}
	key, ok := s.fogWindow(cameraX, cameraZ, viewW, viewH)
	if !ok {
		return
	}
	w, h := key.endX-key.startX, key.endZ-key.startZ
	if s.mode.FogCacheValid() && s.fog.originX == key.startX && s.fog.originZ == key.startZ && s.fog.w == w && s.fog.h == h {
		return
	}
	if s.fog.derivedFor(key) {
		// With a complete set of marks only the marked cells can differ from
		// the recorded input; otherwise every cell is compared. Both visit the
		// changed cells in the same row-major order, so both derive the same
		// bytes and report the same change.
		var changed bool
		if s.fogTracked && s.fog.inLocal == s.local && s.fog.inCurrent == s.mode.CurrentEnabled() {
			changed = s.updateMarkedFogCells(key)
		} else {
			changed = s.fog.updateCells(key, s.fogSource())
		}
		s.trackFogInputs()
		s.mode |= ModeFogCacheValid
		// Unchanged bytes keep their revision, so publication and
		// presentation can keep the copy they already hold.
		if changed {
			s.fogVersion++
		}
		return
	}
	s.fog.derive(key, s.fogSource())
	s.trackFogInputs()
	s.mode |= ModeFogCacheValid
	// The revision moves only after the derived bytes and their address window
	// are complete, so publication can retain a previous immutable copy.
	s.fogVersion++
}

// fogWindow is the cache window of a camera viewport: its cells cover the
// viewport and one border cell on each side, and the key records the map
// the derivation is clipped against [03 §3.3].
func (s *Service) fogWindow(cameraX, cameraZ, viewW, viewH int32) (fogWindowKey, bool) {
	startX := numeric.FloorDiv(cameraX-16, 32) - 1
	startZ := numeric.FloorDiv(cameraZ-16, 32) - 1
	endX := numeric.FloorDiv(cameraX+viewW-16+31, 32) + 1
	endZ := numeric.FloorDiv(cameraZ+viewH-16+31, 32) + 1
	if endX-startX <= 0 || endZ-startZ <= 0 {
		return fogWindowKey{}, false
	}
	return fogWindowKey{startX: startX, startZ: startZ, endX: endX, endZ: endZ, mapW: s.W, mapH: s.H}, true
}

// derivedFor reports whether f holds a complete derivation for key, whose
// recorded input a comparing update can bring up to date.
func (f *FogCache) derivedFor(key fogWindowKey) bool {
	w, h := key.endX-key.startX, key.endZ-key.startZ
	return f.inValid && f.inKey == key && f.w == w && f.h == h && len(f.ch0) == int(w*h) &&
		f.originX == key.startX && f.originZ == key.startZ && len(f.in) == int(key.mapW*key.mapH)
}

// derive fills f for key from src in full — the four corner seeds of every
// map cell, then the map-edge fixups — and records the input it read, so a
// later update need re-derive only the cells whose input changed. Both the
// service's own cache and a caller's (DeriveFogFor) are derived here.
func (f *FogCache) derive(key fogWindowKey, src fogSource) {
	startX, startZ, endX, endZ := key.startX, key.startZ, key.endX, key.endZ
	mapW, mapH := key.mapW, key.mapH
	w, h := endX-startX, endZ-startZ
	n := int(w * h)
	if f.w != w || f.h != h || len(f.ch0) != n {
		f.w, f.h = w, h
		f.ch0 = make([]uint8, n)
		f.ch1 = make([]uint8, n)
	}
	f.originX, f.originZ = startX, startZ
	for i := range f.ch0 {
		f.ch0[i], f.ch1[i] = 0, 0
	}
	put := func(dst []uint8, gx, gz int32, bit uint8) {
		if gx < startX || gx >= endX || gz < startZ || gz >= endZ {
			return
		}
		dst[int((gz-startZ)*w+(gx-startX))] |= bit
	}
	seed := func(dst []uint8, gx, gz int32) {
		put(dst, gx, gz, 1)
		put(dst, gx-1, gz, 2)
		put(dst, gx, gz-1, 4)
		put(dst, gx-1, gz-1, 8)
	}
	cells := int(mapW * mapH)
	if cap(f.in) < cells {
		f.in = make([]uint8, cells)
	}
	f.in = f.in[:cells]
	for gz := int32(0); gz < mapH; gz++ {
		for gx := int32(0); gx < mapW; gx++ {
			i := int(gz*mapW + gx)
			v := src.at(i)
			f.in[i] = v
			if v&fogUnseen != 0 {
				seed(f.ch1, gx, gz)
			}
			if v&fogUnexplored != 0 {
				seed(f.ch0, gx, gz)
			}
		}
	}
	// Border fixups are conditional ORs and run in the established order — top,
	// bottom, left, right, on shared bytes, so a corner cell compounds through
	// two of them and reaches 15 [03 §3.3] "Map-edge propagation".
	//
	// They are anchored to the MAP border, not to the cache window. Retail's
	// rows/cols `0` and `h-2` name the void line just outside the map and the
	// last in-map cell line, which its cache reaches exactly when the window
	// overshoots the edge by one cell; §3.3 records that as supported inference
	// and says anchoring to the map edge is visually equivalent. Nanolathe's
	// window carries a wider border than retail's, so keying off the window's
	// own first/last-but-one line put every fixup on a line no tile ever seeds,
	// where a conditional OR does nothing: the north and west borders then drew
	// partial cloud art instead of the solid unexplored fill. The four lines
	// below are the ones the seeding actually reaches — cell row -1 takes bits
	// 4 and 8 from tile row 0, cell row H-1 takes bits 1 and 2 from tile row
	// H-1, and the two columns mirror that — so each becomes 15 exactly when
	// the in-map tiles behind it are unexplored [03 §3.3].
	fix := func(dst []uint8, gx, gz int32, a, b, c, d uint8) {
		if gx < startX || gx >= endX || gz < startZ || gz >= endZ {
			return
		}
		i := int((gz-startZ)*w + gx - startX)
		if dst[i]&a != 0 {
			dst[i] |= b
		}
		if dst[i]&c != 0 {
			dst[i] |= d
		}
	}
	if startZ < 0 { // window crosses the north edge [03 §3.3]
		for gx := startX; gx < endX; gx++ {
			fix(f.ch1, gx, -1, 4, 1, 8, 2)
			fix(f.ch0, gx, -1, 4, 1, 8, 2)
		}
	}
	if endZ > mapH { // window crosses the south edge [03 §3.3]
		for gx := startX; gx < endX; gx++ {
			fix(f.ch1, gx, mapH-1, 1, 4, 2, 8)
			fix(f.ch0, gx, mapH-1, 1, 4, 2, 8)
		}
	}
	if startX < 0 { // window crosses the west edge [03 §3.3]
		for gz := startZ; gz < endZ; gz++ {
			fix(f.ch1, -1, gz, 8, 4, 2, 1)
			fix(f.ch0, -1, gz, 8, 4, 2, 1)
		}
	}
	if endX > mapW { // window crosses the east edge [03 §3.3]
		for gz := startZ; gz < endZ; gz++ {
			fix(f.ch1, mapW-1, gz, 4, 8, 1, 2)
			fix(f.ch0, mapW-1, gz, 4, 8, 1, 2)
		}
	}
	f.inKey, f.inValid = key, true
}

// The two input bits of one visibility cell, as FogCache.in records them.
const (
	fogUnseen     uint8 = 1 // channel one: current coverage is enabled and the local byte grid is zero
	fogUnexplored uint8 = 2 // channel zero: the local player's history bit is clear
)

// fogSource reads the per-cell fog input from the authoritative grids for the
// local player and the current mode.
type fogSource struct {
	grid    []uint8
	word    []uint16
	bit     uint16
	current bool
}

func (s *Service) fogSource() fogSource {
	return fogSource{grid: s.byteGrids[s.local], word: s.wordMask, bit: cellBit(s.local), current: s.mode.CurrentEnabled()}
}

func (src fogSource) at(i int) uint8 {
	var v uint8
	if src.current && i < len(src.grid) && src.grid[i] == 0 {
		v |= fogUnseen
	}
	if i < len(src.word) && src.word[i]&src.bit == 0 {
		v |= fogUnexplored
	}
	return v
}

// updateCells brings a cache that was fully derived for this window up to
// date with src and reports whether any channel byte changed.
//
// Every channel byte is a function of the input of the (up to) four
// visibility cells whose corners meet at it, plus the map-edge fixups, which
// read and write only that byte. So a byte whose four inputs are unchanged is
// already correct, and one whose inputs changed is re-derived from them in
// full. The walk updates the recorded input as it goes and re-derives the four
// bytes around each change at once; a byte with a second changed corner later
// in the walk is derived again then, so every byte ends derived from final
// inputs. The result is byte-identical to the full rebuild [03 §3.3].
func (f *FogCache) updateCells(key fogWindowKey, src fogSource) bool {
	in := f.in
	changed := false
	for gz := int32(0); gz < key.mapH; gz++ {
		row := int(gz * key.mapW)
		for gx := int32(0); gx < key.mapW; gx++ {
			v := src.at(row + int(gx))
			if v == in[row+int(gx)] {
				continue
			}
			in[row+int(gx)] = v
			for _, d := range [4][2]int32{{0, 0}, {-1, 0}, {0, -1}, {-1, -1}} {
				if f.deriveCell(key, gx+d[0], gz+d[1]) {
					changed = true
				}
			}
		}
	}
	return changed
}

// updateMarkedFogCells is updateCells over the marked cells alone. Every
// cell whose input changed since the recorded input was brought up to date is
// marked (Service.fogDirty), so the cells it skips are cells the full walk
// would skip too; it visits the rest in ascending index — row-major — order,
// exactly as the full walk does, and clears the marks as it goes.
func (s *Service) updateMarkedFogCells(key fogWindowKey) bool {
	src := s.fogSource()
	in := s.fog.in
	w := int(s.W)
	changed := false
	for wi, word := range s.fogDirty {
		if word == 0 {
			continue
		}
		s.fogDirty[wi] = 0
		for word != 0 {
			i := wi<<6 + bits.TrailingZeros64(word)
			word &= word - 1
			v := src.at(i)
			if v == in[i] {
				continue
			}
			in[i] = v
			gx, gz := int32(i%w), int32(i/w)
			for _, d := range [4][2]int32{{0, 0}, {-1, 0}, {0, -1}, {-1, -1}} {
				if s.fog.deriveCell(key, gx+d[0], gz+d[1]) {
					changed = true
				}
			}
		}
	}
	return changed
}

// trackFogInputs starts a complete set of marks against the input the cache
// just recorded: no cell is marked, and the per-cell writers mark from here.
func (s *Service) trackFogInputs() {
	words := (int(s.W)*int(s.H) + 63) / 64
	if len(s.fogDirty) != words {
		s.fogDirty = make([]uint64, words)
	} else {
		clear(s.fogDirty)
	}
	s.fog.inLocal, s.fog.inCurrent = s.local, s.mode.CurrentEnabled()
	s.fogTracked = true
}

// deriveCell recomputes the channel byte at map cell (ox, oz) from the
// recorded inputs, exactly as the full rebuild derives it: the four corner
// seeds, then the edge fixups in their order (top, bottom, left, right). It
// reports whether the byte changed; a cell outside the window has none.
func (f *FogCache) deriveCell(key fogWindowKey, ox, oz int32) bool {
	if ox < key.startX || ox >= key.endX || oz < key.startZ || oz >= key.endZ {
		return false
	}
	mapW, mapH := key.mapW, key.mapH
	var c0, c1 uint8
	corner := func(ix, iz int32, bit uint8) {
		if ix < 0 || iz < 0 || ix >= mapW || iz >= mapH {
			return
		}
		v := f.in[int(iz*mapW+ix)]
		if v&fogUnseen != 0 {
			c1 |= bit
		}
		if v&fogUnexplored != 0 {
			c0 |= bit
		}
	}
	corner(ox, oz, 1)
	corner(ox+1, oz, 2)
	corner(ox, oz+1, 4)
	corner(ox+1, oz+1, 8)
	fix := func(c *uint8, a, b, cc, d uint8) {
		if *c&a != 0 {
			*c |= b
		}
		if *c&cc != 0 {
			*c |= d
		}
	}
	edge := func(a, b, cc, d uint8) {
		fix(&c1, a, b, cc, d)
		fix(&c0, a, b, cc, d)
	}
	if key.startZ < 0 && oz == -1 {
		edge(4, 1, 8, 2)
	}
	if key.endZ > mapH && oz == mapH-1 {
		edge(1, 4, 2, 8)
	}
	if key.startX < 0 && ox == -1 {
		edge(8, 4, 2, 1)
	}
	if key.endX > mapW && ox == mapW-1 {
		edge(4, 8, 1, 2)
	}
	i := int((oz-key.startZ)*f.w + ox - key.startX)
	if f.ch0[i] == c0 && f.ch1[i] == c1 {
		return false
	}
	f.ch0[i], f.ch1[i] = c0, c1
	return true
}

// The plot flag byte's never-explored marker (byte 0x0C bit 0x04) of C14
// [03 §3.3] has no writer here. Nothing in this build sets or clears it:
// presentation answers "never explored" from the fog cache's channel-zero
// solid value at the object's tile, and `world.PlotCell.IsUnexplored` is the
// only reader. The set/clear pair that once stood here was called by nothing
// but its own test.
