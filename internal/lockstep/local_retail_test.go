//go:build retail

package lockstep

import (
	"context"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/netproto"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/relay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
)

// Exercise the actual network/codec/tick boundary, including a first click
// cancelled before its grant assigns a stream position (DESIGN_MULTIPLAYER §16.4).
func TestLocalDriversRelayCommandsRetail(t *testing.T) {
	for _, delay := range []time.Duration{0, 150 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) { testLocalDriversRelayCommandsRetail(t, delay) })
	}
}

func testLocalDriversRelayCommandsRetail(t *testing.T, delay time.Duration) {
	inputs, cfg := playtestInputs(t)
	server, err := relay.ListenLocalWithCommandDelay("127.0.0.1:0", delay)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var sessions [2]*session.Session
	var drivers [2]*LocalDriver
	var commanders [2]pool.Handle
	for seat := range sessions {
		s, err := session.NewPlaytestSkirmish(inputs, cfg, uint8(seat), nil)
		if err != nil {
			t.Fatal(err)
		}
		sessions[seat] = s
		if err = s.PrepareGrantedBattle(); err != nil {
			t.Fatal(err)
		}
		for _, u := range s.Units.IterSliced() {
			if u.Alive && u.Owner == uint8(seat) {
				commanders[seat] = u.Handle
				break
			}
		}
		c, err := relay.DialLocal(context.Background(), server.Addr(), relay.LocalHello{Seat: uint8(seat), Identity: netproto.Identity{Protocol: netproto.CommandSchemaVersion, Rules: netproto.RuleIdentity{Name: "modern"}}, InitialChecksum: s.UnitStateChecksum()})
		if err != nil {
			t.Fatal(err)
		}
		drivers[seat], err = NewLocalDriver(s, c)
		if err != nil {
			t.Fatal(err)
		}
		defer drivers[seat].Close()
		if seat == 0 {
			before := s.UnitStateChecksum()
			for i := 0; i < 10; i++ {
				if advanced, err := drivers[seat].Pump(); err != nil || advanced {
					t.Fatalf("advanced before both seats joined: %v/%v", advanced, err)
				}
			}
			if s.UnitStateChecksum() != before {
				t.Fatal("ungranted host updates changed simulation")
			}
		}
	}
	pumpUntil := func(tick uint32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for sessions[0].Clock.GlobalTick < tick || sessions[1].Clock.GlobalTick < tick {
			for seat, d := range drivers {
				if sessions[seat].Clock.GlobalTick >= tick {
					continue
				}
				if _, err := d.Pump(); err != nil {
					t.Fatal(err)
				}
				for _, r := range sessions[seat].DrainCommandReceipts() {
					if r.Outcome == session.CommandRejected {
						t.Fatal(r.Diagnostic)
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("relay did not deliver the requested tick")
			}
			time.Sleep(time.Millisecond)
		}
		if sessions[0].UnitStateChecksum() != sessions[1].UnitStateChecksum() {
			t.Fatal("replicas differ after relay commands")
		}
	}
	// A prior command guarantees the next seat's client sequence is not its
	// eventual global stream position.
	if _, err := drivers[0].Submit(session.HumanCommand{Kind: session.HumanStop, Stop: session.HumanStopCommand{Handles: []pool.Handle{commanders[0]}}}); err != nil {
		t.Fatal(err)
	}
	pumpUntil(2)
	if drivers[1].AppliedLocalSequence() != 0 {
		t.Fatal("a remote stream position advanced the local submission watermark")
	}
	u := sessions[1].Units.Unit(commanders[1])
	seq, err := drivers[1].Submit(session.HumanCommand{Kind: session.HumanOrder, Order: session.HumanOrderCommand{Handles: []pool.Handle{u.Handle}, Code: 2, Queued: true, TrackQueuedMove: true, Position: orders.ResolvePos{X: u.X + 128<<16, Y: u.Y, Z: u.Z}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drivers[1].Submit(session.HumanCommand{Kind: session.HumanCancelQueuedMove, CancelQueuedMove: session.HumanCancelQueuedMoveCommand{Handles: []pool.Handle{u.Handle}, Sequence: seq}}); err != nil {
		t.Fatal(err)
	}
	// Allow both the tracked move and its later cancellation to pass through
	// the configured delay, while ordinary grants keep advancing.
	cancelTick := uint32(4)
	if delay != 0 {
		cancelTick = 20
	}
	pumpUntil(cancelTick)
	if drivers[0].AppliedLocalSequence() != 1 {
		t.Fatal("a later remote grant changed the first seat's local watermark")
	}
	if drivers[1].AppliedLocalSequence() != seq+1 {
		t.Fatalf("local submission watermark = %d, want %d after move and cancel", drivers[1].AppliedLocalSequence(), seq+1)
	}
	if drivers[1].cancel != nil || drivers[1].moveSequence != 0 {
		t.Fatal("unassigned receipt cancellation did not finish")
	}
	for seat, d := range drivers {
		u := sessions[seat].Units.Unit(commanders[seat])
		if _, err := d.Submit(session.HumanCommand{Kind: session.HumanOrder, Order: session.HumanOrderCommand{Handles: []pool.Handle{u.Handle}, Code: 2, Position: orders.ResolvePos{X: u.X + 128<<16, Y: u.Y, Z: u.Z}}}); err != nil {
			t.Fatal(err)
		}
	}
	pumpUntil(31)
	// Explicit close is terminal even when the next complete grant is buffered.
	deadline := time.Now().Add(5 * time.Second)
	for len(drivers[0].reads) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no buffered grant")
		}
		time.Sleep(time.Millisecond)
	}
	before := sessions[0].UnitStateChecksum()
	_ = drivers[0].Close()
	if advanced, err := drivers[0].Pump(); err == nil || advanced {
		t.Fatalf("closed driver advanced: %v/%v", advanced, err)
	}
	if sessions[0].UnitStateChecksum() != before {
		t.Fatal("closed driver consumed buffered grant")
	}

}

func playtestInputs(t *testing.T) (*content.SimulationInputs, session.EffectiveMatchConfig) {
	t.Helper()
	cat, fs := retailcat.Shared(t)
	setup := session.DirectSkirmishConfig("ashap plateau")
	setup.RNGSimSeed, setup.RNGCrtSeed = 7, 11
	m, err := mission.LoadWithType(fs, mission.TypeSkirmish, setup.MapName, 0, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	room := session.MatchRoomInputs{ContentProfile: "retail",
		PlayerView:    session.MatchView{MinimumScale: 64, MaximumScale: 2048, FullMap: true},
		SpectatorView: session.MatchView{MinimumScale: 64, MaximumScale: 2048, FullMap: true},
		ReplayView:    session.MatchView{MinimumScale: 64, MaximumScale: 2048, FullMap: true},
		Policies:      session.MatchPolicies{Revision: 1, Scheduling: 1, Pacing: 1, Drop: 1, Audience: 1, RejoinGraceMilliseconds: 90000}}
	for i, schema := range cat.Maps[content.CanonicalKey(m.TerrainKey)].Schemas {
		if schema.Name == m.Schema.Name {
			room.MapSchema = uint32(i)
		}
	}
	room.Participants[0][0] = 1
	req, err := session.NewMatchConfigRequest(setup, session.SkirmishEntryOptions{}, room)
	if err != nil {
		t.Fatal(err)
	}
	req.Seats[1].Role, req.Seats[1].HostSeat = session.MatchRoleHuman, session.MatchHostNone
	req.Seats[1].ComputerKind, req.Seats[1].Difficulty = 0, 0
	req.Seats[1].AIParams = nil
	req.Seats[1].Participant[0] = 2
	cfg, err := session.ResolveMatchConfig(req)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := session.FreezeMatchInputs(fs, cat, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return inputs, cfg
}
