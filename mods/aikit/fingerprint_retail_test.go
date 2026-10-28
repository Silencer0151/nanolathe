//go:build retail

package aikit_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
)

// The Modern AI's online fingerprint lock. Every other fingerprint lock is
// a battle of Classic players (DESIGN_GAMEPLAY_RULES "The Modern AI
// controller"), so none of them sees the util+tac controller, its worker or
// its private generator. Online, every client runs every Modern computer, so
// the controller's decisions must come out the same on every architecture a
// seat can run on: tools/check-retail runs this test natively, from an amd64
// build and from a js/wasm build with GOMAXPROCS=1, where the worker cannot
// run beside the simulation at all (DESIGN_MULTIPLAYER §5.1, M1-C11).
//
// The scene is TestOnlineModernComputersAgreeAcrossClientsRetail's
// composition and span on one client, seat 0: two Modern computers and a
// Classic one hosted by seat 0, both humans' scripted commands, six minutes
// in which the Modern computers build and lose units in fights and the
// Classic computer and seat 0 are destroyed. The run costs about 1.3 s
// natively and 6 s under Node. The constants are Nanolathe values recorded
// from a native darwin/arm64 run and confirmed on darwin/amd64 (Rosetta) and
// js/wasm (Node 24, GOMAXPROCS=1):
//
//   - lockModernAIRun is the run's digest: every receipt, the play-test unit
//     checksum and both random streams every 30 ticks, the final partial
//     fingerprint, and each Modern controller's applied-command outcomes and
//     generator position;
//   - lockModernAIRehearsal is the pre-start rehearsal digest the relay
//     compares at Ready for the same room (DESIGN_MULTIPLAYER §16.7), which
//     internal/session's own rehearsal tests compute with a stand-in for the
//     Modern AI.
//
// The Modern AI work from the faction add-ons moves the run digest: the
// production breadth parts (utility README §13.16), the cloak switch
// (§14.1), and the play review's changes (§15): the constructor floor at
// every ambition, water-only fire left out of ground defence, enemy memory
// without mobile nanoframes, and the play parameter's parts. The rehearsal
// does not move.
//
// The run digest folds each controller's aikit.ApplyStats as text, so a
// new counter there moves it with the game unchanged: the walls work's
// breach counter and its command kind's failure slot did, with the unit
// checksum, both streams, the partial fingerprint, the generators and the
// applied counts all as before.
//
// Modern's kill-speed and hit-chance target ranking and its turret threat
// policy (DESIGN_WEAPONS_PROJECTILES "Modern threat targeting and incoming fire") also move
// the run digest. The updated value agrees on native arm64, amd64 under
// Rosetta and js/wasm with one runtime worker; rehearsal is unchanged.
//
// HOW TO UPDATE. A change to the Modern AI's decisions, to a Modern rule or to
// anything else this battle reaches moves these values; take the reported
// ones and say in the commit message which behaviour changed and why. A value
// that differs between architectures, GOMAXPROCS settings or runs of the same
// build is a lockstep defect, never a reason to update.
const (
	lockModernAITicks     = onlineModernSpan
	lockModernAIRun       = "4a0c3019b729f45156bc8825b1530b34153ae0624614eb1372b61ad34f172492"
	lockModernAIRehearsal = "15b9dfe98d242faf6a8811e701d35b1d1478a78a284f51f7d6ad7d565edc2dbc"
)

func TestOnlineModernAIFingerprintIsLocked(t *testing.T) {
	cat, _ := retailcat.Shared(t)
	setup := onlineModernSetup(cat, 0, true)
	inputs, config := onlineModernRoom(t, setup)

	begin := time.Now()
	rehearsal, err := session.RehearsalDigest(inputs, config)
	if err != nil {
		t.Fatal(err)
	}
	rehearsed := time.Since(begin)

	s := onlineModernReplica(t, inputs, config, 0)
	if hosts := modernHosts(t, s, setup); len(hosts) != 2 {
		t.Fatalf("%d Modern controllers, want 2", len(hosts))
	}
	begin = time.Now()
	out := runOnlineLockstep(t, []*session.Session{s}, setup, lockModernAITicks)[0]
	elapsed := time.Since(begin)
	if out.tick != lockModernAITicks {
		t.Fatalf("the battle ended at tick %d, before the lock's span", out.tick)
	}
	for row, st := range out.stats {
		if st.Applied == 0 {
			t.Fatalf("Modern computer %d applied no command: the lock no longer sees its decisions", row)
		}
	}
	t.Logf("rehearsal %s in %v; %d ticks %s in %v", hex.EncodeToString(rehearsal[:]), rehearsed, out.tick, out.digest, elapsed)
	if got := hex.EncodeToString(rehearsal[:]); got != lockModernAIRehearsal {
		t.Errorf("rehearsal digest with Modern computers = %s, want the locked %s", got, lockModernAIRehearsal)
	}
	if out.digest != lockModernAIRun {
		t.Errorf("%d-tick online run with Modern computers = %s, want the locked %s", lockModernAITicks, out.digest, lockModernAIRun)
	}
}
