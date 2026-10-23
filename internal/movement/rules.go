package movement

import (
	"github.com/nanolathe-gg/nanolathe/internal/path"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Rules is the movement system's gameplay seam. It carries the learned-terrain
// policy, the contested-cell claim answer, the follower's re-route throttle,
// the group-order admission spread and the traffic policy. Strict keeps the
// retail blocked mover loop and owner-state claim rule; Community may select
// the stable lower-unit-index claim rule
// (docs/DESIGN_MOVEMENT_PATH.md "Community contested-cell claims") [04 R-MOV-01 §7][04 R-COLL-01 §3]
// (community-patch-engine.md CP-DMG-2).
//
// No other seam owns these decisions. They are asked by this package's own
// algorithms — the occupancy commit, rejected movement commit, follower poll,
// request open and empty publication — about state this package owns, and
// none is an order decision (orders still decides which records may finish
// as unreachable moves), construction decision or choice of search kernel; the kernel still opens the same retail
// search over the same passability port [docs/DESIGN_GAMEPLAY_RULES.md §9 step 3].
//
// The implementation is chosen when the session binds a rule set. ClaimConflict
// is asked per contested cell; the learned-terrain questions are asked once per
// rejected commit and once per opened search, never per expanded node;
// RepathDelay is asked only for an armed follower's staging test and poll;
// FirstRequestSpread once per scheduler call and per staged first request;
// UnreachableMoves once per cannot-get-there publication and, while a
// certificate is live, per follower visit; WedgeEscape at most once per ground
// visit whose proposal fails a static cell test, and once per opened search;
// PocketRelease once per cannot-get-there publication and, while a pocket
// certificate is live, per follower visit; Traffic once a tick.
// Every implementation is a zero-size value or a pointer to one, so dispatch
// allocates nothing; the learned grid, the unreachable-move and
// pocket-release certificates and everything the traffic policy remembers
// belong to the System.
type Rules interface {
	// RepairPadQueue reserves landing pieces for approaching aircraft and
	// keeps other patients circling the base until a piece becomes free.
	// Nanolathe Modern policy: DESIGN_MOVEMENT_PATH "Modern repair-pad queue".
	// The answer is pure; the System owns the queue, never the rule object.
	RepairPadQueue(*System) bool

	// AirTactics flies the Modern pilots of air_tactics.go for the bombing,
	// strafing and hovering attack executors and the constructor-side repair
	// landing. Nanolathe Modern policy: DESIGN_MOVEMENT_PATH "Modern aircraft
	// tactics". The answer is pure; pilot state belongs to the System.
	AirTactics(*System) bool

	// AirPatrolRecovery keeps construction-patrol legs flyable when a retained
	// leg resumes grounded or names an off-map waypoint. Nanolathe Modern
	// policy: DESIGN_MOVEMENT_PATH "Modern construction aircraft patrol recovery".
	// The answer is pure; recovery uses the existing mode setter and marker.
	AirPatrolRecovery(*System) bool

	// ClaimConflict reports whether claimant displaces incumbent from one
	// contested occupancy cell. ArbitrateOverlap asks it for every ground,
	// air and building/yard stamp, including a restamp after a host vacates.
	// It may inspect the System's projected feature table and the incumbent's
	// owner state, but must not mutate either unit or draw RNG; the grid owns
	// the host/intruder effects after this answer.
	ClaimConflict(s *System, incumbent, claimant int) bool

	// StaticRejection is called for a ground mover's rejected commit when the
	// first failing footprint cell failed the static ground test — terrain or
	// a blocking feature — rather than the occupant test [04 R-COLL-01 §2].
	// anchor is the proposed footprint anchor and footX, footZ the footprint
	// the route search reads passability with. It reports whether the mover's
	// owner learned something it did not know.
	//
	// An implementation may write only the System's learned grid. It must not
	// write a transform, occupancy, the visibility grids or resources, and
	// must not draw RNG.
	StaticRejection(s *System, u *units.Unit, anchor Cell, footX, footZ int16) bool

	// LearnedTerrain is called once per opened route search and returns the
	// learned grid that search consults behind the mapping word, or nil for
	// the retail read [04 R-PATH-01 §2].
	LearnedTerrain(s *System) *LearnedTerrain

	// RepathDelay is the follower's re-route throttle: an armed follower whose
	// last admitted poll was at tick last may be admitted again once
	// last+delay <= the current tick [04 R-MOV-01 §7]. slot is the unit's
	// stable pool index. It is asked only for an armed follower, by the
	// follower's staging test and by the scheduler's poll, through the same
	// due test. It may read the committed movement state s keeps — Modern
	// reads the follower's collision record and route and the record of the
	// unit that refused its last step — and must write nothing and draw no
	// RNG. Staging reads that state before the unit's own step and the poll
	// after it, so a request one stages may wait for its poll, or be staged
	// on the next visit.
	RepathDelay(s *System, slot int, last uint32) uint32

	// FirstRequestSpread is the group-order admission spread: when at least
	// minGroup of one player's first route requests — requests staged while
	// the follower's admission stamp is zero, which is what an order's goal
	// installation leaves — come due on one scheduler call, the System admits
	// them over ticks consecutive ticks, nearest goal first, each tick
	// carrying about an equal share of the group's summed goal distance
	// (group_spread.go). minGroup zero disables it and nothing is recorded.
	// It is asked once per scheduler call and once per first-request
	// staging, never per poll, and must be a pure answer: no writes, no RNG.
	FirstRequestSpread(s *System) (minGroup, ticks int)

	// PathWorkBound is the scheduler's bounded path work: carryShares > 0
	// caps each player's carried search work at that many per-call shares,
	// and sweepStop ends a player's polling for the call once a whole sweep
	// of its units has admitted nothing (docs/DESIGN_MOVEMENT_PATH.md
	// "Modern bounded path work"). (0, false) is retail's unbounded carry
	// [04 R-PATH-01 §6]. It is asked once per scheduler call and must be a
	// pure answer: no writes, no RNG.
	PathWorkBound(s *System) (carryShares int32, sweepStop bool)

	// PathBatch is the scheduler's parallel path batches: searches > 0 lets
	// the path scheduler latch up to that many admitted searches at once and
	// expand them concurrently, each at most pops heap pops per scheduler
	// call, charging and publishing in admission order
	// (docs/DESIGN_MOVEMENT_PATH.md "Modern parallel path batches"). (0, 0)
	// is retail's single-request scheduler [04 R-PATH-01 §6]. It is asked
	// once per scheduler call and must be a pure answer: no writes, no RNG,
	// and nothing of the host — the answer may not depend on how many
	// processors run the batch.
	PathBatch(s *System) (searches int, pops int32)

	// PathOpenList is the open list every route search the System opens
	// keeps: path.OpenListHeap is retail's binary heap and its tie order
	// [04 R-PATH-01 §1]; path.OpenListBuckets is the Modern bucket queue
	// (docs/DESIGN_MOVEMENT_PATH.md "Modern bucket open list"). It is asked
	// once per opened search and must be a pure answer: no writes, no RNG.
	PathOpenList(s *System) path.OpenList

	// GroupDestinationSlots reports whether an ordinary group move gives each
	// ground actor its own destination footprint: outliers keep their bearing
	// clamped to the formation cutoff instead of sharing the clicked point,
	// and a shared or blocked goal moves to the nearest free footprint
	// (DESIGN_INTERFACE_HUD_INPUT "Modern group destination slots"). It is
	// asked once per group command and must be a pure answer.
	GroupDestinationSlots(s *System) bool

	// AlliedPassThrough reports whether two ground movers of the same or
	// mutually allied owners, meeting head-on while both are mid-route, may
	// pass through each other's footprints (DESIGN_MOVEMENT_PATH "Modern
	// allied pass-through"). No reserved rule set answers true: Modern
	// retired the policy for steering, and OverlapRules keeps it for the
	// pathfinding laboratory. It is asked once per ground mover visit and
	// must be a pure answer: no writes, no RNG.
	AlliedPassThrough(s *System) bool

	// UnreachableMoves is the unreachable-move completion: frontierCells > 0
	// lets an empty publication that raises the cannot-get-there bit on an
	// eligible terminal ground move certify the goal sealed when a static
	// re-run of the setup ray closes its loop with the unit within that many
	// cells of the walk's frontier, and the follower then completes the move
	// through the ordinary arrival once dwell ticks have passed since the
	// order's first certification and a closing probe agrees
	// (docs/DESIGN_MOVEMENT_PATH.md "Modern unreachable moves"). (0, 0) is
	// retail's endless retry [04 R-PATH-01 §7][04 R-ORD-01 §4]. It is asked
	// once per such empty publication and by the follower only while a
	// certificate is live, and must be a pure answer: no writes, no RNG.
	UnreachableMoves(s *System) (frontierCells int32, dwell uint32)

	// JamRelease is the jam release: jamAfter > 0 lets a ground mover that
	// friendly units have blocked for jamAfter consecutive ticks ignore
	// friendly ground occupants, other than same-way movers ahead of it, for
	// lifetime ticks, planning over the static view meanwhile
	// (docs/DESIGN_MOVEMENT_PATH.md "Modern jam release"). (0, 0) is
	// retail's occupant test [04 R-COLL-01 §1] and every reserved rule set's:
	// Modern retired the policy for steering, and OverlapRules keeps it for
	// the pathfinding laboratory. It is asked once per ground mover visit and
	// once per search opening, and must be a pure answer.
	JamRelease(s *System) (jamAfter uint16, lifetime uint32)

	// WedgeEscape reports whether a ground mover whose committed footprint
	// covers ground the commit's static test rejects — a wreck stamped over
	// it — may leave that ground: the commit's static test passes the cells
	// the committed footprint already covers, a search opened for the mover
	// reads those cells as passable for it alone, and a mover wedged under a
	// route planned elsewhere re-plans at the next scheduler call
	// (docs/DESIGN_MOVEMENT_PATH.md "Modern wedge escape"). false is retail's
	// validator, which tests every cell of the proposed footprint
	// [04 R-COLL-01 §2], and retail's search, which rejects a blocked start
	// without seeding [04 R-PATH-01 §4]. It is asked at most once per ground
	// mover visit, only after a proposed cell has failed the static test, and
	// once per opened search; it must be a pure answer: no writes, no RNG.
	WedgeEscape(s *System) bool

	// PocketRelease extends JamRelease to a unit sealed out of its own free
	// destination (docs/DESIGN_MOVEMENT_PATH.md "Modern pocket release"), and
	// like it is answered by no reserved rule set:
	// nearCells > 0 certifies, at the empty publication that raises the
	// cannot-get-there bit on an eligible terminal ground move, a goal within
	// nearCells per axis whose free footprint a bounded flood finds closed
	// off by parked friendly units and static ground, with the unit standing
	// against a parked friend; dwell ticks after the order's first
	// certificate a closing flood grants a jam release that takes the unit
	// through the ring into its slot, and after two such releases the move
	// finishes where the unit stands. (0, 0) is retail's retry for as long
	// as the record is the last primary one [04 R-ORD-01 §4]; the answer is
	// off whenever JamRelease is. It is asked once per such empty publication
	// and, while a certificate is live, per follower visit, and must be a
	// pure answer: no writes, no RNG.
	PocketRelease(s *System) (nearCells int32, dwell uint32)

	// Traffic is the traffic policy: how a ground mover behaves toward the
	// units around it while every unit keeps its own cells, how its routes
	// are searched, and where the units of a group come to rest (traffic.go;
	// docs/DESIGN_MOVEMENT_PATH.md "Modern traffic"). The zero value is
	// retail's blocked response and nothing else [04 R-COLL-01 §1]. It is
	// asked once a tick, before any unit is visited, and once per group
	// command; it must be a pure answer: no writes, no RNG.
	Traffic(s *System) Traffic
}

// StrictRules is the retail baseline: nothing is learned and nothing learned
// is read, so a search's inputs are exactly retail's. It is zero size, so
// holding it in a Rules never allocates.
type StrictRules struct{}

// CommunityRules is the reserved Community 3.9 layer. It embeds StrictRules so
// every unchanged answer remains retail's; Community movement contracts
// override only the questions they own without changing the other layers.
type CommunityRules struct{ StrictRules }

func (StrictRules) RepairPadQueue(*System) bool  { return false }
func (*ModernRules) RepairPadQueue(*System) bool { return true }

func (StrictRules) AirTactics(*System) bool  { return false }
func (*ModernRules) AirTactics(*System) bool { return true }

func (StrictRules) AirPatrolRecovery(*System) bool  { return false }
func (*ModernRules) AirPatrolRecovery(*System) bool { return true }

// ClaimConflict preserves retail's owner-state branch under Strict 3.1.
func (StrictRules) ClaimConflict(s *System, incumbent, _ int) bool {
	return s != nil && s.Grid != nil && s.Grid.displaceable(incumbent)
}

// ClaimConflict uses the Community patch's unsigned unit-index tie-break when
// the resolved profile enables it. A strictly lower claimant wins; equality
// therefore keeps a self re-claim unchanged. Profiles that disable the feature
// retain the Strict answer (community-patch-engine.md CP-DMG-2).
func (CommunityRules) ClaimConflict(s *System, incumbent, claimant int) bool {
	if s == nil || !s.Community.GridClaimTieBreak {
		return (StrictRules{}).ClaimConflict(s, incumbent, claimant)
	}
	return uint16(claimant) < uint16(incumbent)
}

// StaticRejection does nothing under Strict 3.1: retail records nothing about
// the cell that rejected a proposal [04 R-MOV-01 §7].
func (StrictRules) StaticRejection(*System, *units.Unit, Cell, int16, int16) bool { return false }

// LearnedTerrain is nil under Strict 3.1: the search reads the mapping word
// and nothing else [04 R-PATH-01 §2]. Knowledge a Modern session gathered
// before a switch is kept but not consulted.
func (StrictRules) LearnedTerrain(*System) *LearnedTerrain { return nil }

// RepathDelay is retail's inclusive 60-tick throttle under Strict 3.1
// [04 R-MOV-01 §7].
func (StrictRules) RepathDelay(*System, int, uint32) uint32 { return retailRepathDelay }

// FirstRequestSpread is off under Strict 3.1: every request whose throttle
// has elapsed is admissible on the tick it comes due [04 R-MOV-01 §7].
func (StrictRules) FirstRequestSpread(*System) (int, int) { return 0, 0 }

// PathWorkBound is retail's accounting under Strict 3.1: unspent work carries
// forward without bound and polling continues until it is spent
// [04 R-PATH-01 §6].
func (StrictRules) PathWorkBound(*System) (int32, bool) { return 0, false }

// PathBatch is retail's scheduler under Strict 3.1: one search at a time,
// run until it publishes before the next request is polled
// [04 R-PATH-01 §6]. Community inherits it.
func (StrictRules) PathBatch(*System) (int, int32) { return 0, 0 }

// PathOpenList is retail's binary heap under Strict 3.1 [04 R-PATH-01 §1].
// Community inherits it.
func (StrictRules) PathOpenList(*System) path.OpenList { return path.OpenListHeap }

// GroupDestinationSlots is off under Strict 3.1: actors keep retail's
// centroid offsets and outliers share the clicked point [04 R-STANCE-01 §5].
func (StrictRules) GroupDestinationSlots(*System) bool { return false }

// AlliedPassThrough is off under Strict 3.1: every occupied footprint cell
// rejects a proposal [04 R-COLL-01 §2]. Community and Modern inherit the
// answer.
func (StrictRules) AlliedPassThrough(*System) bool { return false }

// UnreachableMoves is off under Strict 3.1: an empty publication raises the
// cannot-get-there bit and the order's own retry is the whole response
// [04 R-PATH-01 §7][04 R-ORD-01 §4].
func (StrictRules) UnreachableMoves(*System) (int32, uint32) { return 0, 0 }

// JamRelease is off under Strict 3.1: every friendly occupant blocks the
// commit and every search reads the occupancy layer [04 R-COLL-01 §1].
// Community and Modern inherit the answer.
func (StrictRules) JamRelease(*System) (uint16, uint32) { return 0, 0 }

// WedgeEscape is off under Strict 3.1: the validator tests every cell of the
// proposed footprint, including cells the mover already covers, and a search
// whose start anchor the class layer walls is rejected at setup
// [04 R-COLL-01 §2][04 R-PATH-01 §4].
func (StrictRules) WedgeEscape(*System) bool { return false }

// retailRepathDelay is the follower poll's throttle period [04 R-MOV-01 §7].
const retailRepathDelay = 60

// modernRepathDelay is Modern's throttle period: a mover that is blocked, or
// has come to the end of what its route told it, asks again after half a
// second. It is Nanolathe Modern policy tuning
// (docs/DESIGN_MOVEMENT_PATH.md "Modern prompt re-routing").
const modernRepathDelay = 15

// RepathDelay lets a follower ask for a route again half a second after its
// last admission, and a held follower when the unit in its way would be
// walled, within retail's sixty (repath_held.go;
// docs/DESIGN_MOVEMENT_PATH.md "Modern prompt re-routing").
func (*ModernRules) RepathDelay(s *System, slot int, last uint32) uint32 {
	return s.modernRepathDelayFor(slot, last)
}

// modernSpreadMinGroup and modernSpreadTicks are the Modern group-order
// spread's threshold and width. They are Nanolathe Modern policy tuning
// (docs/DESIGN_MOVEMENT_PATH.md "Modern group-order spreading").
const (
	modernSpreadMinGroup = 16
	modernSpreadTicks    = 3
)

// modernCarryShares bounds a player's carried path work to four per-call
// shares. It is Nanolathe Modern policy tuning
// (docs/DESIGN_MOVEMENT_PATH.md "Modern bounded path work").
const modernCarryShares = 4

// PathWorkBound bounds carried search work and stops futile polling
// (docs/DESIGN_MOVEMENT_PATH.md "Modern bounded path work").
func (*ModernRules) PathWorkBound(*System) (int32, bool) { return modernCarryShares, true }

// modernPathBatch is the most searches a Modern path batch latches at once,
// and modernPathBatchPops the most heap pops one search expands in one
// scheduler call. They are Nanolathe Modern policy tuning, constants rather
// than anything of the host, so every machine computes the same batches
// (docs/DESIGN_MOVEMENT_PATH.md "Modern parallel path batches").
const (
	modernPathBatch     = 16
	modernPathBatchPops = 4096
)

// PathBatch runs Modern's route searches in parallel batches
// (docs/DESIGN_MOVEMENT_PATH.md "Modern parallel path batches").
func (*ModernRules) PathBatch(*System) (int, int32) { return modernPathBatch, modernPathBatchPops }

// PathOpenList keeps every route search's open list in a bucket queue
// (docs/DESIGN_MOVEMENT_PATH.md "Modern bucket open list").
func (*ModernRules) PathOpenList(*System) path.OpenList { return path.OpenListBuckets }

// GroupDestinationSlots gives each actor of an ordinary group move its own
// destination (DESIGN_INTERFACE_HUD_INPUT "Modern group destination slots").
func (*ModernRules) GroupDestinationSlots(*System) bool { return true }

// FirstRequestSpread admits a large same-tick group of first requests over
// three ticks (docs/DESIGN_MOVEMENT_PATH.md "Modern group-order spreading").
func (*ModernRules) FirstRequestSpread(*System) (int, int) {
	return modernSpreadMinGroup, modernSpreadTicks
}

// ModernRules carries the approved learned-terrain, prompt re-routing,
// group-order spreading, bounded path work, group destination slot,
// unreachable-move, wedge-escape, repair-pad queue and traffic policies.
// Friendly units never share cells under it: allied pass-through, jam release
// and pocket release are retired from Modern and answered as Strict answers
// them (OverlapRules). It is zero size and is held by pointer so a later set
// may embed it and override one answer.
type ModernRules struct{ CommunityRules }

// StaticRejection teaches the owner the mapping blocks the route search reads
// for the rejected footprint and still calls unexplored.
func (*ModernRules) StaticRejection(s *System, u *units.Unit, anchor Cell, footX, footZ int16) bool {
	return s.learnRejectedFootprint(u, anchor, footX, footZ)
}

// LearnedTerrain hands the search the System's learned grid; nil until the
// first lesson, which leaves the read retail's.
func (*ModernRules) LearnedTerrain(s *System) *LearnedTerrain {
	if s == nil {
		return nil
	}
	return s.learned
}

// strictRules is the shared Strict value, converted to the interface once at
// package initialisation so the default path cannot allocate.
var strictRules Rules = StrictRules{}

// rules returns the bound seam. An unset field is the retail baseline, which
// keeps fixtures and a system reconstructed by a restore on the retail path.
func (s *System) rules() Rules {
	if s == nil || s.Rules == nil {
		return strictRules
	}
	return s.Rules
}
