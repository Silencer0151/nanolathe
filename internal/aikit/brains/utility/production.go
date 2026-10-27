package utility

import (
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// Production kinds share the decision record with the economy.
const (
	pCons commitKind = 20 + iota
	pCombat
	pScout
	pInterceptor
	pUpgrade // a missing upgrade at an idle module (upgrades.go)
	pSupport // a mobile radar or jammer for the army (breadth, prod_breadth.go)
	pAssist  // an assist builder for the factories (breadth, prod_breadth.go)
)

// queueDepth keeps each factory at most this many products deep (the one
// in production plus one queued), so choices track the situation.
const queueDepth = 2

// lowAPM is the action rate below which a persona queues combat units in
// batches of lowAPMBatch (Easy is 25, Medium 60, Hard 150).
const (
	lowAPM      = 100
	lowAPMBatch = 2
)

// batchOf is how many of the chosen product one Produce order queues. A
// persona short of actions queues combat units two at a time, as a player
// short of clicks queues several: at one action per unit, Easy's factories
// took a large share of its action budget and stood empty while the army
// and the builders waited for the same actions. Builders, scouts and other
// products stay one at a time so their counts track the plan, and a
// restriction cap is never overrun.
func batchOf(k *aikit.Kit, best *cand) int32 {
	n := int32(1)
	if best.kind == pCombat && k.Persona.APM > 0 && k.Persona.APM < lowAPM {
		n = lowAPMBatch
	}
	if a := k.Allowance(best.prod); a < n {
		n = max(a, 1)
	}
	return n
}

// factoryReg remembers the last product this brain queued at a factory.
type factoryReg struct {
	def  *aikit.UnitInfo
	gen  uint32
	prod *aikit.UnitInfo
	tick uint32
}

// Production scores every product of each factory that has room in its
// queue: constructors for unmet economy spending or expansion room, combat
// units by cost-efficiency with counters to what was seen, one scout, and
// with the breadth parts a few support units and assist builders.
type Production struct {
	s *shared
	// st is the strategy whose variety (jitter) decides whether a per-game
	// preference is drawn (brain.go wiring); nil draws none.
	st        *Strategy
	br        breadth // production breadth (prod_breadth.go)
	ring      [8]decision
	next      int
	reg       []factoryReg
	scouts    int32 // scouts ever queued
	airScouts int32 // unarmed air scouts ever queued
	cleared   int32 // blocked pads cleared
	// combatQueued counts combat products (not scouts) ever queued
	// (open_army part 2).
	combatQueued int32
	buf          []pool.Handle
}

// maxScouts bounds scouts over a game: a scout that keeps dying is not
// finding anything new.
const maxScouts = 3

// constructorIdleStop is the number of idle economy constructors of a
// product's domain (land or water) at which production stops offering
// ordinary constructors of that domain; advanced constructors stay on the
// tech plan. The commander is not one: an idle commander beside one idle
// constructor must not stop the factory that would give them work. The
// live economy may still have a project for them next think; until it
// does, another one only adds to the base crowd that blocks factory pads.
const constructorIdleStop = 2

func (pr *Production) Init(b *core.Board) {
	pr.s.setup(b.K)
	// Core runs production's Init after the strategy's, so the preference
	// draw follows every draw the strategy makes there.
	pr.setupBreadth(b.K, pr.st != nil && pr.st.vr.v.Jitter)
}

func (pr *Production) regOf(u *aikit.OwnUnit) *factoryReg {
	pr.reg = handleSlots(pr.reg, u.H, pr.s.hcap)
	r := &pr.reg[u.H]
	if r.def != u.Info || r.gen != u.Gen {
		*r = factoryReg{def: u.Info, gen: u.Gen}
	}
	return r
}

func (pr *Production) Plan(b *core.Board) {
	s := pr.s
	k := b.K
	budget := int(s.budget) - k.Emitted()
	if budget > 0 {
		budget -= pr.clearPads(b, budget)
	}
	if budget > 0 {
		pr.produce(b, budget)
	}
	// Upgrade modules take what production left (upgrades.go), then assist
	// builders without products are sent to help (prod_assist.go).
	pr.produceUpgrades(b, int(s.budget)-k.Emitted())
	pr.commandAssists(b, int(s.budget)-k.Emitted())
	s.spendAPM(k.Emitted())
}

// builderRally is a point behind home, away from the factories' forward
// lanes, where units cleared from a pad can stand until the economy gives
// them work. The old destination was one factory footprint plus 128 wu
// along +Z, which is still in the built-up base and can be the next
// factory's exit.
func builderRally(b *core.Board) (int32, int32) {
	if b == nil || b.K == nil || b.K.Map == nil {
		return 0, 0
	}
	m := b.K.Map
	dx, dz := b.HomeX-b.EnemyX, b.HomeZ-b.EnemyZ
	x := clampWorld(b.HomeX+dx/4, m.WorldW)
	z := clampWorld(b.HomeZ+dz/4, m.WorldH)
	return x, z
}

// clearPads moves own units parked on the pad of a factory that has
// stopped starting units out along its exit lane (+Z, the side the executor
// keeps clear). It returns the actions used.
// padLaneCells is how many cells beyond a factory's front edge count as the
// pad's short exit lane for parked units.
const padLaneCells = 4

// onPadLane reports whether a point lies in the short exit lane directly in
// front of a factory (the +Z side the executor keeps clear).
func onPadLane(f *aikit.OwnUnit, x, z int32) bool {
	if f == nil || f.Info == nil {
		return false
	}
	fx, fz := f.Info.FootX, f.Info.FootZ
	x0 := (f.X/16 - fx/2 - 1) * 16
	x1 := (f.X/16 - fx/2 + fx + 1) * 16
	z0 := (f.Z/16 - fz/2 + fz) * 16
	z1 := z0 + padLaneCells*16
	return x >= x0 && x < x1 && z >= z0 && z < z1
}

func (pr *Production) clearPads(b *core.Board, budget int) int {
	if budget <= 0 {
		return 0
	}
	s := pr.s
	o := b.O
	pr.buf = pr.buf[:0]
	for _, fi := range b.Factories {
		f := &o.Own[fi]
		fs := s.fstateOf(f)
		if !fs.blocked || s.tick-fs.lastClear < 300 {
			continue
		}
		fs.lastClear = s.tick
		if fs.clears < 250 {
			fs.clears++
		}
		for i := range o.Own {
			u := &o.Own[i]
			if !u.Built || !u.Info.Role.Has(aikit.RoleMobile) || slices.Contains(pr.buf, u.H) {
				continue
			}
			// A parked unit just outside the footprint can block the same
			// exit. Only idle units are moved: a unit crossing the lane on an
			// order is not parked on the pad.
			if !onPad(f, u.X, u.Z) && !(u.Order == aikit.OrderIdle && onPadLane(f, u.X, u.Z)) {
				continue
			}
			pr.buf = append(pr.buf, u.H)
		}
	}
	if len(pr.buf) == 0 {
		return 0
	}
	// One group move clears every blocked pad this think. Moving them to
	// the builder rally behind home, not one factory lane further, keeps
	// them out of the next pad; a dead factory is cleared too, so its queue
	// can resume instead of being lost for the rest of the game.
	rx, rz := builderRally(b)
	b.K.Move(pr.buf, rx, rz, false)
	pr.cleared++
	return 1
}

// firstConsNeed is the least need of a constructor while we have none.
const firstConsNeed = 2000

// Parts of the play parameter (the play review's fixes).
const (
	playReachCons    = 1 << iota // a stranded factory is not the constructor source while one that reaches is on offer
	playFirstConsSea             // the first constructor ahead of combat units at a shipyard or air plant
	playMexPower                 // an extractor waits for energy coverage while the energy store is below half
	playAttackIncome             // the launch value's army curve scaled by our income
	playFramePower               // an energy frame's pending output counts by its build progress
)

// mexPowerLo and mexPowerHi (permille of energy coverage) are where an
// extractor's score rises from 0 to full under playMexPower.
const (
	mexPowerLo = 400
	mexPowerHi = 900
)

// seaOrAirFactory reports a factory whose family is the shipyard or the air
// plant (econ_family.go). A land factory's opening makes a combat unit or
// two before its constructor and holds off early raids; forcing the
// constructor first there lost the mirrors (the play review,
// README §15).
func seaOrAirFactory(s *shared, f *aikit.UnitInfo) bool {
	fam := int32(famNone)
	if int(f.Index) < len(s.facFam) {
		fam = int32(s.facFam[f.Index])
	} else {
		fam = factoryFamily(s, f)
	}
	return fam == famShip || fam == famAir
}

func (pr *Production) produce(b *core.Board, budget int) {
	s := pr.s
	sp := &s.p
	o := b.O
	k := b.K
	brOn := sp.Breadth != 0
	if brOn {
		pr.setupBreadth(k, false) // a production used without Init (tests): no preference
		pr.observeBreadth(b)
	}
	// Constructors queued but not yet framed.
	var queued int32
	for _, fi := range b.Factories {
		f := &o.Own[fi]
		r := pr.regOf(f)
		if r.prod != nil && f.QueueLen >= 1 && s.tick-r.tick < 1800 && s.info[r.prod.Index].canEco {
			queued++
		}
	}
	pending := s.consPending
	if queued > pending {
		pending = queued
	}
	eco := int64(b.Posture.EcoShare)
	threatened := b.NearHomeThreat > 0
	ext := sp.Naval != 0 || sp.Air != 0 || sp.Tech != 0
	// Army share, doubled while the army is below the size the strategy
	// wants it to reach (deficit falls linearly to zero at that size).
	deficit := lin(int64(b.ArmyValue), s.armyTarget, 0)
	armyF := clamp(mul((100-eco)*20, one+deficit), 200, 2400)
	// Energy gates: combat pauses before an energy stall (which would also
	// stop extraction); constructors pause only in a deep one.
	gate := lin(s.covE, 600, 900)
	if s.eStock*20 < s.eCap && s.eInc < s.eExp {
		gate = 0
	}
	consGate := lin(s.covE, 300, 600)
	if threatened {
		armyF = max64(armyF, 1200)
		gate, consGate = one, one
	}
	ecoCap := s.bpBld * s.rMB / 1000
	desiredEco := s.spendable * eco / 100
	guard := int8(-1) // reach_mix: whether the stranded home guard is full (-1 = not yet asked)
	// growth: constructors to the human ratio, from surplus only.
	// growth: constructors to the human ratio from surplus (steep beyond
	// it), and a floor of one or two on any map (econ_growth.go).
	steep := s.part(gConstructors) && s.growSafe() && s.surplus()
	var consT int64
	if steep {
		consT = s.consTarget()
	}
	if s.part(gConsFloor) {
		consT = max64(consT, s.consFloor())
	}
	// expand: constructors follow the expansion (econ_expand.go).
	consT = max64(consT, s.consExpand())
	// open_army: scouts wait for the first combat unit, and combat units
	// follow the first constructor (opening.go).
	scoutWait := s.scoutsWait(b, pr)
	armyFirst := s.armyFirst(pr, pending)
	for _, fi := range b.Factories {
		if budget <= 0 {
			break
		}
		f := &o.Own[fi]
		if f.QueueLen >= queueDepth || s.fstateOf(f).blocked {
			continue
		}
		builders := int64(s.builders + pending)
		expand := clamp(int64(s.freeSafe)*1000/max64(builders*int64(sp.SpotsPerCon), 1), 0, 1200)
		// Normalizer: the best combat efficiency this factory offers now;
		// with cheap fast units in the mix (breadth), the best of the others,
		// so a raider does not set the floor the others are measured by.
		exclFast := s.bpart(bFast) && pr.br.hasOther(f.Info)
		var bestEff int64 = 1
		if ext {
			bestEff = pr.bestEffN(f.Info, exclFast)
		} else {
			for _, q := range f.Info.Builds {
				if s.info[q.Index].combat && !(exclFast && pr.br.kind[q.Index]&kFast != 0) {
					if ef := s.eff(q, s.aaNeed); ef > bestEff {
						bestEff = ef
					}
				}
			}
		}
		// breadth: this factory's combat units, and its cheap fast ones;
		// facMix -1 counts a product's share of the whole army as before.
		var facN, fastN int64
		facMix := int64(-1)
		if brOn {
			facN, fastN = pr.facCount(f.Info)
			if s.bpart(bVariety) {
				facMix = facN
			}
		}
		// How far this factory's army reaches the enemy: a factory whose
		// units cannot get there makes combat units only to defend.
		freach := s.factoryReach(f.Info)
		// reach_mix: a stranded factory adds combat units only to a home
		// guard (econ_reach.go).
		noArmy := false
		if sp.ReachMix != 0 && s.reach != nil && freach < strandReach {
			if guard < 0 {
				guard = 0
				if s.homeGuardFull(b) {
					guard = 1
				}
			}
			noArmy = guard == 1
		}
		var d decision
		d.reset(s.tick, f.Info, f.X, f.Z)
		for _, q := range f.Info.Builds {
			si := &s.info[q.Index]
			// A product whose restriction cap is used up is not a candidate
			// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI restriction caps").
			if !q.Role.Has(aikit.RoleMobile) || k.Allowance(q) <= 0 {
				continue
			}
			var c cand
			c.prod, c.spot = q, -1
			switch {
			case q.Role.Has(aikit.RoleInterceptor):
				c.kind = pInterceptor
				c.score = pr.interceptorScore(b, q)
			case si.canEco:
				// A builder without work already is not a reason to make
				// another: the long-game report's ally queued constructors
				// with 40 idle, while the economy had no action budget left
				// to assign them. Stop at the first idle builder pair; the
				// economy can still use the ones it has. Advanced
				// constructors are left alone: the tech plan's first one is
				// wanted even beside idle ordinary builders.
				if s.idleCons[domainOf(si)] >= constructorIdleStop && !si.advCon {
					break
				}
				drain := max64(int64(q.BuildPower)*s.rMB/1000, 1)
				gap := clamp((desiredEco-ecoCap)*1000/drain, 0, 1200)
				room := expand
				if sp.Naval != 0 && s.terr.ready {
					// The room this constructor type would have (its class's
					// reachable spots and water work), not our builders'.
					room = clamp(int64(s.consRoom[q.Index])*1000/max64(builders*int64(sp.SpotsPerCon), 1), 0, 1200)
				}
				need := max64(gap, room)
				// Each builder already owned or coming makes the next worth less.
				dim := half(int64(s.cons+pending)*1000, 4000)
				// growth: below the target a constructor is wanted
				// outright; beyond the ratio target each one more is worth
				// less (econ_growth.go).
				if short := consT - int64(s.cons+pending)*1000; short > 0 {
					need = max64(need, one+min64(short, 2000)/2)
					dim = one
				} else if steep {
					dim = half(one-short, 500)
				}
				if sp.Play&playFirstConsSea != 0 && s.cons+pending == 0 && !threatened && seaOrAirFactory(s, f.Info) {
					// At a shipyard or air plant the first constructor
					// goes ahead of combat units while the commander is
					// the only builder: a shipyard first queued two
					// 930-value warships ahead of its naval constructor
					// and an air plant two fighters, leaving the commander
					// alone for five minutes (the play review,
					// README §15). A land factory
					// keeps its opening (seaOrAirFactory).
					need = max64(need, firstConsNeed)
				}
				c.kind = pCons
				c.score = mul(mul(mul(int64(sp.WCons)*10, need), dim), consGate)
				if armyFirst && !threatened {
					// Combat units first (open_army part 2): a constructor
					// still goes ahead when no combat unit can be made.
					c.score /= armyFirstCut
				}
				c.f = [4]int64{need, dim, consGate, int64(s.cons + pending)}
				if sp.Tech != 0 && si.advCon {
					// The tech plan wants a few advanced builders however
					// many ordinary ones there are (its own count decays it).
					if w := mul(mul(int64(sp.WCons)*10, s.advConsWanted()), consGate); w > c.score {
						c.score = w
						c.f = [4]int64{s.advConsWanted(), one, consGate, int64(s.tech.advCons)}
					}
				}
			case sp.Air != 0 && q.Role.Has(aikit.RoleScout|aikit.RoleAir) && !q.Role.Has(aikit.RoleCombat):
				// An air scout sees what land scouts cannot reach.
				if scoutWait || s.airScouts > 0 || s.minutes < 2 || pr.airScouts >= maxScouts || (b.EnemyKnown && s.minutes >= 10) {
					break
				}
				need := int64(one)
				if !b.EnemyKnown {
					need = 2 * one
				}
				c.kind = pScout
				c.score = mul(int64(sp.WScout)*10, need)
				c.f = [4]int64{need, 0, 0, 0}
			case q.Role.Has(aikit.RoleScout) && q.Role.Has(aikit.RoleCombat):
				if scoutWait || s.scouts > 0 || s.minutes < 1 || pr.scouts >= maxScouts || (b.EnemyKnown && s.minutes >= 10) {
					// breadth: beyond the scout allowance it is a combat
					// candidate, up to a share of the factory's combat units.
					if !s.bpart(bFast) || pr.br.kind[q.Index]&kCombat == 0 || (ext && noArmy) {
						break
					}
					c.kind = pCombat
					if ext {
						c.score, c.f = pr.combatScoreN(q, bestEff, armyF, gate, freach, facMix)
					} else {
						c.score, c.f = pr.combatScore(q, bestEff, armyF, gate, facMix)
					}
					c.score = mul(c.score, lin(fastN*1000/(facN+1), fastHi, fastLo))
					break
				}
				need := int64(one)
				if !b.EnemyKnown {
					need = 2 * one
				}
				c.kind = pScout
				c.score = mul(int64(sp.WScout)*10, need)
				c.f = [4]int64{need, 0, 0, 0}
			case ext && si.combatN && (sp.Naval != 0 || si.combat):
				if noArmy {
					break
				}
				c.kind = pCombat
				c.score, c.f = pr.combatScoreN(q, bestEff, armyF, gate, freach, facMix)
			case !ext && si.combat:
				c.kind = pCombat
				c.score, c.f = pr.combatScore(q, bestEff, armyF, gate, facMix)
			}
			if c.score > 0 {
				d.offer(&c)
			}
		}
		if brOn && !threatened && !armyFirst {
			// breadth: the factory's cheapest radar and jammer while the army
			// wants one, and its best assist builder while there are fewer
			// than one per two factories (prod_breadth.go).
			fb := &pr.br.fac[f.Info.Index]
			if s.bpart(bSupport) && !noArmy {
				for _, sk := range [...]uint8{kRadar, kJam} {
					q := fb.radar
					if sk == kJam {
						q = fb.jam
					}
					if c := pr.supportCand(b, q, sk, armyF, gate, freach); c.score > 0 {
						d.offer(&c)
					}
				}
			}
			if s.bpart(bAssist) {
				if c := pr.assistCand(b, fb.assist, consGate); c.score > 0 {
					d.offer(&c)
				}
			}
		}
		if d.n == 0 || d.top[0].score < minScore {
			continue
		}
		best := &d.top[0]
		n := batchOf(k, best)
		k.Produce(f.H, best.prod, n)
		budget--
		r := pr.regOf(f)
		r.prod, r.tick = best.prod, s.tick
		s.count[best.prod.Index] += n
		if brOn {
			for range n {
				pr.br.noteQueued(best.prod, best.kind)
			}
		}
		switch best.kind {
		case pInterceptor:
			s.interceptorQueued = true
		case pCons:
			pending++
			armyFirst = s.armyFirst(pr, pending)
			ecoCap += int64(best.prod.BuildPower) * s.rMB / 1000
		case pCombat:
			s.armyCount += n
			scoutWait = false
			pr.combatQueued += n
			armyFirst = s.armyFirst(pr, pending)
		case pScout:
			if best.prod.Role.Has(aikit.RoleCombat) {
				s.scouts++
				pr.scouts++
			} else {
				s.airScouts++
				pr.airScouts++
			}
		}
		d.chosen, d.note = 0, noteAssign
		pr.ring[pr.next] = d
		pr.next = (pr.next + 1) % len(pr.ring)
	}
}

// combatScore: army share × efficiency (normalized to the factory's best)
// × anti-air counter × range counter × variety × energy gate. facMix is the
// factory's own combat units for breadth's variety (its share is counted
// among them, then the floor and the preference apply: vary), or -1 for a
// share of the whole army.
func (pr *Production) combatScore(q *aikit.UnitInfo, bestEff, armyF, gate, facMix int64) (int64, [4]int64) {
	s := pr.s
	sp := &s.p
	eff := s.eff(q, s.aaNeed) * 1000 / bestEff
	counter := int64(one)
	if q.AirDPS > 0 && s.aaNeed > 0 {
		aaQ := min64(int64(q.AirDPS)*1000/max64(int64(q.DPS), 1), 1000)
		counter += mul(mul(s.aaNeed, aaQ), int64(sp.WAA)*20)
	}
	if s.defShare > 0 {
		counter += mul(mul(s.defShare, lin(int64(q.Range), 300, 800)), int64(sp.WRange)*20)
	}
	mix := pr.mixOf(q, facMix)
	sc := mul(mul(mul(mul(mul(int64(sp.WArmy)*10, armyF), eff), counter), mix), gate)
	if facMix >= 0 {
		sc = pr.vary(sc, q, eff, counter)
	}
	return sc, [4]int64{armyF, eff, counter, mix}
}

// bestEffN is the best reach-weighted efficiency among a factory's combat
// products (the switches' normalizer), leaving cheap fast units out when
// exclFast.
func (pr *Production) bestEffN(f *aikit.UnitInfo, exclFast bool) int64 {
	s := pr.s
	var best int64 = 1
	for _, q := range f.Builds {
		si := &s.info[q.Index]
		if !si.combatN || (s.p.Naval == 0 && !si.combat) || (exclFast && pr.br.kind[q.Index]&kFast != 0) {
			continue
		}
		if ef := s.effFleet(q, s.aaNeed) * max64(s.reachOf(q), 150) / one; ef > best {
			best = ef
		}
	}
	return best
}

// combatScoreN is combatScore with the switches' model: efficiency counts
// torpedoes only against ships and is weighted by how far the unit
// reaches the enemy (normalized to the factory's best), and the whole score
// by how far the factory's army reaches (a factory whose units cannot get
// there makes combat units only to defend).
func (pr *Production) combatScoreN(q *aikit.UnitInfo, bestEff, armyF, gate, freach, facMix int64) (int64, [4]int64) {
	s := pr.s
	sp := &s.p
	if sp.Naval != 0 && s.navalIdle >= navalIdleMax && (q.Role.Has(aikit.RoleNaval) || s.info[q.Index].water) {
		// Ships already launched are not being used: make no more.
		return 0, [4]int64{}
	}
	if s.fleetWar() && s.info[q.Index].light && s.lightFull() {
		// Light boats beyond the few kept for scouting and raiding.
		return 0, [4]int64{}
	}
	eff := s.effFleet(q, s.aaNeed) * max64(s.reachOf(q), 150) / one * 1000 / bestEff
	counter := int64(one)
	if q.AirDPS > 0 && s.aaNeed > 0 {
		aaQ := min64(int64(q.AirDPS)*1000/max64(int64(q.DPS), 1), 1000)
		counter += mul(mul(s.aaNeed, aaQ), int64(sp.WAA)*20)
	}
	if s.defShare > 0 {
		counter += mul(mul(s.defShare, lin(int64(q.Range), 300, 800)), int64(sp.WRange)*20)
	}
	mix := pr.mixOf(q, facMix)
	sc := mul(mul(mul(mul(mul(mul(int64(sp.WArmy)*10, armyF), eff), counter), mix), gate), freach)
	if facMix >= 0 {
		sc = pr.vary(sc, q, eff, counter)
	}
	return sc, [4]int64{armyF, eff, counter, mix}
}

// mixOf is the variety consideration, half(share × w_mix/100, 300) with
// the product's share of the whole army, permille; with breadth's variety
// half(share × w_mix/100, mixHalf) with its share of the factory's own
// combat units (facMix).
func (pr *Production) mixOf(q *aikit.UnitInfo, facMix int64) int64 {
	s := pr.s
	w := int64(s.p.WMix)
	if facMix >= 0 {
		return half(int64(s.count[q.Index])*1000/(facMix+1)*w/100, mixHalf)
	}
	return half(int64(s.count[q.Index])*1000/int64(s.armyCount+1)*w/100, 300)
}

// vary applies breadth's efficiency floor and the product's per-game
// preference to a combat score: the floor reads the efficiency with
// counters (normalized to the factory's best), so a unit far below the best
// stays out whatever its preference, and an anti-air or long-range unit
// the counters call for stays in.
func (pr *Production) vary(sc int64, q *aikit.UnitInfo, eff, counter int64) int64 {
	return mul(mul(sc, lin(mul(eff, counter), effFloorLo, effFloorHi)), pr.br.prefOf(q))
}
