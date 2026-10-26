package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/replay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// mapgenRoomFixture is mapgenFixtureContent with its support files in an
// archive too, as an install holds them: a room's content mounts archives
// only, never loose files (DESIGN_MULTIPLAYER §16.8).
func mapgenRoomFixture(t *testing.T) string {
	t.Helper()
	base := mapgenFixtureContent(t)
	var files []vfs.ArchiveFile
	for _, name := range []string{"gamedata/moveinfo.tdf", "gamedata/sidedata.tdf", "features/z/fixture.tdf", "palettes/palette.pal", "palettes/guipal.pal"} {
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, vfs.ArchiveFile{Path: name, Data: data})
	}
	var archive bytes.Buffer
	if err := vfs.WriteArchive(&archive, files, vfs.ArchiveWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "fixbase.hpi"), archive.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return base
}

// roomHost is a host's shell on base with a generated map mounted, and that
// map.
func roomHost(t *testing.T, base string) (*gameShell, *generatedMap) {
	t.Helper()
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	t.Cleanup(func() { g.releaseAudio(); g.cs.Close() })
	r := generateFromContent(t, g.cs, 21)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.mountGeneratedMap(made); err != nil {
		t.Fatal(err)
	}
	return g, made
}

// A room on a generated map lists the archive its sources come from, which
// holds only maps, and the battle's content serves the map from memory.
func TestRoomContentCarriesAGeneratedMap(t *testing.T) {
	g, made := roomHost(t, mapgenRoomFixture(t))
	name := made.recipe.Name
	if len(g.cs.generatedRecipeRecord(name)) == 0 {
		t.Fatal("the mounted generated map has no recipe")
	}
	archives, err := baseArchives(g.cs)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := onlineRoomContent(g.cs, archives, nil, name)
	if err != nil {
		t.Fatalf("the room refused its generated map: %v", err)
	}
	var listed []string
	for _, e := range entries {
		listed = append(listed, e.Name)
	}
	if !strings.Contains(strings.Join(listed, " "), "fixmaps.hpi") {
		t.Fatalf("the room lists %v, not the archive of the map's sources", listed)
	}
	plan := resolveRoomContent(g.cs, archives, nil, nil, entries)
	plan.generated = []*generatedMap{made}
	mounted, view, err := roomContentFS(g.cs, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer mounted.Close()
	if data, err := view.ReadFileLimit("maps/"+name+".tnt", mapgenTNTLimit(g.cs)); err != nil || !bytes.Equal(data, made.tnt) {
		t.Fatalf("the room's content does not serve the generated map (%v)", err)
	}
	// A playback of a recorded room makes the map again on that content
	// when this install does not hold it.
	plan.generated = nil
	other, err := mountContent(Options{}, []string{filepath.Dir(archives[0].path)}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	again, err := roomGeneratedMap(other, plan, g.cs.generatedRecipeRecord(name))
	if err != nil || !bytes.Equal(again.tnt, made.tnt) {
		t.Fatalf("the recorded room's map was not made again on its content: %v", err)
	}
}

// A recorded room on a generated map, on an install that does not hold the
// map: its content mount, which a playback and the load of a continued
// online replay's save share, makes the map and serves it; and a battle
// continued from that playback saves the recorded recipe, so the save loads
// onto the map (DESIGN_MULTIPLAYER §10.1).
func TestARecordedRoomKeepsItsGeneratedMap(t *testing.T) {
	useOnlineSessionSeams(t, 2)
	g, made := roomHost(t, mapgenRoomFixture(t))
	name := made.recipe.Name
	recipe := g.cs.generatedRecipeRecord(name)
	archives, err := baseArchives(g.cs)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := onlineRoomContent(g.cs, archives, nil, name)
	if err != nil {
		t.Fatal(err)
	}
	other, err := mountContent(Options{}, []string{filepath.Dir(archives[0].path)}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if len(other.generatedRecipeRecord(name)) != 0 {
		t.Fatal("the other install holds the map")
	}
	for _, withRecipe := range []bool{false, true} {
		var raw []byte
		if withRecipe {
			raw = recipe
		}
		mounted, view, err := mountRoomContent(other, entries, raw)
		if err != nil {
			t.Fatal(err)
		}
		data, err := view.ReadFileLimit("maps/"+name+".tnt", mapgenTNTLimit(other))
		mounted.Close()
		if served := err == nil && bytes.Equal(data, made.tnt); served != withRecipe {
			t.Fatalf("with the recipe %v, the room's content serves the map: %v (%v)", withRecipe, served, err)
		}
	}

	frozen := onlineCreationFrozen(g.cs, [2]uint32{1, 2}, content.Mutators{}, content.Restrictions{}, nil, entries)
	frozen.room.GeneratedMap = recipe
	base, err := onlineConfig(g.cs, onlineTestCatalog(), onlineSettings{mapName: name, location: 1, commanderDeath: 1}, onlinePlaceholderSeats(nil), frozen)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := session.EncodeMatchConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	shell := &gameShell{cs: other}
	shell.battle = &battleSession{sess: &session.Session{Skirmish: session.SkirmishConfig{MapName: name}}}
	if len(shell.battleRecipe()) != 0 {
		t.Fatal("a battle on a map this install does not hold has a recipe")
	}
	shell.battle.takeover = &replayTakeover{source: &replayPlayback{header: replay.Header{Config: encoded}}}
	if got := shell.battleRecipe(); !bytes.Equal(got, recipe) {
		t.Fatalf("the continued battle records %q, not the room's recipe", got)
	}
}

// A guest makes the room's map from its recipe on the room's content in the
// background, mounts it, and may then be ready; one whose source archive
// differs is told why it cannot.
func TestAGuestMakesTheRoomsGeneratedMap(t *testing.T) {
	useOnlineSessionSeams(t, 2)
	for _, tc := range []struct {
		name      string
		different bool
	}{{"same archives", false}, {"different maps archive", true}} {
		t.Run(tc.name, func(t *testing.T) {
			j := onlineTestShell(t)
			hostBase := mapgenRoomFixture(t)
			g, made := roomHost(t, hostBase)
			name := made.recipe.Name
			archives, err := baseArchives(g.cs)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := onlineRoomContent(g.cs, archives, nil, name)
			if err != nil {
				t.Fatal(err)
			}
			frozen := onlineCreationFrozen(g.cs, [2]uint32{1, 2}, content.Mutators{}, content.Restrictions{}, nil, entries)
			frozen.room.GeneratedMap = g.cs.generatedRecipeRecord(name)
			base, err := onlineConfig(g.cs, onlineTestCatalog(), onlineSettings{mapName: name, location: 1, commanderDeath: 1}, onlinePlaceholderSeats(nil), frozen)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(base.Request().GeneratedMap, frozen.room.GeneratedMap) {
				t.Fatal("the room's configuration does not carry the recipe")
			}

			// The guest's install: the same archives, without the map.
			guestBase := t.TempDir()
			for _, file := range []string{"fixbase.hpi", "fixmaps.hpi"} {
				data, err := os.ReadFile(filepath.Join(hostBase, file))
				if err != nil {
					t.Fatal(err)
				}
				if file == "fixmaps.hpi" && tc.different {
					data = otherMapsArchive(t)
				}
				if err := os.WriteFile(filepath.Join(guestBase, file), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cs, err := mountContent(Options{}, []string{guestBase}, modSelection{})
			if err != nil {
				t.Fatal(err)
			}
			j.cs.Close()
			j.cs = cs
			defer func() { j.releaseAudio(); j.cs.Close() }()
			guestArchives, err := baseArchives(j.cs)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.openOnlineScreen(); err != nil {
				t.Fatal(err)
			}
			fake := newFakeOnlineLobby(1, 0, 1)
			j.openOnlineLobby(fake, onlineTestCatalog(), base, "wss://relay.example.test/relay", guestArchives, nil)
			r := &j.online.room
			if r.roomMap.job == nil || !strings.Contains(j.onlineLobbyBlock(), "Making the room's map") {
				t.Fatalf("the guest is not making the room's map: %q (err %v)", j.onlineLobbyBlock(), r.roomMap.err)
			}
			deadline := time.Now().Add(time.Minute)
			for r.roomMap.job != nil || r.contentCS != j.cs {
				if time.Now().After(deadline) {
					t.Fatal("the room's map was not made within a minute")
				}
				j.pollOnline()
				time.Sleep(10 * time.Millisecond)
			}
			if tc.different {
				if block := j.onlineLobbyBlock(); !strings.Contains(block, "could not be made here") {
					t.Fatalf("a guest with other source maps is told %q", block)
				}
				return
			}
			if block := j.onlineLobbyBlock(); strings.Contains(block, "room's map") || strings.Contains(block, "you don't have") {
				t.Fatalf("the guest still waits for the map: %q", block)
			}
			if len(r.content.generated) != 1 || !bytes.Equal(r.content.generated[0].tnt, made.tnt) || !j.hasSkirmishMap(name) {
				t.Fatal("the guest does not hold the host's map")
			}
		})
	}
}

// otherMapsArchive is fixmaps.hpi's name over other Test World maps.
func otherMapsArchive(t *testing.T) []byte {
	t.Helper()
	gen, err := mapgen.NewGenerator(mapgenFixtureWorld())
	if err != nil {
		t.Fatal(err)
	}
	var files []vfs.ArchiveFile
	for i, name := range []string{"Fixture A", "Fixture B", "Fixture C"} {
		m, err := gen.Generate(context.Background(), mapgen.Params{Players: 2, Size: 6, Water: 1, Relief: 1, Metal: 1, Seed: uint64(50 + i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ota, err := m.OTA(name, "Authored source map")
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, vfs.ArchiveFile{Path: "maps/" + name + ".ota", Data: ota}, vfs.ArchiveFile{Path: "maps/" + name + ".tnt", Data: m.TNT})
	}
	var archive bytes.Buffer
	if err := vfs.WriteArchive(&archive, files, vfs.ArchiveWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// The host generates the room's map in the picker: Use mounts it and leaves
// the picker, and the room's next configuration carries its recipe and the
// archive its sources come from, while the host holds the map it made.
func TestAHostGeneratesTheRoomsMap(t *testing.T) {
	useOnlineSessionSeams(t, 2)
	h := onlineTestShell(t)
	cs, err := mountContent(Options{}, []string{mapgenRoomFixture(t)}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	h.cs.Close()
	h.cs = cs
	defer func() { h.releaseAudio(); h.cs.Close() }()
	archives, err := baseArchives(h.cs)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := onlineRoomContent(h.cs, archives, nil, "Fixture A")
	if err != nil {
		t.Fatal(err)
	}
	frozen := onlineCreationFrozen(h.cs, [2]uint32{1, 2}, content.Mutators{}, content.Restrictions{}, nil, entries)
	base, err := onlineConfig(h.cs, onlineTestCatalog(), onlineSettings{mapName: "Fixture A", location: 1, commanderDeath: 1}, onlinePlaceholderSeats(nil), frozen)
	if err != nil || len(base.Request().GeneratedMap) != 0 {
		t.Fatalf("a room on an authored map: %v, recipe %q", err, base.Request().GeneratedMap)
	}
	if err := h.openOnlineScreen(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeOnlineLobby(0, 0, 1)
	h.openOnlineLobby(fake, onlineTestCatalog(), base, "wss://relay.example.test/relay", archives, nil)

	r := generateFromContent(t, h.cs, 22)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	h.online.room.choosingMap = true
	if err := h.mountGeneratedMap(made); err != nil {
		t.Fatal(err)
	}
	h.setSetupMap(made.recipe.Name)
	h.leaveMapPicker()
	pushed, err := session.DecodeMatchConfig(fake.config)
	if err != nil {
		t.Fatal(err)
	}
	req := pushed.Request()
	if req.MapName != made.recipe.Name || !bytes.Equal(req.GeneratedMap, h.cs.generatedRecipeRecord(made.recipe.Name)) {
		t.Fatalf("the room's map is %q with recipe %q", req.MapName, req.GeneratedMap)
	}
	var listed []string
	for _, e := range req.RoomContent {
		listed = append(listed, e.Name)
	}
	if !strings.Contains(strings.Join(listed, " "), "fixmaps.hpi") {
		t.Fatalf("the room lists %v, not the archive of the map's sources", listed)
	}
	room := &h.online.room
	if room.roomMap.made == nil || room.roomMap.job != nil || strings.Contains(h.onlineLobbyBlock(), "room's map") {
		t.Fatalf("the host does not hold the map it made: %q", h.onlineLobbyBlock())
	}
}
