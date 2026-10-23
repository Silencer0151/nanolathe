package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/community"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

// A selected save's label follows its recorded entry rules, including a
// switch since entry, a later player override and a missing current rule set.
// The raw bank index and the no-sidecar retail fallback remain unchanged.
func TestSaveDifficultyUsesRecordedRules(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name, rules, base    string
		entry                bool
		player               *bool
		reverse, nextReverse bool
	}{
		{"Community", string(gameplay.Community39), string(gameplay.Community39), true, nil, true, true},
		{"Modern", string(gameplay.Modern), string(gameplay.Modern), true, nil, true, true},
		{"Strict entry", string(gameplay.Strict31), string(gameplay.Strict31), false, nil, false, false},
		{"switched to Strict", string(gameplay.Strict31), string(gameplay.Strict31), true, nil, true, false},
		{"switched from Strict", string(gameplay.Community39), string(gameplay.Community39), false, nil, false, true},
		{"later player override", string(gameplay.Community39), string(gameplay.Community39), true, &off, true, false},
		{"unknown current set keeps entry", "unavailable-set", string(gameplay.Strict31), true, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "saved.SAV")
			sc := save.Sidecar{Rules: tc.rules, Gameplay: tc.base, Community: save.SidecarCommunity{
				Entry: community.Features{AIDifficultyReversed: tc.entry},
				Sources: save.SidecarCommunitySources{
					Content: []community.Overrides{{AIDifficultyReversed: &on}},
					Player:  community.Overrides{AIDifficultyReversed: tc.player},
				},
			}}
			if err := save.WriteSidecar(path, sc); err != nil {
				t.Fatal(err)
			}
			for index := int32(0); index < 3; index++ {
				want := index
				if tc.reverse {
					want = 2 - index
				}
				if got := saveSummaryDifficulty(path, save.Summary{Difficulty: index}); got != want {
					t.Fatalf("index %d displayed as tier %d, want %d", index, got, want)
				}
			}
			// A continuation names the next mission, entered under current rules.
			wantNext := int32(0)
			if tc.nextReverse {
				wantNext = 2
			}
			if got := saveSummaryDifficulty(path, save.Summary{BetweenMissions: 1, HasBetweenMissions: true}); got != wantNext {
				t.Fatalf("continuation displayed tier %d, want next-entry tier %d", got, wantNext)
			}
			if err := save.RemoveSidecar(path); err != nil {
				t.Fatal(err)
			}
			if got := saveSummaryDifficulty(path, save.Summary{}); got != 0 {
				t.Fatalf("no-sidecar index 0 displayed as tier %d", got)
			}
			if err := os.WriteFile(save.SidecarPath(path), []byte("{broken"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := retailSummaryPanelFields(save.Summary{Difficulty: saveSummaryDifficulty(path, save.Summary{})}, true, nil)["DIFF"]; got != "" {
				t.Fatalf("unreadable sidecar displayed guessed difficulty %q", got)
			}
		})
	}
}

// The loader routes by marker presence, including a present zero payload.
// Decode real Summary banks so the label and preflight use the same evidence.
func TestSaveDifficultyUsesSummaryRoutingPresence(t *testing.T) {
	on := true
	for _, tc := range []struct {
		mode       gameplay.Mode
		entry      bool
		battleTier int32
		nextTier   int32
	}{
		{gameplay.Strict31, true, 2, 0},
		{gameplay.Community39, false, 0, 2},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "routing.SAV")
			sc := save.Sidecar{Rules: string(tc.mode), Gameplay: string(tc.mode), Community: save.SidecarCommunity{
				Entry:   community.Features{AIDifficultyReversed: tc.entry},
				Sources: save.SidecarCommunitySources{Content: []community.Overrides{{AIDifficultyReversed: &on}}},
			}}
			if err := save.WriteSidecar(path, sc); err != nil {
				t.Fatal(err)
			}
			for _, marker := range []struct {
				name    string
				present bool
				value   int32
			}{
				{"absent", false, 0},
				{"present zero", true, 0},
				{"present one", true, 1},
			} {
				t.Run(marker.name, func(t *testing.T) {
					builder := save.NewBuilder()
					account := builder.Add(save.SummaryAccount)
					account.SetInt("Gametype", session.GametypeCampaign)
					account.SetInt("Difficulty", 0)
					if marker.present {
						account.SetInt("BetweenMissions", marker.value)
					}
					if err := save.WriteFile(path, builder.Bytes()); err != nil {
						t.Fatal(err)
					}
					bank, err := save.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					loaded, err := session.PreflightRetailLoad(bank)
					if err != nil {
						t.Fatal(err)
					}
					wantRoute, wantTier := session.RetailLoadRouteBattleRestoration, tc.battleTier
					if marker.present {
						wantRoute, wantTier = session.RetailLoadRouteCampaignContinuation, tc.nextTier
					}
					if loaded.Route != wantRoute || loaded.Summary.HasBetweenMissions != marker.present || loaded.Summary.BetweenMissions != marker.value {
						t.Fatalf("decoded route %v, marker %d present=%t; want route %v, marker %d present=%t", loaded.Route, loaded.Summary.BetweenMissions, loaded.Summary.HasBetweenMissions, wantRoute, marker.value, marker.present)
					}
					if got := saveSummaryDifficulty(path, loaded.Summary); got != wantTier {
						t.Fatalf("decoded route %v displays tier %d, want %d", loaded.Route, got, wantTier)
					}
				})
			}
		})
	}
}

// Selecting another file refreshes the production dialog from that file's
// sidecar, even while the shell and the mounted content select other rules.
func TestSaveDialogDifficultyFollowsSelection(t *testing.T) {
	resetSaveLoadScreenState(t)
	g, dir := retailShellForTest(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	on := true
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Community39} {
		path := session.RetailSavePath(dir, string(mode))
		summary := save.Summary{Description: string(mode), Gametype: 2, Players: 2, Difficulty: 0, IsBattle: true}
		bank := save.NewBuilder()
		save.WriteSummary(bank, summary)
		if err := save.WriteFile(path, bank.Bytes()); err != nil {
			t.Fatal(err)
		}
		sc := save.Sidecar{Rules: string(mode), Gameplay: string(mode), Community: save.SidecarCommunity{
			Entry:   community.Features{AIDifficultyReversed: mode != gameplay.Strict31},
			Sources: save.SidecarCommunitySources{Content: []community.Overrides{{AIDifficultyReversed: &on}}},
		}}
		if err := save.WriteSidecar(path, sc); err != nil {
			t.Fatal(err)
		}
	}
	g.cs.gameplayFeatures = []community.Overrides{{AIDifficultyReversed: &on}}
	if err := g.openSaveLoadScreen(loadScreenMode, saveLoadFromFrontend); err != nil {
		t.Fatal(err)
	}
	defer g.closeSaveLoadScreen()
	var capture *client.Client
	if os.Getenv("NANOLATHE_SAVE_UI_CAPTURE_DIR") != "" {
		var err error
		capture, err = client.New(client.Options{Buffer: &frame.Buffer{}, Width: 640, Height: 480})
		if err != nil {
			t.Fatal(err)
		}
		if err := bindShellContent(g, capture); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Community39} {
		g.gameplay = mode
		for i := range saveLoadUI.entries {
			saveLoadUI.Select(i)
			g.refreshSaveLoadPanel()
			entry, _ := saveLoadUI.SelectedEntry()
			want := "Easy"
			if entry.Summary.Description == string(gameplay.Community39) {
				want = "Hard"
			}
			if got := saveLoadPanel.TextOf("DIFF"); got != want || entry.Summary.Difficulty != 0 {
				t.Fatalf("shell %s, save %s: label %q, stored index %d", mode, entry.Summary.Description, got, entry.Summary.Difficulty)
			}
			if capture != nil {
				captureSaveLoadUI(t, capture, "difficulty-"+entry.Summary.Description+"-shell-"+string(mode))
			}
		}
	}
}

// The actual lobby row reads the room's effective feature table even when
// this seat's shell still selects another rule set. Game Options reads the
// running session's table, and neither display rewrites the stored index.
func TestDifficultyLabelsUseTheOwningBattleAndRoom(t *testing.T) {
	useOnlineSessionSeams(t, 4)
	g := onlineTestShell(t)
	g.setup.Difficulty = 0
	g.gameplay = gameplay.Strict31
	fake := newFakeOnlineLobby(0, 0, 1)
	openTestLobby(t, g, fake, onlineTestCatalog())
	g.activateGadget("Player2")
	g.pollOnline()
	r := &g.online.room
	for _, reverse := range []bool{true, false} {
		r.baseReq.Community.AIDifficultyReversed = reverse
		g.refreshOnlineComputerRow(2, 0, 2, []uint8{0, 1, 2})
		want := "Easy"
		if reverse {
			want = "Hard"
		}
		if got := lobbyText(g, lobbyLevel+"2"); got != want || r.settings.computers[0].Difficulty != 0 {
			t.Fatalf("room reverse=%t: label %q, stored index %d", reverse, got, r.settings.computers[0].Difficulty)
		}
		sess := &session.Session{EntryCommunity: community.Features{AIDifficultyReversed: reverse}, Community: community.Features{AIDifficultyReversed: !reverse}}
		sess.Skirmish.Difficulty = 0
		if got := battleGameOptionsRows(&battleSession{sess: sess})[4].Value; got != want || sess.Skirmish.Difficulty != 0 {
			t.Fatalf("battle reverse=%t: label %q, stored index %d", reverse, got, sess.Skirmish.Difficulty)
		}
	}
}

// A continuation's displayed tier must reach the actual briefing Start request,
// even when today's overrides disagree. Its recorded sources belong to that
// briefing only (DESIGN_MODS_MUTATORS §7.3, DESIGN_COMMUNITY_PATCH §4.8).
func TestContinuationDifficultyMatchesBriefingStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     gameplay.Mode
		reversed bool
		marker   int32
	}{
		{"Community Hard", gameplay.Community39, true, 1},
		{"Community Easy, zero marker", gameplay.Community39, false, 0},
		{"Modern Hard, zero marker", gameplay.Modern, true, 0},
		{"Modern Easy", gameplay.Modern, false, 1},
		{"Strict ignores reversal", gameplay.Strict31, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetSaveLoadScreenState(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			cs, campaigns := campaignWorkflowFixture(t)
			defer cs.Close()
			savedReverse, currentReverse := tc.reversed, !tc.reversed
			g := &gameShell{cs: cs, campaigns: campaigns, frontend: ui.NewFrontend(modeMenuSingle),
				opts: Options{Seed: 7, Gameplay: gameplay.Strict31, GameplayOverrides: []community.Overrides{{AIDifficultyReversed: &currentReverse}}},
				assets: &menuAssets{
					panel:    map[shellMode]*retailPanelAssets{modeMenuMission: {window: newgameTestWindow()}},
					briefing: &retailPanelAssets{window: &gui.Window{Gadgets: []gui.Gadget{{Kind: gui.KindPanel, Active: 1}}}},
				}}
			t.Cleanup(func() {
				if g.audioOwner != nil {
					g.audioOwner.Close()
				}
			})
			path := filepath.Join(t.TempDir(), "continuation.SAV")
			summary := save.Summary{Campaign: "camps/arm.tdf", Mission: "Second", Gametype: session.GametypeCampaign,
				Players: 1, Difficulty: 0, Side: 0, BetweenMissions: tc.marker, HasBetweenMissions: true}
			bank := save.NewBuilder()
			save.WriteSummary(bank, summary)
			bank.Add(save.SummaryAccount).SetInt("BetweenMissions", tc.marker)
			if err := save.WriteFile(path, bank.Bytes()); err != nil {
				t.Fatal(err)
			}
			sc := save.Sidecar{Rules: string(tc.mode), Gameplay: string(tc.mode), Community: save.SidecarCommunity{
				Entry: community.Features{AIDifficultyReversed: currentReverse},
				Sources: save.SidecarCommunitySources{
					Content: []community.Overrides{{AIDifficultyReversed: &currentReverse}},
					Player:  community.Overrides{AIDifficultyReversed: &savedReverse},
				},
			}}
			if err := save.WriteSidecar(path, sc); err != nil {
				t.Fatal(err)
			}
			start := func(want session.CommunitySources) int {
				t.Helper()
				if g.briefing == nil {
					t.Fatal("continuation did not open a briefing")
				}
				event, err := g.briefing.Dispatch(BriefingActionStart)
				if err != nil || !event.Valid {
					t.Fatalf("briefing Start: valid=%t, error=%v", event.Valid, err)
				}
				req := event.Request.value
				if req.Gameplay != tc.mode || req.Difficulty != 0 || req.SimulationSeed != 7 || req.CRTSeed != 7 {
					t.Fatalf("Start changed recorded mode/index or host seeds: %+v", req)
				}
				if !reflect.DeepEqual(req.CommunitySources, want) {
					t.Error("Start did not retain the expected Community source layers")
				}
				features, err := session.ResolveCommunity(req.Gameplay, req.CommunitySources)
				if err != nil {
					t.Fatal(err)
				}
				return session.DifficultyTier(features, req.Difficulty)
			}
			if err := g.loadRetailSavePath(path); err != nil {
				t.Fatal(err)
			}
			if got, want := start(session.SidecarCommunitySources(sc.Community)), int(saveSummaryDifficulty(path, summary)); got != want {
				t.Fatalf("briefing starts tier %d, Load Game displays %d", got, want)
			}
			// Back abandons the saved briefing. A fresh one reads today's sources.
			if err := g.loadRetailSavePath(path); err != nil {
				t.Fatal(err)
			}
			g.dispatchBriefing(BriefingActionPrev)
			g.openCampaignBriefing()
			start(communitySources(g.opts, g.cs))
			// A bank without a sidecar keeps the existing fresh-entry policy.
			if err := save.RemoveSidecar(path); err != nil {
				t.Fatal(err)
			}
			if err := g.loadRetailSavePath(path); err != nil {
				t.Fatal(err)
			}
			start(communitySources(g.opts, g.cs))
		})
	}
}
