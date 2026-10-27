package main

// Room content (docs/DESIGN_MULTIPLAYER.md §16.8). The host names, in
// configuration field 16, the base archives and packages an online battle
// mounts; every seat, the host included, resolves those names against its
// own install and libraries and composes the battle's simulation from
// exactly them — never from an archive only it holds, nor from its
// install's loose files, which cannot be described or fetched.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// localArchive is one archive this install's base folders mount: its
// field-16 entry and where it is on this machine. mapOnly marks an archive
// every file of which is under maps/: it adds maps and nothing a battle on
// another map reads, so a room lists it only when it supplies the room's
// map (onlineRoomContent).
type localArchive struct {
	entry   session.MatchContentEntry
	path    string
	mapOnly bool
}

// baseArchives lists the archives cs mounts from its base install's folders,
// each by file name with its byte size and SHA-256, sorted as field 16 sorts
// them. An archive the mount rejected is not content and is not listed. When
// two base roots hold an archive of one name, the copy the mount lets win is
// the one listed. Hashing reads every archive the first time and is cached
// against its path, size and modification time (archiveDigest).
func baseArchives(cs *contentSet) ([]localArchive, error) {
	if cs == nil || cs.unmappedMount == nil {
		return nil, fmt.Errorf("nanolathe: listing the base archives failed: logical path <base install>, providers searched [none], expected mounted content")
	}
	roots := make(map[string]bool, len(cs.baseRoots))
	for _, root := range cs.baseRoots {
		roots[filepath.Clean(root)] = true
	}
	// An archive holding any file outside maps/ can change the catalog, so
	// it is never map-only.
	notMapOnly := make(map[string]bool)
	for _, e := range cs.unmappedMount.Entries() {
		if !e.IsDir && e.Source.ProviderType != "directory" && !strings.HasPrefix(strings.ToLower(e.Path), "maps/") {
			notMapOnly[filepath.Clean(e.Source.SourcePath)] = true
		}
	}
	seen := make(map[string]bool)
	var out []localArchive
	// Providers come best precedence first, so the first archive of a name
	// is the one the mount reads.
	for _, p := range cs.unmappedMount.Providers() {
		if p.Type == "directory" || !roots[filepath.Clean(filepath.Dir(p.ID))] || !vfs.IsArchiveName(p.ID) {
			continue
		}
		name := filepath.Base(p.ID)
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		size, digest, err := archiveDigest(p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, localArchive{
			entry:   session.MatchContentEntry{Kind: session.MatchContentBaseArchive, Name: name, Size: size, Digest: digest},
			path:    p.ID,
			mapOnly: !notMapOnly[filepath.Clean(p.ID)],
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].entry.Name) < strings.ToLower(out[j].entry.Name) })
	return out, nil
}

// archiveDigests is the process's memo of archive digests, loaded from and
// saved to the data directory's cache file.
var archiveDigests struct {
	mu     sync.Mutex
	loaded bool
	byPath map[string]archiveDigestRecord
}

// archiveDigestRecord is one cached digest: the archive's path, size and
// modification time when it was hashed, and its SHA-256.
type archiveDigestRecord struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	SHA256   string `json:"sha256"`
}

type archiveDigestFile struct {
	Schema   int                   `json:"schema"`
	Archives []archiveDigestRecord `json:"archives"`
}

// archiveDigestCachePath is the cache beside the mod and map libraries, or
// "" where there is no data directory (the browser).
func archiveDigestCachePath() string {
	if runtime.GOOS == "js" {
		return ""
	}
	root, err := modlibrary.DefaultRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(root), "archive-digests.json")
}

// archiveDigest is path's byte size and SHA-256, read from the cache while
// the file keeps the size and modification time it was hashed with. A cache
// that cannot be read or written only costs the hashing again.
func archiveDigest(path string) (uint64, [32]byte, error) {
	var digest [32]byte
	info, err := os.Stat(path)
	if err != nil {
		return 0, digest, fmt.Errorf("nanolathe: hashing an archive failed: logical path %s, providers searched [base install], expected a readable archive: %w", filepath.Base(path), err)
	}
	archiveDigests.mu.Lock()
	defer archiveDigests.mu.Unlock()
	cachePath := archiveDigestCachePath()
	if !archiveDigests.loaded {
		archiveDigests.loaded = true
		archiveDigests.byPath = make(map[string]archiveDigestRecord)
		if cachePath != "" {
			if data, err := os.ReadFile(cachePath); err == nil {
				var file archiveDigestFile
				if json.Unmarshal(data, &file) == nil && file.Schema == 1 {
					for _, r := range file.Archives {
						archiveDigests.byPath[r.Path] = r
					}
				}
			}
		}
	}
	if r, ok := archiveDigests.byPath[path]; ok && r.Size == info.Size() && r.Modified == info.ModTime().UnixNano() {
		if b, err := hex.DecodeString(r.SHA256); err == nil && len(b) == len(digest) {
			copy(digest[:], b)
			return uint64(r.Size), digest, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, digest, fmt.Errorf("nanolathe: hashing an archive failed: logical path %s, providers searched [base install], expected a readable archive: %w", filepath.Base(path), err)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return 0, digest, fmt.Errorf("nanolathe: hashing an archive failed: logical path %s, providers searched [base install], expected a readable archive: %w", filepath.Base(path), err)
	}
	copy(digest[:], h.Sum(nil))
	archiveDigests.byPath[path] = archiveDigestRecord{Path: path, Size: n, Modified: info.ModTime().UnixNano(), SHA256: hex.EncodeToString(digest[:])}
	if cachePath != "" {
		saveArchiveDigests(cachePath)
	}
	return uint64(n), digest, nil
}

// saveArchiveDigests writes the memo, sorted by path, through a temporary
// file so a reader never sees half of it. The caller holds the lock.
func saveArchiveDigests(path string) {
	file := archiveDigestFile{Schema: 1}
	for _, r := range archiveDigests.byPath {
		file.Archives = append(file.Archives, r)
	}
	sort.Slice(file.Archives, func(i, j int) bool { return file.Archives[i].Path < file.Archives[j].Path })
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// onlineRoomContent is field 16 for a room on mapName (§16.8): this
// install's base archives — a map-only archive only when it holds the room's
// map — then the official add-ons it mounts, those whose archives sit in its
// folder among them (roomAddons), then the downloaded map package that
// supplies the map, if one does, with the shared feature packages it
// declares. A map only loose files supply is refused, since no seat mounts
// them online.
func onlineRoomContent(cs *contentSet, archives []localArchive, known []knownAddon, mapName string) ([]session.MatchContentEntry, error) {
	holders, err := roomMapHolders(cs, mapName)
	if err != nil {
		return nil, err
	}
	addons, inFolder := roomAddons(cs, archives, known)
	entries := make([]session.MatchContentEntry, 0, len(archives)+len(addons)+2)
	for _, a := range archives {
		if a.mapOnly && !holders[filepath.Clean(a.path)] || inFolder[strings.ToLower(a.entry.Name)] {
			continue
		}
		entries = append(entries, a.entry)
	}
	entries = append(entries, addons...)
	packages, err := roomMapPackages(cs, mapName)
	if err != nil {
		return nil, err
	}
	entries = append(entries, packages...)
	sortRoomContent(entries)
	return entries, nil
}

// roomMapHolders are the archives, by path, that hold mapName's OTA or TNT.
// It refuses a map that only loose files in the install folder supply, which
// no seat mounts online; a map nothing supplies is the map checks' to name.
func roomMapHolders(cs *contentSet, mapName string) (map[string]bool, error) {
	holders := make(map[string]bool)
	if cs == nil || cs.unmappedMount == nil || strings.TrimSpace(mapName) == "" {
		return holders, nil
	}
	if r := cs.generatedRecipe(mapName); r != nil {
		// A generated map travels as its recipe; every seat makes it from
		// the archives its sources come from (DESIGN_MAP_GENERATOR §10).
		return roomRecipeHolders(cs, *r), nil
	}
	base := roomMapBase(mapName)
	found, playable := false, false
	for _, ext := range []string{".ota", ".tnt"} {
		for _, src := range cs.unmappedMount.Sources("maps/" + base + ext) {
			found = found || ext == ".ota"
			path := filepath.Clean(src.Source.SourcePath)
			switch {
			case src.Source.ProviderType != "directory":
				holders[path] = true
				playable = playable || ext == ".ota"
			case mapPackageRootOf(cs.mapRoots, path) != "", cs.mod != nil && mapPackageRootOf([]string{cs.mod.Dir}, path) != "":
				playable = playable || ext == ".ota"
			}
		}
	}
	if found && !playable {
		return nil, fmt.Errorf("nanolathe: naming the room's content failed: logical path online map, providers searched [%s], expected maps/%s.ota in an archive, a downloaded map package or the mod; loose files are not used online", strings.Join(cs.baseRoots, ", "), base)
	}
	return holders, nil
}

// roomMapBase is a map name's file stem, as the skirmish loader forms it.
func roomMapBase(mapName string) string {
	base := strings.TrimSpace(mapName)
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sortRoomContent puts entries in field 16's canonical order: by kind, then
// by name compared without regard to case.
func sortRoomContent(entries []session.MatchContentEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// roomMapPackages is the map package that supplies mapName in cs, and the
// dependencies the cached map catalogue says it requires — or, with no
// cached catalogue, every mounted dependency package. A map the base install
// or the mod supplies needs no package.
func roomMapPackages(cs *contentSet, mapName string) ([]session.MatchContentEntry, error) {
	if cs == nil || len(cs.mapRoots) == 0 || cs.unmappedMount == nil {
		return nil, nil
	}
	if cs.generatedRecipe(mapName) != nil {
		// A saved generated map travels as its recipe, not as its package.
		return nil, nil
	}
	base := roomMapBase(mapName)
	sources := cs.unmappedMount.Sources("maps/" + base + ".ota")
	if len(sources) == 0 {
		return nil, nil
	}
	root := mapPackageRootOf(cs.mapRoots, sources[0].Source.SourcePath)
	if root == "" {
		return nil, nil
	}
	libRoot, err := maplibrary.DefaultRoot()
	if err != nil {
		return nil, err
	}
	lib, err := maplibrary.Open(libRoot)
	if err != nil {
		return nil, err
	}
	installed, err := lib.Installed()
	if err != nil {
		return nil, err
	}
	pkg, ok := installedPackageAt(installed, root)
	if !ok {
		return nil, fmt.Errorf("nanolathe: naming the room's map package failed: logical path maps/%s.ota, providers searched [%s], expected an installed map package", base, root)
	}
	first, err := roomPackageEntry(pkg)
	if err != nil {
		return nil, err
	}
	out := []session.MatchContentEntry{first}
	var depDirs []string
	client := &modfetch.Client{CatalogURL: modfetch.MapCatalogURL(), CacheDir: lib.Root}
	if manifest, _, ok := client.CachedManifest(); ok {
		for _, m := range manifest.Maps {
			if m.ID != pkg.ID {
				continue
			}
			for _, id := range m.Requires {
				for _, other := range installed {
					if other.ID == id && containsDir(cs.mapRoots, other.Dir) {
						depDirs = append(depDirs, other.Dir)
					}
				}
			}
		}
	} else if deps, err := maplibrary.DependencyRoots(lib.Root); err == nil {
		for _, dir := range deps {
			if containsDir(cs.mapRoots, dir) {
				depDirs = append(depDirs, dir)
			}
		}
	}
	for _, dir := range depDirs {
		dep, ok := installedPackageAt(installed, dir)
		if !ok || dep.Dir == pkg.Dir {
			continue
		}
		entry, err := roomPackageEntry(dep)
		if err != nil {
			return nil, err
		}
		if !containsEntry(out, entry) {
			out = append(out, entry)
		}
	}
	return out, nil
}

// roomPackageEntry is an installed map package's field-16 entry: its
// catalogue id and version and the archive its receipt names. A package
// installed without an archive identity cannot be named for other seats.
func roomPackageEntry(pkg modlibrary.Mod) (session.MatchContentEntry, error) {
	entry := session.MatchContentEntry{Kind: session.MatchContentMapPackage, Name: pkg.ID, Version: pkg.Version}
	b, err := hex.DecodeString(strings.ToLower(pkg.Receipt.SHA256))
	if err != nil || len(b) != len(entry.Digest) || pkg.Receipt.Size <= 0 {
		return entry, fmt.Errorf("nanolathe: naming a map package failed: logical path %s, providers searched [%s], expected a package installed from its archive", pkg.ID, pkg.Dir)
	}
	copy(entry.Digest[:], b)
	entry.Size = uint64(pkg.Receipt.Size)
	return entry, nil
}

func installedPackageAt(installed []modlibrary.Mod, dir string) (modlibrary.Mod, bool) {
	for _, m := range installed {
		if sameDir(m.Dir, dir) {
			return m, true
		}
	}
	return modlibrary.Mod{}, false
}

// mapPackageRootOf is the package root among roots that holds path.
func mapPackageRootOf(roots []string, path string) string {
	clean := filepath.Clean(path)
	for _, root := range roots {
		r := filepath.Clean(root)
		if clean == r || strings.HasPrefix(clean, r+string(filepath.Separator)) {
			return root
		}
	}
	return ""
}

func sameDir(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func containsDir(dirs []string, dir string) bool {
	for _, d := range dirs {
		if sameDir(d, dir) {
			return true
		}
	}
	return false
}

func containsEntry(entries []session.MatchContentEntry, e session.MatchContentEntry) bool {
	for _, x := range entries {
		if x.Kind == e.Kind && strings.EqualFold(x.Name, e.Name) {
			return true
		}
	}
	return false
}

// onlineRoomLimits are the content limits a room's battle compiles under on
// every seat (field 9): the running config's own, with the terrain cap a
// downloaded map package raises when, and only when, the room lists one
// (DESIGN_MODS_MUTATORS §5.6).
func onlineRoomLimits(cs *contentSet, entries []session.MatchContentEntry) content.Limits {
	limits := cs.profileLimits
	if limits == (content.Limits{}) {
		// A content set built without a mount names only its limits.
		limits = cs.limits
	}
	for _, e := range entries {
		if e.Kind == session.MatchContentMapPackage {
			limits.TNTBytes = max(limits.TNTBytes, maplibrary.MaxTNTBytes)
			break
		}
	}
	return limits
}

// roomContentPlan is one seat's resolution of a room's content (§16.8):
// which of its archives and packages to mount, and what it lacks or holds
// in another copy.
type roomContentPlan struct {
	// archives maps each listed base archive this install holds, by file
	// name in lower case, to its path.
	archives map[string]string
	// packages are the listed packages' directories, in field-16 order.
	packages []string
	// missing are the entries this install cannot supply; different the
	// base archives it holds in other bytes.
	missing   []session.MatchContentEntry
	different []session.MatchContentEntry
	// addons are the listed add-ons, kept grouped so their audited content
	// selects the same layer and layout as a local mount; enable are add-ons the
	// library holds that this seat's content does not mount, and fetch the
	// missing ones the add-on catalogue offers (resolveRoomAddon).
	addons []addonlibrary.Addon
	enable []settings.AddonSelection
	fetch  []modfetch.Entry
	// generated is the room's generated map once this seat holds it, which
	// the battle's content mounts from memory (DESIGN_MAP_GENERATOR §10).
	generated []*generatedMap
}

// resolveRoomContent sorts each of entries into what this install holds, in
// the listed bytes or not, and what it lacks. local are this install's base
// archives (baseArchives); installed lists the map library, nil when it
// could not be read; known are the add-ons this seat can name.
func resolveRoomContent(cs *contentSet, local []localArchive, installed []modlibrary.Mod, known []knownAddon, entries []session.MatchContentEntry) roomContentPlan {
	plan := roomContentPlan{archives: make(map[string]string)}
	byName := make(map[string]localArchive, len(local))
	for _, a := range local {
		byName[strings.ToLower(a.entry.Name)] = a
	}
	for _, e := range entries {
		switch e.Kind {
		case session.MatchContentBaseArchive:
			a, ok := byName[strings.ToLower(e.Name)]
			switch {
			case !ok:
				plan.missing = append(plan.missing, e)
			case a.entry.Size != e.Size || a.entry.Digest != e.Digest:
				plan.archives[strings.ToLower(e.Name)] = a.path
				plan.different = append(plan.different, e)
			default:
				plan.archives[strings.ToLower(e.Name)] = a.path
			}
		case session.MatchContentMapPackage:
			if dir, ok := mountedPackage(cs, installed, e); ok {
				plan.packages = append(plan.packages, dir)
			} else {
				plan.missing = append(plan.missing, e)
			}
		case session.MatchContentAddon:
			resolveRoomAddon(cs, byName, known, e, &plan)
		default:
			plan.missing = append(plan.missing, e)
		}
	}
	return plan
}

// mountedPackage is the directory of the map package e names when this
// install holds it with the listed identity and its mount accepted it.
func mountedPackage(cs *contentSet, installed []modlibrary.Mod, e session.MatchContentEntry) (string, bool) {
	for _, m := range installed {
		if m.ID != e.Name || m.Version != e.Version || !containsDir(cs.mapRoots, m.Dir) {
			continue
		}
		got, err := roomPackageEntry(m)
		if err == nil && got.Digest == e.Digest && got.Size == e.Size {
			return m.Dir, true
		}
	}
	return "", false
}

// installedMapPackages lists the map library, or nil when there is none or
// it cannot be read; a seat without it simply lacks every map package.
func installedMapPackages() []modlibrary.Mod {
	if runtime.GOOS == "js" {
		return nil
	}
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	lib, err := maplibrary.Open(root)
	if err != nil {
		return nil
	}
	installed, err := lib.Installed()
	if err != nil {
		return nil
	}
	return installed
}

// roomContentFS mounts a room battle's simulation content: plan's packages
// below the base install, the base install's listed archives at their retail
// tiers without its loose files, and the room's mod last, under the running
// config's directory table. The caller closes the returned overlay once the
// battle frozen from it is gone.
func roomContentFS(cs *contentSet, plan roomContentPlan) (*vfs.FS, vfs.FSOps, error) {
	var paths []string
	var supplies []addonSupply
	for _, addon := range plan.addons {
		factions, err := addonlibrary.Factions(addon)
		if err != nil {
			return nil, nil, fmt.Errorf("nanolathe: reading the room's add-on failed: logical path %s, providers searched [%s], expected readable faction declarations: %w", addon.ID, strings.Join(addon.Paths, ", "), err)
		}
		modelsOnly, err := addonlibrary.ModelsOnly(addon)
		if err != nil {
			return nil, nil, fmt.Errorf("nanolathe: reading the room's add-on failed: logical path %s, providers searched [%s], expected readable model paths: %w", addon.ID, strings.Join(addon.Paths, ", "), err)
		}
		paths = append(paths, addon.Paths...)
		supplies = append(supplies, addonSupply{addon: addon, faction: len(factions) > 0, layer: addonLayerOf(addon, len(factions) > 0, modelsOnly)})
	}
	factions, models := factionArchives(supplies), modelArchives(supplies)
	roots := make([]vfs.GameRoot, 0, len(plan.packages)+len(cs.baseRoots)+1)
	for _, dir := range plan.packages {
		roots = append(roots, vfs.GameRoot{Dir: dir})
	}
	for i, root := range cs.baseRoots {
		admit := func(name string) bool {
			path, ok := plan.archives[strings.ToLower(name)]
			return ok && sameDir(filepath.Dir(path), root) && filepath.Base(path) == name
		}
		game := vfs.GameRoot{Dir: root, Archives: admit, NoLoose: true}
		if i == 0 {
			game.Extra = paths
			game.ExtraLayout = addonExtraLayout(cs.layout, factions, models)
			game.ExtraTier = addonExtraTier(factions, models)
		}
		roots = append(roots, game)
	}
	if cs.mod != nil {
		roots = append(roots, vfs.GameRoot{Dir: cs.mod.Dir})
	}
	mounted := vfs.New()
	if err := mounted.MountGameRoots(roots); err != nil {
		mounted.Close()
		return nil, nil, fmt.Errorf("nanolathe: mounting the room's content failed: logical path <room content>, providers searched [%s], expected the archives and packages the room names: %w", strings.Join(cs.baseRoots, ", "), err)
	}
	if err := mountGeneratedMaps(mounted, plan.generated); err != nil {
		mounted.Close()
		return nil, nil, err
	}
	return mounted, cs.layout.Apply(mounted), nil
}

// roomProducts groups field-16 file names into the products the lobby names
// (§16.8): The Core Contingency and Battle Tactics by their archives, every
// other archive and package by its own name.
var roomProducts = []struct {
	name  string
	files []string
}{
	{"The Core Contingency", []string{"ccdata.ccx", "ccmaps.ccx", "ccmiss.ccx"}},
	{"Battle Tactics", []string{"btdata.ccx", "btmaps.ccx", "tactics1.hpi", "tactics2.hpi", "tactics3.hpi", "tactics4.hpi", "tactics5.hpi", "tactics6.hpi", "tactics7.hpi", "tactics8.hpi"}},
}

// roomContentNames names entries for the player: each product whose
// archives appear, with them, and each other entry by file name or as the
// map package or add-on it is.
func roomContentNames(entries []session.MatchContentEntry) []string {
	var out []string
	used := make([]bool, len(entries))
	for _, p := range roomProducts {
		var files []string
		for i, e := range entries {
			if e.Kind != session.MatchContentBaseArchive {
				continue
			}
			for _, f := range p.files {
				if strings.EqualFold(e.Name, f) {
					files, used[i] = append(files, e.Name), true
				}
			}
		}
		if len(files) > 0 {
			out = append(out, p.name+" ("+strings.Join(files, ", ")+")")
		}
	}
	for i, e := range entries {
		if used[i] {
			continue
		}
		switch e.Kind {
		case session.MatchContentMapPackage:
			out = append(out, "the downloaded map package "+e.Name)
		case session.MatchContentAddon:
			out = append(out, "the add-on "+e.Name)
		default:
			out = append(out, e.Name)
		}
	}
	return out
}

// joinNames joins names as a sentence: "a", "a and b", "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// roomMissingText is the lobby's line for content this seat lacks.
func roomMissingText(missing []session.MatchContentEntry) string {
	return "This game needs " + joinNames(roomContentNames(missing)) + ", which you don't have."
}

// roomDifferentText is the lobby's line for archives this seat holds in
// other bytes than the host's.
func roomDifferentText(different []session.MatchContentEntry) string {
	names := roomContentNames(different)
	if len(names) == 1 {
		return "Your copy of " + names[0] + " differs from the host's."
	}
	return "Your copies of " + joinNames(names) + " differ from the host's."
}

// looseSimulationDirs are the install folder directories whose loose files
// an online battle leaves out (§16.8), for the lobby's notice only.
var looseSimulationDirs = []string{"units", "weapons", "features", "gamedata", "scripts", "objects3d", "anims", "maps", "download", "ai"}

// looseSimulationFiles reports whether an install folder holds a loose file
// in a simulation directory, which an online battle does not use.
func looseSimulationFiles(roots []string) bool {
	for _, root := range roots {
		for _, dir := range looseSimulationDirs {
			found := false
			_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
				if err != nil || found {
					return filepath.SkipAll
				}
				if !d.IsDir() {
					found = true
					return filepath.SkipAll
				}
				return nil
			})
			if found {
				return true
			}
		}
	}
	return false
}

// roomCatalog compiles a room's unrestricted catalog from its content on
// this install, under the room's limits: the catalog field 12's definition
// IDs index, as every seat's battle compiles it. The mount closes once the
// catalog, which keeps no reader, is compiled.
func roomCatalog(cs *contentSet, archives []localArchive, known []knownAddon, entries []session.MatchContentEntry) (*content.Catalog, error) {
	plan := resolveRoomContent(cs, archives, installedMapPackages(), known, entries)
	if len(plan.missing) > 0 {
		return nil, fmt.Errorf("nanolathe: compiling the room's catalog failed: logical path <room content>, providers searched [%s], expected %s", strings.Join(cs.baseRoots, ", "), joinNames(roomContentNames(plan.missing)))
	}
	mounted, view, err := roomContentFS(cs, plan)
	if err != nil {
		return nil, err
	}
	defer mounted.Close()
	return content.CompileWithOptions(view, content.Options{Limits: onlineRoomLimits(cs, entries)})
}
