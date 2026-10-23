package session

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// ReplayTakeoverEligible checks the session half of replay takeover. The replay
// player additionally verifies that playback succeeded and owns a completed
// pump. Call only between pumps (DESIGN_MULTIPLAYER §10.1).
func (s *Session) ReplayTakeoverEligible(owner uint8) error {
	refuse := func(expected string) error {
		return fmt.Errorf("nanolathe: replay takeover refused: logical path player %d, providers searched [recorded battle], expected %s", int(owner)+1, expected)
	}
	if s == nil || s.Clock == nil || s.Units == nil || s.Econ == nil || s.State != StateBattle || s.IsPendingBattle() {
		return refuse("a prepared battle that has not ended")
	}
	if s.seatCommands.replay.takeover {
		return refuse("a replay that has not already been taken over")
	}
	if s.Mission != nil && s.Mission.Type == mission.TypeCampaign {
		return refuse("a skirmish or Survival replay")
	}
	if c := s.checkpoints; c != nil && (c.inPump || c.inCapture) {
		return refuse("a completed pump with no capture in progress")
	}
	p := s.playerRecord(int(owner))
	if p == nil || !p.Exists || p.IsObserver || p.Watcher || p.GameEnded || p.Side == 10 || (p.ControllerState != 1 && p.ControllerState != 2) {
		return refuse("a human or computer player who is still playing")
	}
	if s.isSurvivalAttacker(int(owner)) {
		return refuse("a Survival survivor; the wave attacker is scenario controlled")
	}
	if s.seatCommands.removed[owner] || s.Units.LiveCountForPlayer(int(owner)) == 0 {
		return refuse("a player with surviving units who has not been removed")
	}
	if s.onlineResults != nil {
		cfg := s.seatCommands.online
		if cfg == nil || int(owner) >= len(cfg.roles) || (cfg.roles[owner] != MatchRoleHuman && cfg.roles[owner] != MatchRoleComputer) || s.onlineSeatEnded(int(owner)) || s.onlineBattleEnded() {
			return refuse("a playing human or computer seat in the recorded online battle")
		}
	} else if s.Latch.IsEnding() || s.GetResult().Ended {
		return refuse("a battle whose result has not ended")
	}
	if s.seatCommands.lastPosition == ^uint64(0) || s.nextHumanSequence == ^uint64(0) || s.Clock.GlobalTick == ^uint32(0) {
		return refuse("remaining tick and command sequence numbers")
	}
	if len(s.replayTakeoverIdleHumans(owner)) != 0 && s.modernAI == nil && !modernAIRegistered() {
		return refuse("a build with the Modern AI computer player, which plays the recording's other humans")
	}
	return nil
}

// replayTakeoverIdleHumans lists, in ascending slot order, the recorded humans
// other than owner who are still playing: after a takeover nobody would give
// them orders, so the Modern AI plays them (DESIGN_MULTIPLAYER §10.1). A
// human already defeated, ending, watching, removed or without a manager is
// left as it is.
func (s *Session) replayTakeoverIdleHumans(owner uint8) []uint8 {
	var out []uint8
	for i := range s.Econ.Players {
		player := uint8(i)
		p := &s.Econ.Players[i]
		if player == owner || !p.Exists || p.ControllerState != 1 || p.IsObserver || p.Watcher || p.GameEnded || p.Side == 10 {
			continue
		}
		if i >= len(s.AI) || s.AI[i] == nil || s.AI[i].Passive || s.seatCommands.removed[player] || s.Units.LiveCountForPlayer(i) == 0 {
			continue
		}
		if s.onlineResults != nil && s.onlineSeatEnded(i) {
			continue
		}
		out = append(out, player)
	}
	return out
}

// TakeOverReplay retains the live world and transfers control to owner. The
// replay player calls it only at a completed pump; no recorder may consume the
// source afterwards. All failures precede mutation. The returned position is
// the floor for the local host's next online command (DESIGN_MULTIPLAYER §10.1).
func (s *Session) TakeOverReplay(owner uint8) (uint64, error) {
	if err := s.ReplayTakeoverEligible(owner); err != nil {
		return 0, err
	}
	idle := s.replayTakeoverIdleHumans(owner)
	if len(idle) != 0 {
		// Binding the step changes no game; it makes the controller choices
		// below infallible, so every failure still precedes mutation.
		if err := s.resolveModernAI(idle[0]); err != nil {
			return 0, err
		}
	}
	// A fork no longer has the recorded configuration identity. Retain the
	// composition bindings, but retire its diagnostic checkpoint history.
	s.DisableCheckpoints()
	if s.checkpointAdmission != nil {
		s.checkpointAdmission.ready = false
	}
	s.humanMu.Lock()
	position := max(s.seatCommands.lastPosition, s.nextHumanSequence)
	s.nextHumanSequence = position
	clear(s.pendingHuman)
	s.pendingHuman = nil
	s.seatCommands.receipts = nil
	s.seatCommands.replay.recorder = nil
	s.seatCommands.replay.pumpTicks = 0
	s.seatCommands.replay.takeover = true
	s.humanMu.Unlock()

	// Human managers still run weapon maintenance and strategic refresh.
	// Keep the manager and its cadence, but close any background Modern think
	// and return to the bound set's human path. Controller byte 1 gates tasks.
	if m := s.AI[owner]; m != nil {
		closeAIController(m)
		m.Controller, m.Planner = ai.ControllerClassic, s.Rules.Planner
	}
	s.Econ.Players[owner].ControllerState = 1
	if s.onlineResults != nil {
		s.seatCommands.online.roles[owner] = MatchRoleHuman
		if !s.onlineResults.seats[owner].present {
			s.onlineResults.seats[owner] = onlineSeatResult{present: true, latch: NewEndLatch()}
			s.Econ.Players[owner].GameEnded = false
			s.Econ.Players[owner].EndGameCountdown = -1
		}
		s.onlineResults.hosted[owner] = -1
	} else if owner != s.LocalOwner {
		// A pending local result belongs to the old human. Do not transfer
		// their countdown to the new human, or touch pending unit deaths.
		s.clearPendingResult()
		s.Latch = NewEndLatch()
		s.VictoryDone, s.DefeatDone = false, false
		s.publishEndCountdown()
	}
	// Every other recorded human who is still playing becomes a Modern AI
	// computer player on its existing manager, with no new units, resources
	// or random draws. Online it keeps its seat row, so its machine's
	// countdown, sensor pass and hosted computers carry on as before.
	for _, player := range idle {
		s.Econ.Players[player].ControllerState = 2
		if s.onlineResults != nil {
			s.seatCommands.online.roles[player] = MatchRoleComputer
		}
		_ = s.setAIController(s.AI[player], ai.ControllerModern) // the step is bound above
	}
	// The selected player is credited as a human: no Modern full-income mark
	// and no computer seat's own discount word, which the lobby row would
	// otherwise project again (projectComputerIncome).
	s.projectFullIncomePlayers()
	s.Econ.ClearPlayerSelector(owner)
	changedView := owner != s.ViewingOwner
	s.LocalOwner, s.ViewingOwner = owner, owner
	if s.Vis != nil {
		s.Vis.TakeOverPerspective(visibility.PlayerID(owner))
	}
	if changedView && s.onlineResults == nil {
		// The single-player bank is stored on units. Do not inherit the old
		// viewer's radar contacts, or run an extra sensor pass (which could
		// extend shared cloak deadlines). Ordinary dues refresh contacts.
		for _, u := range s.Units.IterSliced() {
			u.Flags &^= visibility.FriendlyMask | visibility.JammedBit
			if u.Owner == owner {
				u.Flags |= visibility.FriendlyMask
			}
		}
	}
	_ = s.SetPresentationPerspective(PresentationPerspective{})
	s.RepublishPresentation()
	return position, nil
}

// modernAIRegistered reports whether this build links the Modern AI think
// step (RegisterModernAI), without binding it to a session.
func modernAIRegistered() bool {
	modernAIMu.Lock()
	defer modernAIMu.Unlock()
	return modernAIStep != nil
}
