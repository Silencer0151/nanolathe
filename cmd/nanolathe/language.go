package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/film"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// The interface language (docs/DESIGN_I18N.md): the saved choice, a
// --language flag over it for one run, and the Language card that changes it.
// Text is translated where the Nanolathe screen sets it (internal/i18n).

// parseLanguage is the --language flag's value as a language code.
func parseLanguage(text string) (string, error) {
	code := strings.ToLower(strings.TrimSpace(text))
	if !i18n.Known(code) {
		codes := make([]string, len(i18n.Languages))
		for i, l := range i18n.Languages {
			codes[i] = l.Code
		}
		return "", fmt.Errorf("nanolathe: unknown language %q: logical path <command line>, providers searched [language], expected one of %s", text, strings.Join(codes, ", "))
	}
	return code, nil
}

// applyLanguage selects the interface language: the --language flag for this
// run, otherwise the saved choice. A catalog that would not load leaves the
// interface in English with a notice.
func (g *gameShell) applyLanguage() {
	code := g.requestedLanguage()
	g.languagePending = ""
	if code == "chinese" {
		film.EnableCJK()
		g.languagePending = code
		g.pollLanguage()
		return
	}
	g.setInterfaceLanguage(code)
}

func (g *gameShell) requestedLanguage() string {
	code := g.language
	if g.opts.Language != "" {
		code = g.opts.Language
	}
	if code == "" {
		code = i18n.English
	}
	return code
}

func (g *gameShell) setInterfaceLanguage(code string) {
	if err := i18n.SetLanguage(code); err != nil {
		fmt.Fprintf(os.Stderr, "nanolathe: %v (using English)\n", err)
	}
}

// A browser download completes between host steps. Activate the catalog only
// once its glyphs exist; a failed request leaves the prior language readable
// and the card's unapplied choice available for another Apply (DESIGN_I18N).
func (g *gameShell) pollLanguage() {
	if g.languagePending != "" {
		g.completeLanguage(film.CJKReady(), film.CJKError())
	}
}

func (g *gameShell) completeLanguage(ready bool, err error) {
	if g.languagePending == "" {
		return
	}
	if err != nil {
		g.languagePending = ""
		fmt.Fprintf(os.Stderr, "nanolathe: Chinese fonts failed: logical path film, providers searched [browser], expected CJK atlases: %v\n", err)
		if s := nlScreenInst; s != nil && s.open && s.shell() == g {
			s.retainLanguageChoice(g)
			s.toast, s.toastLeft = "Chinese fonts could not load. Apply to retry.", 6
		}
		return
	}
	if ready {
		code := g.languagePending
		g.languagePending = ""
		g.setInterfaceLanguage(code)
		if s := nlScreenInst; s != nil && s.open && s.shell() == g {
			if !s.touched["language"] {
				s.draft.language = languageIndex()
			}
			s.toast, s.toastLeft = "Settings saved", 2.2
		}
	}
}

// chooseLanguage makes the Language card's choice the saved language. It
// wins over a --language flag from then on.
func (g *gameShell) chooseLanguage(i int) {
	code := i18n.Languages[i].Code
	if code == i18n.Language() && code == g.requestedLanguage() && g.languagePending == "" {
		return
	}
	g.opts.Language = ""
	g.language = code
	if code == i18n.English {
		g.language = ""
	}
	g.applyLanguage()
}

// languageIndex is the running language's place in i18n.Languages.
func languageIndex() int {
	return languageCodeIndex(i18n.Language())
}

func languageCodeIndex(code string) int {
	for i, l := range i18n.Languages {
		if l.Code == code {
			return i
		}
	}
	return 0
}

// A shell rebind can occur before an optional download completes. Keep its
// requested choice available for Apply without replacing a newer draft edit.
func (s *nlScreen) retainLanguageChoice(g *gameShell) {
	if g.requestedLanguage() == "chinese" && i18n.Language() != "chinese" && !s.touched["language"] {
		s.draft.language = languageCodeIndex("chinese")
	}
}

// languageCard is the Game page's Language card. Each language is named, and
// described under its name, in its own words, so a player who cannot read the
// running language still finds theirs.
func (s *nlScreen) languageCard() nlCard {
	steps := make([]string, len(i18n.Languages))
	subs := []string{"The interface as written", "Інтерфейс українською", "简体中文界面"}
	for i, l := range i18n.Languages {
		steps[i] = l.Name
		if l.Code == "chinese" && !film.CJKReady() {
			// The Chinese name is written in Chinese so a player who cannot
			// read the running language still finds it, but those glyphs live
			// in the atlases only Chinese decodes. Until then the card spells
			// it in Latin, which the base faces always carry.
			steps[i] = "Chinese"
			subs[i] = "Chinese interface"
		}
	}
	return nlCard{
		key: "language", label: "Language", pics: []string{"armcom", "corcom"}, kind: nlHalves,
		steps: steps, subs: subs,
		get: func(d *nlDraft) int { return d.language },
		set: func(d *nlDraft, v int) { d.language = v },
		desc: func(*nlDraft, int) string {
			return "The language of this screen. The menus drawn in the original game's art, the battle interface and the units' own names stay in English for now."
		},
		scene: func(*nlDraft, int) string { return "armor" },
	}
}

// fitStyle is st set smaller, down to smallest, until text fits width: a
// translation can be longer than the English a place was laid out for.
func fitStyle(f *screenkit.Font, text string, st screenkit.Style, width, smallest float64) screenkit.Style {
	if w := f.Measure(text, st); w > width && w > 0 {
		st.Size = max(smallest, st.Size*width/w)
	}
	return st
}

// cardCaption sets a carousel card's value line, smaller when it would run
// past right.
func (s *nlScreen) cardCaption(screen *ebiten.Image, text string, x, y, right, a float64) {
	u := s.u()
	st := screenkit.Style{Size: 10.5 * u, Top: alphaC(nlGreenText, a)}
	s.fonts.Body.Draw(screen, text, x, y, fitStyle(s.fonts.Body, text, st, right-x, 8*u))
}
