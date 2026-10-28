package main

import (
	"image"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/film"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/vfs"

	"github.com/nanolathe-gg/nanolathe/internal/drawlist"
	"github.com/nanolathe-gg/nanolathe/internal/palette"
)

// These muted display-bay colors are Nanolathe decoration, resolved through
// the loaded palette so replacement content retains its own color space.
func unitViewerColor(pal *palette.Tables, r, g, b int) byte {
	if pal == nil {
		return 0
	}
	best, distance := byte(0), math.MaxInt
	for i, c := range pal.Base {
		dr, dg, db := r-int(c[0]), g-int(c[1]), b-int(c[2])
		d := dr*dr + dg*dg + db*db
		if d < distance {
			best, distance = byte(i), d
		}
	}
	return best
}

// A fixed drafting grid gives the viewer depth without implying terrain or a
// battle shadow. It belongs to the display, and never rotates with the unit.
func unitViewerBackdrop(list *drawlist.List, pal *palette.Tables, w, h int) {
	background := unitViewerColor(pal, 10, 14, 12)
	grid := unitViewerColor(pal, 18, 25, 20)
	accent := unitViewerColor(pal, 33, 47, 35)
	list.RecordFill(drawlist.Fill{Rect: drawlist.Rect{W: int32(w), H: int32(h)}, Index: background})
	line := func(x0, y0, x1, y1 float64, color byte) {
		list.RecordLine(drawlist.Line{X0: int32(x0), Y0: int32(y0), X1: int32(x1), Y1: int32(y1), Index: color})
	}
	cx, cy, step := float64(w)/2, float64(h)/2, float64(h)/8
	for i := -12; i <= 12; i++ {
		x, y := cx+float64(i)*step, cy+float64(i)*step
		if x >= 0 && x < float64(w) {
			line(x, 0, x, float64(h-1), grid)
		}
		if y >= 0 && y < float64(h) {
			line(0, y, float64(w-1), y, grid)
		}
	}
	for i := range 96 {
		a, b := float64(i)*2*math.Pi/96, float64(i+1)*2*math.Pi/96
		r := float64(h) * 0.40
		line(cx+r*math.Cos(a), cy+r*math.Sin(a), cx+r*math.Cos(b), cy+r*math.Sin(b), accent)
	}
	// Short corner marks keep the frame legible without another heavy bevel.
	margin, arm := float64(h)*0.045, float64(h)*0.055
	for _, x := range []float64{margin, float64(w) - margin} {
		for _, y := range []float64{margin, float64(h) - margin} {
			dx, dy := arm, arm
			if x > cx {
				dx = -arm
			}
			if y > cy {
				dy = -arm
			}
			line(x, y, x+dx, y, accent)
			line(x, y, x, y+dy, accent)
		}
	}
}

const (
	unitViewerBodySize   = 14
	unitViewerTextMetric = 18
	unitViewerTextInset  = 10
)

func (s *toolsScreen) layout(w, h float64) {
	s.scale = math.Min(w/unitViewerWidth, h/unitViewerHeight)
	s.canvas = screenkit.Rect{X: (w - unitViewerWidth*s.scale) / 2, Y: (h - unitViewerHeight*s.scale) / 2, W: unitViewerWidth * s.scale, H: unitViewerHeight * s.scale}
}

func unitViewerRect(r gui.Rect) screenkit.Rect {
	return screenkit.Rect{X: float64(r.X), Y: float64(r.Y), W: float64(r.W), H: float64(r.H)}
}

func (s *toolsScreen) deviceRect(r screenkit.Rect) screenkit.Rect {
	return screenkit.Rect{X: s.canvas.X + r.X*s.scale, Y: s.canvas.Y + r.Y*s.scale, W: r.W * s.scale, H: r.H * s.scale}
}

func (s *toolsScreen) clip(dst *ebiten.Image, r screenkit.Rect) *ebiten.Image {
	d := s.deviceRect(r)
	b := image.Rect(int(math.Ceil(d.X)), int(math.Ceil(d.Y)), int(math.Floor(d.X+d.W)), int(math.Floor(d.Y+d.H))).Intersect(dst.Bounds())
	if b.Empty() {
		return nil
	}
	return dst.SubImage(b).(*ebiten.Image)
}

// Film and screenkit share these typeface metrics. Measuring on the CPU keeps
// editor admission and wrapping identical before and after the graphics device
// exists; the fonts themselves are uploaded lazily by Draw.
func unitViewerMeasure(text string, style screenkit.Style, display bool) float64 {
	font := "body"
	if display {
		font = "display"
	}
	if !style.Verbatim {
		text = i18n.T(text)
	}
	text = unitViewerFontText(text)
	if style.Upper {
		text = strings.ToUpper(text)
	}
	return film.MeasureText(text, film.TextStyle{Font: font, Size: style.Size, Tracking: style.Tracking})
}

func unitViewerTextWidth(text string) int {
	return int(math.Ceil(unitViewerMeasure(text, screenkit.Style{Size: unitViewerBodySize, Verbatim: true}, false)))
}

// Like the native wrapper, prefer spaces and split an overlong word when it
// has none. Walk rune boundaries so Chinese and authored Unicode keep every
// character. Text has its final spelling before layout (DESIGN_I18N).
func unitViewerWrapLines(text string, style screenkit.Style, display bool, width float64) []string {
	style.Verbatim = true
	measure := func(s string) float64 {
		return unitViewerMeasure(s, style, display)
	}
	if text == "" || width <= 0 {
		return []string{text}
	}
	var lines []string
	for text != "" {
		if measure(text) <= width {
			lines = append(lines, text)
			break
		}
		cut := -1
		for i, r := range text {
			if r == ' ' {
				if measure(text[:i]) > width {
					break
				}
				cut = i
			}
		}
		if cut <= 0 {
			cut = 0
			for i := range text {
				_, size := utf8.DecodeRuneInString(text[i:])
				end := i + size
				if cut > 0 && measure(text[:end]) > width {
					break
				}
				cut = end
			}
			lines = append(lines, text[:cut])
			text = text[cut:]
			continue
		}
		lines = append(lines, text[:cut])
		text = strings.TrimLeft(text[cut:], " ")
	}
	return lines
}

func unitViewerFontText(text string) string {
	// Match screenkit's readable substitutions in both measurement and paint.
	text = strings.ReplaceAll(text, "·", "-")
	return strings.ReplaceAll(text, "→", ">")
}

func unitViewerFitText(text string, width float64, style screenkit.Style, display bool) string {
	if !style.Verbatim {
		text = i18n.T(text)
		style.Verbatim = true
	}
	if unitViewerMeasure(text, style, display) <= width {
		return text
	}
	r := []rune(text)
	for len(r) > 0 && unitViewerMeasure(string(r)+"...", style, display) > width {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

func (s *toolsScreen) label(dst *ebiten.Image, text string, r screenkit.Rect, style screenkit.Style, display bool) {
	target := s.clip(dst, r)
	if target == nil || text == "" {
		return
	}
	// Fit translates the whole UI label before measuring it. Authored text,
	// player input and already-translated info rows explicitly ask for Verbatim.
	text = unitViewerFitText(text, r.W, style, display)
	font := s.fonts.Body
	if display {
		font = s.fonts.Display
	}
	x := r.X
	switch style.Align {
	case 1:
		x += r.W / 2
	case 2:
		x += r.W
	}
	y := r.Y + style.Size
	style.Size *= s.scale
	// The label was translated and fitted above, so it draws what
	// unitViewerMeasure measured.
	style.Verbatim = true
	font.Draw(target, unitViewerFontText(text), s.canvas.X+x*s.scale, s.canvas.Y+y*s.scale, style)
}

// The settings art is borrowed from the base install. Close this temporary
// mount after decoding: the detached images and GAF records retain no archive
// reader, so closing the viewer has no content-lifetime work to postpone.
func (s *toolsScreen) preparePaint() {
	s.fonts = s.fonts.Refresh()
	if s.infoLanguage != i18n.Language() {
		s.refreshInfo()
	}
	if s.art != nil {
		return
	}
	var fs vfs.FSOps
	if s.cs != nil {
		fs = s.cs.fs
		if len(s.cs.baseRoots) > 0 {
			base := vfs.New()
			defer base.Close()
			if base.MountGameDirectories(s.cs.baseRoots) == nil {
				fs = base
			}
		}
	}
	if fs != nil {
		s.art = loadNLArt(fs)
	} else {
		s.art = &nlArt{}
	}
}
