package main

import (
	"context"
	"encoding/json"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// A battle's generated map stays recoverable after its memory mount is gone
// (docs/DESIGN_MAP_GENERATOR.md §10): its save's sidecar and its replay carry
// the recipe, and loading either makes the map again when it is not mounted.

// battleRecipe is the recipe of the generated map the running battle was
// entered on, as a save records it; nil for any other map. A battle
// continued from a replay plays the recorded map, whose recipe the replay
// carries even when this content does not hold the map: a recorded online
// room's playback makes it on the room's content alone
// (DESIGN_MULTIPLAYER §10.1).
func (g *gameShell) battleRecipe() json.RawMessage {
	if g == nil || g.battle == nil || g.battle.sess == nil {
		return nil
	}
	if t := g.battle.takeover; t != nil && t.source != nil {
		if config, err := t.source.header.Configuration(); err == nil && len(config.Request().GeneratedMap) != 0 {
			return json.RawMessage(config.Request().GeneratedMap)
		}
	}
	return g.cs.generatedRecipeRecord(g.battle.sess.Skirmish.MapName)
}

// recoverGeneratedMap makes sure the generated map a recipe names is the one
// mounted. A mounted map of that name whose files have the recipe's hashes
// is used as it is; with none, the map is made again from its recipe and
// returned for mounting. A map of that name with other files is refused, as
// is a recipe this build cannot make again.
func (g *gameShell) recoverGeneratedMap(raw json.RawMessage) (*generatedMap, error) {
	var r mapgenRecipe
	if err := json.Unmarshal(raw, &r); err != nil || !mapgenNameValid(r.Name) {
		return nil, refuseLoad("This game's generated map record is unreadable")
	}
	limit := g.cs.limits.TNTBytes
	if limit <= 0 {
		limit = maplibrary.MaxTNTBytes
	}
	if tnt, err := g.cs.fs.ReadFileLimit("maps/"+r.Name+".tnt", limit); err == nil {
		ota, err := g.cs.fs.ReadFileLimit("maps/"+r.Name+".ota", int64(formats.DefaultTDFLimits().MaxBytes))
		if err == nil && mapgenSum(tnt) == r.TNT && mapgenSum(ota) == r.OTA {
			return nil, nil
		}
		return nil, refuseLoad("This game was played on a generated map that differs from the map %s here", r.Name)
	}
	made, err := regenerateMap(context.Background(), g.cs.fs, g.cs.mapRoots, limit, r)
	if err != nil {
		return nil, refuseLoad("This game's generated map %s could not be made again: %s", r.Name, noticeReason(err))
	}
	return made, nil
}

// runningModSelection is the running content's mod as a reload names it.
func (g *gameShell) runningModSelection() settings.ModSelection {
	if g.cs != nil && g.cs.mod != nil {
		return settings.ModSelection{ID: g.cs.mod.ID, Version: g.cs.mod.Version}
	}
	return g.modSetting
}
