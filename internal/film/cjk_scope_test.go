package film

import "testing"

// The base atlases carry Latin and Cyrillic only: a build that never selects
// Chinese must not hold a Han glyph, or every player pays for the atlas.
func TestBaseFaceHasNoHanGlyphs(t *testing.T) {
	face := textFace("display")
	if _, r := face.glyph('选'); r != ' ' {
		t.Fatalf("the base face resolves 选 to %q, want the space fallback", r)
	}
}

// EnableCJK merges the Chinese atlases into both faces and leaves the base
// entries alone, so Latin text keeps the glyphs and metrics it always had.
func TestCJKFaceAddsHanGlyphs(t *testing.T) {
	latin := textFace("display").Glyphs["A"]
	EnableCJK()
	merged := textFace("display")
	g, r := merged.glyph('选')
	if r != '选' {
		t.Fatalf("选 resolves to %q after EnableCJK", r)
	}
	if g.units() < 1.5 {
		t.Fatalf("选 units = %v, want the rasterized-below-atlas scale", g.units())
	}
	if len(g.levels) == 0 {
		t.Fatal("选 has no coverage mipmaps")
	}
	if merged.Glyphs["A"] != latin {
		t.Fatal("the merged face replaced a Latin glyph instead of keeping the base entry")
	}
}
