package movement

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Change markers at the arrival boundary, before the ordinary integrator
// brakes to a hold (Modern aircraft tactics, issue 121).
func TestAirHoverSlidesAtArrivalWithoutDwell(t *testing.T) {
	for _, tick := range []uint32{101, 0} {
		sys, w, u := wideAirFixture(t)
		target := airTargetFor(t, sys, w, 28, 16)
		u.InstallWeapon(0, &content.WeaponDef{ID: 1, Range: 300})
		n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Owner: u.Handle, Target: target.Handle, Phase: 2}
		p := sys.airPilotFor(u, n, tick-1)
		p.fresh, p.standSet, p.standHeld = false, true, true
		p.standX, p.standZ, p.standTick = int64(u.X>>16)+25, int64(u.Z>>16), tick-1
		p.standVisit = tick - 1
		random := *orders.QueueOfUnit(u).Binding().SimRNG
		sys.legAirToGroundHoverModern(u, n, tick)
		if p.standTick != tick-1 {
			t.Fatal("gunship turned before reaching the arrival boundary")
		}
		u.X += numeric.FixedFromInt(1)
		sys.legAirToGroundHoverModern(u, n, tick+1)
		if p.standTick != tick+1 || p.standX == int64(u.X>>16)+24 && p.standZ == int64(u.Z>>16) {
			t.Fatal("gunship waited at the arrival boundary")
		}
		if *orders.QueueOfUnit(u).Binding().SimRNG != random {
			t.Fatal("Modern standoff selection drew RNG")
		}
	}
}

// A lower exposure score must not turn a requested slide into another hold
// at the same firing point. Both minimum moves are existing Modern tuning.
func TestAirHoverRepositionMakesProgressUnderCover(t *testing.T) {
	for _, underFire := range []bool{false, true} {
		sys, _, u := wideAirFixture(t)
		u.X, u.Z = numeric.FixedFromInt(1330), numeric.FixedFromInt(512)
		p := &airPilot{standSet: true, standX: 1330, standZ: 512}
		threats := []airThreat{{x: 824, z: 512, r: 550, w: 100}}
		minMove := int64(64)
		if underFire {
			minMove = 120
		}
		for tick := uint32(100); tick < 103; tick++ {
			x, z := int64(u.X>>16), int64(u.Z>>16)
			sys.airChooseStandoff(u, p, 1024, 512, 360, threats, underFire, tick)
			dx, dz := p.standX-x, p.standZ-z
			if dx*dx+dz*dz < minMove*minMove {
				t.Fatalf("under fire=%v: repeated standoff moved only (%d,%d)", underFire, dx, dz)
			}
			u.X, u.Z = numeric.FixedFromInt(p.standX), numeric.FixedFromInt(p.standZ)
		}
	}
}

func TestAirHoverRepositionKeepsFallbackWhenNoFullMoveFits(t *testing.T) {
	sys, _, u := wideAirFixture(t)
	u.X, u.Z = numeric.FixedFromInt(1024), numeric.FixedFromInt(512)
	p := &airPilot{standSet: true, standX: 1024, standZ: 512}
	// Both rings lie within 120 units. Keep an on-map firing point rather
	// than dropping the marker or inventing an out-of-range destination.
	sys.airChooseStandoff(u, p, 1024, 512, 64, nil, true, 100)
	dx, dz := p.standX-1024, p.standZ-512
	if dx*dx+dz*dz == 0 || dx*dx+dz*dz >= 120*120 || !sys.airOnMap(p.standX, p.standZ, 48) {
		t.Fatal("short-range gunship lost its valid fallback firing point")
	}
}

// Issue 121: changing rings on the same bearing meets the old travel-distance
// floor but sends the gunship forwards and backwards instead of sideways.
func TestAirHoverRepositionMovesAcrossTargetBearing(t *testing.T) {
	for _, under := range []bool{false, true} {
		sys, _, u := wideAirFixture(t)
		const tx, tz, weaponRange = 1024, 512, 360
		for j := 0; j < 16; j++ {
			for _, radius := range []int64{weaponRange * 2 / 3, weaponRange * 17 / 20} {
				x, z := airAlong(tx, tz, uint16(j*4096), radius)
				u.X, u.Z = numeric.FixedFromInt(x), numeric.FixedFromInt(z)
				p := &airPilot{standSet: true, standX: x, standZ: z}
				sys.airChooseStandoff(u, p, tx, tz, weaponRange, nil, under, 100)
				cross := abs64((p.standX-x)*(z-tz) - (p.standZ-z)*(x-tx))
				minimum := int64(64)
				if under {
					minimum = 120
				}
				if cross < minimum*isqrt64((x-tx)*(x-tx)+(z-tz)*(z-tz)) {
					t.Fatalf("under=%v bearing=%d radius=%d: radial reposition from (%d,%d) to (%d,%d)", under, j, radius, x, z, p.standX, p.standZ)
				}
			}
		}
	}
}

// Range eligibility does not establish a clear shot. A physical refusal
// from the actual weapon visit must redirect even outside the old sampling
// window; reload/aim/coordination waits supply no such observation.
func TestAirHoverUsesPhysicalRefusalForFiringPoint(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		sys, w, u := wideAirFixture(t)
		target := airTargetFor(t, sys, w, 64, 32)
		u.InstallWeapon(0, &content.WeaponDef{ID: 1, Range: 360})
		u.X, u.Z = target.X+240<<16, target.Z
		n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Owner: u.Handle, Target: target.Handle, Phase: 2}
		p := sys.airPilotFor(u, n, 100)
		p.fresh, p.standSet, p.standHeld = false, true, true
		p.standX, p.standZ = int64(u.X>>16), int64(u.Z>>16)+80
		p.standTick, p.standVisit = 100, 100
		previews := 0
		q := orders.QueueOfUnit(u)
		q.Binding().Weapons = orders.NewWeaponAdapter(orders.WeaponAdapterConfig{
			CanEngage: func(*units.Unit, pool.Handle, int) bool { return true },
			FiringPositionBlocked: func(shooter, victim *units.Unit, tick uint32) bool {
				if shooter != u || victim != target {
					t.Fatal("wrong firing observation identities")
				}
				return blocked
			},
			FiringPositionClear: func(shooter, victim *units.Unit, tick uint32, x, y, z numeric.Fixed) bool {
				previews++
				marker := sys.newFrozenTerrainPointMarker(u, target.Handle, Vec3{X: x, Z: z})
				marker.setAltitudeOffset(int16(u.Def.CruiseAlt))
				if y != marker.goal.Y {
					t.Fatal("preview did not use candidate cruise altitude")
				}
				return x < target.X
			},
		})
		random := *q.Binding().SimRNG
		sys.legAirToGroundHoverModern(u, n, 101)
		if blocked {
			if p.standTick != 101 || p.standX >= int64(target.X>>16) || previews == 0 {
				t.Fatal("physical refusal retained an obstructed firing point")
			}
			point := [2]int64{p.standX, p.standZ}
			sys.legAirToGroundHoverModern(u, n, 102)
			if p.standTick != 101 || point != [2]int64{p.standX, p.standZ} {
				t.Fatal("repeated refusal abandoned an already-clear escape leg")
			}
			if p.refused != 0 {
				t.Fatal("physical obstruction was counted as a range refusal")
			}
		} else if p.standTick != 100 || previews != 0 {
			t.Fatal("a nonphysical wait searched for a new firing point")
		}
		if *q.Binding().SimRNG != random {
			t.Fatal("candidate previews drew RNG")
		}
	}
}

// Escaping an obstruction must not immediately select another blind firing
// point. Reuse only a slide previewed clear between both endpoints, for the
// same target geometry and threat picture (Modern aircraft tactics).
func TestAirHoverKeepsClearSlideUntilConditionsChange(t *testing.T) {
	for _, change := range []string{"none", "position", "target", "threats", "damage", "order", "visit-gap"} {
		t.Run(change, func(t *testing.T) {
			sys, w, u := wideAirFixture(t)
			target := airTargetFor(t, sys, w, 64, 32)
			u.InstallWeapon(0, &content.WeaponDef{ID: 1, Range: 360})
			u.X, u.Z = target.X+240<<16, target.Z
			n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Owner: u.Handle, Target: target.Handle, Phase: 2}
			p := sys.airPilotFor(u, n, 100)
			p.fresh = false
			blocked := true
			previews := 0
			q := orders.QueueOfUnit(u)
			q.Binding().Weapons = orders.NewWeaponAdapter(orders.WeaponAdapterConfig{
				CanEngage:             func(*units.Unit, pool.Handle, int) bool { return true },
				FiringPositionBlocked: func(*units.Unit, *units.Unit, uint32) bool { return blocked },
				FiringPositionClear: func(_ *units.Unit, _ *units.Unit, _ uint32, x, y, z numeric.Fixed) bool {
					previews++
					return x < target.X
				},
			})
			random := *q.Binding().SimRNG
			sys.legAirToGroundHoverModern(u, n, 101)
			if p.slideTarget != target {
				t.Fatal("clear half-plane did not produce a reusable lateral slide")
			}
			a, b := [2]int64{p.standX, p.standZ}, [2]int64{p.slideX, p.slideZ}
			u.X, u.Z = numeric.FixedFromInt(a[0]), numeric.FixedFromInt(a[1])
			blocked, previews = false, 0
			tick := uint32(102)
			switch change {
			case "position":
				target.X++ // even a sub-unit target move invalidates the observation
			case "target":
				replacement := airTargetFor(t, sys, w, 70, 32)
				replacement.X, replacement.Y, replacement.Z = target.X, target.Y, target.Z
				n.Target = replacement.Handle
			case "threats":
				sys.airThreats[u.Owner].version++
			case "damage":
				u.Health--
			case "order":
				copy := *n
				n = &copy
			case "visit-gap":
				tick++ // a retail-leg visit must not retain Modern movement goals
			}
			sys.legAirToGroundHoverModern(u, n, tick)
			if change == "none" {
				if [2]int64{p.standX, p.standZ} != b || [2]int64{p.slideX, p.slideZ} != a {
					t.Fatal("arrival discarded the clear slide after firing resumed")
				}
			} else if p.slideTarget != nil {
				t.Fatal("changed conditions retained the previous firing evidence")
			}
			if previews != 0 || *q.Binding().SimRNG != random {
				t.Fatal("slide reuse or invalidation queried expired firing evidence or drew RNG")
			}
		})
	}
}

func TestAirHoverFailedFiringSearchKeepsMovingUntilRetry(t *testing.T) {
	for _, change := range []string{"wait", "position", "threats", "arrival"} {
		t.Run(change, func(t *testing.T) {
			sys, w, u := wideAirFixture(t)
			target := airTargetFor(t, sys, w, 64, 32)
			u.InstallWeapon(0, &content.WeaponDef{ID: 1, Range: 360})
			u.X, u.Z = target.X+240<<16, target.Z
			n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Owner: u.Handle, Target: target.Handle, Phase: 2}
			p := sys.airPilotFor(u, n, 100)
			p.fresh = false
			previews := 0
			orders.QueueOfUnit(u).Binding().Weapons = orders.NewWeaponAdapter(orders.WeaponAdapterConfig{
				CanEngage:             func(*units.Unit, pool.Handle, int) bool { return true },
				FiringPositionBlocked: func(*units.Unit, *units.Unit, uint32) bool { return true },
				FiringPositionClear: func(*units.Unit, *units.Unit, uint32, numeric.Fixed, numeric.Fixed, numeric.Fixed) bool {
					previews++
					return false
				},
			})
			sys.legAirToGroundHoverModern(u, n, 101)
			if p.noClearTarget != target || previews == 0 {
				t.Fatal("first physical refusal did not search immediately")
			}
			flight := sys.FlightCommandFor(u.Handle, u)
			point, marker := [2]int64{p.standX, p.standZ}, flight.Payload
			previews = 0
			switch change {
			case "position":
				target.X++
			case "threats":
				sys.airThreats[u.Owner].version++
			case "arrival":
				u.X, u.Z = numeric.FixedFromInt(p.standX), numeric.FixedFromInt(p.standZ)
			}
			for tick := uint32(102); tick < 116; tick++ {
				sys.legAirToGroundHoverModern(u, n, tick)
				if change != "wait" {
					if p.standTick != tick || previews == 0 {
						t.Fatal("changed conditions retained an obsolete failed search")
					}
					return
				}
				if p.standTick != 101 || [2]int64{p.standX, p.standZ} != point || flight.Payload != marker || previews != 0 {
					t.Fatal("all-blocked search churned its moving fallback before retry")
				}
			}
			sys.legAirToGroundHoverModern(u, n, 116)
			if p.standTick != 116 || previews == 0 {
				t.Fatal("failed search did not retry after 15 ticks")
			}
		})
	}
}

func TestAirHoverClearSlideMovesLaterallyInBothDirections(t *testing.T) {
	for _, bearing := range []uint16{4096, 8192} {
		sys, w, u := wideAirFixture(t)
		target := airTargetFor(t, sys, w, 64, 32)
		tx, tz := int64(target.X>>16), int64(target.Z>>16)
		const weaponRange, minimum = int64(370), int64(120)
		ax, az := airAlong(tx, tz, 0, weaponRange*2/3)
		bx, bz := airAlong(tx, tz, bearing, weaponRange*17/20)
		u.X, u.Z = numeric.FixedFromInt(ax+1000), numeric.FixedFromInt(az)
		n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Target: target.Handle}
		p := sys.airPilotFor(u, n, 100)
		points := make(map[[2]numeric.Fixed]bool)
		for k := int64(0); k <= 4; k++ {
			points[[2]numeric.Fixed{numeric.FixedFromInt(ax + (bx-ax)*k/4), numeric.FixedFromInt(az + (bz-az)*k/4)}] = true
		}
		orders.QueueOfUnit(u).Binding().Weapons = orders.NewWeaponAdapter(orders.WeaponAdapterConfig{
			FiringPositionBlocked: func(*units.Unit, *units.Unit, uint32) bool { return true },
			FiringPositionClear: func(_ *units.Unit, _ *units.Unit, _ uint32, x, y, z numeric.Fixed) bool {
				return points[[2]numeric.Fixed{x, z}]
			},
		})
		sys.airChooseStandoff(u, p, tx, tz, weaponRange, nil, true, 100)
		if bearing == 4096 {
			// This mixed-ring pair moves 120 units laterally one way, but
			// only 94 on return. It must remain a single escape point.
			if p.slideTarget != nil {
				t.Fatal("accepted a return leg below the lateral floor")
			}
			continue
		}
		if p.slideTarget == nil {
			t.Fatal("rejected a clear mixed-ring pair with two lateral legs")
		}
		dx, dz := p.slideX-p.standX, p.slideZ-p.standZ
		cross := abs64(dx*(p.standZ-tz) - dz*(p.standX-tx))
		for _, point := range [][2]int64{{p.standX, p.standZ}, {p.slideX, p.slideZ}} {
			rx, rz := point[0]-tx, point[1]-tz
			if cross < minimum*isqrt64(rx*rx+rz*rz) {
				t.Fatal("retained slide fails a directional lateral floor")
			}
		}
	}
}

func TestAirHoverClearEndpointsDoNotProveClearSlide(t *testing.T) {
	sys, w, u := wideAirFixture(t)
	target := airTargetFor(t, sys, w, 64, 32)
	n := &orders.Node{ID: orders.Lookup("AirToGroundHover"), Target: target.Handle}
	p := sys.airPilotFor(u, n, 100)
	// Only the discrete endpoints are clear; every connecting segment has
	// blocked interior samples. A pilot may escape to one, but cannot shuttle
	// between two of them on the strength of those endpoints alone.
	points := make(map[[2]numeric.Fixed]bool)
	for _, radius := range []int64{240, 306} {
		for j := 0; j < 16; j++ {
			x, z := airAlong(int64(target.X>>16), int64(target.Z>>16), uint16(j*4096), radius)
			points[[2]numeric.Fixed{numeric.FixedFromInt(x), numeric.FixedFromInt(z)}] = true
		}
	}
	orders.QueueOfUnit(u).Binding().Weapons = orders.NewWeaponAdapter(orders.WeaponAdapterConfig{
		FiringPositionBlocked: func(*units.Unit, *units.Unit, uint32) bool { return true },
		FiringPositionClear: func(_ *units.Unit, _ *units.Unit, _ uint32, x, y, z numeric.Fixed) bool {
			return points[[2]numeric.Fixed{x, z}]
		},
	})
	sys.airChooseStandoff(u, p, int64(target.X>>16), int64(target.Z>>16), 360, nil, false, 100)
	if !points[[2]numeric.Fixed{numeric.FixedFromInt(p.standX), numeric.FixedFromInt(p.standZ)}] || p.slideTarget != nil {
		t.Fatal("point selection lost its clear fallback or joined obstructed endpoints")
	}
}

// Only Modern flies the aircraft pilots; Strict 3.1 and Community 3.9 keep the
// retail legs [04 R-AIR-01 §8] (DESIGN_MOVEMENT_PATH "Modern aircraft tactics").
func TestAirTacticsRulePolarity(t *testing.T) {
	if (StrictRules{}).AirTactics(nil) || (&CommunityRules{}).AirTactics(nil) || !(&ModernRules{}).AirTactics(nil) {
		t.Fatal("air tactics must be Modern only")
	}
}

// A bomber times its release on the dropped projectile's own motion and
// contact [06 §6.4][06 §8.1]: launch velocity from heading and scalar speed,
// wind, gravity, and the terrain test. The prediction must land where combat's
// kernel lands, on the same tick, or the middle bomb drifts off the target.
func TestAirDropImpactMatchesDroppedMotion(t *testing.T) {
	sys, _, _ := wideAirFixture(t)
	weapon := &content.WeaponDef{Dropped: true}
	for _, wind := range [][2]int32{{0, 0}, {9000, -4000}, {-12000, 15000}} {
		sys.AirWind = &world.Wind{DirX: wind[0], DirZ: wind[1]}
		for _, h := range []uint16{0, 0x2345, 0x9000, 0xC100} {
			sx, sy, sz := numeric.FixedFromInt(2000), numeric.FixedFromInt(220), numeric.FixedFromInt(512)
			speed := numeric.Fixed(7<<16 + 12345)
			ix, iz, k, ok := sys.airDropImpact(sx, sy, sz, h, speed, 0)
			if !ok {
				t.Fatalf("wind=%v heading=%#x: no prediction", wind, h)
			}
			var p combat.Projectile
			combat.InitDropped(&p, weapon, 0, combat.Vec3{X: sx, Y: sy, Z: sz}, combat.Vec3{}, 0, numeric.Angle(h), speed)
			ticks := int64(0)
			for {
				ticks++
				combat.AdvanceDropped(&p, weapon, uint32(ticks), combat.Vec3{X: numeric.Fixed(wind[0]), Z: numeric.Fixed(wind[1])}, sys.Terrain.Gravity)
				cell := sys.Terrain.PlotAt(world.WorldToCell(p.Pos.X), world.WorldToCell(p.Pos.Z))
				if int16(p.Pos.Y.Raw()>>16) < int16(cell.MinHeight()) || int16(p.Pos.Y.Raw()>>16) < int16(sys.Terrain.SeaLevel) {
					break
				}
				if ticks > 300 {
					t.Fatal("reference bomb never landed")
				}
			}
			if ticks != k || p.Pos.X != ix || p.Pos.Z != iz {
				t.Fatalf("wind=%v heading=%#x: predicted (%d,%d) at %d, dropped motion (%d,%d) at %d", wind, h, ix, iz, k, p.Pos.X, p.Pos.Z, ticks)
			}
		}
	}
}

// Going round anti-air: a circle dead ahead bends the heading onto a tangent
// that clears it, the chosen side is kept, and a circle round the goal itself
// is entered rather than avoided.
func TestAirAvoidClearsCircleAndKeepsSide(t *testing.T) {
	threats := []airThreat{{x: 1000, z: 500, r: 200, w: 30}}
	p := &airPilot{}
	// Heading 0xC000 travels toward +X [04 R-MOV-01 §4].
	h := airAvoid(p, 0, 500, 0xC000, 2000, 2000, 500, threats)
	if h == 0xC000 || p.avoidSide == 0 {
		t.Fatal("a circle dead ahead was not avoided")
	}
	ex, ez := airAlong(0, 500, h, 2000)
	if airSegmentExposure(0, 500, ex, ez, 0, threats) != 0 {
		t.Fatal("the avoiding heading still crosses the circle")
	}
	side := p.avoidSide
	airAvoid(p, 0, 520, 0xC100, 2000, 2000, 500, threats)
	if p.avoidSide != side {
		t.Fatal("the avoidance side flipped between ticks")
	}
	if got := airAvoid(&airPilot{}, 0, 500, 0xC000, 1000, 1000, 500, threats); got != 0xC000 {
		t.Fatal("a circle holding the goal was avoided")
	}
}

// Exposure is inside length times damage rate; a leg outside every circle
// costs nothing.
func TestAirSegmentExposure(t *testing.T) {
	threats := []airThreat{{x: 500, z: 0, r: 100, w: 10}}
	// A central crossing is 200 world units at rate 10, doubled for passing
	// over the turret.
	if got := airSegmentExposure(0, 0, 1000, 0, 0, threats); got < 3980 || got > 4020 {
		t.Fatalf("central crossing exposure=%d, want about 4000", got)
	}
	if got := airSegmentExposure(0, 200, 1000, 200, 0, threats); got != 0 {
		t.Fatalf("clear leg exposure=%d", got)
	}
}

// A strafing run breaks early only when its turn is then clear of every
// circle over the target: a target that one circle covers beyond the
// weapon's reach is overflown at speed, not broken off short inside it.
func TestAirStrafeBreakNeedsEveryCircle(t *testing.T) {
	const rng, turn = 659, 100
	near := airThreat{x: 100, z: 0, r: 200, w: 10}
	if got := airStrafeBreak(0, 0, -1000, 0, rng, turn, []airThreat{near}); got != 200 {
		t.Fatalf("break=%d, want 200: a turn radius beyond the reachable circle", got)
	}
	wide := airThreat{x: 0, z: 300, r: 800, w: 10}
	if got := airStrafeBreak(0, 0, -1000, 0, rng, turn, []airThreat{near, wide}); got != rng/5 {
		t.Fatalf("break=%d, want %d: overfly a target no break can clear", got, rng/5)
	}
}

// After a pass under anti-air a pilot flies straight out before it turns,
// when its heading leaves the cover soon enough.
func TestAirFlyOut(t *testing.T) {
	sys := &System{}
	threats := []airThreat{{x: 0, z: 0, r: 300, w: 10}}
	u := &units.Unit{}
	u.Move.Heading = 0xC000 // travelling toward +X [04 R-MOV-01 §4]
	if !sys.airFlyOut(u, 100, 0, 500, threats) {
		t.Fatal("a pilot inside cover with a short way out did not fly on")
	}
	if sys.airFlyOut(u, 100, 0, 100, threats) {
		t.Fatal("a way out beyond the limit was flown")
	}
	if sys.airFlyOut(u, 400, 0, 500, threats) {
		t.Fatal("a pilot already clear of cover flew on")
	}
}

// Against a tracking missile slower than twice its own speed, a fighter's leg
// straight over the launcher is priced below one passing it a few hundred
// units off; a slower aircraft keeps the plain rule, where the leg over the
// launcher is the dearer.
func TestAirSegmentExposureByAspect(t *testing.T) {
	threats := []airThreat{{x: 500, z: 0, r: 400, w: 10, missile: 1419946}}
	over := func(v int64) int64 { return airSegmentExposure(0, 0, 1000, 0, v, threats) }
	beside := func(v int64) int64 { return airSegmentExposure(0, 250, 1000, 250, v, threats) }
	if fast := int64(12 << 16); over(fast) >= beside(fast) {
		t.Fatalf("fighter: over=%d beside=%d, want the leg over the launcher cheaper", over(fast), beside(fast))
	}
	if slow := int64(6 << 16); over(slow) <= beside(slow) {
		t.Fatalf("slow aircraft: over=%d beside=%d, want the leg over the launcher dearer", over(slow), beside(slow))
	}
}
