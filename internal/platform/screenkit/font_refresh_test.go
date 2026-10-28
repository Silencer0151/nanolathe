package screenkit

import (
	"github.com/nanolathe-gg/nanolathe/internal/film"
	"testing"
)

// Retained screens must adopt the optional faces once and keep their uploaded
// images afterward (DESIGN_I18N). A base-only copy models an already-open UI.
func TestRetainedFontsAdoptChineseOnce(t *testing.T) {
	film.EnableCJK()
	base := film.FilmTypeface("body")
	delete(base.Glyphs, '选')
	old := newFont(base)
	fonts := Fonts{Display: old, Body: old}
	if _, r := fonts.Body.glyph('选'); r != ' ' {
		t.Fatal("fixture already has Chinese")
	}
	fonts = fonts.Refresh()
	if _, r := fonts.Body.glyph('选'); r != '选' {
		t.Fatal("retained font did not adopt the Chinese glyph")
	}
	again := fonts.Refresh()
	if again.Body != fonts.Body || again.Display != fonts.Display {
		t.Fatal("unchanged readiness uploaded the fonts again")
	}
}
