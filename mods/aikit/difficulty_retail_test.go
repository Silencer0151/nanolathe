//go:build retail

package aikit

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/community"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
)

// Rule switches keep the live Modern host, persona, merged parameter layer
// and private generator; a load reconstructs that same entry difficulty.
func TestReversedDifficultyKeepsModernPersonaAcrossRulesAndLoadRetail(t *testing.T) {
	cat, fs := retailcat.Shared(t)
	if _, ok := cat.Maps[content.CanonicalKey("The Pass")]; !ok {
		t.Skip("retail map The Pass is absent")
	}
	on := true
	for _, entry := range []gameplay.Mode{gameplay.Community39, gameplay.Strict31} {
		t.Run(string(entry), func(t *testing.T) {
			cfg := session.DirectSkirmishConfig("The Pass")
			cfg.Gameplay, cfg.Difficulty = entry, 0
			cfg.RNGSimSeed, cfg.RNGCrtSeed = 41, 41
			cfg.Players[1].AI = ai.ControllerModern
			options := session.SkirmishEntryOptions{
				CommunitySources: session.CommunitySources{Content: []community.Overrides{{AIDifficultyReversed: &on}}},
				AIOverrides: session.AIOverrides{Difficulty: [3]string{
					"jitter=0,style=eco", "jitter=0,style=balanced", "jitter=0,style=tower",
				}},
			}
			src, err := session.NewSkirmishWithEntryOptions(fs, cat, cfg, options)
			if err != nil {
				t.Fatal(err)
			}
			defer closeControllers(src)
			stepBattle(src, 120)
			h, ok := src.AI[1].Ext.(*aikit.Host)
			if !ok {
				t.Fatal("the Modern controller did not start")
			}
			position, started := h.Generator()
			want, params, next := ai.DifficultyHard, "jitter=0,style=tower", gameplay.Strict31
			persona := aikit.PersonaHard
			if entry == gameplay.Strict31 {
				want, params, next = ai.DifficultyEasy, "jitter=0,style=eco", gameplay.Community39
				persona = aikit.PersonaEasy
			}
			persona.Async = true
			if err := src.SetRules(string(next)); err != nil {
				t.Fatal(err)
			}
			after, running := h.Generator()
			if src.AI[1].Ext != h || !started || !running || after != position || h.Persona() != persona || src.AI[1].Profile.Plan != want || src.AI[1].ControllerParams != params {
				t.Fatalf("switch %s -> %s changed entry controller state: persona %+v, profile %s, params %q, generator %d -> %d", entry, next, h.Persona(), src.AI[1].Profile.Plan, src.AI[1].ControllerParams, position, after)
			}
			sc := session.SaveSidecar(src)
			var record session.AIControllers
			if err := json.Unmarshal(sc.AI, &record); err != nil {
				t.Fatal(err)
			}
			inputs, err := src.RetailBattleSaveInputs(session.RetailBattleSummary(src, "difficulty", "0", src.Skirmish.UnitLimit), save.Camera{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "DIFFICULTY.SAV")
			if err := src.WriteRetailSave(path, inputs); err != nil {
				t.Fatal(err)
			}
			loaded, err := session.LoadRetailSavePath(path, session.RetailLoadDeps{
				FS: fs, Catalog: cat, SimSeed: 41, CRTSeed: 41, UnitLimit: src.Skirmish.UnitLimit,
				Gameplay: next, CommunitySources: session.SidecarCommunitySources(sc.Community),
				EntryCommunity: &sc.Community.Entry, AIControllers: &record,
			})
			if err != nil {
				t.Fatal(err)
			}
			dst := loaded.Battle.Session
			defer closeControllers(dst)
			stepBattle(dst, 120)
			restored, ok := dst.AI[1].Ext.(*aikit.Host)
			if !ok || restored.Persona() != persona || dst.AI[1].Profile.Plan != want || dst.AI[1].ControllerParams != params || dst.Skirmish.Difficulty != 0 {
				t.Fatalf("restored entry %s under %s: profile %s, params %q, stored %d", entry, next, dst.AI[1].Profile.Plan, dst.AI[1].ControllerParams, dst.Skirmish.Difficulty)
			}
		})
	}
}
