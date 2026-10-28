package main

import (
	"errors"
	"io"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

func restoreEnglish(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = i18n.SetLanguage(i18n.English) })
}

func TestLanguageFlag(t *testing.T) {
	opts, err := parseFlags([]string{"--language", " Ukrainian "}, io.Discard)
	if err != nil || opts.Language != "ukrainian" {
		t.Fatalf("--language Ukrainian: %q, %v", opts.Language, err)
	}
	if _, err := parseFlags([]string{"--language", "klingon"}, io.Discard); err == nil {
		t.Fatal("an unknown language was accepted")
	}
}

// The Language card's choice applies on Apply, is saved, and comes back with
// the settings; English saves as no language.
func TestLanguageCardAppliesAndSaves(t *testing.T) {
	restoreEnglish(t)
	g, s := settingsRegressionScreen(nil, settings.Defaults())
	card := configurationCard(t, s, "language")
	if card.get(&s.draft) != 0 || i18n.Language() != i18n.English {
		t.Fatalf("a fresh player starts in %q", i18n.Language())
	}
	s.setCard(card, 1)
	if i18n.Language() != i18n.English {
		t.Fatal("the draft changed the language before Apply")
	}
	s.apply()
	if i18n.Language() != "ukrainian" || g.captureSettings().Language != "ukrainian" {
		t.Fatalf("Apply: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
	_ = i18n.SetLanguage(i18n.English)
	g, s = settingsRegressionScreen(nil, g.captureSettings())
	if i18n.Language() != "ukrainian" || configurationCard(t, s, "language").get(&s.draft) != 1 {
		t.Fatalf("restart: running %q", i18n.Language())
	}
	s.setCard(configurationCard(t, s, "language"), 0)
	s.apply()
	if i18n.Language() != i18n.English || g.captureSettings().Language != "" {
		t.Fatalf("back to English: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
}

// --language sets the language for one run without becoming the saved
// choice; choosing on the card replaces it.
func TestLanguageFlagIsForOneRun(t *testing.T) {
	restoreEnglish(t)
	g := &gameShell{cs: &contentSet{}}
	g.opts.Language = "ukrainian"
	g.applySettings(settings.Defaults())
	if i18n.Language() != "ukrainian" || g.captureSettings().Language != "" {
		t.Fatalf("flag: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
	g.chooseLanguage(0)
	if i18n.Language() != i18n.English || g.opts.Language != "" {
		t.Fatalf("card: running %q, flag %q", i18n.Language(), g.opts.Language)
	}
}

// The screen's kept capitals and wrapped lines follow a language change.
func TestKeptTextFollowsTheLanguage(t *testing.T) {
	restoreEnglish(t)
	var c nlUICache
	if got := c.upperCase("Back"); got != "BACK" {
		t.Fatalf("English capitals %q", got)
	}
	if err := i18n.SetLanguage("ukrainian"); err != nil {
		t.Fatal(err)
	}
	if got := c.upperCase("Back"); got != "НАЗАД" {
		t.Fatalf("Ukrainian capitals %q", got)
	}
}

// An asynchronous font request must not activate text that the current face
// cannot draw; failures retain a readable language and an explicit retry.
func TestChineseLanguageWaitsForFontsAndCanRetry(t *testing.T) {
	restoreEnglish(t)
	_ = i18n.SetLanguage(i18n.English)
	g := &gameShell{language: "chinese", languagePending: "chinese"}
	g.completeLanguage(false, nil)
	if i18n.Language() != i18n.English || g.languagePending != "chinese" {
		t.Fatal("a pending download changed the interface")
	}
	g.completeLanguage(false, errors.New("download failed"))
	if i18n.Language() != i18n.English || g.languagePending != "" || g.language != "chinese" {
		t.Fatal("failure lost the readable interface or saved choice")
	}
	g.chooseLanguage(2)
	if i18n.Language() != "chinese" || g.languagePending != "" {
		t.Fatal("selecting Chinese after failure did not retry and activate the available fonts")
	}
}

// Applying the previous language while Chinese is pending cancels activation,
// even though that language is still the one currently being drawn.
func TestPendingChineseCanBeCancelled(t *testing.T) {
	restoreEnglish(t)
	_ = i18n.SetLanguage(i18n.English)
	g := &gameShell{language: "chinese", languagePending: "chinese"}
	g.chooseLanguage(0)
	g.completeLanguage(true, nil)
	if i18n.Language() != i18n.English || g.languagePending != "" || g.language != "" {
		t.Fatal("cancelled Chinese activated after the download completed")
	}
}

// Reopening settings while optional fonts load must not turn an unrelated
// Apply into an English selection or cancel the saved Chinese request.
func TestUntouchedLanguageKeepsPendingChinese(t *testing.T) {
	restoreEnglish(t)
	_ = i18n.SetLanguage(i18n.English)
	g, s := settingsRegressionScreen(nil, settings.Defaults())
	g.language, g.languagePending = "chinese", "chinese"
	s.touched["unitlimit"] = true
	s.draft.unitLimit = 500
	s.apply()
	if g.language != "chinese" || g.languagePending != "chinese" || s.draft.language != languageCodeIndex("chinese") {
		t.Fatal("an unrelated Apply replaced the pending language")
	}
}

// Content reloads and newly opened settings may have rebuilt their draft
// before a download settles. Failure must leave a real retry, while success
// must not leave an old-language change staged against the new active one.
func TestChineseCompletionAfterDraftRebind(t *testing.T) {
	restoreEnglish(t)
	previous := nlScreenInst
	t.Cleanup(func() { nlScreenInst = previous })
	for _, failed := range []bool{true, false} {
		_ = i18n.SetLanguage(i18n.English)
		g, s := settingsRegressionScreen(nil, settings.Defaults())
		g.language, g.languagePending = "chinese", "chinese"
		s.open = true
		nlScreenInst = s
		if failed {
			g.completeLanguage(false, errors.New("download failed"))
			if !s.touched["language"] || s.draft.language != languageCodeIndex("chinese") {
				t.Fatal("rebound draft cannot retry Chinese")
			}
			s.apply()
			if i18n.Language() != "chinese" {
				t.Fatal("Apply after rebind failure did not retry")
			}
		} else {
			g.completeLanguage(true, nil)
			if s.draft.language != languageCodeIndex("chinese") {
				t.Fatal("completion left the old language staged")
			}
		}
	}
	_ = i18n.SetLanguage(i18n.English)
	g, s := settingsRegressionScreen(nil, settings.Defaults())
	g.language, g.languagePending = "chinese", "chinese"
	s.open = true
	s.draft.language = languageCodeIndex("ukrainian")
	s.touched["language"] = true
	nlScreenInst = s
	g.completeLanguage(true, nil)
	if s.draft.language != languageCodeIndex("ukrainian") {
		t.Fatal("completion replaced the player's newer draft choice")
	}
}
