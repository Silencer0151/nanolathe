package community

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RepairRate configures the proportional repair helper. A disabled helper in
// a shipped table still carries its source multipliers (both one); the zero
// value is reserved for the retail identity.
type RepairRate struct {
	Enabled            bool `json:"enabled"`
	RepairMultiplier   int  `json:"repairMultiplier"`
	SelfHealMultiplier int  `json:"selfHealMultiplier"`
}

// Features is the complete Community 3.9 table resolved for one session. Its
// zero value is the retail identity used by Strict 3.1.
//
// Snap maxima are part of the value because the patch's per-profile maximum
// bounds the player-configured radius. They come only from a complete value
// — the mainline Table or a mod config's ParseFeatures, carried as an
// Overrides Base; a field-level override may change a radius but cannot
// change its evidence-backed cap (community-patch-engine.md CP-CON-6).
type Features struct {
	ConstructionKickout      bool `json:"constructionKickout"`
	GuardingBuildersHold     bool `json:"guardingBuildersHold"`
	PatrollingBuilderFilters bool `json:"patrollingBuilderFilters"`
	ReclaimToggleKeepsBuild  bool `json:"reclaimToggleKeepsBuild"`
	StructureRotation        bool `json:"structureRotation"`
	AreaDamageOverflow       bool `json:"areaDamageOverflow"`
	AreaDamageDedupCap       bool `json:"areaDamageDedupCap"`
	GridClaimTieBreak        bool `json:"gridClaimTieBreak"`
	TransportedExplosions    bool `json:"transportedExplosions"`
	BuildWeaponSlotGuard     bool `json:"buildWeaponSlotGuard"`
	AntinukeCircularCoverage bool `json:"antinukeCircularCoverage"`
	AlliedJammingIgnored     bool `json:"alliedJammingIgnored"`
	ResurrectionFinalization bool `json:"resurrectionFinalization"`
	WeaponTargetKeys         bool `json:"weaponTargetKeys"`
	Veterancy                bool `json:"veterancy"`
	SchemaUnits              bool `json:"schemaUnits"`
	AirCorpseFall            bool `json:"airCorpseFall"`
	ScriptPorts              bool `json:"scriptPorts"`
	MexSnap                  bool `json:"mexSnap"`
	WreckSnap                bool `json:"wreckSnap"`

	// The ProTA 4.8 package switches are historical behaviours of that
	// package's engine loader, not of any tdraw build profile
	// (research/extensions/prota-engine.md "AI and economy evidence audit").
	// The mainline table leaves them false, so only a mod config's
	// communityFeatures or a player override enables them
	// (DESIGN_COMMUNITY_PATCH §4.7). They are omitted from the canonical JSON
	// while false, so the mainline table keeps its established digest; an
	// enabled switch enters the digest by name.
	AIDifficultyIncome     bool `json:"aiDifficultyIncome,omitempty"`
	AIStockpileProducts    bool `json:"aiStockpileProducts,omitempty"`
	TargetLockRelease      bool `json:"targetLockRelease,omitempty"`
	AIApplianceEnergy      bool `json:"aiApplianceEnergy,omitempty"`
	AIBuilderStopThreshold bool `json:"aiBuilderStopThreshold,omitempty"`
	// The order, drawing and text patches of the same loader
	// (research/extensions/prota-engine.md "Shipped order, drawing, sound and
	// text patches"), under the same policy.
	WorkingWeaponsAutonomous bool `json:"workingWeaponsAutonomous,omitempty"`
	AttackSingleSlotTake     bool `json:"attackSingleSlotTake,omitempty"`
	MapFeatureOwnerEleven    bool `json:"mapFeatureOwnerEleven,omitempty"`
	ResurrectionTextFix      bool `json:"resurrectionTextFix,omitempty"`
	// Historical executable caller, selected by a mod config separately
	// from the repair helper's contribution and multiplier configuration.
	HealTimeBitmask bool `json:"healTimeBitmask,omitempty"`
	// TA: Escalation's patched executable swaps the difficulty name slots, so
	// a stored difficulty index of 0 plays Hard and 2 Easy, and its menus
	// spell the stages Hard|Medium|Easy to match
	// (research/extensions/taesc-engine.md "AI"). The index is still what a
	// setup and a save store; only its reading reverses (session.DifficultyTier).
	AIDifficultyReversed bool `json:"aiDifficultyReversed,omitempty"`
	// Zero leaves the reposition threshold unchanged; zero preserves the
	// existing retail/ProTA placement choice.
	AIBuilderPlacementLimit int `json:"aiBuilderPlacementLimit,omitempty"`

	RepairRate                RepairRate `json:"repairRate"`
	OffMapAircraftMarginTiles int        `json:"offMapAircraftMarginTiles"`
	ProjectileCapacity        int        `json:"projectileCapacity"`
	ExplosionCapacity         int        `json:"explosionCapacity"`
	DebrisCapacity            int        `json:"debrisCapacity"`
	// SfxLimit is CP-LIM-2's special-effects limit: the per-strip eviction
	// threshold of the ten effect strips, and ten times it the shared
	// strip-object pool. Zero is retail's 400 and 1000
	// (community-patch-engine.md CP-LIM-2; DESIGN_COMMUNITY_PATCH §4.1).
	SfxLimit           int `json:"sfxLimit"`
	PathStepAllowance  int `json:"pathStepAllowance"`
	UnitLimit          int `json:"unitLimit"`
	MexSnapRadius      int `json:"mexSnapRadius"`
	WreckSnapRadius    int `json:"wreckSnapRadius"`
	MexSnapRadiusMax   int `json:"mexSnapRadiusMax"`
	WreckSnapRadiusMax int `json:"wreckSnapRadiusMax"`
}

// Digest returns the SHA-256 digest of the canonical JSON representation of
// the resolved value. Features contains no maps or optional encodings, so the
// representation and digest are stable across calls.
func (f Features) Digest() string {
	b, _ := json.Marshal(f) // Features contains only JSON's primitive value kinds.
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ParseFeatures reads a complete feature value as a mod config's
// `rules.communityFeatures` spells it (docs/DESIGN_MODS_MUTATORS.md §4.2):
// the Features JSON shape, decoded over the mainline table, so a field the
// document omits keeps its mainline value. Unknown keys are refused, every
// value must lie within the feature bounds, and a snap radius may not exceed
// its own maximum, because a complete value is the one place a cap is
// declared and a silently clamped radius would describe a table nobody
// wrote. origin names the document for the diagnostic.
func ParseFeatures(data []byte, origin string) (Features, error) {
	f := mainline
	if err := decodeClosed(data, &f); err != nil {
		return Features{}, featureError("decode community features", origin, "JSON", "known lowerCamelCase feature fields", err)
	}
	if err := validateComplete(f); err != nil {
		return Features{}, featureError("decode community features", origin, "community feature table", "values within the documented feature bounds", err)
	}
	return f, nil
}

// validateComplete is validate plus the two rules a complete value owes:
// representable snap maxima, and radii within them.
func validateComplete(f Features) error {
	if err := validate(f); err != nil {
		return err
	}
	for _, snap := range []struct {
		name          string
		radius, limit int
	}{
		{"mexSnapRadius", f.MexSnapRadius, f.MexSnapRadiusMax},
		{"wreckSnapRadius", f.WreckSnapRadius, f.WreckSnapRadiusMax},
	} {
		if snap.limit < 0 {
			return fmt.Errorf("%sMax %d: expected a non-negative integer", snap.name, snap.limit)
		}
		if snap.radius > snap.limit {
			return fmt.Errorf("%s %d exceeds %sMax %d", snap.name, snap.radius, snap.name, snap.limit)
		}
	}
	return nil
}
