package screenkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/nanolathe-gg/nanolathe/internal/film"
)

// Latin text breaks only at its spaces, so a " · " separator at the margin
// starts the next line as it always has, instead of hanging on to the word
// before it (docs/DESIGN_I18N.md).
func TestWrapKeepsLatinSeparatorsAtSpaces(t *testing.T) {
	f := LoadFonts().Body
	s := Style{Size: 12, Verbatim: true}
	text := "Free flow · orders auto"
	for width := 10.0; width <= 200; width += 3 {
		lines := f.Wrap(text, s, width)
		if got := strings.Join(lines, " "); got != text {
			t.Fatalf("width %v: lines %q rejoin as %q", width, lines, got)
		}
	}
}

// A Chinese line breaks between characters, but a closing mark travels with
// the character before it and an opening mark with the one after it, so no
// line starts with the one or ends with the other, and no character is lost.
func TestWrapBindsChinesePunctuation(t *testing.T) {
	f := LoadFonts().Body
	s := Style{Size: 12, Verbatim: true}
	text := "选择你的队伍、阵营与颜色，然后点“准备”。原版游戏的“界面类型”：左键单击时取消。"
	for width := 10.0; width <= 400; width += 3 {
		lines := f.Wrap(text, s, width)
		if got := strings.Join(lines, ""); got != text {
			t.Fatalf("width %v: lines %q lose text", width, lines)
		}
		for _, l := range lines {
			r := []rune(l)
			if noBreakBefore(r[0]) || noBreakAfter(r[len(r)-1]) {
				t.Fatalf("width %v: line %q starts with a closing or ends with an opening mark", width, l)
			}
		}
	}
}

// Every character a catalog draws has a glyph in both faces: a catalog edited
// without regenerating the atlases (tools/film-font-cjk) would otherwise set
// the missing characters as spaces.
func TestEveryCatalogCharacterHasAGlyph(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "i18n", "locales", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("catalogs: %v %v", paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var catalog map[string]string
		if err := json.Unmarshal(data, &catalog); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		// A language may carry its glyphs in its own atlas, loaded only when
		// that language is selected; the faces must hold them by the time its
		// catalog is checked.
		if strings.HasPrefix(filepath.Base(path), "chinese") {
			film.EnableCJK()
		}
		fonts := LoadFonts()
		for _, text := range catalog {
			for _, r := range text {
				if unicode.IsSpace(r) {
					continue // spaces and the line breaks callers split on
				}
				for _, f := range []*Font{fonts.Display, fonts.Body} {
					if _, n := f.glyph(r); n == ' ' {
						t.Errorf("%s: %q has no glyph in every face (in %q)", filepath.Base(path), r, text)
					}
				}
			}
		}
	}
}
