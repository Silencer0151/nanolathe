package combat

import (
	"github.com/nanolathe-gg/nanolathe/internal/cob"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// TargetQuery is reusable service-owned storage for one acquisition request.
// Neither the policy nor its caller retains the candidate scratch afterwards.
type TargetQuery struct {
	Candidates  []Candidate
	Acquisition Acquisition
	Shooter     *units.Unit
	Slot        *units.Slot
	Index       int
	World       *units.World
	Terrain     *world.Terrain
	Visibility  *visibility.Service
	Economy     *economy.Service
	Catalog     *content.Catalog
	// FromSecondary reports that Candidates came from the registry's
	// secondary (seen-bit) list: the visible scan was empty and the scanning
	// player's targeting-upgrade gate was open [06 §3.1][04 R-SPEC-01 §8].
	FromSecondary bool
	// Attempt-local physical ranking scratch; no script query or live shot.
	obstruction ShotQuery
}

// All tuning in this file is Nanolathe Modern policy, documented in
// docs/DESIGN_WEAPONS_PROJECTILES.md "Modern threat targeting and incoming fire".
const modernIncomingHorizon = 6
const modernAllyRadius = 256

// incomingShot is deliberately separate from the retail record. Identity is
// an allocation's Unit pointer, never a new generation tag on retail handles.
// Reserve clears it and compaction moves it. A restored service starts empty.
type incomingShot struct {
	target, shooter *units.Unit
	weapon          *content.WeaponDef
	motion          modernBeamMotion
	beamInvalid     bool
}

// Keep a building's damage-time retaliation target while it remains unseen.
// The retail offer already installed that target without a sensor query
// [06 R-WPN-04 §2]; maintaining it does not reveal any other hidden unit.
// Mobile units keep Modern's observed-danger response and visible acquisition.
func (*ModernRules) ReconsiderTarget(shooter *units.Unit, slot *units.Slot, target *units.Unit, vis *visibility.Service) bool {
	if shooter == nil || shooter.Def == nil || shooter.Def.CanMove || slot == nil ||
		target == nil || target.Def == nil || vis == nil ||
		shooter.EngagementTarget == 0 || slot.Target.Kind != units.TargetUnit ||
		slot.Target.Unit != shooter.EngagementTarget || target.Handle != shooter.EngagementTarget {
		return true
	}
	return vis.IsVisible(visibility.PlayerID(shooter.Owner), visibilityTarget(target, perspectiveStatus(vis, shooter.Owner, target)))
}
func (*ModernRules) CombatTick(s *Service, tick uint32, afterProjectiles bool) {
	s.modernTick = tick
	if afterProjectiles {
		s.modernNextProjectileTick = tick + 1
	}
}

func (*ModernRules) ObserveDanger(s *Service, victim, attacker *units.Unit, tick uint32) {
	if s == nil || s.DangerNoticeHook() == nil || victim == nil || attacker == nil ||
		victim == attacker || !victim.Alive || victim.Dying || !attacker.Alive || attacker.Dying ||
		victim.Owner == attacker.Owner || victim.Def == nil || attacker.Def == nil {
		return
	}
	// A composed session supplies the alliance row. Without it, fail closed.
	if s.Reaction == nil || s.Reaction.AlliedHook() == nil || s.Reaction.AlliedHook()(victim.Owner, attacker.Owner) {
		return
	}
	s.DangerNoticeHook()(victim, attacker, tick)
}

func (r *ModernRules) Launched(s *Service, h pool.Handle, q *ShotQuery) {
	if s == nil || q == nil || q.Target == nil || q.Shooter == nil || !s.Alive(h) {
		return
	}
	s.incoming[int(h)-1] = incomingShot{target: q.Target, shooter: q.Shooter, weapon: q.Launch.Weapon, motion: beamMotion(q.Target)}
	r.ObserveDanger(s, q.Target, q.Shooter, q.Tick)
}

// ModernResponseAdmits is the out-of-range response suitability gate. Session
// checks knowledge and hostility; orders owns pursuit distance and provenance.
// Bad categories remain a preference in acquisition, but a response does not
// start a pursuit with a weapon explicitly disfavored for that target. The
// live service keeps its estimate on the same bound veterancy answers as
// accepted damage; omission retains the Strict fixture default.
func ModernResponseAdmits(shooter, target *units.Unit, idx int, terrain *world.Terrain, catalog *content.Catalog, services ...*Service) bool {
	if shooter == nil || target == nil || shooter.Def == nil || target.Def == nil || !target.Alive || target.Dying {
		return false
	}
	slot := orderSlot(shooter, idx)
	if slot == nil || !slot.IsEnabled() || slot.Weapon == nil || slot.Weapon.CommandFire || slot.Weapon.Interceptor {
		return false
	}
	weapon := slot.Weapon
	var service *Service
	if len(services) != 0 {
		service = services[0]
	}
	if modernEffectiveDamage(service, weapon, target, shooter, false, false) <= 0 || (weapon.Paralyzer && (target.Stunned || target.Def.ImmuneToParalyzer)) {
		return false
	}
	if catalog != nil && !IsPreferredCategoryMask(target.Def.DefinitionMask(), badMaskForSlot(shooter.Def, idx)) {
		return false
	}
	sea := int32(0)
	if terrain != nil {
		sea = int32(terrain.SeaLevel)
	}
	return unitToUnitAdmitsBeforeRange(gateEndForUnit(shooter), gateEndForUnit(target), sea, weapon.WaterWeapon, weapon.ToAirWeapon)
}

// ModernAirPursuitAdmits reports whether shooter has a weapon worth pursuing
// an airborne target with: some enabled slot that the out-of-range response
// gate above admits against target and whose weapon is not ballistic. A
// ballistic weapon's lob reaches an aircraft only at the solver's mercy — low,
// close and slow — so it is no anti-air capability to chase with; a laser,
// missile or other direct weapon that the retail acquisition gate does not
// refuse against a flying target is. Nanolathe Modern policy
// (DESIGN_SESSIONS_AI_SAVE "Modern wave air targets").
func ModernAirPursuitAdmits(shooter, target *units.Unit, terrain *world.Terrain, catalog *content.Catalog, services ...*Service) bool {
	if shooter == nil {
		return false
	}
	for idx := 0; idx < units.NumSlots; idx++ {
		slot := orderSlot(shooter, idx)
		if slot == nil || slot.Weapon == nil || slot.Weapon.Ballistic {
			continue
		}
		if ModernResponseAdmits(shooter, target, idx, terrain, catalog, services...) {
			return true
		}
	}
	return false
}

// Only straight ordinary shots whose first impact directly damages the target
// are counted. Small-area direct hits take full damage [06 §9.1]; splash,
// bursts, homing, ballistic, paralyzer and persistent effects fail open.
func modernReliableWeapon(w *content.WeaponDef) bool {
	return w != nil && liveCreationFamilyForWeapon(w) == CreationOrdinary && MotionFamilyForWeapon(w) == MotionDirect &&
		w.WeaponVelocity > 0 && w.Range > 0 && w.Range < 32768 && uint32(w.AreaOfEffect) <= 16 &&
		w.Burst == 0 && !w.NoExplode && !w.GroundBounce && !w.Paralyzer && !w.Interceptor && !w.WaterWeapon
}

// reliableETA predicts copies through the existing motion/contact arithmetic,
// never a live tick, event, damage or RNG path. Ordinary shots require static
// geometry; precise beams use the bounded ground-motion estimate below. Both
// are confidence estimates: later movement can still invalidate a forecast.
func (s *Service) reliableETA(p Projectile, weapon *content.WeaponDef, target *units.Unit, w *units.World, terrain *world.Terrain, tick uint32) int {
	if modernBeamWeapon(weapon) && modernBeamTarget(target) {
		return s.beamETA(p, weapon, target, w, terrain, tick)
	}
	if !modernReliableWeapon(weapon) || w == nil || terrain == nil || target == nil || target.Def == nil ||
		!target.Alive || target.Dying || target.Def.BMCode != 0 || target.Attachment.Carrier != 0 || target.Move.Speed != 0 ||
		!terrainPointValid(p.Pos) || !terrainPointValid(p.Velocity) || p.BurstRemaining != 0 {
		return 0
	}
	if tick < s.modernNextProjectileTick {
		tick = s.modernNextProjectileTick
	}
	if tick < p.CreationTick {
		return 0
	}
	for step := 0; step < modernIncomingHorizon; step++ {
		now := tick + uint32(step)
		if now < tick || AdvanceDirect(&p, weapon, now) != AdvanceAlive || !terrainPointValid(p.Pos) {
			return 0
		}
		hit, feature, water, off, ground, bounce := checkCollision(&p, weapon, w, terrain, s.Features, s.OpaqueLiquidMode)
		if hit != 0 {
			if hit == target.Handle {
				return step + 1
			}
			return 0
		}
		if feature != nil || water || off || ground || bounce {
			return 0
		}
	}
	return 0
}

func (s *Service) effectiveDamage(weapon *content.WeaponDef, target, shooter *units.Unit) int64 {
	return modernEffectiveDamage(s, weapon, target, shooter, s.doubleShot, s.halfShot)
}

func modernEffectiveDamage(service *Service, weapon *content.WeaponDef, target, shooter *units.Unit, doubleShot, halfShot bool) int64 {
	if weapon == nil || target == nil || target.Def == nil {
		return 0
	}
	level := uint32(0)
	if shooter != nil {
		level = service.VeteranLevel(shooter.Def, shooter.Kills)
	}
	nominal := weaponNominal(SelectBaseDamage(weapon, target.Def.UnitName), 1, int32(level), shooter != nil, doubleShot, halfShot)
	// Negative authored damage and narrowing overflow are not kill promises.
	if nominal <= 0 || nominal > 65535 {
		return 0
	}
	defenderLevel := service.VeteranLevel(target.Def, target.Kills)
	if defenderLevel > 25 {
		defenderLevel = 25
	}
	amount := scaleAcceptedAmount(nominal, int32(defenderLevel), UnitArmored(target), target.Def.DamageModifier)
	if weapon.Paralyzer {
		// Paralyzer credit never enters health subtraction [06 §9.1].
		return int64(amount)
	}
	// Health is a signed word, and the receiver subtracts modulo its width
	// [06 §9.1]. A large packed amount can increase health instead of killing.
	// Only an ordinary, decreasing subtraction supports a damage promise;
	// malformed wider health and wrapped results conservatively promise none.
	health := int32(int16(target.Health))
	if health <= 0 || health != target.Health {
		return 0
	}
	after := ApplyDamage(health, amount)
	if after >= health || after != health-int32(amount) {
		return 0
	}
	return int64(health - after)
}

func (s *Service) incomingDamage(observer, target *units.Unit, w *units.World, terrain *world.Terrain, tick uint32, eta int) int64 {
	if target == nil || target.Health <= 0 || eta <= 0 {
		return 0
	}
	var damage int64
	for i := 0; i < s.Count(); i++ {
		in := &s.incoming[i]
		if in.target != target || in.shooter == nil || in.weapon == nil || !s.Alive(pool.Handle(i+1)) ||
			w.Unit(target.Handle) != target || w.Unit(in.shooter.Handle) != in.shooter {
			continue
		}
		p := s.Records[i]
		if !s.DamageRoutingAdmitted(p.ShooterSide) {
			continue
		}
		if p.TargetUnit != target.Handle || p.Shooter != in.shooter.Handle || p.WeaponID != in.weapon.ID {
			continue
		}
		// Only the observer's own/allied fire can justify withholding its shot.
		if observer.Owner != p.ShooterSide && (s.Reaction == nil || s.Reaction.AlliedHook() == nil || !s.Reaction.AlliedHook()(observer.Owner, p.ShooterSide)) {
			continue
		}
		if modernBeamWeapon(in.weapon) && target.Def != nil && target.Def.BMCode != 0 {
			if in.motion != beamMotion(target) {
				in.beamInvalid = true
			}
			if in.beamInvalid {
				continue
			}
		}
		arrival := s.reliableETA(p, in.weapon, target, w, terrain, tick)
		if arrival == 0 || arrival > eta {
			continue
		}
		damage += s.effectiveDamage(in.weapon, target, in.shooter)
	}
	return damage
}

// proposedShotWindow compares a new direct shot with existing promises. A
// longer or unproved new trajectory cannot outrun a proved incoming kill within
// the short horizon. It creates no promise of its own until actually launched.
func (s *Service) proposedShotWindow(p Projectile, weapon *content.WeaponDef, target *units.Unit, w *units.World, terrain *world.Terrain, tick uint32) int {
	if !modernReliableWeapon(weapon) || target == nil || target.Def == nil {
		return 0
	}
	beam := modernBeamWeapon(weapon) && modernBeamTarget(target)
	if target.Def.BMCode != 0 && !beam {
		return 0
	}
	if eta := s.reliableETA(p, weapon, target, w, terrain, tick); eta > 0 {
		return eta
	}
	if beam {
		return modernBeamHorizon
	}
	return modernIncomingHorizon
}

func (s *Service) candidateCoverage(q *TargetQuery, target *units.Unit) (int64, int64) {
	if !modernReliableWeapon(q.Slot.Weapon) {
		return 0, 0
	}
	p := Projectile{Shooter: q.Shooter.Handle, ShooterSide: q.Shooter.Owner}
	origin := Vec3{X: q.Shooter.X, Y: q.Shooter.Y, Z: q.Shooter.Z}
	aim := Vec3{X: target.X, Y: target.Y, Z: target.Z}
	InitOrdinary(&p, q.Slot.Weapon, s.modernTick, origin, aim, target.Handle)
	eta := s.proposedShotWindow(p, q.Slot.Weapon, target, q.World, q.Terrain, s.modernTick)
	if eta == 0 {
		return 0, 0
	}
	return s.incomingDamage(q.Shooter, target, q.World, q.Terrain, s.modernTick, eta), s.effectiveDamage(q.Slot.Weapon, target, q.Shooter)
}

func (s *Service) allied(a, b uint8, econ *economy.Service) bool {
	return a == b || registryOwnerDeclaresAllianceWithCandidate(a, b, econ)
}

// threatScore examines at most three enemy slots. A weapon must actually be
// capable of damaging this unit or its currently targeted nearby ally. A
// shooter that cannot move defends what lies beyond it, so for it a weapon that
// could engage it from somewhere also counts while out of range. There is no
// building-name table, or whole-world scan inside the candidate loop.
func (s *Service) threatScore(q *TargetQuery, target *units.Unit) int64 {
	score := int64(1)
	if target.Stunned || target.Remaining != 0 {
		return score
	}
	for idx := 0; idx < NumSlots; idx++ {
		slot := target.SlotAt(idx)
		if slot == nil || !slot.IsEnabled() || slot.Weapon == nil || slot.Weapon.Interceptor || slot.Weapon.CommandFire {
			continue
		}
		weapon := slot.Weapon
		if weapon.Stockpile && slot.Ammo <= 0 {
			continue
		}
		victim := q.Shooter
		active := false
		if slot.Target.Kind == units.TargetUnit {
			ally := q.World.Unit(slot.Target.Unit)
			if ally != nil && ally.Def != nil && s.allied(q.Shooter.Owner, ally.Owner, q.Economy) && WithinRange(q.Shooter.X, q.Shooter.Z, ally.X, ally.Z, modernAllyRadius) && s.CanEngageSlotTarget(target, ally, idx, q.Terrain) {
				victim = ally
				active = true
			}
		}
		if !s.CanEngageSlotTarget(target, victim, idx, q.Terrain) && (q.Shooter.Def == nil || q.Shooter.Def.CanMove || !s.modernWeaponCouldEngage(target, victim, weapon, q.Terrain)) {
			continue
		}
		damage := s.effectiveDamage(weapon, victim, target)
		if damage <= 0 || (weapon.Paralyzer && victim.Def.ImmuneToParalyzer) {
			continue
		}
		reload := int64(weapon.ReloadTime)
		if reload < 1 {
			reload = 1
		}
		strength := damage * 300 / reload
		if strength > 10000 {
			strength = 10000
		}
		value := int64(1000) + strength
		if active {
			value += 1000
		}
		if value > score {
			score = value
		}
	}
	return score
}

// modernWeaponCouldEngage is the unit-to-unit acquisition gate without its
// range clause: the weapon's medium and height admit this victim, wherever
// the two units now stand.
func (s *Service) modernWeaponCouldEngage(u, victim *units.Unit, weapon *content.WeaponDef, terrain *world.Terrain) bool {
	if u == nil || u.Def == nil || victim == nil || !victim.Alive || victim.Def == nil {
		return false
	}
	sea := int32(0)
	if terrain != nil {
		sea = int32(terrain.SeaLevel)
	}
	return s.rules().AdmitTarget(TargetAdmission{Service: s, Weapon: weapon, Shooter: gateEndForUnit(u), Target: gateEndForUnit(victim),
		Sea: sea, WaterWeapon: weapon.WaterWeapon, ToAir: weapon.ToAirWeapon})
}

// modernKillScale weights a target's threat by how soon this slot removes it:
// the whole shots its health needs, after the reliable incoming fire already
// promised, times the slot's reload. A threat that dies sooner stops firing
// sooner, and the shots this slot has landed bring its own target closer.
// Counting whole shots leaves targets one shot apart to the overage
// preference.
func modernKillScale(score, damage int64, weapon *content.WeaponDef, target *units.Unit, incoming int64) int64 {
	remaining := int64(target.Health) - incoming
	if remaining < 1 {
		remaining = 1
	}
	if damage < 1 {
		damage = 1
	}
	reload := int64(weapon.ReloadTime)
	if reload < 1 {
		reload = 1
	}
	shots := (remaining + damage - 1) / damage
	return score * 64 * 300 / (shots * reload)
}

// modernHitFactor is the share, out of 1024, of an unguided projectile's hits
// that a moving target leaves it: 32 / (32 + the distance the target travels
// during the shot's flight at its current speed). Beam and tracking weapons
// keep every hit. The shape follows hit rates measured over Modern arena
// games (DESIGN_WEAPONS_PROJECTILES "Modern threat targeting").
func modernHitFactor(weapon *content.WeaponDef, shooter, target *units.Unit) int64 {
	if weapon.BeamWeapon || weapon.Tracks || weapon.WeaponVelocity <= 0 {
		return 1024
	}
	dx, dz := int64(shooter.X.Int()-target.X.Int()), int64(shooter.Z.Int()-target.Z.Int())
	speed := int64(target.Move.Speed.Raw())
	if speed < 0 {
		speed = -speed
	}
	// Both speeds are 16.16 per tick, so their ratio times the distance is
	// the target's travel in world units.
	travel := speed * numeric.ISqrt64(dx*dx+dz*dz) / int64(weapon.WeaponVelocity)
	return 1024 * 32 / (32 + travel)
}

// modernTargetObstructed applies the existing Modern physical previews to an
// automatic direct-fire candidate. Use the retained muzzle identity and the
// default target-piece centre without running Query* or SweetSpot callbacks.
// Only a constant SweetSpot output cell supplies a query-free scripted point;
// any other callback or projectile family keeps its ordinary rank. Actual
// launch admission remains authoritative (DESIGN_WEAPONS_PROJECTILES
// "Modern threat targeting and incoming fire").
func (s *Service) modernTargetObstructed(q *TargetQuery, target *units.Unit) bool {
	weapon := q.Slot.Weapon
	if q.Terrain == nil || liveCreationFamilyForWeapon(weapon) != CreationOrdinary || MotionFamilyForWeapon(weapon) != MotionDirect {
		return false
	}
	muzzle := Vec3{X: q.Shooter.X, Y: q.Shooter.Y, Z: q.Shooter.Z}
	if q.Shooter.COBBinding() != nil {
		var ok bool
		muzzle, ok = muzzleWorldPosResolved(q.Shooter, q.Slot.MuzzlePiece)
		if !ok {
			return false
		}
	}
	aim := Vec3{X: target.X, Y: target.Y, Z: target.Z}
	if binding := target.COBBinding(); binding != nil {
		piece, known := modernConstantSweetSpotPiece(binding.Program)
		if !known {
			return false
		}
		if _, scripted := binding.Program.Scripts["SweetSpot"]; scripted {
			// A full pool or missing callback owner leaves the Q query's zero
			// seed untouched [04 R-COB-01 §1]. The literal is therefore not a
			// usable current aim point unless this exact VM can allocate it.
			if binding.Callbacks == nil || binding.VM == nil || binding.Callbacks.VM != binding.VM || binding.VM.Program() != binding.Program {
				return false
			}
			free := false
			for idx := range binding.VM.Threads {
				if binding.VM.Threads[idx].Status == cob.ThreadIdle {
					free = true
					break
				}
			}
			if !free {
				return false
			}
		}
		centre, resolved := pieceVertexBoxCentre(binding, piece)
		if !resolved {
			return false
		}
		aim.X, aim.Y, aim.Z = aim.X.Add(centre[0]), aim.Y.Add(centre[1]), aim.Z.Add(centre[2])
	}
	aim = PreFireLeadPoint(q.Shooter, target, q.Slot, weapon, aim, s)
	q.obstruction = ShotQuery{Service: s, World: q.World, Shooter: q.Shooter, Target: target,
		Launch: Slot{Weapon: weapon, Flags: units.SlotFlagAutonomous, Target: Target{Kind: TargetUnit, Unit: target.Handle}},
		Muzzle: muzzle, Aim: aim, Tick: s.modernTick, Terrain: q.Terrain, Wind: s.ProjectileWind}
	return modernObstructedShot(&q.obstruction) ||
		modernTerrainAdmission(q.obstruction.Launch, muzzle, aim, s.modernTick, q.Terrain, target, s.ProjectileWind) == terrainShotBlocked
}

// modernConstantSweetSpotPiece recognizes only a literal query-cell assignment:
// optional one-local allocation, constant push, store to local zero, constant
// push and return [04 §4.3]. A Q callback returns its four argument cells, not
// the RETURN scalar [04 R-COB-01 §1]. Nothing before that assignment may have
// side effects, branches or state-dependent reads; all other bodies are unknown.
// This is a bounded immutable-content inspection, never another VM execution.
func modernConstantSweetSpotPiece(program *cob.Program) (int32, bool) {
	if program == nil {
		return 0, false
	}
	pc, exists := program.Scripts["SweetSpot"]
	if !exists {
		return 0, true // the ordinary query's zero seed [06 R-WPN-03 §6]
	}
	code := program.Code
	if pc < 0 || pc >= len(code) {
		return 0, false
	}
	if code[pc] == 0x10022000 { // allocate-local [04 §4.3]
		pc++
	}
	if len(code)-pc < 7 || code[pc] != 0x10021001 || // push constant
		code[pc+2] != 0x10023002 || code[pc+3] != 0 || // store local zero
		code[pc+4] != 0x10021001 || code[pc+6] != 0x10065000 { // constant RETURN
		return 0, false
	}
	return int32(code[pc+1]), true
}

func (*ModernRules) SelectTarget(s *Service, q *TargetQuery) (pool.Handle, bool) {
	if q == nil || q.Shooter == nil || q.Slot == nil || q.World == nil {
		return 0, false
	}
	// Modern keeps fire-at-will automatic acquisition, while order requests
	// with an explicit released slot retain their caller's existing stance gate.
	//
	// Modern replaces the sampling and scoring of [06 §3.2], not the medium,
	// range and trajectory gates ("retains the existing medium, range and
	// trajectory gates", docs/DESIGN_WEAPONS_PROJECTILES.md "Modern threat
	// targeting and incoming fire"). The physical gate below therefore compares
	// against the slot weapon's authored range under both rule sets, and the
	// sight-distance caller's `nochasecategory` rejection holds here too — the
	// same mask Modern's own danger response already honors before it proposes
	// an attack. No Modern policy claims the no-chase category; a Modern unit
	// that chased aircraft its Strict twin ignores would be an accident, not a
	// departure.
	//
	// A slot with NO active weapon is outside this policy altogether. The
	// approved Modern threat targeting is scoped to an armed slot — it ranks by
	// the damage "the weapon can cause" — so it has nothing to say about the
	// weaponless kamikaze search that carries a stock mine or crawling bomb
	// [04 R-SPEC-01 §1]. Modern therefore delegates that query to the strict
	// sampled selection, unchanged, rather than duplicating or approximating
	// it; the boundary is recorded in docs/DESIGN_WEAPONS_PROJECTILES.md
	// "Modern threat targeting and incoming fire". This adds no departure: the
	// scan is a detonation TRIGGER, not a weapon aim, and a deterministic
	// ranker feeding `Standby_Mine`'s grounded-target post-check could starve
	// a mine that a sampled search would have fired.
	if q.Slot.Weapon == nil {
		return StrictRules{}.SelectTarget(s, q)
	}

	var best, retained pool.Handle
	var bestScore, retainedScore int64
	var bestDistance int64
	var bestOverage, retainedOverage bool
	var bestBlocked, retainedBlocked bool
	var bestCovered, retainedCovered bool
	air := s.airTargetContext(q)
	consider := func(c Candidate, extra bool) {
		target := q.World.Unit(c.Handle)
		if target == nil || target.Def == nil || !c.Hostile || s.allied(q.Shooter.Owner, target.Owner, q.Economy) ||
			(!extra && (!q.Acquisition.admits(c) || q.Acquisition.rejectsNoChase(c) || q.Acquisition.rejectsStunned(c))) ||
			s.effectiveDamage(q.Slot.Weapon, target, q.Shooter) <= 0 || (q.Slot.Weapon.Paralyzer && target.Def.ImmuneToParalyzer) {
			return
		}
		// A registry may be thirty ticks old. Recheck present contact knowledge;
		// never acquire or retain a vanished contact merely because it scored well.
		status := perspectiveStatus(q.Visibility, q.Shooter.Owner, target)
		visible := q.Visibility != nil && q.Visibility.IsVisible(visibility.PlayerID(q.Shooter.Owner), visibilityTarget(target, status))
		if q.Visibility == nil && s.VisibilityHook() != nil {
			visible = s.VisibilityHook()(visibility.PlayerID(q.Shooter.Owner), visibilityTarget(target, status))
		}
		// A secondary-list population exists only behind an open targeting
		// upgrade, and its membership test is the candidate's seen bit
		// [06 §3.1][04 R-SPEC-01 §8]. The recheck for it is that same bit,
		// read now: requiring direct sight here would silently cancel the
		// Targeting Facility under Modern (issue #36).
		if !visible && !(q.FromSecondary && status&visibility.SeenBit != 0) {
			return
		}
		incoming, shot := s.candidateCoverage(q, target)
		if target.Health > 0 && incoming >= int64(target.Health) {
			return
		}
		score := s.threatScore(q, target)
		// A visible active takeover capability is an additional local threat.
		// Keep category, incoming-fire and retention policy below unchanged.
		// Radar-only secondary contacts disclose no capability for this bonus.
		if visible && s.InfectionThreatHook() != nil && s.InfectionThreatHook()(target) {
			score += 12000
		}
		// An aircraft takes out what can shoot aircraft first, and what is
		// shooting them before that, so later passes are safer. Nanolathe
		// Modern policy: DESIGN_MOVEMENT_PATH "Modern aircraft tactics".
		if air.active {
			score += s.airScore(air, q, target, visible)
		}
		preferred := IsPreferredCategory(c.Category, q.Acquisition.BadMask)
		if q.Acquisition.MaskResolved && c.CategoryMaskResolved {
			preferred = IsPreferredCategoryMask(c.CategoryMask, q.Acquisition.BadTargetMask)
		}
		if preferred {
			score += 100
		}
		// Twenty percent is a preference only. Undercovered targets remain legal,
		// including a sole indivisible shot that would overshoot by far more.
		overage := target.Health > 0 && shot > 0 && (incoming+shot)*5 > int64(target.Health)*6
		if overage {
			score = score * 4 / 5
		}
		score = modernKillScale(score, s.effectiveDamage(q.Slot.Weapon, target, q.Shooter), q.Slot.Weapon, target, incoming)
		score = score * modernHitFactor(q.Slot.Weapon, q.Shooter, target) / 1024
		// An aircraft looking for work ranks a target other aircraft of its
		// side are already expected to destroy below every other candidate.
		covered := air.sight && airCovered(air.claims, target)
		dx, dz := q.Shooter.X.Int()-target.X.Int(), q.Shooter.Z.Int()-target.Z.Int()
		distance := int64(dx)*int64(dx) + int64(dz)*int64(dz)
		blocked := s.modernTargetObstructed(q, target)
		if best == 0 || (bestBlocked && !blocked) || (bestBlocked == blocked && ((bestCovered && !covered) || (bestCovered == covered &&
			(score > bestScore || (score == bestScore && (distance < bestDistance || (distance == bestDistance && c.Handle < best))))))) {
			best, bestScore, bestDistance = c.Handle, score, distance
			bestOverage = overage
			bestBlocked = blocked
			bestCovered = covered
		}
		if q.Slot.Target.Kind == units.TargetUnit && q.Slot.Target.Unit == c.Handle {
			retained, retainedScore = c.Handle, score
			retainedOverage = overage
			retainedBlocked = blocked
			retainedCovered = covered
		}
	}
	for _, c := range q.Candidates {
		consider(c, false)
	}
	if air.sight && !q.FromSecondary {
		s.airReachScratch = s.airThreatReach(q, air.friends, s.airReachScratch)
		for _, c := range s.airReachScratch {
			consider(c, true)
		}
	}
	// A 25% improvement is required to leave a still-eligible automatic target.
	// Orders can request automatic selection while owning a nonautonomous slot.
	// Explicit bindings remain protected by the maintenance scan admission;
	// this query returns a choice without installing it.
	// Avoid cancelling the 20% overage preference with the 25% retention
	// margin: an otherwise preferred non-overage alternative may take over.
	// A physically obstructed target likewise cannot pin a clear alternative;
	// when both are blocked, the ordinary retention margin still prevents churn.
	if retained != 0 && retainedBlocked == bestBlocked && retainedCovered == bestCovered && !(retainedOverage && !bestOverage) && bestScore*4 <= retainedScore*5 {
		best = retained
	}
	if air.sight && bestCovered {
		// Everything in reach is already expected to be destroyed by other
		// aircraft of the side: keep looking rather than pile on.
		best = 0
	}
	if air.sight && best != 0 {
		// Claim the choice at once: wingmen scanning on this same tick must
		// see it before this aircraft's pilot first runs.
		s.AirBoard.Claim(q.Shooter, q.World.Unit(best), s.modernTick)
	}
	return best, best != 0
}

func (*ModernRules) AutonomousSlot(w *content.WeaponDef, _ uint8) bool {
	return w != nil && !w.Dropped && !w.CommandFire
}
