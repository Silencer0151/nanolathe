package film

import "image"

// TypefaceGlyph is one glyph of a bundled face as the film rasterizer holds
// it: coverage images with a two-pixel transparent border, halving in size
// from level 0, plus the metrics that place it on a baseline. Every length is
// in face units; the face's Cap is the cap height in those units.
type TypefaceGlyph struct {
	// Levels are coverage mipmaps; level 0 is the atlas resolution.
	Levels []*image.Gray
	// Left and Top place level 0's inked box relative to the pen; Top is
	// negative above the baseline. The border is not part of these.
	Left, Top int
	W, H      int
	Advance   float64
	// Scale is how many face units one atlas pixel covers, for a face whose
	// glyphs are rasterized below the atlas resolution tools/film-font uses
	// (the CJK additions, docs/DESIGN_I18N.md). Zero means one, the value
	// every Latin glyph carries implicitly.
	Scale float64
}

// Typeface is a read-only view of one bundled face for text drawn outside a
// film: the Nanolathe screen sets its type in the same OFL faces the film
// titles use, so no second copy of the atlases ships.
type Typeface struct {
	Cap     float64
	Glyphs  map[rune]TypefaceGlyph
	kerning map[string]float64
}

// Kern is the pair adjustment between two runes, in face units.
func (t *Typeface) Kern(previous, next rune) float64 {
	return t.kerning[string([]rune{previous, next})]
}

// FilmTypeface returns the "display" or "body" face. Glyph images are shared
// with the film rasterizer and must not be modified.
func FilmTypeface(name string) *Typeface {
	face := textFace(name)
	out := &Typeface{Cap: face.Cap, Glyphs: make(map[rune]TypefaceGlyph, len(face.Glyphs)), kerning: face.Kerning}
	for key, g := range face.Glyphs {
		runes := []rune(key)
		if len(runes) != 1 {
			continue
		}
		out.Glyphs[runes[0]] = TypefaceGlyph{Levels: g.levels, Left: g.Left, Top: g.Top, W: g.W, H: g.H, Advance: g.Advance, Scale: g.Scale}
	}
	return out
}
