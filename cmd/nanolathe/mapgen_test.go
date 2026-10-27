package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// mapgenFixtureWorld is an authored world: one rolling source map round a
// lake, its tiles flat colours by what lies under them.
func mapgenFixtureWorld() mapgen.World {
	const w, h, sea = 64, 64, 40
	src := mapgen.Source{Name: "fixture", Width: w, Height: h, SeaLevel: sea, Heights: make([]byte, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 90 + (x*7+y*3)%40 - 20
			if dx, dy := x-w/2, y-h/2; dx*dx+dy*dy < 100 {
				v = 20
			}
			src.Heights[y*w+x] = byte(v)
		}
	}
	src.TileIndices = make([]uint16, (w/2)*(h/2))
	for ty := 0; ty < h/2; ty++ {
		for tx := 0; tx < w/2; tx++ {
			src.TileIndices[ty*(w/2)+tx] = uint16(ty*(w/2) + tx)
			art := make([]byte, 1024)
			for i := range art {
				hgt := src.Heights[(2*ty+i/512)*w+2*tx+(i%32)/16]
				art[i] = 64 + hgt/2
				if hgt < sea {
					art[i] = 10
				}
			}
			src.TileGraphics = append(src.TileGraphics, art...)
		}
	}
	pal := mapgenFixturePalette()
	return mapgen.World{
		Name: "Test World", Sources: []mapgen.Source{src}, Palette: &pal,
		Trees: []mapgen.Feature{{Name: "TestTree", FootX: 1, FootZ: 1, Blocking: true}},
		Classes: []mapgen.MoveClass{{Name: "tanksh3", FootX: 3, FootZ: 3, MaxSlope: 15, BadSlope: 7,
			MaxWaterSlope: 15, BadWaterSlope: 7, MaxWaterDepth: 12, MinWaterDepth: -10000}},
		MinWind: 100, MaxWind: 2000, Gravity: 112, SurfaceMetal: 3,
	}
}

func mapgenFixturePalette() [256][3]byte {
	var pal [256][3]byte
	for i := range pal {
		pal[i] = [3]byte{byte(i / 2), byte(i), byte(i / 3)}
	}
	pal[10] = [3]byte{20, 40, 200}
	return pal
}

// mapgenFixtureContent is authored content the generator reads a world
// from as it reads a real one: a ground class, the palettes, a tree, and an
// archive of three Test World maps the fixture world made.
func mapgenFixtureContent(t *testing.T) string {
	t.Helper()
	base, _ := modFixture(t)
	writeModFixtureFile(t, base, "gamedata/moveinfo.tdf", "[CLASS0]\n{\n\tName=TANKSH3;\n\tFootprintX=3;\n\tFootprintZ=3;\n\tMaxWaterDepth=12;\n\tMaxSlope=15;\n\tBadSlope=7;\n\tMaxWaterSlope=15;\n\tBadWaterSlope=7;\n}\n")
	writeModFixtureFile(t, base, "features/z/fixture.tdf", "[TestTree]\n{\n\tfootprintx=1;\n\tfootprintz=1;\n\tenergy=250;\n\tblocking=1;\n\treclaimable=1;\n\tfilename=fixtrees;\n\tseqname=tree1;\n}\n")
	pal := mapgenFixturePalette()
	raw := make([]byte, 1024)
	for i, c := range pal {
		copy(raw[i*4:], c[:])
	}
	writeModFixtureFile(t, base, "palettes/palette.pal", string(raw))
	writeModFixtureFile(t, base, "palettes/guipal.pal", string(raw))
	gen, err := mapgen.NewGenerator(mapgenFixtureWorld())
	if err != nil {
		t.Fatal(err)
	}
	var files []vfs.ArchiveFile
	for i, name := range []string{"Fixture A", "Fixture B", "Fixture C"} {
		m, err := gen.Generate(context.Background(), mapgen.Params{Players: 2, Size: 6, Water: 1, Relief: 1, Metal: 1, Seed: uint64(5 + i)}, nil)
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
	if err := os.WriteFile(filepath.Join(base, "fixmaps.hpi"), archive.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return base
}

// generateFromContent makes a map of the fixture content's Test World.
func generateFromContent(t *testing.T, cs *contentSet, seed uint64) *mapgenResult {
	t.Helper()
	worlds, err := mapgenWorlds(cs.fs, cs.mapRoots)
	if err != nil || len(worlds) != 1 || worlds[0].Planet != "Test World" || len(worlds[0].Maps) != 3 {
		t.Fatalf("worlds %+v, err %v; want Test World with its three archive maps", worlds, err)
	}
	w, sources, err := mapgenWorld(context.Background(), cs.fs, worlds[0], maplibrary.MaxTNTBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 || sources[0].Archive != "fixmaps.hpi" || !strings.EqualFold(sources[0].Map, "maps/Fixture A.tnt") {
		t.Fatalf("sources %+v, want the archive's three maps in order", sources)
	}
	gen, err := mapgen.NewGenerator(w)
	if err != nil {
		t.Fatal(err)
	}
	m, err := gen.Generate(context.Background(), mapgen.Params{Players: 2, Size: 6, Relief: 1, Metal: 1, Seed: seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &mapgenResult{m: m, world: worlds[0].Planet, class: w.Classes[0].Name, sources: sources}
}

// A recipe makes its map again from exactly its source maps; a source that
// changed is named instead of quietly making another map.
func TestRecipeMakesItsMapAgain(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	r := generateFromContent(t, cs, 11)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	again, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, made.recipe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.tnt, made.tnt) || !bytes.Equal(again.ota, made.ota) {
		t.Fatal("the recipe made other files")
	}

	changed := made.recipe
	changed.Sources = slices.Clone(changed.Sources)
	changed.Sources[1].TNT = strings.Repeat("0", 64)
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, changed); err == nil || !strings.Contains(strings.ToLower(err.Error()), "fixture b") {
		t.Fatalf("a changed source map: %v, want an error naming it", err)
	}
	other := made.recipe
	other.Generator = mapgen.Version + 1
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, other); err == nil || !strings.Contains(err.Error(), "generator") {
		t.Fatalf("another generator version: %v", err)
	}
	wrong := made.recipe
	wrong.TNT = strings.Repeat("0", 64)
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, wrong); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("a recipe whose files differ: %v", err)
	}
	// The archive is part of a source's identity, and a recipe from a save
	// or a replay file never makes the generator read more than it would.
	moved := made.recipe
	moved.Sources = slices.Clone(moved.Sources)
	moved.Sources[0].Archive = "elsewhere.hpi"
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, moved); err == nil || !strings.Contains(err.Error(), "elsewhere.hpi") {
		t.Fatalf("a source from another archive: %v, want an error naming it", err)
	}
	twice := made.recipe
	twice.Sources = append(slices.Clone(twice.Sources), twice.Sources[0])
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, twice); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a source named twice: %v", err)
	}
	many := made.recipe
	for len(many.Sources) <= mapgenMaxSources {
		many.Sources = append(many.Sources, mapgenSourceID{Map: fmt.Sprintf("maps/extra %d.tnt", len(many.Sources))})
	}
	if _, err := regenerateMap(context.Background(), cs.fs, cs.mapRoots, maplibrary.MaxTNTBytes, many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("more sources than the generator reads: %v", err)
	}
}

// A map library package never feeds the generator, even an archive one
// mounted before the install: what a machine has downloaded must not change
// what a code makes (docs/DESIGN_MAP_GENERATOR.md §10).
func TestMapLibraryArchivesNeverFeedTheGenerator(t *testing.T) {
	base := mapgenFixtureContent(t)
	archive, err := os.ReadFile(filepath.Join(base, "fixmaps.hpi"))
	if err != nil {
		t.Fatal(err)
	}
	pkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "downloaded.ufo"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(base, "fixmaps.hpi")); err != nil {
		t.Fatal(err)
	}
	// The install keeps one archive map of its own; the package, mounted
	// first as the map library is, holds the three fixture maps.
	files := []vfs.ArchiveFile{}
	src := vfs.New()
	defer src.Close()
	if _, err := src.MountArchiveReader("fixmaps.hpi", bytes.NewReader(archive), int64(len(archive)), 0, vfs.ArchiveOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".ota", ".tnt"} {
		data, err := src.ReadFileLimit("maps/Fixture A"+ext, maplibrary.MaxTNTBytes)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, vfs.ArchiveFile{Path: "maps/Own Map" + ext, Data: data})
	}
	var own bytes.Buffer
	if err := vfs.WriteArchive(&own, files, vfs.ArchiveWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "own.hpi"), own.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := vfs.New()
	defer fs.Close()
	if err := fs.MountGameRoots([]vfs.GameRoot{{Dir: pkg}, {Dir: base}}); err != nil {
		t.Fatal(err)
	}
	worlds, err := mapgenWorlds(fs, []string{pkg})
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 || len(worlds[0].Maps) != 1 || worlds[0].Maps[0].TNTProvenance.ProviderID != "own.hpi" {
		t.Fatalf("worlds %+v, want only the install's own archive map", worlds)
	}
}

// Use mounts the map from memory below everything else and selects it; a
// generated map never feeds the generator.
func TestGeneratedMapMountsFromMemory(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	defer func() { g.releaseAudio(); g.cs.Close() }()
	r := generateFromContent(t, cs, 12)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.mountGeneratedMap(made); err != nil {
		t.Fatal(err)
	}
	name := made.recipe.Name
	if !slices.Contains(g.maps, name) {
		t.Fatalf("%q is not listed: %v", name, g.maps)
	}
	info, err := g.cs.unmappedMount.Stat("maps/" + name + ".tnt")
	if err != nil || info.Source.ProviderID() != made.provider() {
		t.Fatalf("terrain from %v (%v), want %s", info.Source.ProviderID(), err, made.provider())
	}
	data, err := g.cs.fs.ReadFileLimit("maps/"+name+".tnt", maplibrary.MaxTNTBytes)
	if err != nil || !bytes.Equal(data, made.tnt) {
		t.Fatalf("mounted terrain differs (%v)", err)
	}
	if got := g.cs.generatedRecipe(name); got == nil || got.Code != r.m.Code {
		t.Fatalf("recipe of the mounted map: %+v", got)
	}
	if g.mapNameTaken(made) {
		t.Fatal("Use again of a map held in memory is refused")
	}
	worlds, err := mapgenWorlds(g.cs.fs, g.cs.mapRoots)
	if err != nil || len(worlds) != 1 || len(worlds[0].Maps) != 3 {
		t.Fatalf("a generated map feeds the generator: %+v (%v)", worlds, err)
	}
}

// Save keeps the map in the library as a package whose receipt records the
// recipe; it is listed, its recipe is found by name, and the picker can
// remove it as a downloaded map.
func TestGeneratedMapSaves(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	defer func() { g.releaseAudio(); g.cs.Close() }()
	r := generateFromContent(t, cs, 13)
	made, err := newGeneratedMap(r.m, r.world, r.sources, "Lakeside Duel", "Two bases round a lake")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.saveGeneratedMap(made); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(g.maps, "Lakeside Duel") {
		t.Fatalf("the saved map is not listed: %v", g.maps)
	}
	root, _ := maplibrary.DefaultRoot()
	installed, err := (&modlibrary.Library{Root: root}).Installed()
	if err != nil || len(installed) != 1 || installed[0].Receipt.Source != "generated:"+r.m.Code || installed[0].Receipt.Recipe == "" {
		t.Fatalf("installed %+v (%v), want one package from generated:%s with its recipe", installed, err, r.m.Code)
	}
	got := g.cs.generatedRecipe("Lakeside Duel")
	if got == nil || got.TNT != made.recipe.TNT || got.OTA != made.recipe.OTA || len(got.Sources) != 3 {
		t.Fatalf("recipe found by name: %+v", got)
	}
	if _, target := g.removableLibraryMap("maps/Lakeside Duel.ota"); target == nil {
		t.Fatal("the picker cannot remove a saved generated map")
	}
	if got := g.cs.generatedRecipe("Fixture A"); got != nil {
		t.Fatalf("an archive map has a recipe: %+v", got)
	}
	// Use would hide nothing behind the saved map's own files, but refuses
	// to put other files under its name.
	if g.mapNameTaken(made) {
		t.Fatal("the saved map's own files count as another map")
	}
	other := generateFromContent(t, g.cs, 31)
	hidden, err := newGeneratedMap(other.m, other.world, other.sources, "Lakeside Duel", "")
	if err != nil {
		t.Fatal(err)
	}
	if !g.mapNameTaken(hidden) {
		t.Fatal("Use would hide another map under a saved map's name")
	}
	// A description that would open a TDF comment is refused by the form
	// with its reason, before anything is written.
	s := &mapgenScreen{open: true, naming: true, name: "Comment Duel", description: "see //here"}
	s.job.result = other
	s.save(g)
	if s.message != "Leave // and /* out of the description" || slices.Contains(g.maps, "Comment Duel") {
		t.Fatalf("a description with // saved, or refused with %q", s.message)
	}
}

// A battle restart's archive remount keeps the run's generated maps, held
// only in memory, so a battle on one restarts on it (DESIGN_MAP_GENERATOR §10).
func TestBattleRestartKeepsGeneratedMaps(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := openContent(Options{Root: base, Mod: "none", ModSet: true})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	defer func() { g.releaseAudio(); _ = g.cs.Close() }()
	r := generateFromContent(t, cs, 21)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.mountGeneratedMap(made); err != nil {
		t.Fatal(err)
	}
	if !g.prepareBattleRestartContent() {
		t.Fatal("restart remount failed")
	}
	if got := g.cs.generatedRecipe(made.recipe.Name); got == nil || got.TNT != made.recipe.TNT {
		t.Fatalf("after the restart remount the generated map is %+v", got)
	}
}

// A burst of requests ends on the latest one's map, whose world's index the
// job keeps; stopping joins the work and lets the index go, and nothing
// arrives after it (DESIGN_MAP_GENERATOR §10).
func TestGeneratorJobKeepsTheLatestRequest(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var j mapgenJob
	world := content.CanonicalKey("Test World")
	p := mapgen.Params{Players: 2, Size: 6, Relief: 1, Metal: 1, Seed: 1}
	// The request comes before the bind so the listing cannot finish first:
	// started after the bind, it raced the listing goroutine.
	j.start(world, p)
	j.bind(cs)
	defer j.bind(nil)
	for _, ready, _ := j.listed(); !ready; _, ready, _ = j.listed() {
		time.Sleep(time.Millisecond)
	}
	if !j.waiting() {
		t.Fatal("a request made before the worlds were listed is not waiting")
	}
	for seed := uint64(100); seed < 106; seed++ {
		p.Seed = seed
		j.start(world, p)
	}
	for _, running, _, _, _, _ := j.state(); running; _, running, _, _, _, _ = j.state() {
		time.Sleep(time.Millisecond)
	}
	r, _, _, _, _, err := j.state()
	if err != nil || r == nil || r.m.Code != mapgen.Code("Test World", p) {
		t.Fatalf("result %v (%v), want the latest request's map", r, err)
	}
	j.mu.Lock()
	kept := j.gen != nil && j.genWorld == world && j.indexing == ""
	j.mu.Unlock()
	if !kept {
		t.Fatal("the latest world's index is not kept")
	}
	p.Seed = 999
	j.start(world, p)
	j.stop()
	after, _, _, _, _, _ := j.state()
	j.mu.Lock()
	gone := j.gen == nil
	j.mu.Unlock()
	if after != r || !gone {
		t.Fatalf("after stop: result changed %v, index kept %v", after != r, !gone)
	}
}

// A library refresh closes an open generator panel, joining its work,
// before it closes the content that work reads (DESIGN_MAP_GENERATOR §10).
func TestLibraryRefreshClosesTheGenerator(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	defer func() { g.releaseAudio(); _ = g.cs.Close() }()
	previous := mapgenScreenInst
	s := newMapgenScreen(func() *gameShell { return g })
	mapgenScreenInst = s
	defer func() { mapgenScreenInst = previous }()
	s.open, s.world, s.p = true, content.CanonicalKey("Test World"), mapgen.Params{Players: 2, Size: 6, Relief: 1, Metal: 1, Seed: 7}
	s.job.bind(cs)
	s.regenerate()
	if err := g.refreshMapLibrary(); err != nil {
		t.Fatal(err)
	}
	if s.open || s.job.cs != nil {
		t.Fatalf("after a refresh the panel is open %v, bound %v", s.open, s.job.cs != nil)
	}
}

// Names stay inside what a map file name and a TDF value may hold, whatever
// a world calls itself; typed names are checked the same way.
func TestGeneratedMapNames(t *testing.T) {
	m := &mapgen.Map{Code: "x", Params: mapgen.Params{Players: 10}}
	for _, world := range []string{"Green Planet", "Crude Metal", "[V] Ice; Moon {2}", strings.Repeat("Very Long World ", 8), "Ωmega"} {
		name := mapgenName(world, m)
		if !mapgenNameValid(name) || !strings.HasPrefix(name, "Gen ") || !strings.HasSuffix(name, " 10p "+mapgenTag("x")) {
			t.Errorf("%q named %q", world, name)
		}
	}
	for name, want := range map[string]bool{"Lakeside Duel": true, "Two Rivers (v2)": true, "": false, " padded": false, "semi;colon": false, "brace{": false, strings.Repeat("x", mapgenNameRunes+1): false} {
		if mapgenNameValid(name) != want {
			t.Errorf("%q valid %v, want %v", name, !want, want)
		}
	}
	if got := teamSteps(6); !slices.Equal(got, []int{0, 2, 3}) {
		t.Errorf("teams for 6 players: %v", got)
	}
	if stepIn(mapgenSizes, 24, 1) != 6 || stepIn(mapgenSizes, 6, -1) != 24 || stepIn(mapgenSizes, 7, 1) != 8 {
		t.Error("size steps do not wrap")
	}
	if id := mapgenPackageID(mapgenRecipe{Code: "a-2-0-8-1-1-1-5-v1", Name: "Lakeside Duel"}); !strings.HasPrefix(id, "gen-") || strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		t.Errorf("package id %q", id)
	}
}

// Only a generated receipt that names a code is removable beside downloads.
func TestGeneratedReceiptIsRemovable(t *testing.T) {
	item := func(source string) modlibrary.Mod {
		return modlibrary.Mod{Receipt: modlibrary.Receipt{Source: source, SHA256: strings.Repeat("a", 64), Size: 10}}
	}
	for source, want := range map[string]bool{
		"generated:green_planet-2-0-8-0-1-1-5-v1": true,
		"https://nanolathe.gg/maps/x.zip":         true,
		"generated:":                              false,
		"local:x.zip":                             false,
	} {
		if got := downloadReceipt(item(source)); got != want {
			t.Errorf("%q removable %v, want %v", source, got, want)
		}
	}
}

// Generate stands beside More maps above the minimap, or above it where the
// pair does not fit the minimap's width; neither covers the other.
func TestGenerateButtonBesideMoreMaps(t *testing.T) {
	for _, previewW := range []int32{120, 300} {
		w := &gui.Window{Gadgets: []gui.Gadget{{Name: "HEADER"}, {Name: "LOAD", Text: "Load", Rect: gui.Rect{X: 200, Y: 300, W: 120, H: 24}}, {Name: "MAPPIC", Rect: gui.Rect{X: 200, Y: 100, W: previewW, H: 120}}}}
		installGetMapsButton(w)
		more, gen := w.GadgetIndex("GETMAPS"), w.GadgetIndex("GENMAP")
		if more < 0 || gen < 0 || w.Gadgets[gen].Text != "Generate" {
			t.Fatalf("preview %d: no Generate beside More maps", previewW)
		}
		a, b, preview := w.Gadgets[more].Rect, w.Gadgets[gen].Rect, w.Gadgets[w.GadgetIndex("MAPPIC")].Rect
		overlap := a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
		if overlap || b.Y+b.H >= preview.Y || a.Y+a.H >= preview.Y {
			t.Fatalf("preview %d: More maps %+v and Generate %+v over the minimap %+v", previewW, a, b, preview)
		}
	}
}

// A save's recipe finds its map mounted with the same files, makes it again
// when it is gone, and refuses a map of that name with other files.
func TestRecoverGeneratedMap(t *testing.T) {
	base := mapgenFixtureContent(t)
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuSkirmish), setup: newSkirmishMenuConfig("")}
	defer func() { g.releaseAudio(); g.cs.Close() }()
	r := generateFromContent(t, cs, 14)
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(made.recipe)
	if err != nil {
		t.Fatal(err)
	}
	again, err := g.recoverGeneratedMap(raw)
	if err != nil || again == nil || !bytes.Equal(again.tnt, made.tnt) || again.recipe.Name != made.recipe.Name {
		t.Fatalf("a missing map: made %v, err %v; want it made again", again != nil, err)
	}
	if err := g.mountGeneratedMap(again); err != nil {
		t.Fatal(err)
	}
	if again, err := g.recoverGeneratedMap(raw); err != nil || again != nil {
		t.Fatalf("a mounted map: made %v, err %v; want it used as it is", again != nil, err)
	}
	other := made.recipe
	other.TNT = strings.Repeat("0", 64)
	raw, _ = json.Marshal(other)
	if _, err := g.recoverGeneratedMap(raw); err == nil || !strings.Contains(loadFailureMessage(err), "differs") {
		t.Fatalf("a map of that name with other files: %v", err)
	}
	if _, err := g.recoverGeneratedMap([]byte(`{"name":"bad;name"}`)); err == nil {
		t.Fatal("an unreadable record was accepted")
	}
}
