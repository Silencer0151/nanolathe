//go:build retail

package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
)

// A room on a generated map (DESIGN_MAP_GENERATOR §10): the host makes a map
// from Archipelago and opens the room on it; the guest, which does not hold
// the map, makes it from the room's recipe on the room's content. Both seats
// compose the same battle, the room names the archives of the map's sources,
// and the battle starts and runs on the map.
func TestOnlineRoomOnAGeneratedMapRetail(t *testing.T) {
	retail := testsupport.RetailRoot(t)
	var made *generatedMap
	host, guest := roomTwoInstalls(t, retail, retail, func(host *gameShell) {
		worlds, err := mapgenWorlds(host.cs.fs, host.cs.mapRoots)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return strings.EqualFold(w.Planet, "archipelago") })
		if i < 0 {
			t.Fatal("no Archipelago world")
		}
		world, sources, err := mapgenWorld(context.Background(), host.cs.fs, worlds[i], mapgenTNTLimit(host.cs), nil)
		if err != nil {
			t.Fatal(err)
		}
		gen, err := mapgen.NewGenerator(world)
		if err != nil {
			t.Fatal(err)
		}
		gen.Workers = mapgenWorkers()
		m, err := gen.Generate(context.Background(), mapgen.Params{Players: 4, Size: 10, Water: 1, Relief: 1, Metal: 1, Seed: 7}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if made, err = newGeneratedMap(m, worlds[i].Planet, sources, mapgenName(worlds[i].Planet, m), "An online test map"); err != nil {
			t.Fatal(err)
		}
		if err := host.mountGeneratedMap(made); err != nil {
			t.Fatal(err)
		}
		host.setup.MapName = made.recipe.Name
	})
	h, g := &host.online.room, &guest.online.room
	if len(g.baseReq.GeneratedMap) == 0 || g.baseReq.MapName != made.recipe.Name {
		t.Fatalf("the room's map is %q without its recipe", g.baseReq.MapName)
	}
	if h.state.Mismatch || h.prepared == nil || g.prepared == nil {
		t.Fatalf("mismatch %v; host %q, guest %q", h.state.Mismatch, host.onlineLobbyStatus(host.onlineLobbyBlock()), guest.onlineLobbyStatus(guest.onlineLobbyBlock()))
	}
	if h.prepared.identity != g.prepared.identity || h.prepared.rehearsal != g.prepared.rehearsal {
		t.Fatal("the seats composed different battles")
	}
	if len(g.content.generated) != 1 || !bytes.Equal(g.content.generated[0].tnt, made.tnt) || !guest.hasSkirmishMap(made.recipe.Name) {
		t.Fatal("the guest does not hold the host's map")
	}
	for _, id := range made.recipe.Sources {
		if !slices.ContainsFunc(g.baseReq.RoomContent, func(e session.MatchContentEntry) bool { return strings.EqualFold(e.Name, id.Archive) }) {
			t.Fatalf("the room does not name %s, the archive of source %s", id.Archive, id.Map)
		}
	}
	host.activateGadget("Start")
	shells := []*gameShell{host, guest}
	awaitOnlineShells(t, "every battle", func() bool { return host.battle != nil && guest.battle != nil }, shells...)
	for _, s := range shells {
		t.Cleanup(func() { s.teardownBattle(nil) })
		if !strings.EqualFold(s.battle.sess.Skirmish.MapName, made.recipe.Name) {
			t.Fatalf("a battle plays %q", s.battle.sess.Skirmish.MapName)
		}
	}
	pumpOnlineRetail(t, shells, 60, nil)
}
