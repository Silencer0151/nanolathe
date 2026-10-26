// Package screenkit draws the Nanolathe screen: full-window, true-colour
// presentation that sits outside the retail 640×480 draw list
// (docs/DESIGN_INTERFACE_HUD_INPUT.md §3.17). It owns type set in the bundled
// OFL film faces, gradient and glow painting, TA-style frames and lamps, and
// pointer hit regions. Nothing here reaches the simulation.
package screenkit

import (
	"image"
	"image/color"
	"math"
	"strings"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/film"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
)

// Font is one bundled face uploaded as per-glyph mip chains.
type Font struct {
	face   *film.Typeface
	glyphs map[rune]*fontGlyph
}

type fontGlyph struct {
	levels  []*ebiten.Image
	left    float64
	top     float64
	advance float64
	// units is how many face units one atlas pixel covers. The Latin faces
	// store glyphs at the atlas resolution, so it is one; the CJK additions
	// are rasterized smaller and carry their own scale (internal/film).
	units float64
}

// Fonts are the two faces the screen sets: the condensed display face for
// headings and controls, and the body face for descriptions.
type Fonts struct {
	Display *Font
	Body    *Font
}

// LoadFonts uploads both faces. It must run on the game goroutine after the
// graphics device exists.
func LoadFonts() Fonts {
	return Fonts{Display: newFont(film.FilmTypeface("display")), Body: newFont(film.FilmTypeface("body"))}
}

func newFont(face *film.Typeface) *Font {
	f := &Font{face: face, glyphs: make(map[rune]*fontGlyph, len(face.Glyphs))}
	for r, g := range face.Glyphs {
		units := g.Scale
		if units == 0 {
			units = 1
		}
		fg := &fontGlyph{left: float64(g.Left), top: float64(g.Top), advance: g.Advance, units: units}
		for _, level := range g.Levels {
			fg.levels = append(fg.levels, coverageImage(level))
		}
		f.glyphs[r] = fg
	}
	return f
}

// coverageImage turns a coverage level into a white image whose alpha is the
// coverage, premultiplied as Ebitengine expects.
func coverageImage(src *image.Gray) *ebiten.Image {
	b := src.Bounds()
	rgba := image.NewRGBA(b)
	for i, c := range src.Pix {
		rgba.Pix[i*4+0] = c
		rgba.Pix[i*4+1] = c
		rgba.Pix[i*4+2] = c
		rgba.Pix[i*4+3] = c
	}
	return ebiten.NewImageFromImage(rgba)
}

func (f *Font) glyph(r rune) (*fontGlyph, rune) {
	if g, ok := f.glyphs[r]; ok {
		return g, r
	}
	switch r {
	case '·':
		if g, ok := f.glyphs['-']; ok {
			return g, '-'
		}
	case '→':
		if g, ok := f.glyphs['>']; ok {
			return g, '>'
		}
	}
	return f.glyphs[' '], ' '
}

// Style is one run of text. Size is the cap height in pixels; Tracking is
// extra advance per glyph as a fraction of Size.
type Style struct {
	Size     float64
	Tracking float64
	// Top and Bottom colour the glyphs from their top edge to the baseline;
	// a zero Bottom repeats Top.
	Top, Bottom color.RGBA
	// Shadow draws a dark copy offset down by this fraction of Size.
	Shadow float64
	// Align is 0 left, 1 centre, 2 right about the pen x.
	Align int
	// Upper renders the run in capitals.
	Upper bool
	// Verbatim sets the text as given. Otherwise a run is first put into the
	// selected interface language (internal/i18n): text the player
	// typed, and lines Wrap already translated, set it.
	Verbatim bool
}

// text is a run as it is set: translated unless verbatim, then in capitals
// when the style asks.
func (s Style) text(text string) string {
	if !s.Verbatim {
		text = i18n.T(text)
	}
	if s.Upper {
		text = strings.ToUpper(text)
	}
	return text
}

// Measure is the advance width of a run in pixels.
func (f *Font) Measure(text string, s Style) float64 {
	return f.measure(s.text(text), s)
}

// measure is the advance width of a run already in its final spelling.
func (f *Font) measure(text string, s Style) float64 {
	scale := s.Size / f.face.Cap
	w, prev := 0.0, rune(0)
	for _, r := range text {
		g, n := f.glyph(r)
		if prev != 0 {
			w += f.face.Kern(prev, n)*scale + s.Tracking*s.Size
		}
		w += g.advance * scale
		prev = n
	}
	return w
}

// Draw sets a run on its baseline at (x, y) and returns its width.
func (f *Font) Draw(dst *ebiten.Image, text string, x, y float64, s Style) float64 {
	text = s.text(text)
	w := f.measure(text, s)
	switch s.Align {
	case 1:
		x -= w / 2
	case 2:
		x -= w
	}
	if s.Shadow > 0 {
		shadow := s
		shadow.Top, shadow.Bottom, shadow.Shadow = color.RGBA{0, 0, 0, uint8(float64(s.Top.A) * 0.85)}, color.RGBA{}, 0
		shadow.Align, shadow.Upper = 0, false
		f.run(dst, text, x, y+s.Shadow*s.Size, shadow)
	}
	f.run(dst, text, x, y, s)
	return w
}

func (f *Font) run(dst *ebiten.Image, text string, x, y float64, s Style) {
	scale := s.Size / f.face.Cap
	top, bottom := s.Top, s.Bottom
	if bottom == (color.RGBA{}) {
		bottom = top
	}
	pen, prev := x, rune(0)
	for _, r := range text {
		g, n := f.glyph(r)
		if prev != 0 {
			pen += f.face.Kern(prev, n)*scale + s.Tracking*s.Size
		}
		prev = n
		if len(g.levels) == 0 {
			pen += g.advance * scale
			continue
		}
		// Pick the mip whose texel footprint is closest to one pixel from
		// above, so linear filtering never has to span more than two texels.
		// A level-0 texel covers units face units, so a glyph rasterized below
		// the atlas resolution starts from a larger footprint.
		u := g.units
		level := 0
		for scale*u*math.Pow(2, float64(level+1)) <= 1.0001 {
			level++
		}
		l := min(level, len(g.levels)-1)
		img := g.levels[l]
		levelScale := math.Pow(2, float64(l))
		b := img.Bounds()
		// Level images carry a two-texel border at level 0, halved per level,
		// so a border texel spans units face units and half of them per level.
		border := 2 * u / levelScale
		gx := pen + (g.left-border)*scale
		gy := y + (g.top-border)*scale
		gw := float64(b.Dx()) * levelScale * scale * u
		gh := float64(b.Dy()) * levelScale * scale * u
		// Colour runs from the cap line to the baseline, independent of each
		// glyph's own box, so a word shades as one piece.
		capTop := y - s.Size
		ct := lerpColor(top, bottom, clamp01((gy-capTop)/s.Size))
		cb := lerpColor(top, bottom, clamp01((gy+gh-capTop)/s.Size))
		drawQuad(dst, img, gx, gy, gw, gh, ct, ct, cb, cb)
		pen += g.advance * scale
	}
}

// Wrap breaks text into lines no wider than width. The text is translated
// whole, before it is broken, so the lines are in the interface language
// already: set them Verbatim.
func (f *Font) Wrap(text string, s Style, width float64) []string {
	if !s.Verbatim {
		text = i18n.T(text)
		s.Verbatim = true
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		lines = append(lines, f.wrapParagraph(para, s, width)...)
	}
	return lines
}

// wide reports whether a rune is set at full width, as Chinese and the other
// East Asian scripts are.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0x303E,
		r >= 0x3041 && r <= 0x33FF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xA000 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFF:
		return true
	}
	return false
}

// noBreakBefore reports the closing punctuation a line may not start with: a
// Chinese line breaks between characters, but never before these.
func noBreakBefore(r rune) bool {
	return strings.ContainsRune("。，、；：？！）〕〉》」』】’”·…％", r)
}

// noBreakAfter reports the opening punctuation a line may not end with.
func noBreakAfter(r rune) bool {
	return strings.ContainsRune("（〔〈《「『【‘“", r)
}

// wrapParagraph breaks one paragraph. Latin words stay whole; a run of wide
// characters may break between any two of them, which is how a Chinese line
// fills its column when it has no spaces to break at. A closing mark that
// touches the text before it travels with that text, and an opening mark
// with the text after it, so neither strands at a line's edge. Only marks
// written without a space are bound: Latin text breaks only at spaces, so its
// lines are those of a plain word wrap, " · " separators included.
func (f *Font) wrapParagraph(para string, s Style, width float64) []string {
	type token struct {
		text  string
		space bool // a space separated this token from the one before it
	}
	var tokens []token
	pending := false
	var word strings.Builder
	// glue binds the next token to the last one: that one opened a quote
	// or bracket the next token's text starts inside.
	glue := false
	push := func(t token) {
		r := []rune(t.text)
		single := len(r) == 1
		if len(tokens) > 0 && !t.space && (glue || single && noBreakBefore(r[0])) {
			tokens[len(tokens)-1].text += t.text
		} else {
			tokens = append(tokens, t)
		}
		glue = single && noBreakAfter(r[0])
	}
	flushWord := func() {
		if word.Len() > 0 {
			push(token{text: word.String(), space: pending})
			word.Reset()
			pending = false
		}
	}
	for _, r := range para {
		switch {
		case unicode.IsSpace(r):
			flushWord()
			pending = true
		case wide(r):
			flushWord()
			push(token{text: string(r), space: pending})
			pending = false
		default:
			word.WriteRune(r)
		}
	}
	flushWord()

	var lines []string
	line := ""
	for _, t := range tokens {
		joined := t.text
		if line != "" && t.space {
			joined = line + " " + t.text
		} else if line != "" {
			joined = line + t.text
		}
		if f.Measure(joined, s) <= width || line == "" {
			line = joined
			continue
		}
		lines = append(lines, line)
		line = t.text
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// DrawWrapped sets a paragraph from the top-left (x, top) and returns the
// height used.
func (f *Font) DrawWrapped(dst *ebiten.Image, text string, x, top, width, leading float64, s Style) float64 {
	lines := f.Wrap(text, s, width)
	s.Verbatim = true
	y := top + s.Size
	for _, l := range lines {
		f.Draw(dst, l, x, y, s)
		y += s.Size * leading
	}
	return float64(len(lines)) * s.Size * leading
}

func clamp01(v float64) float64 { return max(0, min(1, v)) }

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}
