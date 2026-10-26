//go:build retail

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/relay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The bonus unit archives a reference install may hold beside the base game
// (DESIGN_MODS_MUTATORS §5.7). The test only names them; it reads the
// player's own copies.
var roomBonusUnitArchives = []string{"afark.ufo", "aflea.ufo", "ascarab.ufo", "cormabm.ufo", "cornecro.ufo", "corplas.ufo"}

// roomInstallWithout links every archive of the reference install except
// those named into a fresh folder, a second install with fewer archives. It
// skips when the reference install holds none of them.
func roomInstallWithout(t *testing.T, retail string, left []string) string {
	t.Helper()
	entries, err := os.ReadDir(retail)
	if err != nil {
		t.Fatal(err)
	}
	dir, removed := t.TempDir(), 0
	for _, e := range entries {
		if e.IsDir() || !vfs.IsArchiveName(e.Name()) {
			continue
		}
		skip := false
		for _, name := range left {
			skip = skip || strings.EqualFold(e.Name(), name)
		}
		if skip {
			removed++
			continue
		}
		if err := os.Symlink(filepath.Join(retail, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if removed == 0 {
		t.Skip("the reference install holds none of the bonus unit archives")
	}
	return dir
}

// roomTwoInstalls opens a room through a real local relay with the host on
// hostRoot and a guest on guestRoot, each with its own content, on Ashap
// Plateau with the host's two Easy Modern computers, both humans on one team,
// and readies both. prepare may set the host up before it creates the room.
// It returns the two shells once both seats are ready or the guest's lobby
// blocks Ready.
func roomTwoInstalls(t *testing.T, hostRoot, guestRoot string, prepare ...func(host *gameShell)) (host, guest *gameShell) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	server, err := relay.ListenHostedWebSocket("127.0.0.1:0", relay.HostedConfig{InsecureLoopback: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	address := "ws://" + server.Addr() + "/relay"
	open := func(root string) *gameShell {
		opts := Options{Root: root, Seed: -1}
		cs, err := openContent(opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		opts.Root, opts.Roots = cs.root, cs.roots
		return onlineRetailShell(t, cs, opts)
	}
	host, guest = open(hostRoot), open(guestRoot)
	shells := []*gameShell{host, guest}
	for _, m := range host.maps {
		if strings.EqualFold(m, "ashap plateau") {
			host.setup.MapName = m
		}
	}
	host.setup.Difficulty = 0
	for _, f := range prepare {
		f(host)
	}
	if err := host.openOnlineScreen(); err != nil {
		t.Fatal(err)
	}
	host.onlineServer = address
	host.activateGadget("CREATE")
	awaitOnlineShells(t, "the host's lobby", func() bool { return host.online.phase == onlineInLobby }, host)
	code := host.online.room.lobby.Code()
	if err := guest.openOnlineScreen(); err != nil {
		t.Fatal(err)
	}
	guest.onlineServer = address
	guest.activateGadget("JOIN")
	guest.online.panel.SetText("ADDRESS", strings.ToLower(code[:3]+" "+code[3:]))
	guest.activateGadget("OK")
	awaitOnlineShells(t, "the guest's lobby", func() bool {
		return guest.online != nil && guest.online.phase == onlineInLobby && len(host.online.room.presentSeats()) == 2
	}, shells...)
	for i, g := range shells {
		r := &g.online.room
		before := r.state.Seats[r.lobby.Seat()].Team
		g.activateGadget("Allies" + string(rune('0'+i)))
		awaitOnlineShells(t, "a team change", func() bool { return r.state.Seats[r.lobby.Seat()].Team != before }, shells...)
	}
	for _, name := range []string{"Player2", "Player3", "Side2", "Allies2", "Allies2", "Allies3", "Allies3"} {
		host.activateGadget(name)
	}
	awaitOnlineShells(t, "the room's latest settings", func() bool {
		return guest.online.room.base.Digest() == host.online.room.base.Digest()
	}, shells...)
	// A room on a generated map: the guest makes it and mounts it first.
	awaitOnlineShells(t, "the room's generated map", func() bool {
		return guest.online.room.roomMap.job == nil && guest.online.room.contentCS == guest.cs
	}, shells...)
	if guest.onlineLobbyBlock() != "" {
		return host, guest
	}
	for _, g := range shells {
		g.activateGadget(lobbyReady)
	}
	end := time.Now().Add(60 * time.Second)
	for !host.online.room.state.Seats[0].Ready || !host.online.room.state.Seats[1].Ready {
		if time.Now().After(end) {
			t.Fatalf("never ready: host %q, guest %q", host.onlineLobbyStatus(host.onlineLobbyBlock()), guest.onlineLobbyStatus(guest.onlineLobbyBlock()))
		}
		for _, g := range shells {
			g.pollOnline()
		}
		time.Sleep(2 * time.Millisecond)
	}
	// The relay sets the mismatch bit in the same state that shows both
	// seats ready (§16.6.1).
	return host, guest
}

// The 2026-10-16 report: a host whose folder lacks Cavedog's bonus unit
// archives and a guest whose folder holds them compose the same battle. The
// room names the host's archives, the guest leaves its extras out, and both
// seats report equal identities and rehearsals, so the relay sees no
// mismatch. The guest's own catalog keeps its bonus unit; the battle's does
// not.
func TestOnlineRoomContentAgreesAcrossInstallsRetail(t *testing.T) {
	retail := testsupport.RetailRoot(t)
	host, guest := roomTwoInstalls(t, roomInstallWithout(t, retail, roomBonusUnitArchives), retail)
	h, g := host.online.room, guest.online.room
	if h.state.Mismatch || h.prepared == nil || g.prepared == nil {
		t.Fatalf("mismatch %v; host %q, guest %q", h.state.Mismatch, host.onlineLobbyStatus(""), guest.onlineLobbyStatus(""))
	}
	if h.prepared.identity != g.prepared.identity || h.prepared.rehearsal != g.prepared.rehearsal {
		t.Fatalf("digests differ: host %x/%x, guest %x/%x", h.prepared.identity, h.prepared.rehearsal, g.prepared.identity, g.prepared.rehearsal)
	}
	for _, e := range g.baseReq.RoomContent {
		for _, bonus := range roomBonusUnitArchives {
			if strings.EqualFold(e.Name, bonus) {
				t.Fatalf("the room names %s, which the host lacks", e.Name)
			}
		}
	}
	if _, ok := g.prepared.sess.Catalog.Unit("armfark"); ok {
		t.Fatal("the guest's battle has the FARK the host lacks")
	}
	if _, ok := g.cat.Unit("armfark"); !ok {
		t.Fatal("the guest's own catalog lost its FARK")
	}
}

// The other way round, the guest lacks the host's bonus archives: its lobby
// names them, and it cannot be ready.
func TestOnlineRoomContentNamesWhatAGuestLacksRetail(t *testing.T) {
	retail := testsupport.RetailRoot(t)
	_, guest := roomTwoInstalls(t, retail, roomInstallWithout(t, retail, roomBonusUnitArchives))
	block := guest.onlineLobbyBlock()
	if !strings.HasPrefix(block, "This game needs ") || !strings.Contains(strings.ToLower(block), "afark.ufo") {
		t.Fatalf("the guest's lobby says %q", block)
	}
	if guest.online.room.ready || guest.online.room.readyJob != nil {
		t.Fatal("the guest became ready without the room's content")
	}
}

// A room whose map comes from a downloaded package names that package, and
// a guest whose library lacks it downloads it from the map catalogue without
// asking, installs it, and resolves the room with nothing missing (§16.8).
// The map is an authored fixture served by a local catalogue; retail assets
// supply only the base game.
func TestOnlineRoomFetchesItsMapPackageRetail(t *testing.T) {
	retail := testsupport.RetailRoot(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ota := `[GlobalHeader]{missionname=Fixture Islands;missiondescription=An authored room fixture.;numplayers=2;SCHEMACOUNT=1;[Schema 0]{Type=Network 1;[specials]{[special0]{specialwhat=StartPos1;XPos=8;ZPos=8;}[special1]{specialwhat=StartPos2;XPos=24;ZPos=24;}}}}`
	zipped := mapDownloadFixtureArchive(t, "fixture-islands", map[string][]byte{
		"maps/Fixture Islands.ota": []byte(ota),
		"maps/Fixture Islands.tnt": mapDownloadFixtureTNT(),
	})
	entry := modfetch.Entry{ID: "fixture-islands", Name: "Fixture Islands", Version: "1", Map: "maps/Fixture Islands.ota",
		Archive: modfetch.Archive{URL: "/islands.zip", Size: int64(len(zipped)), SHA256: fmt.Sprintf("%x", sha256.Sum256(zipped))}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_ = json.NewEncoder(w).Encode(modfetch.Manifest{Schema: 1, Maps: []modfetch.Entry{entry}})
		case "/islands.zip":
			_, _ = w.Write(zipped)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer mapDownload.cancelNetwork()
	t.Setenv("NANOLATHE_MAP_CATALOG", server.URL+"/manifest.json")

	// The host has the package installed and plays its map.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := maplibrary.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "islands.zip")
	if err := os.WriteFile(archive, zipped, 0o600); err != nil {
		t.Fatal(err)
	}
	options := entry.InstallOptions()
	options.Validate = maplibrary.InstallValidator([]string{retail}, nil, entry.Map)
	if _, err := lib.InstallArchive(archive, options); err != nil {
		t.Fatal(err)
	}
	host, err := openContent(Options{Root: retail, Seed: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	archives, err := baseArchives(host)
	if err != nil {
		t.Fatal(err)
	}
	roomContent, err := onlineRoomContent(host, archives, nil, "Fixture Islands")
	if err != nil {
		t.Fatal(err)
	}
	last := roomContent[len(roomContent)-1]
	if last.Kind != session.MatchContentMapPackage || last.Name != entry.ID || last.Size != uint64(entry.Archive.Size) {
		t.Fatalf("the room does not name the map's package: %+v", last)
	}
	hostCat, err := host.compileCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := onlineSettings{rules: session.ModernRuleSetName, mapName: "Fixture Islands", location: 1, commanderDeath: 1}
	base, err := onlineBaseConfig(host, hostCat, settings, onlineCreationFrozen(host, [2]uint32{3, 5}, content.Mutators{}, content.Restrictions{}, nil, roomContent))
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Request().ContentProfile.TNTBytes; got < uint64(maplibrary.MaxTNTBytes) {
		t.Fatalf("a room on a downloaded map carries terrain cap %d", got)
	}

	// The guest's library is empty.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cs, err := openContent(Options{Root: retail, Seed: -1})
	if err != nil {
		t.Fatal(err)
	}
	guest := onlineRetailShell(t, cs, Options{Root: cs.root, Roots: cs.roots, Seed: -1})
	t.Cleanup(func() { _ = guest.cs.Close() })
	guestArchives, err := baseArchives(cs)
	if err != nil {
		t.Fatal(err)
	}
	guestCat, err := cs.compileCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := guest.openOnlineScreen(); err != nil {
		t.Fatal(err)
	}
	guest.openOnlineLobby(newFakeOnlineLobby(1, 0, 1), guestCat, base, "wss://relay.example.test/relay", guestArchives, nil)
	r := &guest.online.room
	if len(r.content.missing) != 1 || r.fetch.want.Name != entry.ID {
		t.Fatalf("missing %+v, fetching %q", r.content.missing, r.fetch.want.Name)
	}
	if block := guest.onlineLobbyBlock(); !strings.Contains(block, "for this game") {
		t.Fatalf("the lobby says %q while fetching", block)
	}
	deadline := time.Now().Add(30 * time.Second)
	for len(r.content.missing) > 0 || r.fetch.want.Name != "" {
		if time.Now().After(deadline) {
			t.Fatalf("never fetched: %q, failed %v", guest.onlineLobbyBlock(), r.fetch.failed)
		}
		guest.pollMapsFetch()
		guest.pollOnline()
		time.Sleep(2 * time.Millisecond)
	}
	if len(r.content.packages) != 1 || len(guest.cs.mapRoots) != 1 || r.mapMissing {
		t.Fatalf("packages %v, mounted %v, map missing %v", r.content.packages, guest.cs.mapRoots, r.mapMissing)
	}
	// The fetch has ended for good: a later map's package could be fetched.
	for range 5 {
		guest.pollMapsFetch()
		guest.pollOnline()
	}
	if r.fetch.want.Name != "" || len(r.fetch.failed) != 0 {
		t.Fatalf("the fetch is left fetching %q, failed %v", r.fetch.want.Name, r.fetch.failed)
	}
	if block := guest.onlineLobbyBlock(); strings.Contains(block, "needs") || strings.Contains(block, "for this game") {
		t.Fatalf("the lobby still says %q", block)
	}
}
