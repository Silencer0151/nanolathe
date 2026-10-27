package film

import (
	"bytes"
	"embed"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"sync/atomic"
)

// These are presentation assets, independent of the retail game's fonts.
// Provenance, reproduction instructions and OFL terms accompany the atlases.
//
//go:embed assets/*.png assets/*.json assets/OFL.txt assets/README.md
var fontAssets embed.FS

type atlasGlyph struct {
	X, Y, W, H int
	Left, Top  int
	Advance    float64
	// Scale is how many face units one atlas pixel covers; zero means one.
	// tools/film-font-cjk writes it for glyphs it rasterizes below the atlas
	// resolution the Latin faces use.
	Scale  float64
	levels []*image.Gray
}

// units is how many face units one atlas texel of the glyph covers.
func (g *atlasGlyph) units() float64 {
	if g.Scale == 0 {
		return 1
	}
	return g.Scale
}

type atlasFace struct {
	Cap     float64
	Glyphs  map[string]*atlasGlyph
	Kerning map[string]float64
}

// loadAtlasFace decodes one atlas pair into coverage mipmaps. name is the
// asset stem: "display" and "body" for the Latin faces, "display-cjk" and
// "body-cjk" for the optional Chinese additions.
func loadAtlasFace(name string) *atlasFace {
	metadata, err := fontAssets.ReadFile("assets/" + name + ".json")
	if err != nil {
		panic(err)
	}
	face := new(atlasFace)
	if err := json.Unmarshal(metadata, face); err != nil {
		panic(err)
	}
	data, err := fontAssets.ReadFile("assets/" + name + ".png")
	if err != nil {
		panic(err)
	}
	atlas, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	for _, g := range face.Glyphs {
		if g.W == 0 || g.H == 0 {
			continue
		}
		// A transparent border prevents neighbouring glyphs from bleeding
		// into the filtered edge. Mips preserve coverage in small captions.
		base := image.NewGray(image.Rect(0, 0, g.W+4, g.H+4))
		draw.Draw(base, image.Rect(2, 2, g.W+2, g.H+2), atlas, image.Pt(g.X, g.Y), draw.Src)
		g.levels = append(g.levels, base)
		for base.Bounds().Dx() > 8 && base.Bounds().Dy() > 8 {
			next := image.NewGray(image.Rect(0, 0, (base.Bounds().Dx()+1)/2, (base.Bounds().Dy()+1)/2))
			for y := 0; y < next.Bounds().Dy(); y++ {
				for x := 0; x < next.Bounds().Dx(); x++ {
					sum := 0
					for dy := 0; dy < 2; dy++ {
						for dx := 0; dx < 2; dx++ {
							sum += int(base.GrayAt(x*2+dx, y*2+dy).Y)
						}
					}
					next.Pix[y*next.Stride+x] = uint8((sum + 2) / 4)
				}
			}
			g.levels = append(g.levels, next)
			base = next
		}
	}
	return face
}

// filmFaces is the base pair every language loads: Latin and Cyrillic only.
var filmFaces = sync.OnceValue(func() map[string]*atlasFace {
	faces := make(map[string]*atlasFace, 2)
	for _, name := range []string{"display", "body"} {
		faces[name] = loadAtlasFace(name)
	}
	return faces
})

// cjkEnabled gates the optional Chinese face. It is off until a language that
// needs it is selected, so a battle or a menu in English or Ukrainian never
// decodes the CJK atlases or pays for their mipmaps.
var cjkEnabled atomic.Bool

// EnableCJK loads the Chinese additions into both faces. Calling it more than
// once is free: the merge happens on the first call that needs it.
func EnableCJK() { cjkEnabled.Store(true) }

// CJKEnabled reports whether the Chinese atlases have been requested. Text that
// names Chinese in Chinese itself must fall back to a Latin spelling while they
// have not, or the language card would be blank.
func CJKEnabled() bool { return cjkEnabled.Load() }

// cjkFaces is the base pair with the Chinese additions merged in, built once
// on first use. The CJK atlas carries every glyph it draws, so a key already
// present in the base face keeps the base entry.
var cjkFaces = sync.OnceValue(func() map[string]*atlasFace {
	faces := make(map[string]*atlasFace, 2)
	for _, name := range []string{"display", "body"} {
		base := filmFaces()[name]
		cjk := loadAtlasFace(name + "-cjk")
		merged := &atlasFace{
			Cap:     base.Cap,
			Glyphs:  make(map[string]*atlasGlyph, len(base.Glyphs)+len(cjk.Glyphs)),
			Kerning: base.Kerning,
		}
		for k, g := range base.Glyphs {
			merged.Glyphs[k] = g
		}
		for k, g := range cjk.Glyphs {
			if _, ok := merged.Glyphs[k]; !ok {
				merged.Glyphs[k] = g
			}
		}
		faces[name] = merged
	}
	return faces
})

func textFace(name string) *atlasFace {
	key := "display"
	if name == "body" {
		key = "body"
	}
	if cjkEnabled.Load() {
		return cjkFaces()[key]
	}
	return filmFaces()[key]
}

// Unsupported characters retain a space's advance, preserving the existing
// script contract instead of collapsing the rest of the line.
func (f *atlasFace) glyph(r rune) (*atlasGlyph, rune) {
	if g, ok := f.Glyphs[string(r)]; ok {
		return g, r
	}
	return f.Glyphs[" "], ' '
}
