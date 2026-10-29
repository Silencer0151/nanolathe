package main

import (
	"math"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/film"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// A content name or query may be identical to an interface label or match a
// translation pattern. Its fit, caret and admission metrics stay authored.
func TestUnitViewerVerbatimTextIgnoresCatalogCollisions(t *testing.T) {
	previous := i18n.Language()
	t.Cleanup(func() { _ = i18n.SetLanguage(previous) })
	film.EnableCJK()
	if err := i18n.SetLanguage("chinese"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Radar", "Downloading Radar", "ON"} {
		if i18n.T(text) == text {
			t.Fatalf("fixture %q does not collide with the catalog", text)
		}
		for _, display := range []bool{false, true} {
			style := screenkit.Style{Size: unitViewerBodySize, Verbatim: true}
			face := "body"
			if display {
				face = "display"
			}
			want := film.MeasureText(text, film.TextStyle{Font: face, Size: style.Size})
			if got := unitViewerMeasure(text, style, display); got != want {
				t.Fatalf("%q display=%v: authored width %v, want %v", text, display, got, want)
			}
			if got := unitViewerFitText(text, want+1, style, display); got != text {
				t.Fatalf("%q display=%v: authored fit became %q", text, display, got)
			}
		}
		want := int(math.Ceil(film.MeasureText(text, film.TextStyle{Font: "body", Size: unitViewerBodySize})))
		if got := unitViewerTextWidth(text); got != want {
			t.Fatalf("query %q: admission width %d, want %d", text, got, want)
		}
	}
}

// UI geometry measures the complete translation, and fitting elides that
// translation rather than an English fragment that no longer has a key.
func TestUnitViewerMeasuresAndFitsTranslatedUI(t *testing.T) {
	previous := i18n.Language()
	t.Cleanup(func() { _ = i18n.SetLanguage(previous) })
	film.EnableCJK()
	if err := i18n.SetLanguage("chinese"); err != nil {
		t.Fatal(err)
	}
	style := screenkit.Style{Size: 14, Upper: true}
	verbatim := style
	verbatim.Verbatim = true
	text := "Search name or ID"
	translated := i18n.T(text)
	width := unitViewerMeasure(translated, verbatim, true) / 2
	if got, want := unitViewerMeasure(text, style, true), unitViewerMeasure(translated, verbatim, true); got != want {
		t.Fatalf("UI width %v, want translated width %v", got, want)
	}
	got := unitViewerFitText(text, width, style, true)
	want := unitViewerFitText(translated, width, verbatim, true)
	if got != want || got == translated || unitViewerMeasure(got, verbatim, true) > width {
		t.Fatalf("UI fit %q, want translated fit %q within %v", got, want, width)
	}
}
