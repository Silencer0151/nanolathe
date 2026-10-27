package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
)

// Online rooms on generated maps (docs/DESIGN_MAP_GENERATOR.md §10,
// DESIGN_MULTIPLAYER §16.8). A room's configuration carries its map's recipe
// as extension 20 and its content lists the archives the recipe's sources
// come from. Every seat holds the map before Ready: the host the one it
// made, and any other seat one it makes from the recipe on the room's own
// content, checked against the recipe's hashes.

// onlineRoomMap is this seat's hold on its room's generated map.
type onlineRoomMap struct {
	// recipe is the record the room's base configuration carries; nil for a
	// room on any other map.
	recipe []byte
	// made is the map once this seat holds it; err is why it cannot be held
	// here.
	made *generatedMap
	err  error
	// job makes the map in the background; nil when none runs.
	job *onlineRoomMapJob
}

type onlineRoomMapJob struct {
	cancel context.CancelFunc
	done   chan onlineRoomMapResult
}

type onlineRoomMapResult struct {
	made *generatedMap
	err  error
}

// stop cancels a running job; it owns its mount and closes it itself.
func (m *onlineRoomMap) stop() {
	if m.job != nil {
		m.job.cancel()
		m.job = nil
	}
}

// heldGeneratedMap is the map a recipe names as cs holds it: one the mount
// holds from memory with the recipe's files, or the files of the mounted map
// of that name when they have the recipe's hashes, as a saved map has. It is
// nil when cs holds no map of that name, and an error when it holds one with
// other files.
func heldGeneratedMap(cs *contentSet, r mapgenRecipe) (*generatedMap, error) {
	for _, m := range cs.generated {
		if strings.EqualFold(m.recipe.Name, r.Name) && m.recipe.TNT == r.TNT && m.recipe.OTA == r.OTA {
			return m, nil
		}
	}
	tnt, err := cs.fs.ReadFileLimit("maps/"+r.Name+".tnt", mapgenTNTLimit(cs))
	if err != nil {
		return nil, nil
	}
	ota, err := cs.fs.ReadFileLimit("maps/"+r.Name+".ota", int64(formats.DefaultTDFLimits().MaxBytes))
	if err != nil || mapgenSum(tnt) != r.TNT || mapgenSum(ota) != r.OTA {
		return nil, fmt.Errorf("nanolathe: generated map %q: logical path maps/%s.tnt, providers searched [%s], expected the files its recipe records", r.Name, r.Name, strings.Join(cs.roots, ", "))
	}
	return &generatedMap{recipe: r, tnt: tnt, ota: ota}, nil
}

// mapgenTNTLimit is the terrain read cap of cs, or the map library's.
func mapgenTNTLimit(cs *contentSet) int64 {
	if cs != nil && cs.limits.TNTBytes > 0 {
		return cs.limits.TNTBytes
	}
	return maplibrary.MaxTNTBytes
}

// parseMapgenRecipe reads a recipe record a configuration or a file carries.
func parseMapgenRecipe(raw []byte) (mapgenRecipe, error) {
	var r mapgenRecipe
	if err := json.Unmarshal(raw, &r); err != nil || !mapgenNameValid(r.Name) {
		return r, errors.New("the generated map's record is unreadable")
	}
	return r, nil
}

// roomRecipeHolders are the archives, by path, that hold a recipe's source
// maps as the recipe names them: a room on a generated map lists them even
// when they hold only maps, so every seat can make the map (§16.8).
func roomRecipeHolders(cs *contentSet, r mapgenRecipe) map[string]bool {
	holders := map[string]bool{}
	for _, id := range r.Sources {
		for _, src := range cs.unmappedMount.Sources(id.Map) {
			if strings.EqualFold(src.Source.ProviderID(), id.Archive) {
				holders[filepath.Clean(src.Source.SourcePath)] = true
			}
		}
	}
	return holders
}

// adoptRoomMap follows the room's recipe. A new recipe drops what was made
// for the old one; the map is then taken from what this seat holds, or made
// in the background on the room's own content once that content resolves
// with nothing missing (pollRoomMap).
func (g *gameShell) adoptRoomMap() {
	r := &g.online.room
	m := &r.roomMap
	recipe := r.baseReq.GeneratedMap
	if !bytes.Equal(recipe, m.recipe) {
		m.stop()
		*m = onlineRoomMap{recipe: bytes.Clone(recipe)}
	}
	if len(recipe) == 0 || m.made != nil || m.job != nil || m.err != nil || len(r.content.missing) > 0 {
		return
	}
	rec, err := parseMapgenRecipe(recipe)
	if err != nil {
		m.err = err
		return
	}
	if m.made, m.err = heldGeneratedMap(g.cs, rec); m.made != nil || m.err != nil {
		return
	}
	// The room's own content, as the battle mounts it, mounted for the job
	// alone: a remount of this seat's content cannot close it under the job.
	mounted, view, err := roomContentFS(g.cs, r.content)
	if err != nil {
		m.err = err
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &onlineRoomMapJob{cancel: cancel, done: make(chan onlineRoomMapResult, 1)}
	m.job = job
	packages, limit := r.content.packages, mapgenTNTLimit(g.cs)
	onlineWork.Add(1)
	go func() {
		defer onlineWork.Done()
		defer mounted.Close()
		defer cancel()
		made, err := regenerateMap(ctx, view, packages, limit, rec)
		job.done <- onlineRoomMapResult{made: made, err: err}
	}()
}

// pollRoomMap takes a finished map: it is mounted with the run's generated
// maps, so the lobby shows it and the battle draws it, and the remount
// resolves the room again on the next poll. It reports whether the lobby's
// status may have changed.
func (g *gameShell) pollRoomMap() bool {
	m := &g.online.room.roomMap
	job := m.job
	if job == nil {
		return false
	}
	select {
	case res := <-job.done:
		m.job = nil
		if res.err != nil {
			m.err = res.err
			return true
		}
		m.made = res.made
		if err := g.mountGeneratedMap(res.made); err != nil {
			m.err = err
		}
		return true
	default:
		return false
	}
}

// roomMapText is the lobby's line while this seat cannot hold the room's
// generated map yet, or "" when it holds it or the room has none.
func (g *gameShell) roomMapText() string {
	m := &g.online.room.roomMap
	switch {
	case len(m.recipe) == 0:
		return ""
	case m.job != nil:
		return "Making the room's map from its recipe..."
	case m.err != nil:
		return "The room's map could not be made here: " + noticeReason(m.err)
	}
	return ""
}

// roomGeneratedMap is the generated map a recorded room's recipe names, for
// a playback composing from the room's content: the one cs holds, or one
// made now on that content, which is what every seat made it on.
func roomGeneratedMap(cs *contentSet, plan roomContentPlan, raw []byte) (*generatedMap, error) {
	r, err := parseMapgenRecipe(raw)
	if err != nil {
		return nil, err
	}
	if made, err := heldGeneratedMap(cs, r); made != nil || err != nil {
		return made, err
	}
	mounted, view, err := roomContentFS(cs, plan)
	if err != nil {
		return nil, err
	}
	defer mounted.Close()
	return regenerateMap(context.Background(), view, plan.packages, mapgenTNTLimit(cs), r)
}
