package visibility

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// A promoted online computer reads its own existing history. It must not
// carry its former host's explored map into ordinary play (§10.1).
func TestReplayTakeoverDetachesBorrowedHistory(t *testing.T) {
	s := newTestService(&world.Terrain{CellW: 64, CellH: 64}, ModeHistoryEnabled)
	s.EnableOwnerPerspectives()
	s.SetPerspectiveHost(2, 0)
	s.SetPerspectiveHost(3, 0)
	const hostX, ownX, z = 200 << 16, 400 << 16, 200 << 16
	hostCell := int((200>>5)*s.W + (200 >> 5))
	ownCell := int((200>>5)*s.W + (400 >> 5))
	s.wordMask[hostCell], s.wordMask[ownCell] = cellBit(0), cellBit(2)
	if !s.VisiblePoint(2, hostX, 0, z) || s.VisiblePoint(2, ownX, 0, z) {
		t.Fatal("fixture did not borrow host history")
	}
	s.TakeOverPerspective(2)
	if s.VisiblePoint(2, hostX, 0, z) || !s.VisiblePoint(2, ownX, 0, z) || !s.VisiblePoint(3, hostX, 0, z) {
		t.Fatal("takeover lost its own history, inherited host history or changed the other computer")
	}
	if s.wordMask[hostCell] != cellBit(0) || s.wordMask[ownCell] != cellBit(2) {
		t.Fatal("switching perspective rewrote explored history")
	}
}
