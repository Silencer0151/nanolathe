package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/film"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// Info rows keep authored strings even when a catalog has an exact or pattern
// match, while the surrounding labels are stored in their final language.
func TestUnitViewerInfoRowsPreserveAuthoredCollisions(t *testing.T) {
	previous := i18n.Language()
	t.Cleanup(func() { _ = i18n.SetLanguage(previous) })
	film.EnableCJK()
	if err := i18n.SetLanguage("chinese"); err != nil {
		t.Fatal(err)
	}
	for _, description := range []string{"Radar", "Downloading Radar"} {
		def := &content.UnitDef{Description: description, Side: "Radar", BMCode: 1, MovementClass: "ON"}
		rows := unitViewerStatsRows(def, 2000)
		if rows[0].Label != i18n.T("Overview") || rows[1].Label != description {
			t.Fatalf("description %q: heading or authored description changed: %+v", description, rows[:2])
		}
		for _, pair := range [][2]string{{"Side", "Radar"}, {"Movement class", "ON"}} {
			found := false
			for _, row := range rows {
				if row.Kind == unitViewerRowPair && row.Label == i18n.T(pair[0]) {
					found = row.Value == pair[1]
				}
			}
			if !found {
				t.Fatalf("authored %s %q did not survive: %+v", pair[0], pair[1], rows)
			}
		}
		b := &unitViewerRows{width: 2000}
		unitViewerWeaponCard(b, &content.WeaponDef{Name: description}, "Weapon 1", false)
		if b.rows[0].Label != description || b.rows[0].Value != i18n.T("Weapon 1") {
			t.Fatalf("weapon card translated authored text or retained English UI: %+v", b.rows[0])
		}
		body := screenkit.Style{Size: unitViewerBodySize, Verbatim: true}
		b = &unitViewerRows{width: unitViewerMeasure(description, body, false) + 1}
		b.pair("Side", description, "")
		if len(b.rows) != 2 || b.rows[1].Label != description {
			t.Fatalf("wrapped authored value %q changed: %+v", description, b.rows)
		}
	}
}

// The complete UI note is translated before it becomes retained list rows.
// Unspaced Chinese wraps without losing bytes or measuring English fragments.
func TestUnitViewerInfoWrapsTranslatedNoteBeforeStorage(t *testing.T) {
	previous := i18n.Language()
	t.Cleanup(func() { _ = i18n.SetLanguage(previous) })
	film.EnableCJK()
	if err := i18n.SetLanguage("chinese"); err != nil {
		t.Fatal(err)
	}
	const text = "Statistics unavailable: this record was discovered but never received its gameplay parse."
	b := &unitViewerRows{width: 70}
	b.note(text)
	var lines []string
	for _, row := range b.rows {
		if !utf8.ValidString(row.Label) {
			t.Fatalf("wrapped row has partial UTF-8: %q", row.Label)
		}
		if unitViewerMeasure(row.Label, screenkit.Style{Size: unitViewerNoteSize, Verbatim: true}, false) > b.width {
			t.Fatalf("wrapped row exceeds its measured column: %q", row.Label)
		}
		lines = append(lines, row.Label)
	}
	if len(lines) < 2 || strings.Join(lines, "") != i18n.T(text) {
		t.Fatalf("stored note %q differs from the wrapped translation %q", lines, i18n.T(text))
	}
}

// An already-open viewer can outlive an optional language download. Its next
// paint adopts the new language once, without rebuilding rows on every draw.
func TestUnitViewerRetainedInfoFollowsLanguage(t *testing.T) {
	previous := i18n.Language()
	t.Cleanup(func() { _ = i18n.SetLanguage(previous) })
	if err := i18n.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	s := unitViewerUIFixture()
	s.art = &nlArt{}
	s.preparePaint()
	film.EnableCJK()
	if err := i18n.SetLanguage("chinese"); err != nil {
		t.Fatal(err)
	}
	s.preparePaint()
	if s.infoLanguage != "chinese" || s.infoRows[0].Label != i18n.T("Overview") {
		t.Fatalf("retained info language %q, heading %q", s.infoLanguage, s.infoRows[0].Label)
	}
	row := &s.infoRows[0]
	s.preparePaint()
	if &s.infoRows[0] != row {
		t.Fatal("unchanged language rebuilt the retained rows")
	}
}
