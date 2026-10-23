package movement

// Modern aircraft tactics — Nanolathe Modern policy
// (DESIGN_MOVEMENT_PATH "Modern aircraft tactics"). Strict 3.1 and Community
// 3.9 never reach this file: movement.Rules.AirTactics answers false for
// them and the retail legs of airorders.go run unchanged.
//
// The policy changes only the goals the air executors give the ordinary
// flight command block. Speed, acceleration, braking, turn rate, cruise
// altitude, weapons, reload, damage and costs are the definition's and the
// retail integrator's [04 §10.1][04 R-AIR-01 §1]. Three ideas carry it:
//
//   - Carrot steering. The integrator accelerates toward its command point
//     and brakes on the curve that would stop it there [04 §10.1]; a goal
//     behind the aircraft therefore stalls it to a crawl while it pivots. A
//     pilot instead keeps its goal ahead of the nose, never more than
//     airTacticsTurnLimit off it, and far enough away that the braking curve
//     never engages, so turns are flown at the full authored turn rate and
//     at speed.
//   - A visible anti-air picture per player: the visible hostile units whose
//     weapons can reach an aircraft, with their range and damage rate, and
//     the frozen last-seen position of ones that have dropped out of sight.
//     Hidden positions are never read.
//   - Exposure: the length of a straight flight leg inside each anti-air
//     circle weighted by its damage rate. Runs and standoff points are chosen
//     by estimated time plus expected damage.
//
// Bombers additionally predict where a bomb released now would land, with
// the dropped motion's own arithmetic [06 §6.4] — the carrier's heading and
// scalar speed, global wind, per-tick gravity and terrain contact — and
// start the string so its middle bomb lands on the target's position at
// impact time. Every number here is Modern tuning, not a retail constant.
// The pilots draw no random numbers and charge no resources.

import (
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

const (
	// airTacticsTurnLimit is how far off the nose a steering goal may sit:
	// thirty degrees, more than any stock turn rate covers in a tick, so the
	// turn runs at the authored rate while the integrator's acceleration still
	// pulls mostly along the flight path.
	airTacticsTurnLimit = 0x1555
	// airTacticsCarrotMin keeps a steering goal beyond the producer's 320
	// world-unit heading consultation range [04 R-AIR-01 §1].
	airTacticsCarrotMin = 420
	// airThreatRefresh is how often a player's anti-air picture is rebuilt;
	// airThreatMemory how long a contact that left sight keeps its frozen
	// position; airThreatMargin the clearance added to an anti-air range.
	airThreatRefresh = 15
	airThreatMemory  = 900
	airThreatMargin  = 48
	airThreatMoved   = 96
	// airRunAlign is the straight, settled flight a bomber wants before its
	// release point, and airRunCandidates the number of run directions a
	// pilot compares. airPlanHold is how long a chosen run is kept before it is
	// reconsidered, unless the anti-air picture changes first.
	airRunAlign      = 160
	airRunCandidates = 16
	airPlanHold      = 240
	// airUnderFire is how long after losing health a pilot counts as being
	// shot at.
	airUnderFire = 90
	// airLossTicks prices losing a whole aircraft, in ticks of extra flight,
	// when a pilot trades time against expected damage.
	airLossTicks = 3000
	// airTurnInCover weights the exposure of a turn flown inside anti-air
	// cover: a turning aircraft is slower and easier to hit than one crossing
	// the cover in a straight line at speed (measured in the aircraft lab).
	airTurnInCover = 3
	// airWingSpread is the planning cost, in ticks, of a run heading within
	// about 17 degrees of a wingman's on the same target.
	airWingSpread = 90
	// airQuarterTurn and airHalfTurn are angle words (65536 to the circle);
	// airTangentMargin is the clearance added to a tangent past anti-air.
	airQuarterTurn   = 0x4000
	airHalfTurn      = 0x8000
	airTangentMargin = 0x0300
	// airRetargetHold is how long after giving up an automatic target for
	// the anti-air shooting it an aircraft keeps whatever it is given next.
	airRetargetHold = 90
)

// Pilot modes. The order record's phase stays the pump's; the mode is the
// pilot's own transient decision state.
const (
	pilotApproach uint8 = iota
	pilotReposition
)

// airReplan reports whether a held plan should be reconsidered: never
// planned, older than airPlanHold, or the anti-air picture has changed —
// though a changed picture is acted on at most every 30 ticks.
func airReplan(p *airPilot, pic *airThreatPicture, tick uint32) bool {
	return !p.planned || tick-p.planTick >= airPlanHold || (p.picture != pic.version && tick-p.planTick >= 30)
}

// airPilot is one aircraft's transient tactical state, bound to the exact
// unit and order record it was created for. It is never saved: a restored or
// re-ordered aircraft starts a fresh pilot on its next visit.
type airPilot struct {
	unit *units.Unit
	node *orders.Node

	fresh    bool
	mode     uint8
	run      uint16 // planned run heading
	entryX   int64  // reposition entry point, whole world units
	entryZ   int64
	planTick uint32
	planned  bool
	picture  uint32 // anti-air picture version the plan used

	hp         int32
	hitTick    uint32
	retargetAt uint32 // kept across the unit's order records
	healHP     int32
	avoidSide  int8
	turnSide   int8

	// coverExit caches airCoverExit on each run bearing for one target
	// point and picture version.
	coverExit      [airRunCandidates]int32
	coverX, coverZ int64
	coverVer       uint32
	coverSet       bool

	standX, standZ int64
	standTick      uint32
	standVisit     uint32
	standAim       Vec3
	standPicture   uint32
	standUnder     bool
	// A failed search retains its fallback briefly; a refused shot can recur
	// every tick without reloading, so searching again each time churns goals.
	noClearTarget *units.Unit
	// A refused shot can establish two firing points and the space between
	// them. Keep that slide after escaping cover instead of choosing an
	// unproven point again as soon as firing resumes.
	slideX, slideZ int64
	slideTarget    *units.Unit
	standSet       bool
	standHeld      bool
	refused        uint8
}

// airThreat is one anti-air contact in whole world units.
type airThreat struct {
	unit    *units.Unit
	x, z    int64
	r       int64 // weapon range plus airThreatMargin
	w       int64 // damage per second
	seen    uint32
	missile int64 // slowest tracking-missile speed, 16.16 per tick; 0 unless every weapon counted tracks
}

// airThreatPicture is one player's visible anti-air.
type airThreatPicture struct {
	ok         bool
	built      uint32
	version    uint32
	list       []airThreat
	pointIndex airPointIndex
}

// AirTacticsActive reports whether this system flies Modern pilots.
func (s *System) airTacticsActive() bool {
	return s != nil && s.rules().AirTactics(s)
}

func (s *System) airPilotFor(u *units.Unit, n *orders.Node, tick uint32) *airPilot {
	if s.airPilots == nil {
		s.airPilots = make(map[pool.Handle]*airPilot)
	}
	p := s.airPilots[u.Handle]
	if p == nil {
		p = &airPilot{}
		s.airPilots[u.Handle] = p
	}
	if p.unit != u || p.node != n {
		hp, hit, retarget := p.hp, p.hitTick, p.retargetAt
		if p.unit != u {
			hp, hit, retarget = u.Health, 0, 0
		}
		*p = airPilot{unit: u, node: n, fresh: true, hp: hp, hitTick: hit, retargetAt: retarget}
	}
	if u.Health < p.hp {
		p.hitTick = tick
	}
	p.hp = u.Health
	return p
}

func (p *airPilot) underFire(tick uint32) bool {
	return p.hitTick != 0 && tick-p.hitTick < airUnderFire
}

// airClaim posts the attack's target on the shared board, so other aircraft
// of the side looking for work leave a target this one is expected to
// destroy (combat's Modern target choice).
func (s *System) airClaim(u, target *units.Unit, tick uint32) {
	if s.AirBoard != nil {
		s.AirBoard.Claim(u, target, tick)
	}
}

// airRetarget ends an automatically issued attack whose aircraft is being
// hurt by visible anti-air other than its target, while the target itself is
// not anti-air: the order that issued the attack chooses again, and Modern
// target choice puts the anti-air hurting this side's aircraft first. A
// player's own attack order keeps its target.
func (s *System) airRetarget(u *units.Unit, n *orders.Node, p *airPilot, pic *airThreatPicture, tick uint32) bool {
	if n.Flags&orders.FlagAutoOp == 0 || !p.underFire(tick) || tick < p.retargetAt ||
		u.EngagementTarget == 0 || u.EngagementTarget == n.Target {
		return false
	}
	a, t := s.unitFor(u.EngagementTarget), s.unitFor(n.Target)
	if a == nil || !a.Alive || a.Dying || a.Owner == u.Owner {
		return false
	}
	shooting, current := false, false
	for i := range pic.list {
		switch c := &pic.list[i]; c.unit {
		case a:
			shooting = c.seen == pic.built // in sight now, so it can be chosen
		case t:
			current = true
		}
	}
	if !shooting || current {
		return false
	}
	p.retargetAt = tick + airRetargetHold
	return true
}

// --- the anti-air picture ---

// airAntiAir reports the longest range and the summed damage rate of t's
// weapons that can engage an aircraft: enabled, ranged, not dropped,
// command-fired, interceptor, water-only or paralyzing, and either direct or
// an authored anti-air ballistic weapon. Ballistic ground guns can rarely
// solve a moving aircraft and are not counted [06 §3.1].
func airAntiAir(t *units.Unit) (rng, dps, missile int64) {
	tracks := true
	for slot := 0; slot < units.NumSlots; slot++ {
		sl := t.SlotAt(slot)
		if sl == nil || sl.Weapon == nil || !sl.IsEnabled() {
			continue
		}
		w := sl.Weapon
		if w.Range <= 16 || w.Dropped || w.CommandFire || w.Interceptor || w.WaterWeapon || w.Paralyzer {
			continue
		}
		if w.Ballistic && !w.ToAirWeapon {
			continue
		}
		dmg := int64(w.DamageDefault)
		if dmg <= 0 {
			continue
		}
		if w.Burst > 1 {
			dmg *= int64(w.Burst)
		}
		reload := int64(w.ReloadTime)
		if reload < 1 {
			reload = 1
		}
		dps += dmg * 30 / reload
		if int64(w.Range) > rng {
			rng = int64(w.Range)
		}
		tracks = tracks && w.SelfProp && w.Tracks && w.WeaponVelocity > 0
		if tracks && (missile == 0 || int64(w.WeaponVelocity) < missile) {
			missile = int64(w.WeaponVelocity)
		}
	}
	if !tracks {
		missile = 0
	}
	return rng, dps, missile
}

// airThreatsFor returns observer's player's anti-air picture, rebuilding it
// at most every airThreatRefresh ticks. Visibility is the session's ordinary
// hostile-contact test, asked for the observing aircraft.
func (s *System) airThreatsFor(u *units.Unit, tick uint32) *airThreatPicture {
	owner := int(u.Owner)
	for len(s.airThreats) <= owner {
		s.airThreats = append(s.airThreats, airThreatPicture{})
	}
	pic := &s.airThreats[owner]
	if pic.ok && tick-pic.built < airThreatRefresh && tick >= pic.built {
		return pic
	}
	pic.ok, pic.built = true, tick
	changed := false
	kept := pic.list[:0]
	for _, t := range pic.list {
		if t.unit.Alive && !t.unit.Dying && tick-t.seen < airThreatMemory {
			kept = append(kept, t)
		} else {
			changed = true
		}
	}
	for i := len(kept); i < len(pic.list); i++ {
		pic.list[i] = airThreat{}
	}
	pic.list = kept
	var visible func(observer, target *units.Unit) bool
	if b := airBinding(u); b != nil {
		visible = b.DangerVisibleHook()
	}
	if visible != nil && s.world != nil {
		s.airScratch = s.world.AppendLiveSliced(s.airScratch[:0])
		for _, t := range s.airScratch {
			if t == nil || !t.Alive || t.Dying || t.Def == nil || t.Remaining != 0 || t.Owner == u.Owner {
				continue
			}
			r, w, mv := airAntiAir(t)
			if r == 0 || !visible(u, t) {
				continue
			}
			x, z := int64(t.X>>16), int64(t.Z>>16)
			found := false
			for i := range pic.list {
				e := &pic.list[i]
				if e.unit != t {
					continue
				}
				if abs64(e.x-x) > airThreatMoved || abs64(e.z-z) > airThreatMoved {
					changed = true
				} else {
					x, z = e.x, e.z // small moves keep the picture, and the plans made on it, stable
				}
				e.x, e.z, e.r, e.w, e.seen, e.missile = x, z, r+airThreatMargin, w, tick, mv
				found = true
				break
			}
			if !found {
				pic.list = append(pic.list, airThreat{unit: t, x: x, z: z, r: r + airThreatMargin, w: w, seen: tick, missile: mv})
				changed = true
			}
		}
	}
	if changed {
		pic.version++
	}
	pic.pointIndex.rebuild(pic.list) // every refresh, including unchanged plan versions
	return pic
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func isqrt64(v int64) int64 {
	if v <= 0 {
		return 0
	}
	return int64(isqrt(uint64(v)))
}

// airSegmentExposure is the summed damage rate times the length of the
// straight leg a→b inside each anti-air circle, in damage-per-second world
// units, for an aircraft whose full speed is v (16.16 per tick).
func airSegmentExposure(ax, az, bx, bz, v int64, threats []airThreat) int64 {
	dx, dz := bx-ax, bz-az
	l := isqrt64(dx*dx + dz*dz)
	var sum int64
	for i := range threats {
		t := &threats[i]
		cx, cz := t.x-ax, t.z-az
		r2 := t.r * t.r
		if l == 0 {
			continue
		}
		proj := (cx*dx + cz*dz) / l
		d2 := cx*cx + cz*cz - proj*proj
		if d2 < 0 {
			d2 = 0
		}
		if d2 >= r2 {
			continue
		}
		h := isqrt64(r2 - d2)
		s0, s1 := max(proj-h, 0), min(proj+h, l)
		if s1 <= s0 {
			continue
		}
		d := isqrt64(d2)
		if t.missile == 0 || 2*v < t.missile {
			// A leg passing close over the turret is worth more than one
			// grazing the edge of its reach: up to double at the centre.
			sum += (s1 - s0) * t.w * (2*t.r - d) / t.r
			continue
		}
		// A tracking missile pursues the aircraft's current position after a
		// leading launch [06 §6.7], so against an aircraft flying at least
		// half its speed how often it hits depends on where the launcher sits
		// off the nose. Nanolathe's aircraft lab measured Hawks at full speed
		// hit about four times as often with the launcher 45 to 90 degrees
		// off the nose as head-on, about half as often 90 to 135 degrees off,
		// and almost never once it is behind: a leg straight over a launcher
		// is safer than one passing it at a few hundred units. Slower
		// aircraft were hit about as often from every side. The weights are
		// sixteenths of the crossing rate, which counts double, as a pass
		// over the turret does under the plain rule.
		marks := [4]int64{proj - d, proj, proj + d, l}
		weights := [4]int64{4, 16, 7, 1}
		from := s0
		var w16 int64
		for k, m := range marks {
			to := min(max(m, s0), s1)
			if to > from {
				w16 += (to - from) * weights[k]
				from = to
			}
		}
		sum += w16 * t.w / 8
	}
	return sum
}

// airPointExposure is the summed damage rate of every anti-air circle that
// covers (x, z), with a soft edge: a point well inside a circle scores more
// than one at its rim.
func airPointExposure(x, z int64, threats []airThreat) int64 {
	var sum int64
	for i := range threats {
		t := &threats[i]
		dx, dz := t.x-x, t.z-z
		d := isqrt64(dx*dx + dz*dz)
		if d >= t.r {
			continue
		}
		sum += t.w * (64 + (t.r-d)*64/t.r)
	}
	return sum
}

// --- geometry helpers, whole world units unless stated ---

// airDir is the Q13 unit travel vector of heading h: (−sin h, −cos h)
// [04 R-MOV-01 §4].
func airDir(h uint16) (int64, int64) {
	return -int64(numeric.Sin(numeric.Angle(h))), -int64(numeric.Cos(numeric.Angle(h)))
}

func airAlong(x, z int64, h uint16, d int64) (int64, int64) {
	dx, dz := airDir(h)
	return x + dx*d/8192, z + dz*d/8192
}

func airBearingWU(ax, az, bx, bz int64) uint16 {
	return bearing(numeric.Fixed(ax<<16), numeric.Fixed(az<<16), numeric.Fixed(bx<<16), numeric.Fixed(bz<<16))
}

func airAngleAbs(a, b uint16) int64 {
	d := int64(int16(a - b))
	return abs64(d)
}

// airTurnRadius is speed / angular rate in whole world units, with speed in
// raw 16.16 per tick and the authored turn rate in angle units per tick.
func airTurnRadius(u *units.Unit, speed int64) int64 {
	if u == nil || u.Def == nil || u.Def.TurnRate <= 0 {
		return 1 << 20
	}
	return (speed * 10430 / int64(u.Def.TurnRate)) >> 16
}

// airArc is the length of an arc of radius r through angle a (65536 units
// per turn).
func airArc(r, a int64) int64 { return r * a * 6283 / (65536 * 1000) }

// airPlanSpeed is the raw 16.16 speed a plan assumes: the current speed,
// but never less than three quarters of the authored maximum.
func airPlanSpeed(u *units.Unit) int64 {
	v := int64(u.Move.Speed)
	if u.Def != nil {
		v = max(v, int64(u.Def.MaxVelocity)*3/4)
	}
	return max(v, 1<<16)
}

// airFullSpeed is u's authored full speed, 16.16 per tick.
func airFullSpeed(u *units.Unit) int64 {
	if u.Def == nil {
		return 0
	}
	return int64(u.Def.MaxVelocity)
}

func (s *System) airOnMap(x, z, margin int64) bool {
	if s.Terrain == nil {
		return true
	}
	w, h := int64(s.Terrain.CellW)*16, int64(s.Terrain.CellH)*16
	return x >= margin && z >= margin && x <= w-margin && z <= h-margin
}

// --- steering ---

// airCarrotDistance is how far ahead a steering goal sits: beyond the
// producer's heading consultation range and beyond the distance the
// integrator would need to brake to a stop from full speed.
func airCarrotDistance(u *units.Unit) int64 {
	d := int64(airTacticsCarrotMin)
	if u.Def != nil && u.Def.Acceleration > 0 {
		v := int64(u.Def.MaxVelocity)
		brake := (v * v / (2 * int64(u.Def.Acceleration))) >> 16
		d = max(d, brake+160)
	}
	return d
}

// airSteer turns the aircraft toward the desired heading at its full rate,
// keeping its goal ahead of the nose. The goal stays fixed in the world while
// the turn catches up with it: the integrator adds the goal's own motion to
// the velocity it seeks [04 §10.1], so a goal swung round with the nose every
// tick would pull the aircraft sideways and bleed its speed. It is moved
// only when the nose has caught up, the turn wants something else, or the
// aircraft has closed on it.
func (s *System) airSteer(u *units.Unit, n *orders.Node, desired uint16) {
	carrot := airCarrotDistance(u)
	if c := s.FlightCommandFor(u.Handle, u); c != nil && c.payloadOwner == n {
		if m, ok := c.Payload.(*airMarker); ok && m.flags == airMarkerTerrainAlt|airMarkerExplicitRadius && m.radius == 0x10 {
			gx, gz := int64(m.goal.X>>16), int64(m.goal.Z>>16)
			px, pz := int64(u.X>>16), int64(u.Z>>16)
			gd := isqrt64((gx-px)*(gx-px) + (gz-pz)*(gz-pz))
			if gd >= carrot-64 {
				gb := airBearingWU(px, pz, gx, gz)
				onLine := airAngleAbs(desired, gb) <= 0x0400
				need := int16(desired - u.Move.Heading)
				lead := int16(gb - u.Move.Heading)
				turning := (need > 0) == (lead > 0) && abs64(int64(lead)) >= 0x0600 && airAngleAbs(desired, gb) < abs64(int64(need))
				if onLine || turning {
					return
				}
			}
		}
	}
	off := int16(desired - u.Move.Heading)
	if off > airTacticsTurnLimit {
		off = airTacticsTurnLimit
	} else if off < -airTacticsTurnLimit {
		off = -airTacticsTurnLimit
	}
	h := u.Move.Heading + uint16(off)
	ox, oz := offsetAtBearing(h, numeric.Fixed(carrot<<16))
	s.airGoalAt(u, n, Vec3{X: u.X - ox, Y: u.Y, Z: u.Z - oz}, 0x10)
}

// airGoalAt installs a point goal, reusing this record's point marker.
func (s *System) airGoalAt(u *units.Unit, n *orders.Node, goal Vec3, radius uint16) {
	if c := s.FlightCommandFor(u.Handle, u); c != nil && c.payloadOwner == n {
		if m, ok := c.Payload.(*airMarker); ok && m.flags == airMarkerTerrainAlt|airMarkerExplicitRadius && m.radius == radius {
			m.goal.X, m.goal.Z = goal.X, goal.Z
			return
		}
	}
	m := s.newPointMarker(u, goal)
	m.setArrivalRadius(radius)
	s.installAirGoal(u, n, m)
}

// airHold re-dispatches the record on the next tick while keeping retail's
// wake bits armed.
func airHold(n *orders.Node, tick uint32, gate uint32) orders.Code {
	n.DynamicGate |= gate
	airDeadline(n, tick, 1)
	return 2
}

// --- target state ---

// airTargetState is the attack target's position and per-tick velocity: the
// live unit when there is one, else the record's goal.
func (s *System) airTargetState(n *orders.Node) (x, y, z, vx, vz numeric.Fixed, t *units.Unit) {
	x, y, z = n.GoalX, n.GoalY, n.GoalZ
	if t = s.unitFor(n.Target); t != nil && t.Alive {
		x, y, z = t.X, t.Y, t.Z
		vx, vz = t.Move.VelX, t.Move.VelZ
		return
	}
	return x, y, z, 0, 0, nil
}

// --- bomb prediction ---

// airDropImpact runs the dropped projectile's own motion [06 §6.4] for a bomb
// released at (x, y, z) by a carrier on heading h at scalar speed: horizontal
// velocity from the heading and speed, wind added to position each tick,
// gravity taken from vertical velocity. Contact uses the projectile ladder's
// terrain and water tests [06 §8.1]: the whole-unit height below the plot
// cell's minimum height byte, or below sea level. floor, when positive, ends
// the fall earlier at a target's body. It reports the contact point and the
// number of ticks it took; a bomb leaving the map has no prediction.
func (s *System) airDropImpact(x, y, z numeric.Fixed, h uint16, speed numeric.Fixed, floor numeric.Fixed) (ix, iz numeric.Fixed, ticks int64, ok bool) {
	if s.Terrain == nil || s.Terrain.Gravity <= 0 {
		return 0, 0, 0, false
	}
	yaw := numeric.Angle(h).Add(airHalfTurn)
	mag := int32(speed.Raw())
	vx := numeric.Fixed(int64(numeric.MulRound(numeric.Sin(yaw), mag)))
	vz := numeric.Fixed(int64(numeric.MulRound(numeric.Cos(yaw), mag)))
	var wx, wz numeric.Fixed
	if s.AirWind != nil {
		wx, wz = numeric.Fixed(int64(s.AirWind.DirX)), numeric.Fixed(int64(s.AirWind.DirZ))
	}
	g := s.Terrain.Gravity
	sea := int16(s.Terrain.SeaLevel)
	var vy numeric.Fixed
	for k := int64(1); k <= 300; k++ {
		x += vx + wx
		y += vy
		z += vz + wz
		vy -= g
		cx, cz := world.WorldToCell(x), world.WorldToCell(z)
		cell := s.Terrain.PlotAt(cx, cz)
		if cell == nil || cx < 0 || cz < 0 || cx >= s.Terrain.CellW || cz >= s.Terrain.CellH {
			return 0, 0, 0, false
		}
		whole := int16(y.Raw() >> 16)
		if whole < int16(cell.MinHeight()) || whole < sea || (floor > 0 && y <= floor) {
			return x, z, k, true
		}
	}
	return 0, 0, 0, false
}

// airBombSolution is a bomber's view of a release one tick from now: where
// the target will be when that bomb lands, measured along and across the
// heading from the predicted impact, and the heading that would put the
// impact line through it.
type airBombSolution struct {
	ok           bool
	along, cross int64 // whole world units; along > 0: the target is still ahead of the impact
	fall         int64 // ticks
	aim          uint16
	dist         int64 // bomber to target, whole world units
	lead         int64 // bomber to predicted impact, whole world units
}

func (s *System) airSolveBomb(u *units.Unit, tx, ty, tz, tvx, tvz numeric.Fixed) airBombSolution {
	var sol airBombSolution
	px, pz := u.X+u.Move.VelX, u.Z+u.Move.VelZ
	sol.dist = isqrt64(((int64(tx)-int64(u.X))>>16)*((int64(tx)-int64(u.X))>>16) + ((int64(tz)-int64(u.Z))>>16)*((int64(tz)-int64(u.Z))>>16))
	ix, iz, k, ok := s.airDropImpact(px, u.Y, pz, u.Move.Heading, u.Move.Speed, ty+numeric.Fixed(8<<16))
	if !ok {
		// Off the map or no gravity: steer straight at the target.
		sol.aim = bearing(u.X, u.Z, tx, tz)
		return sol
	}
	sol.ok, sol.fall = true, k
	lx, lz := (int64(ix)-int64(u.X))>>16, (int64(iz)-int64(u.Z))>>16
	sol.lead = isqrt64(lx*lx + lz*lz)
	ttx := int64(tx) + int64(tvx)*(k+1)
	ttz := int64(tz) + int64(tvz)*(k+1)
	ex, ez := (ttx-int64(ix))>>16, (ttz-int64(iz))>>16
	dx, dz := airDir(u.Move.Heading)
	sol.along = (ex*dx + ez*dz) / 8192
	sol.cross = (ex*dz - ez*dx) / 8192
	var wx, wz int64
	if s.AirWind != nil {
		wx, wz = int64(s.AirWind.DirX)*k, int64(s.AirWind.DirZ)*k
	}
	sol.aim = bearing(u.X, u.Z, numeric.Fixed(ttx-wx), numeric.Fixed(ttz-wz))
	return sol
}

// airCruiseFall is the number of ticks a bomb takes to fall the unit's
// cruise altitude: the smallest k with g·k(k−1)/2 ≥ cruisealt [06 §6.4].
func (s *System) airCruiseFall(u *units.Unit) int64 {
	if s.Terrain == nil || s.Terrain.Gravity <= 0 || u.Def == nil || u.Def.CruiseAlt <= 0 {
		return 60
	}
	h := int64(u.Def.CruiseAlt) << 16
	g := int64(s.Terrain.Gravity)
	k := isqrt64(2*h/g) + 1
	for k > 1 && g*(k-1)*(k-2)/2 >= h {
		k--
	}
	for g*k*(k-1)/2 < h {
		k++
	}
	return k
}

// airTurnAway keeps a reversal from swinging through anti-air: when the
// desired heading is more than about 110 degrees off the nose, the turn goes
// the way whose turning circle is less exposed, by asking for a heading a
// quarter turn off the nose on that side. The side is kept until the nose has
// come round, so the choice cannot flip from tick to tick.
func airTurnAway(p *airPilot, u *units.Unit, desired uint16, threats []airThreat) uint16 {
	off := int16(desired - u.Move.Heading)
	if abs64(int64(off)) < 0x2000 {
		p.turnSide = 0
		return desired
	}
	if p.turnSide == 0 {
		if abs64(int64(off)) < 0x5000 {
			return desired
		}
		px, pz := int64(u.X>>16), int64(u.Z>>16)
		r := airTurnRadius(u, airPlanSpeed(u))
		lx, lz := airAlong(px, pz, u.Move.Heading+airQuarterTurn, r)
		rx, rz := airAlong(px, pz, u.Move.Heading-airQuarterTurn, r)
		le, re := airPointExposure(lx, lz, threats), airPointExposure(rx, rz, threats)
		switch {
		case le < re:
			p.turnSide = 1
		case re < le:
			p.turnSide = -1
		case off > 0:
			p.turnSide = 1
		default:
			p.turnSide = -1
		}
	}
	if p.turnSide > 0 {
		return u.Move.Heading + airQuarterTurn
	}
	return u.Move.Heading - airQuarterTurn
}

// airBombString is the string a pass releases. Retail's overflight holds
// point fire until the bomber has flown the authored run length past the
// release point [04 R-AIR-01 §8], and the weapon drops a bomb every reload
// while it is held. It returns the string's duration in ticks and how far
// past the first bomb's impact its middle lands, in world units, for bombs
// that fall for fall ticks. The bomber's speed is flown forward on the
// integrator's approach to full speed [04 §10.1], so a string released
// while still accelerating out of a turn, whose later bombs carry further,
// is still centred.
func airBombString(u *units.Unit, v0, fall int64) (ticks uint32, half int64) {
	run := int64(0)
	if u.Def != nil {
		run = int64(u.Def.AttackRunLength) << 16
	}
	vmax, accel := int64(1)<<16, int64(0)
	if u.Def != nil {
		vmax, accel = max(int64(u.Def.MaxVelocity), 1<<16), max(int64(u.Def.Acceleration), 0)
	}
	reload := airReload(u)
	v0 = max(v0, 1<<16)
	step := func(v int64) int64 { return min(v+accel-v*accel/vmax, vmax) }
	// The marker's arrival is seen the tick after it happens, and acted on
	// the tick after that.
	var p int64
	v, t := v0, int64(0)
	for ; p < run && t < 300; t++ {
		p += v
		v = step(v)
	}
	ticks = uint32(t + 2)
	bombs := (int64(ticks)-1)/reload + 1
	m1, m2 := (bombs-1)/2*reload, bombs/2*reload
	var mid int64
	p, v = 0, v0
	for k := int64(0); k <= m2; k++ {
		if k == m1 || k == m2 {
			mid += p + v*fall
		}
		p += v
		v = step(v)
	}
	if m1 == m2 {
		mid *= 2
	}
	return ticks, (mid/2 - v0*fall) >> 16
}

// airReload is the first weapon's reload in ticks, at least one.
func airReload(u *units.Unit) int64 {
	if sl := u.SlotAt(0); sl != nil && sl.Weapon != nil {
		return max(int64(sl.Weapon.ReloadTime), 1)
	}
	return 1
}

// airTargetHalf is half the target's footprint width, the cross-track
// tolerance a release accepts.
func airTargetHalf(t *units.Unit) int64 {
	if t == nil || t.Def == nil {
		return 24
	}
	half := int64(min(t.Def.FootprintX, t.Def.FootprintZ)) * 8
	return min(max(half, 20), 48)
}

// --- going round anti-air ---

// airAvoid bends a desired heading round the anti-air circles ahead that
// contain neither the aircraft nor its destination (dx, dz), dist away: it
// takes the tangent past the nearest blocking circle, on the side nearer the
// desired heading, plus a small margin, and repeats for the next circle.
// Circles around the destination must be entered and are not avoided; one
// the aircraft is already inside is left by the ordinary steering.
func airAvoid(p *airPilot, px, pz int64, desired uint16, dist, dx, dz int64, threats []airThreat) uint16 {
	h := desired
	for iter := 0; iter < 3; iter++ {
		hit, nearest := -1, int64(1)<<62
		vx, vz := airDir(h)
		for i := range threats {
			t := &threats[i]
			cx, cz := t.x-px, t.z-pz
			if cx*cx+cz*cz <= t.r*t.r {
				continue
			}
			ex, ez := t.x-dx, t.z-dz
			if ex*ex+ez*ez <= t.r*t.r {
				continue
			}
			proj := (cx*vx + cz*vz) / 8192
			if proj <= 0 || proj > dist+t.r {
				continue
			}
			if abs64((cx*vz-cz*vx)/8192) >= t.r {
				continue
			}
			if proj < nearest {
				hit, nearest = i, proj
			}
		}
		if hit < 0 {
			if iter == 0 && p != nil {
				p.avoidSide = 0
			}
			return h
		}
		t := &threats[hit]
		cx, cz := t.x-px, t.z-pz
		l := isqrt64(cx*cx + cz*cz - t.r*t.r)
		to := airBearingWU(px, pz, t.x, t.z)
		delta := bearing(0, 0, numeric.Fixed(-t.r<<16), numeric.Fixed(-max(l, 1)<<16)) + airTangentMargin
		a, b := to+delta, to-delta
		// Keep going round on the side already chosen, so a circle dead
		// ahead cannot flip the choice from tick to tick.
		side := int8(1)
		if airAngleAbs(b, desired) < airAngleAbs(a, desired) {
			side = -1
		}
		if p != nil {
			if p.avoidSide != 0 {
				side = p.avoidSide
			} else if iter == 0 {
				p.avoidSide = side
			}
		}
		if side > 0 {
			h = a
		} else {
			h = b
		}
	}
	return h
}

// airPointRate is the summed damage rate of the anti-air circles covering a
// point: what a turn flown there costs per world unit of arc.
func airPointRate(x, z int64, threats []airThreat) int64 {
	var sum int64
	for i := range threats {
		t := &threats[i]
		dx, dz := t.x-x, t.z-z
		if dx*dx+dz*dz <= t.r*t.r {
			sum += t.w
		}
	}
	return sum
}

// airTransit prices the straight leg a→b for planning: its exposure inside
// circles that contain a or b, and for every other circle it crosses the
// extra length of going round it instead.
func airTransit(ax, az, bx, bz int64, threats []airThreat) (extra, expo int64) {
	dx, dz := bx-ax, bz-az
	l := isqrt64(dx*dx + dz*dz)
	if l == 0 {
		return 0, 0
	}
	for i := range threats {
		t := &threats[i]
		cx, cz := t.x-ax, t.z-az
		r2 := t.r * t.r
		proj := (cx*dx + cz*dz) / l
		d2 := max(cx*cx+cz*cz-proj*proj, 0)
		if d2 >= r2 {
			continue
		}
		h := isqrt64(r2 - d2)
		s0, s1 := max(proj-h, 0), min(proj+h, l)
		if s1 <= s0 {
			continue
		}
		inA := cx*cx+cz*cz <= r2
		ex, ez := t.x-bx, t.z-bz
		inB := ex*ex+ez*ez <= r2
		if inA || inB {
			expo += (s1 - s0) * t.w
		} else {
			// Round the circle: about half its circumference for a central
			// crossing, less for a glancing one.
			extra += (s1 - s0) * 3 / 5
		}
	}
	return extra, expo
}

// --- run planning ---

// airPlanRun chooses where a pilot turns in for its next run: a turn point
// out at dx from the target on one of airRunCandidates bearings, from which
// the aircraft turns and runs at the target. The choice minimizes estimated
// flight time — the leg to the turn point, both turns at the current turn
// radius and the run back in — plus the priced expected damage of the leg
// out, the run in and exit past the target.
func (s *System) airPlanRun(u *units.Unit, p *airPilot, tx, tz int64, dx, exit int64, threats []airThreat, tick uint32) {
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	v := airPlanSpeed(u)
	vw := max(v>>16, 1)
	r := airTurnRadius(u, v)
	maxHP := int64(1)
	if u.Def != nil && u.Def.MaxDamage > 0 {
		maxHP = int64(u.Def.MaxDamage)
	}
	h := u.Move.Heading
	wings := s.airWingRuns(u, tx, tz)
	best, bestCost := -1, int64(0)
	var bestX, bestZ int64
	var bestRun uint16
	for c := 0; c < 2*airRunCandidates; c++ {
		j := c % airRunCandidates
		out := uint16(j * (65536 / airRunCandidates)) // bearing from the target to the turn point
		d := dx
		if c >= airRunCandidates {
			// The second ring turns just outside the anti-air cover on this
			// bearing, so a target deep under cover is attacked by longer
			// runs whose turns are flown in the clear.
			d = s.airPilotCoverExit(p, tx, tz, j, out, threats) + r
			if d <= dx+64 {
				continue
			}
		}
		xx, xz := airAlong(tx, tz, out, d)
		if !s.airOnMap(xx, xz, 96) {
			continue
		}
		if c < airRunCandidates && airPointRate(xx, xz, threats) > 0 {
			continue // the second ring turns on this bearing in the clear
		}
		run := out + airHalfTurn // the run heads back at the target
		b1 := airBearingWU(px, pz, xx, xz)
		d1 := isqrt64((xx-px)*(xx-px) + (xz-pz)*(xz-pz))
		turn1 := airAngleAbs(b1, h)
		turnX := airAngleAbs(run, b1)
		if d1 < r {
			turn1, turnX = 0, airAngleAbs(run, h)
		}
		extra, expo := airTransit(px, pz, xx, xz, threats)
		a1, aX := airArc(r, turn1), airArc(r, turnX)
		expo += airTurnInCover * (a1*airPointRate(px, pz, threats) + aX*airPointRate(xx, xz, threats))
		path := d1 + extra + a1 + aX + d
		cost := path / vw
		ex, ez := airAlong(tx, tz, run, exit)
		expo += airSegmentExposure(xx, xz, ex, ez, airFullSpeed(u), threats)
		// Damage taken: inside length / speed ticks at w/30 per tick.
		dmg := expo / (vw * 30)
		cost += dmg * airLossTicks / maxHP
		// Wingmen on the same target take different runs, so one blast or
		// one turret's attention does not catch the whole flight.
		for k := range wings.n {
			if airAngleAbs(wings.runs[k], run) < 0x0C00 {
				cost += airWingSpread
			}
		}
		if best < 0 || cost < bestCost {
			best, bestCost, bestX, bestZ, bestRun = j, cost, xx, xz, run
		}
	}
	if best < 0 {
		bestRun = airBearingWU(px, pz, tx, tz)
		bestX, bestZ = px, pz
	}
	if p.planned {
		// Keep the current plan unless the new one is clearly cheaper, so a
		// pilot does not turn back and forth between near-equal runs.
		if old := s.airPlannedCost(u, p, tx, tz, dx, exit, threats); best < 0 || old*20 <= bestCost*23 {
			p.planTick = tick
			return
		}
	}
	p.run, p.entryX, p.entryZ = bestRun, bestX, bestZ
	p.planned, p.planTick = true, tick
}

// airCoverExit is how far from (tx, tz) along bearing b a point leaves every
// anti-air circle, in 32 world-unit steps up to limit.
func airCoverExit(tx, tz int64, b uint16, threats []airThreat, limit int64) int64 {
	for d := int64(0); d <= limit; d += 32 {
		x, z := airAlong(tx, tz, b, d)
		if airPointRate(x, z, threats) == 0 {
			return d
		}
	}
	return limit
}

// airWings is the run headings other pilots of the same owner hold against
// the same target point.
type airWings struct {
	n    int
	runs [8]uint16
}

func (s *System) airWingRuns(u *units.Unit, tx, tz int64) airWings {
	var w airWings
	if s.world == nil || s.airPilots == nil {
		return w
	}
	s.airScratch = s.world.AppendLiveSliced(s.airScratch[:0])
	for _, f := range s.airScratch {
		if w.n == len(w.runs) {
			break
		}
		if f == nil || f == u || !f.Alive || f.Owner != u.Owner || f.Def == nil || !f.Def.CanFly {
			continue
		}
		q := s.airPilots[f.Handle]
		if q == nil || q.unit != f || !q.planned || q.node == nil {
			continue
		}
		if abs64(int64(q.node.GoalX>>16)-tx) > 64 || abs64(int64(q.node.GoalZ>>16)-tz) > 64 {
			continue
		}
		w.runs[w.n] = q.run
		w.n++
	}
	return w
}

// airPilotCoverExit is airCoverExit on run bearing j, cached on the pilot
// for its current target point and anti-air picture.
func (s *System) airPilotCoverExit(p *airPilot, tx, tz int64, j int, out uint16, threats []airThreat) int64 {
	ver := uint32(0)
	if p.unit != nil && int(p.unit.Owner) < len(s.airThreats) {
		ver = s.airThreats[p.unit.Owner].version
	}
	if !p.coverSet || p.coverX != tx || p.coverZ != tz || p.coverVer != ver {
		for k := range p.coverExit {
			p.coverExit[k] = -1
		}
		p.coverSet, p.coverX, p.coverZ, p.coverVer = true, tx, tz, ver
	}
	if p.coverExit[j] < 0 {
		p.coverExit[j] = int32(airCoverExitIndexed(tx, tz, out, threats, 2400, s.airIndexFor(p.unit, threats)))
	}
	return int64(p.coverExit[j])
}

// airDirectCost is the planner's cost of simply running in from here along
// the current bearing to the target, for comparison with a planned run.
func airDirectCost(u *units.Unit, tx, tz int64, exit int64, threats []airThreat) int64 {
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	v := airPlanSpeed(u)
	vw := max(v>>16, 1)
	r := airTurnRadius(u, v)
	maxHP := int64(1)
	if u.Def != nil && u.Def.MaxDamage > 0 {
		maxHP = int64(u.Def.MaxDamage)
	}
	run := airBearingWU(px, pz, tx, tz)
	d := isqrt64((tx-px)*(tx-px) + (tz-pz)*(tz-pz))
	a := airArc(r, airAngleAbs(run, u.Move.Heading))
	cost := (d + a) / vw
	ex, ez := airAlong(tx, tz, run, exit)
	dmg := (airSegmentExposure(px, pz, ex, ez, airFullSpeed(u), threats) + airTurnInCover*a*airPointRate(px, pz, threats)) / (vw * 30)
	return cost + dmg*airLossTicks/maxHP
}

// airReposition flies a pilot toward its planned turn point and reports
// whether it has arrived, or is already lined up for a long enough run, and
// so should approach.
func (s *System) airReposition(u *units.Unit, n *orders.Node, p *airPilot, aim uint16, dist, room int64, threats []airThreat) bool {
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	d := isqrt64((p.entryX-px)*(p.entryX-px) + (p.entryZ-pz)*(p.entryZ-pz))
	r := airTurnRadius(u, airPlanSpeed(u))
	// Already lined up for a long enough run counts as arrived, but only
	// where it is no more exposed than the planned turn point: a run must not
	// start from inside cover the plan went round.
	lined := airAngleAbs(aim, u.Move.Heading) < 0x0E00 && airAngleAbs(aim, p.run) < 0x1000 && dist >= room &&
		airPointRate(px, pz, threats) <= airPointRate(p.entryX, p.entryZ, threats)
	if d <= max(r, 96) || lined {
		return true
	}
	s.airSteer(u, n, airTurnAway(p, u, airAvoid(p, px, pz, airBearingWU(px, pz, p.entryX, p.entryZ), d, p.entryX, p.entryZ, threats), threats))
	return false
}

// --- AirStrike ---

// legAirStrikeModern is the Modern bombing run. Phase 0 is retail's takeoff
// preamble, but the pilot starts steering on the next tick rather than after
// the vertical climb. Phase 1 approaches or repositions; phase 2 holds the
// line while the string falls; phase 6 is the end of a pass, where the
// ordinary entry has already applied the maneuver leash, and the low-health
// repair seek runs as in retail [04 R-AIR-01 §8][04 R-AIR-01 §11].
func (s *System) legAirStrikeModern(u *units.Unit, n *orders.Node, tick uint32) orders.Code {
	switch n.Phase {
	case 0:
		if !s.airMoverReady(u) {
			return 7 // *cancel-all* [04 R-ORD-02 §3]
		}
		s.takeoffPreamble(u, n)
		airDeadline(n, tick, 1)
		return 1
	case 1, 2, 6:
	default:
		return 7
	}
	p := s.airPilotFor(u, n, tick)
	if p.fresh {
		p.fresh = false
		inhibitAirWeapons(u)
		releaseWeapon(u, 0)
	}
	tx, ty, tz, tvx, tvz, tgt := s.airTargetState(n)
	s.airClaim(u, tgt, tick)
	if n.Phase == 2 {
		// The overflight marker is reached: the string is complete.
		stopPrimaryWeapon(u)
		n.Phase = 6
	}
	if n.Phase == 6 {
		stopPrimaryWeapon(u)
		if airBelowThreeQuarters(u) {
			if s.airFindBaseAndLand(u, n, s.simRNG(u), tick) || s.airSeekFieldRepair(u, n, tick) {
				return 0 // *restart* behind the spawned landing [04 R-AIR-01 §11]
			}
		}
		n.Phase = 1
		p.mode, p.planned = pilotReposition, false
	}

	pic := s.airThreatsFor(u, tick)
	if s.airRetarget(u, n, p, pic, tick) {
		return 5 // *complete*: the issuing order chooses again
	}
	sol := s.airSolveBomb(u, tx, ty, tz, tvx, tvz)
	// Plan for the fall from cruise altitude even while still climbing.
	fall := s.airCruiseFall(u)
	if sol.ok {
		fall = max(fall, sol.fall)
	}
	// Release reads the string at the current speed; the run is planned for
	// the string at planning speed, not a slow takeoff's.
	_, half := airBombString(u, int64(u.Move.Speed), fall)
	_, halfPlan := airBombString(u, airPlanSpeed(u), s.airCruiseFall(u))
	vw := max(int64(u.Move.Speed>>16), 1)
	late := max(2*vw, 24)
	twx, twz := int64(tx>>16), int64(tz>>16)
	// The run a pass needs: the fall at planning speed, half the string and
	// a settled stretch before release. The turn point sits one turn radius
	// further out, so the turn in still leaves that run.
	r := airTurnRadius(u, airPlanSpeed(u))
	leadPlan := (airPlanSpeed(u) * fall) >> 16
	room := leadPlan + halfPlan + airRunAlign
	dx := room + r
	// The string ends half its length short of the release point, lead
	// before the target; the bomber then turns away, carrying on at most a
	// turn radius. The exposed run ends there, short of the target when the
	// lead is long.
	exit := r + halfPlan - leadPlan

	if p.mode == pilotApproach {
		// A bomber that lines up too close to centre its string releases
		// anyway while the target still lies inside the string, at least
		// half a bomb spacing past the first impact, rather than going round.
		spacing := max(airReload(u)*vw, 8)
		centred := sol.along-half <= vw && sol.along-half >= -late
		bracket := sol.along-half < -late && sol.along >= spacing/2
		if sol.ok && abs64(sol.cross) <= airTargetHalf(tgt) && (centred || bracket) {
			// Release: the middle of the string lands on the target. The
			// string is retail's whole string: point fire held until the
			// bomber reaches an overflight marker the authored run length
			// plus 960 world units ahead, with a 960-unit arrival radius
			// [04 R-AIR-01 §8] phase 5 — laid along the heading, so the line
			// the release was aimed on is held.
			releaseWeapon(u, 0)
			firePrimaryPoint(u, tx, tz, tick)
			run := int64(0)
			if u.Def != nil {
				run = int64(u.Def.AttackRunLength)
			}
			ox, oz := offsetAtBearing(u.Move.Heading, numeric.Fixed((run+airOverflyBonus)<<16))
			m := s.newPointMarker(u, Vec3{X: u.X - ox, Y: u.Y, Z: u.Z - oz})
			m.setArrivalRadius(airOverflyBonus)
			s.installAirGoal(u, n, m)
			n.DynamicGate = airLegGateReposition
			n.Phase = 2
			return 2
		}
		aimErr := airAngleAbs(sol.aim, u.Move.Heading)
		// The window is missed by a lined-up bomber whose middle bomb would
		// now land past the target, or by one still far off its aim with no
		// room left to turn and run.
		missed := sol.ok && aimErr < 0x1000 && sol.along-half < -late && sol.along < spacing/2
		cramped := aimErr >= 0x2000 && sol.dist < room/2
		if missed || cramped {
			p.mode, p.planned = pilotReposition, false
		} else if sol.dist > dx+r && airReplan(p, pic, tick) {
			// While the approach is still long, compare it with a planned
			// run: anti-air may make another heading much cheaper.
			s.airPlanRun(u, p, twx, twz, dx, exit, pic.list, tick)
			p.picture = pic.version
			if airAngleAbs(p.run, sol.aim) > 0x1000 && airDirectCost(u, twx, twz, exit, pic.list) > s.airPlannedCost(u, p, twx, twz, dx, exit, pic.list)*11/10 {
				p.mode = pilotReposition
			}
		}
	}
	if p.mode == pilotReposition {
		if airReplan(p, pic, tick) {
			s.airPlanRun(u, p, twx, twz, dx, exit, pic.list, tick)
			p.picture = pic.version
		}
		if !s.airReposition(u, n, p, sol.aim, sol.dist, room, pic.list) {
			return airHold(n, tick, airLegGateStrike)
		}
		p.mode = pilotApproach
	}
	if sol.dist > room+r {
		px, pz := int64(u.X>>16), int64(u.Z>>16)
		s.airSteer(u, n, airAvoid(p, px, pz, sol.aim, sol.dist, twx, twz, pic.list))
	} else {
		s.airSteer(u, n, sol.aim)
	}
	return airHold(n, tick, airLegGateStrike)
}

// airPlannedCost is airPlanRun's cost of the chosen plan.
func (s *System) airPlannedCost(u *units.Unit, p *airPilot, tx, tz, dx, exit int64, threats []airThreat) int64 {
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	v := airPlanSpeed(u)
	vw := max(v>>16, 1)
	r := airTurnRadius(u, v)
	maxHP := int64(1)
	if u.Def != nil && u.Def.MaxDamage > 0 {
		maxHP = int64(u.Def.MaxDamage)
	}
	b1 := airBearingWU(px, pz, p.entryX, p.entryZ)
	d1 := isqrt64((p.entryX-px)*(p.entryX-px) + (p.entryZ-pz)*(p.entryZ-pz))
	extra, expo := airTransit(px, pz, p.entryX, p.entryZ, threats)
	a1, aX := airArc(r, airAngleAbs(b1, u.Move.Heading)), airArc(r, airAngleAbs(p.run, b1))
	expo += airTurnInCover * (a1*airPointRate(px, pz, threats) + aX*airPointRate(p.entryX, p.entryZ, threats))
	run := isqrt64((p.entryX-tx)*(p.entryX-tx) + (p.entryZ-tz)*(p.entryZ-tz))
	path := d1 + extra + a1 + aX + max(run, dx)
	ex, ez := airAlong(tx, tz, p.run, exit)
	expo += airSegmentExposure(p.entryX, p.entryZ, ex, ez, airFullSpeed(u), threats)
	return path/vw + expo/(vw*30)*airLossTicks/maxHP
}

// --- AirToGround ---

// legAirToGroundModern is the Modern strafing run. The weapon is bound to the
// target for the whole pass, so ordinary aiming fires whenever range and
// facing allow; the pilot flies at the target, breaks when it is overhead or
// behind, and comes round by the cheapest run instead of retail's three-range
// fly-through and fixed break [04 R-AIR-01 §8].
func (s *System) legAirToGroundModern(u *units.Unit, n *orders.Node, tick uint32) orders.Code {
	switch n.Phase {
	case 0:
		if !s.airMoverReady(u) {
			return 7
		}
		s.takeoffPreamble(u, n)
		airDeadline(n, tick, 1)
		return 1
	case 1, 2:
	default:
		return 7
	}
	p := s.airPilotFor(u, n, tick)
	tx, _, tz, _, _, tgt := s.airTargetState(n)
	s.airClaim(u, tgt, tick)
	pic := s.airThreatsFor(u, tick)
	if s.airRetarget(u, n, p, pic, tick) {
		return 5 // *complete*: the issuing order chooses again
	}
	if p.fresh || n.Phase == 1 {
		// Retail's phase 1 hands every slot to its own scan and phase 2 then
		// binds slot 0 to the attack [04 R-AIR-01 §8]: a second weapon keeps
		// choosing its own targets, such as anti-air passed on the run.
		p.fresh = false
		n.Phase = 2
		inhibitAirWeapons(u)
		releaseWeapon(u, 0)
		if n.Target != 0 {
			firePrimaryTarget(u, n.Target, tick)
		} else {
			firePrimaryPoint(u, n.GoalX, n.GoalZ, tick)
		}
	}
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	twx, twz := int64(tx>>16), int64(tz>>16)
	dist := isqrt64((twx-px)*(twx-px) + (twz-pz)*(twz-pz))
	aim := airBearingWU(px, pz, twx, twz)
	rng := int64(firstWeaponRange(u))
	r := airTurnRadius(u, airPlanSpeed(u))
	room := rng * 3 / 4
	dx := room + r
	exit := r - max(rng/5, 48)
	if p.mode == pilotApproach {
		err := airAngleAbs(aim, u.Move.Heading)
		if dist < airStrafeBreak(twx, twz, px, pz, rng, r, pic.list) || (err > 0x4000 && dist < room) {
			if airBelowThreeQuarters(u) {
				if s.airFindBaseAndLand(u, n, s.simRNG(u), tick) || s.airSeekFieldRepair(u, n, tick) {
					return 0
				}
			}
			p.mode, p.planned = pilotReposition, false
		} else if dist > dx+r && airReplan(p, pic, tick) {
			s.airPlanRun(u, p, twx, twz, dx, exit, pic.list, tick)
			p.picture = pic.version
			if airAngleAbs(p.run, aim) > 0x1000 && airDirectCost(u, twx, twz, exit, pic.list) > s.airPlannedCost(u, p, twx, twz, dx, exit, pic.list)*11/10 {
				p.mode = pilotReposition
			}
		}
	}
	if p.mode == pilotReposition {
		if airAngleAbs(aim, u.Move.Heading) > airQuarterTurn && s.airFlyOut(u, px, pz, rng*3, pic.list) {
			// A pass that ends under anti-air flies on out of it at speed, as
			// retail's fly-through does, before it turns: a turn flown under
			// cover bleeds the speed that keeps missiles from catching it. The
			// next run is planned from where the turn will be flown.
			p.planned = false
			s.airSteer(u, n, u.Move.Heading)
			return airHold(n, tick, airLegGateStrike)
		}
		if airReplan(p, pic, tick) {
			s.airPlanRun(u, p, twx, twz, dx, exit, pic.list, tick)
			p.picture = pic.version
		}
		if !s.airReposition(u, n, p, aim, dist, room, pic.list) {
			return airHold(n, tick, airLegGateStrike)
		}
		p.mode = pilotApproach
	}
	switch {
	case dist > room:
		aim = airAvoid(p, px, pz, aim, dist, twx, twz, pic.list)
	case dist <= rng*9/10 && airAngleAbs(aim, u.Move.Heading) < 0x2000:
		// Inside firing range the pass is committed: hold the line at speed,
		// as retail's run does once it reaches weapon range [04 R-AIR-01 §8],
		// rather than chasing the target round as it passes underneath.
		aim = u.Move.Heading
	}
	s.airSteer(u, n, aim)
	return airHold(n, tick, airLegGateStrike)
}

// airFlyOut reports whether an aircraft at (px, pz) is under anti-air that
// its current heading leaves within limit world units, at a point on the map.
func (s *System) airFlyOut(u *units.Unit, px, pz, limit int64, threats []airThreat) bool {
	if airPointRate(px, pz, threats) == 0 {
		return false
	}
	d := airCoverExitIndexed(px, pz, u.Move.Heading, threats, limit+32, s.airIndexFor(u, threats))
	if d > limit {
		return false
	}
	x, z := airAlong(px, pz, u.Move.Heading, d)
	return s.airOnMap(x, z, 96)
}

// airStrafeBreak is how close to its target a strafing run comes before it
// breaks away: a fifth of the weapon's range, or, when the target sits under
// anti-air, a turn radius before the outer edge of all that cover along the
// run if the weapon still reaches the target from there. A target the weapon
// cannot reach from outside every circle over it is overflown at speed
// instead: breaking short of one circle would only fly the turn, slowly,
// inside another.
func airStrafeBreak(tx, tz, px, pz, rng, turn int64, threats []airThreat) int64 {
	brk := max(rng/5, 48)
	dx, dz := px-tx, pz-tz
	l := isqrt64(dx*dx + dz*dz)
	if l == 0 {
		return brk
	}
	edge := int64(0)
	for i := range threats {
		t := &threats[i]
		cx, cz := t.x-tx, t.z-tz
		r2 := t.r * t.r
		if cx*cx+cz*cz > r2 {
			continue
		}
		proj := (cx*dx + cz*dz) / l
		perp2 := max(cx*cx+cz*cz-proj*proj, 0)
		edge = max(edge, proj+isqrt64(max(r2-perp2, 0))+turn)
	}
	if edge <= rng*17/20 {
		brk = max(brk, edge)
	}
	return brk
}

// --- AirToGroundHover ---

// legAirToGroundHoverModern is the Modern `hoverattack` standoff. The gunship
// keeps retail's frozen marker about its target, so it faces the target while
// it slides [04 R-AIR-01 §8], but chooses its standoff point: inside weapon
// range, by the least visible anti-air exposure and then the shortest move.
// It changes points on arrival, without waiting for incoming damage.
func (s *System) legAirToGroundHoverModern(u *units.Unit, n *orders.Node, tick uint32) orders.Code {
	if s.installOffMapRecoveryMarker(u, n) {
		return 2
	}
	switch n.Phase {
	case 0:
		if !s.airMoverReady(u) {
			return 7
		}
		s.takeoffPreamble(u, n)
		airDeadline(n, tick, 1)
		return 1
	case 1, 2:
	default:
		return 7
	}
	p := s.airPilotFor(u, n, tick)
	// The Modern hover leg runs every tick. A gap can include a live switch
	// through the retail leg, which replaces its marker without this pilot.
	if p.standSet && tick-p.standVisit > 1 {
		p.standSet, p.standHeld, p.slideTarget = false, false, nil
	}
	p.standVisit = tick
	if p.fresh || n.Phase == 1 {
		// Retail's phase 1 hands every slot to its own scan and phase 2 then
		// binds slot 0 to the attack [04 R-AIR-01 §8]: a second weapon keeps
		// choosing its own targets.
		p.fresh = false
		n.Phase = 2
		inhibitAirWeapons(u)
		releaseWeapon(u, 0)
		if n.Target != 0 {
			firePrimaryTarget(u, n.Target, tick)
		} else {
			firePrimaryPoint(u, n.GoalX, n.GoalZ, tick)
		}
	}
	tx, ty, tz, _, _, tgt := s.airTargetState(n)
	s.airClaim(u, tgt, tick)
	twx, twz := int64(tx>>16), int64(tz>>16)
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	rng := max(int64(firstWeaponRange(u)), 64)
	pic := s.airThreatsFor(u, tick)
	if s.airRetarget(u, n, p, pic, tick) {
		return 5 // *complete*: the issuing order chooses again
	}
	under := p.underFire(tick)
	samePlan := p.standAim == (Vec3{X: tx, Y: ty, Z: tz}) && p.standPicture == pic.version && p.standUnder == under
	weapons := orderWeapons(u)
	blocked := tgt != nil && weapons != nil && weapons.FiringPositionBlockedHook() != nil && weapons.FiringPositionBlockedHook()(u, tgt, tick)

	// Engagement refusals count only at the firing point: on the way there
	// the target is simply out of range.
	atStand := p.standSet && isqrt64((p.standX-px)*(p.standX-px)+(p.standZ-pz)*(p.standZ-pz)) <= 48
	if atStand && tick-p.standTick >= 30 && tick%15 == 0 {
		if n.Target != 0 && !weaponCanEngage(u, n.Target, 0) {
			p.refused++
		} else {
			p.refused = 0
		}
	}
	replan := !p.standSet
	if p.standSet {
		dt := isqrt64((p.standX-twx)*(p.standX-twx) + (p.standZ-twz)*(p.standZ-twz))
		ds := isqrt64((p.standX-px)*(p.standX-px) + (p.standZ-pz)*(p.standZ-pz))
		arrived := ds <= 24
		// A moving gunship may never reach the old refusal sampling window.
		// React in this weapon visit, but finish an already-clear escape leg.
		// Only an all-blocked search waits for the existing 15-tick refusal
		// cadence. New target geometry or threats invalidate that result.
		retry := p.noClearTarget != tgt || !samePlan || tick-p.standTick >= 15
		if blocked && retry && !s.airHoverPointClear(u, tgt, p.standX, p.standZ, tick) {
			replan = true
		}
		switch {
		case dt > rng*9/10:
			replan = true // the target moved away from the point
		case p.refused > 1:
			replan = true
		case arrived:
			replan = true // turn before the frozen marker brakes to a hold
		case tick-p.standTick >= 240:
			replan = true
		}
	}
	if replan {
		if p.slideTarget != nil && p.slideTarget == tgt && samePlan && !blocked && p.refused <= 1 {
			p.standX, p.slideX = p.slideX, p.standX
			p.standZ, p.slideZ = p.slideZ, p.standZ
			p.standTick, p.refused = tick, 0
		} else {
			s.airChooseStandoff(u, p, twx, twz, rng, pic.list, under, tick)
			p.standAim, p.standPicture, p.standUnder = Vec3{X: tx, Y: ty, Z: tz}, pic.version, under
		}
		p.standHeld = false
		if airBelowThreeQuarters(u) && (under || airPointExposure(p.standX, p.standZ, pic.list) > 0) {
			if s.airFindBaseAndLand(u, n, s.simRNG(u), tick) || s.airSeekFieldRepair(u, n, tick) {
				return 0
			}
		}
	}
	// Far from the standoff point the gunship flies there round any anti-air
	// that does not cover the point itself; close in it takes retail's frozen
	// marker about the target, so it faces the target as it settles.
	ds := isqrt64((p.standX-px)*(p.standX-px) + (p.standZ-pz)*(p.standZ-pz))
	if ds > 240 {
		s.airSteer(u, n, airAvoid(p, px, pz, airBearingWU(px, pz, p.standX, p.standZ), ds, p.standX, p.standZ, pic.list))
		p.standHeld = false
	} else if !p.standHeld {
		m := s.newFrozenTerrainPointMarker(u, n.Target, Vec3{X: numeric.Fixed(p.standX << 16), Y: ty, Z: numeric.Fixed(p.standZ << 16)})
		if u.Def != nil {
			m.setAltitudeOffset(int16(u.Def.CruiseAlt))
		}
		m.setArrivalRadius(0x10)
		s.installAirGoal(u, n, m)
		p.standHeld = true
	}
	return airHold(n, tick, airLegGateStrike)
}

// airHoverPointClear previews the observed physical refusal at a firing point.
func (s *System) airHoverPointClear(u, target *units.Unit, x, z int64, tick uint32) bool {
	weapons := orderWeapons(u)
	if weapons == nil || weapons.FiringPositionClearHook() == nil || target == nil {
		return false
	}
	fx, fz := numeric.FixedFromInt(x), numeric.FixedFromInt(z)
	y := u.Y
	if u.Def != nil {
		y = CruiseAltitudeForOffset(s.Terrain, fx, fz, int32(int16(u.Def.CruiseAlt)))
	}
	return weapons.FiringPositionClearHook()(u, target, tick, fx, y, fz)
}

// airChooseStandoff ranks standoff points on two rings about the target and
// sixteen bearings. Repositioning prefers a lateral move, longer under fire,
// before ranking exposure, wingmen and distance; otherwise a low-exposure
// current point can win forever. A short move remains the fallback when no
// full move fits. Nanolathe Modern policy: DESIGN_MOVEMENT_PATH
// "Modern aircraft tactics".
func (s *System) airChooseStandoff(u *units.Unit, p *airPilot, tx, tz, rng int64, threats []airThreat, under bool, tick uint32) {
	px, pz := int64(u.X>>16), int64(u.Z>>16)
	// Wingmen: other aircraft of this owner whose pilots hold a standoff
	// point near this target. Read once per choice.
	var wing [8][2]int64
	nw := 0
	if s.world != nil && s.airPilots != nil {
		s.airScratch = s.world.AppendLiveSliced(s.airScratch[:0])
		for _, f := range s.airScratch {
			if nw == len(wing) {
				break
			}
			if f == nil || f == u || !f.Alive || f.Owner != u.Owner || f.Def == nil || !f.Def.CanFly {
				continue
			}
			q := s.airPilots[f.Handle]
			if q == nil || q.unit != f || !q.standSet {
				continue
			}
			if abs64(q.standX-tx) > 2*rng || abs64(q.standZ-tz) > 2*rng {
				continue
			}
			wing[nw] = [2]int64{q.standX, q.standZ}
			nw++
		}
	}
	minMove := int64(64)
	if under {
		minMove = 120
	}
	// A ring change on the same bearing is forward/backward motion, not a
	// gunship's slide. Measure the component across the target bearing so
	// those cheap radial moves cannot displace every lateral candidate.
	rx, rz := px-tx, pz-tz
	radialDistance := isqrt64(rx*rx + rz*rz)
	best := -1
	bestMoves := false
	bestClear := false
	// Reuse the actual weapon visit's physical refusal. Range admission alone
	// cannot tell that every selected point fires into the same ridge/feature.
	// These previews spend no RNG, query no script and never authorize a shot.
	weapons := orderWeapons(u)
	var target *units.Unit
	if p.node != nil {
		target = s.unitFor(p.node.Target)
	}
	preview := target != nil && weapons != nil && weapons.FiringPositionBlockedHook() != nil && weapons.FiringPositionClearHook() != nil && weapons.FiringPositionBlockedHook()(u, target, tick)
	// Keep the first pass's evidence for the bounded companion-point search.
	var candidates [32]struct {
		x, z  int64
		clear bool
	}
	pointCost := func(x, z, move int64) int64 {
		cost := airPointExposure(x, z, threats)*64 + move
		if move < minMove {
			cost += 4000
		}
		for k := 0; k < nw; k++ {
			wx, wz := wing[k][0], wing[k][1]
			if (wx-x)*(wx-x)+(wz-z)*(wz-z) < 120*120 || airAngleAbs(airBearingWU(tx, tz, wx, wz), airBearingWU(tx, tz, x, z)) < 0x0800 {
				cost += 3000
			}
		}
		return cost
	}
	var bestCost int64
	var bx, bz int64
	for ring := 0; ring < 2; ring++ {
		d := rng * 2 / 3
		if ring == 1 {
			d = rng * 17 / 20
		}
		if p.refused > 1 {
			d = rng / 2
		}
		for j := 0; j < 16; j++ {
			b := uint16(j * 4096)
			sx, sz := airAlong(tx, tz, b, d)
			if !s.airOnMap(sx, sz, 48) {
				continue
			}
			move := isqrt64((sx-px)*(sx-px) + (sz-pz)*(sz-pz))
			lateral := abs64((sx-px)*rz - (sz-pz)*rx)
			moves := !p.standSet || lateral >= minMove*radialDistance && move >= minMove
			clear := false
			if preview {
				clear = s.airHoverPointClear(u, target, sx, sz, tick)
			}
			c := &candidates[ring*16+j]
			c.x, c.z, c.clear = sx, sz, clear
			cost := pointCost(sx, sz, move)
			if best < 0 || clear && !bestClear || clear == bestClear && (moves && !bestMoves || moves == bestMoves && cost < bestCost) {
				best, bestCost, bx, bz = ring*16+j, cost, sx, sz
				bestMoves = moves
				bestClear = clear
			}
		}
	}
	if best < 0 {
		bx, bz = px, pz
	}
	p.slideTarget = nil
	p.noClearTarget = nil
	if preview && !bestClear {
		p.noClearTarget = target
	}
	if preview && bestClear {
		// Endpoints alone can straddle a ridge or feature. Admit a reusable
		// slide only when quarter, half and three-quarter points also preview
		// clear. Actual curved flight and every pellet still use normal weapon
		// admission; later physical refusals reconsider this plan.
		radial := isqrt64((bx-tx)*(bx-tx) + (bz-tz)*(bz-tz))
		var score int64
		for _, c := range candidates {
			if !c.clear {
				continue
			}
			dx, dz := c.x-bx, c.z-bz
			move := isqrt64(dx*dx + dz*dz)
			otherRadius := isqrt64((c.x-tx)*(c.x-tx) + (c.z-tz)*(c.z-tz))
			// The retained pair is flown both ways. Mixed rings must meet
			// the lateral floor about either endpoint's target bearing.
			if move < minMove || abs64(dx*(bz-tz)-dz*(bx-tx)) < minMove*max(radial, otherRadius) {
				continue
			}
			cost := pointCost(c.x, c.z, move)
			if p.slideTarget != nil && cost >= score {
				continue
			}
			clear := true
			for k := int64(1); k < 4; k++ {
				if !s.airHoverPointClear(u, target, bx+dx*k/4, bz+dz*k/4, tick) {
					clear = false
					break
				}
			}
			if clear {
				p.slideTarget, p.slideX, p.slideZ, score = target, c.x, c.z, cost
			}
		}
	}
	p.standX, p.standZ, p.standTick, p.standSet, p.refused = bx, bz, tick, true, 0
}

// --- repair beside a constructor ---

// airSeekFieldRepair is the Modern fallback when a damaged aircraft finds no
// repair pad: land beside a friendly constructor that is patrolling, whose
// patrol repairs grounded units in its sight [04 R-ORD-02 §4], and wait to be
// repaired. It head-inserts an ordinary `VTOL_GetRepaired` naming the
// constructor; the Modern leg below flies, lands and waits.
func (s *System) airSeekFieldRepair(u *units.Unit, n *orders.Node, tick uint32) bool {
	if !s.airTacticsActive() || u == nil || u.Def == nil || !u.Def.CanFly || s.world == nil {
		return false
	}
	b := airBinding(u)
	pic := s.airThreatsFor(u, tick)
	var best *units.Unit
	var bestCost int64
	s.airScratch = s.world.AppendLiveSliced(s.airScratch[:0])
	for _, c := range s.airScratch {
		if !s.airFieldRepairer(u, c, b) {
			continue
		}
		d := repairPlanarDistance(u.X, u.Z, c.X, c.Z) >> 16
		if d > 0xF00 {
			continue
		}
		cost := d + airPointExposure(int64(c.X>>16), int64(c.Z>>16), pic.list)*64
		if best == nil || cost < bestCost {
			// Admit only a repair the landing leg can start. An immediate
			// failure would expose the suspended patrol, which retries in
			// this same pump [04 R-ORD-01 §10] (issue 131).
			if _, _, ok := s.airFieldRepairPoint(u, c); !ok {
				continue
			}
			best, bestCost = c, cost
		}
	}
	if best == nil {
		return false
	}
	s.ReleaseGoalPayload(n)
	airSpawnAtHead(u, "VTOL_GetRepaired", best.Handle, Vec3{X: best.X, Y: best.Y, Z: best.Z}, tick)
	n.DynamicGate = 0
	return true
}

// airFieldRepairer admits a live, finished, friendly mobile constructor that
// is not an aircraft itself and whose current order is a patrol.
func (s *System) airFieldRepairer(u, c *units.Unit, b *orders.QueueBinding) bool {
	if c == nil || c == u || !c.Alive || c.Dying || c.Def == nil || c.Remaining != 0 ||
		!c.Def.Builder || !c.Def.CanMove || c.Def.CanFly || c.Def.IsAirBase || c.Def.BuildDistance <= 0 {
		return false
	}
	if c.Owner != u.Owner {
		if b == nil || b.World == nil || b.World.DeclaresAllianceHook() == nil ||
			!b.World.DeclaresAllianceHook()(c.Owner, u.Owner) || !b.World.DeclaresAllianceHook()(u.Owner, c.Owner) {
			return false
		}
	}
	q := orders.QueueOfUnit(c)
	if q == nil || q.LenPrimary() == 0 {
		return false
	}
	switch orders.DescriptorFor(q.PrimaryAt(0).ID).Name {
	case "Patrol", "RepairPatrol":
		return true
	}
	return false
}

// airFieldRepairPoint shares the landing search between admission and flight,
// so both use the same footprint, sight radius and landing predicate.
func (s *System) airFieldRepairPoint(u, c *units.Unit) (numeric.Fixed, numeric.Fixed, bool) {
	near := int64(96)
	if c.Def != nil && c.Def.SightDistance > 0 {
		near = min(near, int64(c.Def.SightDistance)/2)
	}
	cx, cz := int64(c.X>>16), int64(c.Z>>16)
	h := airBearingWU(cx, cz, int64(u.X>>16), int64(u.Z>>16))
	fx, fz := s.pathFootprint(u)
	for k := 0; k < 8; k++ {
		sx, sz := airAlong(cx, cz, h+uint16(k*8192), near)
		x, z := snapToOwnFootprint(numeric.Fixed(sx<<16), numeric.Fixed(sz<<16), fx, fz)
		if s.landable(u, x, z) {
			return x, z, true
		}
	}
	return 0, 0, false
}

// legFieldRepairModern is `VTOL_GetRepaired` named at a constructor rather
// than a pad: fly beside the constructor, land, and wait for its patrol to
// repair the grounded aircraft. It ends repaired, when the constructor is
// gone, or after waiting airFieldRepairWait ticks without being healed; the
// suspended order then resumes and takes off as usual.
const airFieldRepairWait = 600

func (s *System) legFieldRepairModern(u *units.Unit, n *orders.Node, satisfied uint32, tick uint32) orders.Code {
	c := s.unitFor(n.Target)
	if c == nil || !c.Alive || c.Dying {
		return 5
	}
	p := s.airPilotFor(u, n, tick)
	if u.Def != nil && u.Health >= u.Def.MaxDamage {
		return 5
	}
	switch n.Phase {
	case 0:
		// Approach: a landable spot on the near side of the constructor,
		// inside its sight.
		if u.Move.Mode&3 != 2 {
			n.Phase = 2
			p.standTick, p.healHP = tick, u.Health
			return airHold(n, tick, 0)
		}
		if x, z, ok := s.airFieldRepairPoint(u, c); ok {
			p.standX, p.standZ, p.standSet = int64(x>>16), int64(z>>16), true
		}
		if !p.standSet {
			return 5
		}
		d := isqrt64((p.standX-int64(u.X>>16))*(p.standX-int64(u.X>>16)) + (p.standZ-int64(u.Z>>16))*(p.standZ-int64(u.Z>>16)))
		if d <= 24 {
			m := s.newPointMarker(u, Vec3{X: u.X, Y: u.Y, Z: u.Z})
			offset := int16(0)
			if hh, sea, ok := s.terrainAndSea(u.X, u.Z); ok && hh <= sea {
				offset = int16(hh - sea)
			}
			m.setAltitudeOffset(offset)
			s.installAirGoal(u, n, m)
			u.SetActivationEdge(false)
			n.Phase = 1
			n.DynamicGate = airLegGate
			return 2
		}
		s.airGoalAt(u, n, Vec3{X: numeric.Fixed(p.standX << 16), Y: u.Y, Z: numeric.Fixed(p.standZ << 16)}, 0x10)
		return airHold(n, tick, airLegGate)
	case 1:
		if satisfied&0x20 == 0 {
			return airHold(n, tick, airLegGate)
		}
		s.SetMoverMode(u, 1)
		n.Phase = 2
		p.standTick, p.healHP = tick, u.Health
		return airHold(n, tick, 0)
	case 2:
		if u.Health > p.healHP {
			p.standTick = tick
		}
		p.healHP = u.Health
		if tick-p.standTick >= airFieldRepairWait {
			return 5
		}
		n.Deadline = int32(tick + 30)
		n.DynamicGate |= 1
		return 2
	default:
		return 7
	}
}

// AirPilotState is a diagnostic copy of an aircraft's Modern pilot, for the
// aircraft laboratory. It reads committed state and changes nothing.
type AirPilotState struct {
	Bound          bool
	Mode           uint8
	Run            uint16
	EntryX, EntryZ int64
	StandX, StandZ int64
	UnderFire      bool
	Threats        int
}

// AirPilotDiag reports h's pilot, if one is bound.
func (s *System) AirPilotDiag(h pool.Handle, tick uint32) AirPilotState {
	if s == nil || s.airPilots == nil {
		return AirPilotState{}
	}
	p := s.airPilots[h]
	if p == nil {
		return AirPilotState{}
	}
	st := AirPilotState{Bound: true, Mode: p.mode, Run: p.run, EntryX: p.entryX, EntryZ: p.entryZ, StandX: p.standX, StandZ: p.standZ, UnderFire: p.underFire(tick)}
	if p.unit != nil && int(p.unit.Owner) < len(s.airThreats) {
		st.Threats = len(s.airThreats[p.unit.Owner].list)
	}
	return st
}
