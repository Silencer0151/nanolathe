package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The fallback is content presentation policy; [07 R-FE-01 §4] supplies
// the campaign-count boundary. Fixtures are independently authored.
func TestMissionBackgroundFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		campaigns  int
		background string
		playAny    []byte
		layout     missionMenuLayout
		wantError  bool
	}{
		{name: "retail needs no unused campaign art", playAny: nlTestPCX(2, 1)},
		{name: "one campaign", campaigns: 1, background: "newcampaign4x", layout: missionLayoutFixedCampaign},
		{name: "two campaigns remain fixed", campaigns: 2, background: "newcampaign4x", layout: missionLayoutFixedCampaign},
		{name: "three campaigns use a list", campaigns: 3, background: "newcampaign4", layout: missionLayoutCampaign},
		{name: "missing chosen background", campaigns: 1, layout: missionLayoutFixedCampaign, wantError: true},
		{name: "corrupt retail does not fall back", campaigns: 1, playAny: []byte("invalid PCX"), background: "newcampaign4x", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name string, data []byte) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < tc.campaigns; i++ {
				side := []string{"ARM", "CORE"}[i%2]
				write(fmt.Sprintf("camps/campaign%d.TDF", i), []byte("[HEADER]{campaignside="+side+";}"))
			}
			// A subdirectory ending in TDF and a non-TDF file do not change
			// the boundary; use different sides above the counting layer.
			write("camps/ignored.tdf/nested.tdf", nil)
			write("camps/readme.txt", nil)
			if tc.playAny != nil {
				write("bitmaps/playanygame4.pcx", tc.playAny)
			}
			if tc.background != "" {
				write("bitmaps/"+tc.background+".pcx", nlTestPCX(1, 2))
			}
			fs := vfs.New()
			defer fs.Close()
			if err := fs.MountDirectory(root, 0); err != nil {
				t.Fatal(err)
			}
			background, layout, err := loadMissionBackground(testContentSet(fs))
			if (err != nil) != tc.wantError || layout != tc.layout {
				t.Fatalf("layout=%d error=%v, want layout=%d error=%v", layout, err, tc.layout, tc.wantError)
			}
			if tc.wantError {
				if tc.playAny == nil && !strings.Contains(err.Error(), "logical path bitmaps/newcampaign4x.pcx") {
					t.Fatalf("missing background lost its logical path: %v", err)
				}
				return
			}
			wantWidth := uint16(1)
			if tc.playAny != nil {
				wantWidth = 2
			}
			if background == nil || background.Width != wantWidth {
				t.Fatal("selected the wrong authored image")
			}
		})
	}
}

func TestCampaignFallbackUsesAuthoredLayout(t *testing.T) {
	campaigns := []mission.Campaign{
		{Name: "Alternative Arm", Document: mustParseCampaignHeader(t, "ARM")},
		{Name: "Arm Campaign", Document: mustParseCampaignHeader(t, "ARM"), Missions: []mission.Stub{{Index: 0}, {Index: 1}}},
		{Name: "Core Campaign", Document: mustParseCampaignHeader(t, "CORE"), Missions: []mission.Stub{{Index: 0}, {Index: 1}}},
	}
	for _, layout := range []missionMenuLayout{missionLayoutCampaign, missionLayoutFixedCampaign} {
		for side := 0; side < 2; side++ {
			window := newgameTestWindow()
			window.Gadgets[1].Rect.Y, window.Gadgets[1].Rect.H = 314, 142
			before := window.Gadgets[1].Rect
			background := &formats.PCX{Width: 1}
			g := &gameShell{frontend: ui.NewFrontend(modeMenuMission), campaigns: campaigns, missionSide: side, missionIdx: 1,
				assets: &menuAssets{missionLayout: layout, missionBackground: background,
					panel: map[shellMode]*retailPanelAssets{modeMenuMission: {window: window}}}}
			if layout == missionLayoutCampaign && side == 0 {
				g.campaignIdx = 1 // Retain the selected Arm campaign, after the alternative.
			}
			g.applyRetailMissionLayout(window)
			g.frontend.Panels.Replace(ui.NewPanel(window))
			g.refreshMissionPanel()
			p := g.activePanel()
			if p.ActiveOf("Missions") || p.ActiveOf("MissionsKnob") || g.missionIdx != 1 {
				t.Fatal("campaign fallback exposed the mission list or lost the carried mission")
			}
			if p.ActiveOf("Campaign") != (layout == missionLayoutCampaign) || p.ActiveOf("CampaignKnob") != (layout == missionLayoutCampaign) {
				t.Fatal("campaign visibility does not match the selected art")
			}
			if window.Gadgets[1].Rect != before || g.panelBackground() != background {
				t.Fatal("fallback changed authored rectangles or lost its background")
			}
			if layout == missionLayoutFixedCampaign {
				want := []string{"Arm Campaign", "Core Campaign"}[side]
				if len(g.campaignOptions) != 1 || g.campaignOptions[0].Name != want || window.Header.DefaultFocus != "Difficulty" {
					t.Fatalf("fixed side %d selected %+v", side, g.campaignOptions)
				}
			} else if window.Header.DefaultFocus != "Campaign" {
				t.Fatal("visible campaign list lost initial focus")
			}
		}
	}
}

// The supplied demo greys Core unconditionally [07 R-FE-01 §4]; Nanolathe's
// user-authorized fallback instead admits each side with playable content.
func TestCampaignFallbackDisablesUnavailableSides(t *testing.T) {
	for _, layout := range []missionMenuLayout{missionLayoutFixedCampaign, missionLayoutCampaign} {
		window := newgameTestWindow()
		for i, name := range []string{"Side0", "Arm", "Side1", "Core", "Start"} {
			window.Gadgets = append(window.Gadgets, gui.Gadget{Kind: gui.KindButton, Name: name, Active: 1,
				Rect: gui.Rect{X: int32(210 + i*30), Y: 20, W: 20, H: 20}})
		}
		g := &gameShell{frontend: ui.NewFrontend(modeMenuMission),
			campaigns: []mission.Campaign{{Name: "Arm Campaign", Document: mustParseCampaignHeader(t, "ARM"), Missions: []mission.Stub{{Index: 0}}}},
			assets:    &menuAssets{missionLayout: layout}}
		g.frontend.Panels.Replace(ui.NewPanel(window))
		g.refreshMissionPanel()
		p := g.activePanel()
		for _, name := range []string{"Side1", "Core"} {
			i := p.Index(name)
			r := p.Window.PlacedRect(i)
			if p.Window.Gadgets[i].GrayedOut&1 == 0 || p.Fires(i) || p.PressTest(r.X+1, r.Y+1) != -1 {
				t.Fatalf("layout %d: unavailable %s still accepts activation", layout, name)
			}
			p.SetFocus(i)
			if p.DefaultKeyAction(false).Kind != ui.ActionNone {
				t.Fatalf("layout %d: unavailable %s accepts keyboard activation", layout, name)
			}
		}
		for _, name := range []string{"Side0", "Arm", "Start"} {
			if !p.Fires(p.Index(name)) {
				t.Fatalf("layout %d: available %s disabled", layout, name)
			}
		}
		// An empty Core descriptor still provides no mission. A playable one
		// enables both the portrait and caption, without changing the layout.
		g.campaigns = append(g.campaigns, mission.Campaign{Name: "Core Campaign", Document: mustParseCampaignHeader(t, "CORE")})
		g.refreshMissionPanel()
		if p.Fires(p.Index("Core")) {
			t.Fatal("empty campaign enabled Core")
		}
		g.campaigns[1].Missions = []mission.Stub{{Index: 0}}
		g.refreshMissionPanel()
		if !p.Fires(p.Index("Side1")) || !p.Fires(p.Index("Core")) {
			t.Fatal("playable Core campaign did not re-enable its controls")
		}
	}
}

// A between-missions save carries an authored successor, not a request to
// start a new campaign [08 R-SAVE-02 §2][08 R-CAMP-01 §8]. Check the loaded
// briefing's map, not only the shell's row, across both fallback layouts.
func TestCampaignFallbackContinuationKeepsSelectedMission(t *testing.T) {
	for _, layout := range []missionMenuLayout{missionLayoutCampaign, missionLayoutFixedCampaign} {
		cs, campaigns := campaignWorkflowFixture(t)
		defer cs.Close()
		for i := range campaigns {
			if campaigns[i].Path == "camps/arm.tdf" {
				campaigns[i].Name = "Arm Campaign"
			}
		}
		g := &gameShell{cs: cs, campaigns: campaigns, frontend: ui.NewFrontend(modeMenuSingle),
			assets: &menuAssets{missionLayout: layout,
				panel:    map[shellMode]*retailPanelAssets{modeMenuMission: {window: newgameTestWindow()}},
				briefing: &retailPanelAssets{window: &gui.Window{Gadgets: []gui.Gadget{{Kind: gui.KindPanel, Active: 1}}}}}}
		if err := g.applyRetailContinuation(&session.RetailCampaignContinuation{
			CampaignPath: "camps/arm.tdf", MissionIndex: 1, Side: 0,
			Thumbs: [25]byte{'W', 'U'},
		}, nil); err != nil {
			t.Fatal(err)
		}
		if g.audioOwner != nil {
			defer g.audioOwner.Close()
		}
		if g.briefing == nil || g.briefing.mission.TerrainKey != "second" || g.missionIdx != 1 || g.campaignProgress.Thumbs[0] != 'W' {
			t.Fatalf("layout %d: continuation lost the successor briefing or progress", layout)
		}
		g.activateGadget("Start")
		if g.briefing == nil || g.briefing.mission.TerrainKey != "first" || g.missionIdx != 0 {
			t.Fatalf("layout %d: explicit New Campaign Start did not select mission zero", layout)
		}
	}
}
