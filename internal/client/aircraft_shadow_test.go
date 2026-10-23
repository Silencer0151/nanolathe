package client

import (
	"github.com/nanolathe-gg/nanolathe/internal/drawlist"
	"github.com/nanolathe-gg/nanolathe/internal/render"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"testing"
)

// Enhanced artistic metadata: a hill reduces clearance, and water receives
// the shadow at sea level rather than at the submerged terrain (GPU design §34).
func TestAircraftShadowClearanceRefresh(t *testing.T) {
	c := reflectionScene(t) // detail scale, sea level 10
	g := &drawlist.ModelGeometry{Supersample: &drawlist.ModelGeometry{}}
	draw := &render.UnitDraw{Airborne: true, GroundY: 80 * numeric.FixedOne, WorldPos: [3]numeric.Fixed{0, 140 * numeric.FixedOne, 0}}
	for _, tc := range []struct {
		ground   int64
		airborne bool
		want     float32
	}{{80, true, 120}, {0, true, 260}, {0, false, 0}, {160, true, 0}} {
		draw.GroundY, draw.Airborne = numeric.Fixed(tc.ground)*numeric.FixedOne, tc.airborne
		c.setModelLightingHeight(g, draw)
		for _, p := range []*drawlist.ModelGeometry{g, g.Supersample, g.Clone()} {
			if p.AircraftShadowHeight != tc.want || p.AircraftShadowScale != 2 {
				t.Fatalf("ground=%d airborne=%v: height=%v scale=%v", tc.ground, tc.airborne, p.AircraftShadowHeight, p.AircraftShadowScale)
			}
		}
	}
	c.enhanced = false
	draw.Airborne = true
	c.setModelLightingHeight(g, draw)
	if g.AircraftShadowHeight != 0 {
		t.Fatal("classic acquired Enhanced shadow metadata")
	}
}

// The Enhanced receiver extends the nearest valid terrain sample beyond the
// bilinear query's boundary. Original keeps the retail sentinel and its jump;
// the presentation-only extension must not reach the shared terrain query
// (DESIGN_GPU_RENDERER §34.1; [03 R-REN-03D §3]).
func TestAircraftShadowMapEdgeReceiver(t *testing.T) {
	const one = numeric.FixedOne
	c, v := cachedLiveRegressionSubject(t)
	c.terrain = &world.Terrain{CellW: 4, CellH: 5, Plot: make([]world.PlotCell, 20)}
	for z := int32(0); z < c.terrain.CellH; z++ {
		for x := int32(0); x < c.terrain.CellW; x++ {
			c.terrain.Plot[z*c.terrain.CellW+x].SetHeight(uint8(80 + 16*x + 8*z))
		}
	}
	v.BMCode, v.Y = true, 200*one
	for _, mode := range []struct {
		name               string
		enhanced, geometry bool
		mover              uint8
		modernAircraft     bool
	}{
		{"original", false, false, 2, false},
		{"classic comparison with enhanced art", true, false, 2, false},
		{"enhanced aircraft", true, true, 2, true},
		{"enhanced ground unit", true, true, 1, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			c.enhanced, c.geometryOnlyModels, v.MoverMode = mode.enhanced, mode.geometry, mode.mover
			for _, p := range []struct {
				x, z   numeric.Fixed
				height int64
			}{
				{24 * one, 24 * one, 116},
				{0, 24 * one, 92}, {-1, 24 * one, 92}, {-64 * one, 24 * one, 92},
				{48*one - 1, 24 * one, 139}, {48 * one, 24 * one, 139}, {80 * one, 24 * one, 139},
				{24 * one, 0, 104}, {24 * one, -1, 104}, {24 * one, -64 * one, 104},
				{24 * one, 64*one - 1, 135}, {24 * one, 64 * one, 135}, {24 * one, 96 * one, 135},
				{-1, -1, 80}, {80 * one, 96 * one, 158},
				{24 * one, 24 * one, 116}, // return from off-map without remembered height
			} {
				v.X, v.Z = p.x, p.z
				retail := c.terrain.HeightAt(v.X, v.Z)
				want := retail
				if mode.modernAircraft {
					want = numeric.FixedFromInt(p.height)
				}
				for _, deferred := range []bool{false, true} {
					draw, ok := c.buildUnitDraw(v, deferred)
					if !ok {
						t.Fatal("aircraft draw was not built")
					}
					if draw.GroundY != want {
						t.Fatalf("position (%v,%v), deferred=%v: ground=%v, want %v", p.x, p.z, deferred, draw.GroundY, want)
					}
					if draw.WorldPos != [3]numeric.Fixed{v.X, v.Y, v.Z} {
						t.Fatal("shadow receiver moved the aircraft body")
					}
				}
				if c.GroundHeightAt(v.X, v.Z) != retail {
					t.Fatal("shadow receiver changed the shared terrain sample")
				}
			}
		})
	}
}
