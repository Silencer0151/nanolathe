package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// The unit data column has three tabs. Each builds typed rows from the
// immutable definition alone; the native list only scrolls them
// (DESIGN_DEVELOPER_TOOLS §7). Every figure is a base value before mutators.
const (
	unitViewerTabStats = iota
	unitViewerTabWeapons
	unitViewerTabBuild
)

// unitViewerTabs names each tab's control; a lookup, never ranged.
var unitViewerTabs = map[string]int{"TABSTATS": unitViewerTabStats, "TABWEAPONS": unitViewerTabWeapons, "TABBUILD": unitViewerTabBuild}

type unitViewerRowKind uint8

const (
	unitViewerRowBlank    unitViewerRowKind = iota
	unitViewerRowHeading                    // Label: title; Value: right caption
	unitViewerRowText                       // Label: one wrapped body line
	unitViewerRowNote                       // Label: one wrapped dim line
	unitViewerRowPair                       // Label, Value and dimmed Unit
	unitViewerRowCard                       // Label: weapon name; Value: slot
	unitViewerRowTags                       // Tags: one wrapped line of chips
	unitViewerRowLink                       // Link with Value detail; 2 rows tall
	unitViewerRowLinkTail                   // the second row of the link above
)

// Every stored row is ready to draw Verbatim. Its builders translate UI text
// before layout and retain authored text explicitly (DESIGN_I18N).
type unitViewerRow struct {
	Kind               unitViewerRowKind
	Label, Value, Unit string
	Tags               []string
	Link               *content.UnitDef
	Modern, Card       bool
	Wide               bool // a unitless value that uses the unit column too
}

const (
	unitViewerNoteSize  = 12
	unitViewerTagSize   = 11
	unitViewerUnitSize  = 11
	unitViewerUnitWidth = 48 // the dimmed unit column right of each value
	unitViewerTagPad    = 6
	unitViewerTagGap    = 5
)

// unitViewerRows wraps content to the measured column width as it is added,
// so paint and the native list's row count agree.
type unitViewerRows struct {
	rows  []unitViewerRow
	width float64
	card  bool
}

func (b *unitViewerRows) add(r unitViewerRow) {
	r.Card = b.card && r.Kind != unitViewerRowHeading
	b.rows = append(b.rows, r)
}

func (b *unitViewerRows) heading(title, caption string) {
	if len(b.rows) > 0 {
		b.add(unitViewerRow{Kind: unitViewerRowBlank})
	}
	b.card = false
	b.add(unitViewerRow{Kind: unitViewerRowHeading, Label: i18n.T(title), Value: i18n.T(caption)})
}

func (b *unitViewerRows) wrapped(kind unitViewerRowKind, text string, size float64) {
	for _, line := range unitViewerWrapLines(text, screenkit.Style{Size: size}, false, b.width) {
		b.add(unitViewerRow{Kind: kind, Label: line})
	}
}

func (b *unitViewerRows) text(s string) { b.wrapped(unitViewerRowText, i18n.T(s), unitViewerBodySize) }
func (b *unitViewerRows) note(s string) { b.wrapped(unitViewerRowNote, i18n.T(s), unitViewerNoteSize) }

// pair keeps label and value on one row while both fit; otherwise the value
// wraps on its own rows below the label.
func (b *unitViewerRows) pair(label, value, unit string) {
	// Pair values are numbers or authored side/movement keys. Only the UI
	// label and unit of measurement are translated.
	label, unit = i18n.T(label), i18n.T(unit)
	body := screenkit.Style{Size: unitViewerBodySize, Verbatim: true}
	need := unitViewerMeasure(label, body, false) + unitViewerMeasure(value, body, false) + 12
	if need <= b.width-unitViewerUnitWidth {
		b.add(unitViewerRow{Kind: unitViewerRowPair, Label: label, Value: value, Unit: unit})
		return
	}
	if unit == "" && need <= b.width {
		b.add(unitViewerRow{Kind: unitViewerRowPair, Label: label, Value: value, Wide: true})
		return
	}
	b.add(unitViewerRow{Kind: unitViewerRowPair, Label: label})
	b.wrapped(unitViewerRowText, strings.TrimSpace(value+" "+unit), unitViewerBodySize)
}

func (b *unitViewerRows) tags(tags []string) {
	var line []string
	used := 0.0
	for _, tag := range tags {
		tag = i18n.T(tag)
		w := unitViewerTagWidth(tag)
		if len(line) > 0 && used+unitViewerTagGap+w > b.width {
			b.add(unitViewerRow{Kind: unitViewerRowTags, Tags: line})
			line, used = nil, 0
		}
		if len(line) > 0 {
			used += unitViewerTagGap
		}
		line, used = append(line, tag), used+w
	}
	if len(line) > 0 {
		b.add(unitViewerRow{Kind: unitViewerRowTags, Tags: line})
	}
}

func unitViewerTagWidth(tag string) float64 {
	return unitViewerMeasure(tag, screenkit.Style{Size: unitViewerTagSize, Tracking: 0.04, Upper: true, Verbatim: true}, false) + 2*unitViewerTagPad
}

func (b *unitViewerRows) link(def *content.UnitDef, detail string, modern bool) {
	b.add(unitViewerRow{Kind: unitViewerRowLink, Link: def, Value: i18n.T(detail), Modern: modern})
	b.add(unitViewerRow{Kind: unitViewerRowLinkTail, Link: def, Modern: modern})
}

func unitViewerNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// unitViewerSplitInfo separates a unit-information string such as "12.0 m/s"
// into the value and the unit column [07 R-HUD-03 §8].
func unitViewerSplitInfo(s string) (string, string) {
	value, unit, _ := strings.Cut(s, " ")
	return value, unit
}

// unitViewerStatsRows is the Stats tab. Costs and the mobile statistics reuse
// the unit-information conversions [07 R-HUD-03 §8]; buildtime is authored
// work, not seconds [05 "Construction arithmetic"]; ranges stay in world units
// [02 "Unit record"]. Economy rates are per settlement pass, which runs every
// thirty ticks, so they are per second at normal speed [05 "Authoritative
// settlement order"]. Zero and inapplicable rows are omitted.
func unitViewerStatsRows(def *content.UnitDef, width float64) []unitViewerRow {
	b := &unitViewerRows{width: width}
	if def == nil {
		return nil
	}
	b.heading("Overview", "")
	if d := strings.TrimSpace(def.Description); d != "" {
		b.wrapped(unitViewerRowText, d, unitViewerBodySize)
	}
	if def.DiscoveryOnly {
		// The secondary parse never populated the gameplay fields; their
		// allocation zeros are not authored statistics [02 R-CAT-01 §5].
		b.note("Statistics unavailable: this record was discovered but never received its gameplay parse.")
		return b.rows
	}
	if def.Side != "" {
		b.pair("Side", def.Side, "")
	}
	b.pair("Health", fmt.Sprint(def.MaxDamage), "")
	if def.FootprintX > 0 || def.FootprintZ > 0 {
		b.pair("Footprint", fmt.Sprintf("%d x %d", def.FootprintX, def.FootprintZ), "cells")
	}
	if tags := unitViewerUnitTags(def); len(tags) > 0 {
		b.tags(tags)
	}

	info := unitInfoValues(def)
	b.heading("Economy", "")
	for i, label := range [3]string{"Energy cost", "Metal cost", "Build work"} {
		if info[1+i] != "0" {
			b.pair(label, info[1+i], "")
		}
	}
	if def.Builder && def.WorkerTime != 0 {
		b.pair("Worker time", fmt.Sprint(uint16(def.WorkerTime)), "")
	}
	rate := func(label string, v float64) {
		if v != 0 {
			b.pair(label, unitViewerNumber(v), "/s")
		}
	}
	factor := func(label string, v float64) {
		if v != 0 {
			b.pair(label, unitViewerNumber(v), "")
		}
	}
	rate("Energy make", def.EnergyMake)
	rate("Energy use", def.EnergyUse)
	rate("Metal make", def.MetalMake)
	if def.MakesMetal != 0 {
		rate("Metal maker", float64(uint8(def.MakesMetal)))
	}
	factor("Metal extraction", def.ExtractsMetal)
	factor("Wind generator", def.WindGenerator)
	factor("Tidal generator", def.TidalGenerator)
	factor("Energy storage", def.EnergyStorage)
	factor("Metal storage", def.MetalStorage)
	factor("Cloak cost", float64(def.CloakCost))
	if def.CloakCost != 0 || def.CloakCostMoving != 0 {
		factor("Cloak cost moving", float64(def.CloakCostMoving))
	}

	if def.BMCode != 0 {
		b.heading("Mobility", "")
		for i, label := range [3]string{"Speed", "Acceleration", "Turn rate"} {
			value, unit := unitViewerSplitInfo(info[5+i])
			b.pair(label, value, unit)
		}
		if def.MovementClass != "" {
			b.pair("Movement class", strings.ToUpper(def.MovementClass), "")
		}
		if def.CanFly && def.CruiseAlt != 0 {
			b.pair("Cruise altitude", fmt.Sprint(def.CruiseAlt), "wu")
		}
		if def.TransportCapacity != 0 {
			b.pair("Transport capacity", fmt.Sprint(def.TransportCapacity), "")
		}
	}

	sensors := []struct {
		label string
		value int32
	}{
		{"Sight", def.SightDistance}, {"Radar", def.RadarDistance}, {"Sonar", def.SonarDistance},
		{"Radar jamming", def.RadarDistanceJam}, {"Sonar jamming", def.SonarDistanceJam},
	}
	headed := false
	for _, sensor := range sensors {
		if sensor.value == 0 {
			continue
		}
		if !headed {
			b.heading("Sensors", "")
			headed = true
		}
		b.pair(sensor.label, fmt.Sprint(sensor.value), "wu")
	}
	return b.rows
}

// unitViewerUnitTags names authored capability flags without interpreting
// them [02 "Unit record"].
func unitViewerUnitTags(def *content.UnitDef) []string {
	var tags []string
	add := func(on bool, tag string) {
		if on {
			tags = append(tags, tag)
		}
	}
	add(def.Commander, "commander")
	add(def.BMCode == 0, "structure")
	add(def.Builder, "builder")
	add(def.CanFly, "aircraft")
	add(def.CanHover, "hover")
	add(def.Amphibious, "amphibious")
	add(def.Floater, "floater")
	add(def.Stealth, "stealth")
	add(def.OnOffable, "on/off")
	add(def.CanReclamate, "reclaim")
	add(def.CanResurrect, "resurrect")
	add(def.CanCapture, "capture")
	add(def.IsAirBase, "air base")
	add(def.Kamikaze, "kamikaze")
	add(def.ImmuneToParalyzer, "paralyzer immune")
	return tags
}

// unitViewerWeaponRows is the Weapons tab: one card per active weapon, then
// the two death explosions.
func unitViewerWeaponRows(def *content.UnitDef, width float64) []unitViewerRow {
	b := &unitViewerRows{width: width}
	if def == nil {
		return nil
	}
	if def.DiscoveryOnly {
		b.heading("Weapons", "")
		b.note("Weapons unavailable: this record never received its gameplay parse.")
		return b.rows
	}
	b.heading("Weapons", "")
	armed, nominal := false, false
	for i, w := range [3]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
		if content.IsWeaponInactive(w) {
			continue
		}
		if armed {
			b.add(unitViewerRow{Kind: unitViewerRowBlank})
		}
		armed = true
		nominal = unitViewerWeaponCard(b, w, fmt.Sprintf("Weapon %d", i+1), true) || nominal
	}
	if !armed {
		b.text("No active weapons.")
	}
	headed := false
	for _, death := range []struct {
		slot string
		w    *content.WeaponDef
	}{{"On death", def.ExplodeAsDef}, {"Self-destruct", def.SelfDestructAsDef}} {
		if content.IsWeaponInactive(death.w) {
			continue
		}
		if !headed {
			b.heading("Death explosions", "")
			headed = true
		} else {
			b.add(unitViewerRow{Kind: unitViewerRowBlank})
		}
		unitViewerWeaponCard(b, death.w, death.slot, false)
	}
	b.card = false
	if armed {
		b.add(unitViewerRow{Kind: unitViewerRowBlank})
		b.note("Base reload = compiled ticks / 30. Health, veterancy and unit scripts change the real firing cadence.")
	}
	if nominal {
		b.note("Nominal DPS = default damage x max(burst, 1) / base reload. It ignores aiming, travel, damage overrides and blast falloff.")
	}
	return b.rows
}

// unitViewerWeaponCard adds one weapon. The record already holds tick counts
// and 16.16-per-tick velocity [02 "Weapon record"]; the authored area of
// effect is a diameter whose halved word is the blast radius [06 §9.3], and
// edge effectiveness is the falloff multiplier approached at that radius.
// It reports whether a nominal DPS row was shown.
func unitViewerWeaponCard(b *unitViewerRows, w *content.WeaponDef, slot string, firing bool) bool {
	name := strings.TrimSpace(w.Name)
	if name == "" {
		name = w.CanonicalKey
	}
	b.card = true
	b.add(unitViewerRow{Kind: unitViewerRowCard, Label: name, Value: i18n.T(slot)})
	b.pair("Damage", fmt.Sprint(w.DamageDefault), "")
	unitViewerDamageOverrides(b, w)
	if firing {
		b.pair("Base reload", fmt.Sprintf("%.2f", float64(uint16(w.ReloadTime))/unitInfoTicksPerSecond), "s")
		if w.Burst > 1 {
			b.pair("Burst", fmt.Sprint(w.Burst), "shots")
			b.pair("Burst interval", fmt.Sprintf("%.2f", float64(uint16(w.BurstRate))/unitInfoTicksPerSecond), "s")
		}
		b.pair("Range", fmt.Sprint(w.Range), "wu")
	}
	if radius := uint16(w.AreaOfEffect) >> 1; radius > 0 {
		b.pair("Blast radius", fmt.Sprint(radius), "wu")
		if w.EdgeEffectiveness != 0 {
			b.pair("Edge effectiveness", unitViewerNumber(w.EdgeEffectiveness), "")
		}
	}
	if !firing {
		b.card = false
		return false
	}
	if w.WeaponVelocity > 0 {
		b.pair("Velocity", fmt.Sprintf("%.0f", float64(w.WeaponVelocity)*unitInfoTicksPerSecond/65536), "wu/s")
	}
	if w.EnergyPerShot != 0 {
		b.pair("Energy per shot", unitViewerNumber(w.EnergyPerShot), "")
	}
	if w.MetalPerShot != 0 {
		b.pair("Metal per shot", unitViewerNumber(w.MetalPerShot), "")
	}
	dps, nominal := unitViewerNominalDPS(w)
	if nominal {
		b.pair("Nominal DPS", fmt.Sprintf("%.1f", dps), "")
	}
	if tags := unitViewerWeaponTags(w); len(tags) > 0 {
		b.tags(tags)
	}
	b.card = false
	return nominal
}

// unitViewerNominalDPS is a labelled presentation figure, not a prediction:
// default damage times the pellets one shot releases, divided by the base
// reload in seconds. An authored burst of N releases N pellets and zero
// releases the root itself [06 §4.3]. A stockpile launch writes no reload and
// command fire is manual [06 §4.2]; a paralyzer's figure is not damage. Those
// show no DPS.
//
// TODO(question): a dropped weapon's creator writes no burst count [06 §4.3],
// yet the burst-clone dispatcher has a dropped arm [06 §6.4], so whether a
// dropped burst releases N bombs is not settled. Tracing that dispatcher's
// dropped arm would settle it; until then a dropped weapon with burst > 1
// shows no DPS. No stock dropped weapon authors a burst.
func unitViewerNominalDPS(w *content.WeaponDef) (float64, bool) {
	if w.CommandFire || w.Stockpile || w.Paralyzer || w.DamageDefault <= 0 || w.Burst < 0 || uint16(w.ReloadTime) == 0 ||
		(w.Dropped && w.Burst > 1) {
		return 0, false
	}
	pellets := max(1, int64(w.Burst))
	return float64(int64(w.DamageDefault)*pellets) * unitInfoTicksPerSecond / float64(uint16(w.ReloadTime)), true
}

// unitViewerDamageOverrides lists per-unit damage entries whose effective
// value differs from the default, grouped by value. Keys are authored unit
// names, resolved first-match as the runtime lookup does [06 §9.2][06
// R-DMG-01 §1].
func unitViewerDamageOverrides(b *unitViewerRows, w *content.WeaponDef) {
	type group struct {
		value int32
		names []string
	}
	var groups []group
	var seen []string
	for _, key := range w.DamageKeysSorted() {
		fold := strings.ToLower(key)
		if fold == "default" || containsFold(seen, fold) {
			continue
		}
		seen = append(seen, fold)
		value, ok := w.DamageOverride(key)
		if !ok || value == w.DamageDefault {
			continue
		}
		i := -1
		for j := range groups {
			if groups[j].value == value {
				i = j
			}
		}
		if i < 0 {
			groups, i = append(groups, group{value: value}), len(groups)
		}
		groups[i].names = append(groups[i].names, strings.ToUpper(key))
	}
	for _, g := range groups {
		if len(g.names) == 1 {
			b.pair("vs "+g.names[0], fmt.Sprint(g.value), "")
			continue
		}
		b.pair(fmt.Sprintf("vs %d units", len(g.names)), fmt.Sprint(g.value), "")
		b.wrapped(unitViewerRowNote, strings.Join(g.names, ", "), unitViewerNoteSize)
	}
}

func containsFold(list []string, fold string) bool {
	for _, s := range list {
		if s == fold {
			return true
		}
	}
	return false
}

// unitViewerWeaponTags names retail weapon flags [02 "Weapon record"]. Only
// toairweapon is described by its acquisition effect, which is established:
// automatic targets must be airborne [06 §3.1]. Inert community keys are not
// shown as abilities.
func unitViewerWeaponTags(w *content.WeaponDef) []string {
	var tags []string
	add := func(on bool, tag string) {
		if on {
			tags = append(tags, tag)
		}
	}
	add(w.Turret, "turret")
	add(w.Guidance, "guided")
	add(w.Tracks, "tracking")
	add(w.Ballistic, "ballistic")
	add(w.LineOfSight, "line of sight")
	add(w.BeamWeapon, "beam")
	add(w.Dropped, "dropped")
	add(w.VLaunch, "vertical launch")
	add(w.Cruise, "cruise")
	add(w.Stockpile, "stockpile")
	add(w.CommandFire, "command fire")
	add(w.Paralyzer, "paralyzer")
	add(w.Interceptor, "interceptor")
	add(w.Targetable, "targetable")
	add(w.ToAirWeapon, "air targets only")
	add(w.WaterWeapon, "underwater")
	add(w.UnitsOnly, "units only")
	add(w.NoExplode, "no explode")
	add(w.BurnBlow, "burn blow")
	return tags
}

// buildRows is the Build tab: this unit's build-menu products and every
// builder whose list contains it, each with the unhindered construction work
// time at normal speed (construction.WorkTicks / 30).
func (s *toolsScreen) buildRows(def *content.UnitDef, width float64) []unitViewerRow {
	b := &unitViewerRows{width: width}
	if def == nil {
		return nil
	}
	if s.tree.hidden[def] {
		b.heading("Build tree", "")
		b.note("Another definition with the same unit name hides this record; battles build and use that one instead.")
		return b.rows
	}
	modern := false
	if def.Builder {
		links := s.tree.builds[def]
		b.heading(fmt.Sprintf("Builds (%d)", len(links)), "work at 1x")
		if len(links) == 0 {
			b.note("This builder's build menu lists no products.")
		}
		for _, l := range links {
			b.link(l.Def, s.workText(def, l.Def), l.Modern)
			modern = modern || l.Modern
		}
	}
	links := s.tree.builtBy[def]
	b.heading(fmt.Sprintf("Built by (%d)", len(links)), "work at 1x")
	if len(links) == 0 {
		b.note("No builder's build menu lists this unit.")
	}
	for _, l := range links {
		b.link(l.Def, s.workText(l.Def, def), l.Modern)
		modern = modern || l.Modern
	}
	b.add(unitViewerRow{Kind: unitViewerRowBlank})
	b.note("Work times count construction steps only, at normal speed. They exclude travel, factory opening, build-stance waits and resource stalls.")
	if modern {
		b.note("MODERN entries come from the complete authored build membership that Modern rules offer; the Strict 3.1 list omits them.")
	}
	return b.rows
}

// workText formats construction.WorkTicks as seconds at 30 ticks per second.
// A zero quantum, a malformed buildtime or the viewer bound never becomes a
// guessed number.
func (s *toolsScreen) workText(builder, product *content.UnitDef) string {
	switch {
	case builder == nil || product == nil || builder.DiscoveryOnly || product.DiscoveryOnly:
		return "n/a"
	case construction.WorkerQuantum(builder.WorkerTime) == 0:
		return "no work"
	case product.BuildTime <= 0:
		return "n/a"
	}
	ticks, ok := s.workTicks(builder, product)
	if !ok {
		return "over 24 h"
	}
	return unitViewerDuration(ticks)
}

// unitViewerDuration truncates to the shown precision throughout.
func unitViewerDuration(ticks int) string {
	if ticks < 100*unitViewerTickRate {
		tenths := ticks * 10 / unitViewerTickRate
		return fmt.Sprintf("%d.%d s", tenths/10, tenths%10)
	}
	whole := ticks / unitViewerTickRate
	if whole < 3600 {
		return fmt.Sprintf("%d:%02d min", whole/60, whole%60)
	}
	return fmt.Sprintf("%d:%02d:%02d h", whole/3600, whole/60%60, whole%60)
}
