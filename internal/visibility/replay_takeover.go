package visibility

// TakeOverPerspective gives a former hosted computer its own sensor bank in
// a local replay continuation (DESIGN_MULTIPLAYER §10.1). Its own explored
// history and coverage already exist. Drop the borrowed contacts; the next
// ordinary sensor due populates its own bank without an extra sensor pass or
// shared cloak-deadline write at the takeover boundary.
func (s *Service) TakeOverPerspective(player PlayerID) {
	if s == nil || !validPlayer(player) {
		return
	}
	if s.perspectiveHost[player] != 0 {
		s.perspectiveHost[player] = 0
		s.perspectiveStatus[player] = nil
	}
	s.SetLocal(player)
}
