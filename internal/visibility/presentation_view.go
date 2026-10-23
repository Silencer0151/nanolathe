package visibility

// Another player's picture, for presentation only (DESIGN_MULTIPLAYER §10
// "Viewing is presentation only"). A replay playback may draw any recorded
// seat while the simulation keeps its own local and viewing player, which a
// single-player battle reads as simulation inputs. The two reads here derive
// that seat's fog and sensor contacts into caller-owned storage. They write no
// grid, no mode bit, nothing of the service's own fog cache or its revision
// and cell marks, no unit status word and no cloak deadline; what they leave
// behind is read by publication alone. Retail draws only its local player's
// picture: this is Nanolathe replay presentation, not retail behaviour.

// DeriveFogFor derives into dst the fog RebuildFog(0, 0) derives — the
// full-map window, its corner seeds and map-edge fixups [03 §3.3] — from
// player's inputs rather than the local player's: player's current-coverage
// grid, the explored-history bit player's own picture reads
// (fogHistoryPlayer) and the current mode. dst is caller storage that holds
// one derivation; a later call re-derives only the cells whose input changed
// since, which is byte-identical to deriving them afresh. The service's own
// cache is refused.
//
// ok reports that dst holds a derivation; changed, that its window or a
// channel byte differs from what dst held before the call.
func (s *Service) DeriveFogFor(dst *FogCache, player PlayerID) (changed, ok bool) {
	if s == nil || dst == nil || dst == &s.fog || !validPlayer(player) || s.W <= 0 || s.H <= 0 {
		return false, false
	}
	key, ok := s.fogWindow(0, 0, s.W*32, s.H*32)
	if !ok {
		return false, false
	}
	src := fogSource{grid: s.byteGrids[player], word: s.wordMask, bit: cellBit(s.fogHistoryPlayer(player)), current: s.mode.CurrentEnabled()}
	if dst.derivedFor(key) {
		return dst.updateCells(key, src), true
	}
	dst.derive(key, src)
	return true, true
}

// fogHistoryPlayer is the explored-history bit player's own picture reads:
// the bit a query on player's behalf reads when player is the local player
// (historyPlayer) — its own, or online its host's for a hosted computer.
func (s *Service) fogHistoryPlayer(player PlayerID) PlayerID {
	if s.ownerPerspectives {
		return s.historyPlayer(player)
	}
	return player
}

// PresentationSensors is caller storage for one viewer's sensor contacts: the
// sensor bits the five sensor passes leave on every unit when that viewer is
// the viewing player [03 R-VIS-01 §4], kept per allocation as an online
// owner perspective keeps them. A single-player battle stores the viewing
// player's bits on the units, where the simulation reads them; this bank is
// never read by the simulation.
type PresentationSensors struct {
	rows      []perspectiveSensorStatus // by unit ID
	units     []SensorUnit
	transient []uint32
	// The service's pass outputs and candidate index are exchanged with these
	// for the length of a presentation pass, so the service's own stay exactly
	// as the last real pass left them.
	inputs []SensorInput
	byID   []sensorStatus
	index  sensorCandidateIndex
}

// Clear empties the bank: every allocation then reads as one the bank has not
// seen.
func (b *PresentationSensors) Clear() {
	if b != nil {
		clear(b.rows)
		b.rows = b.rows[:0]
	}
}

// Status reads one allocation's sensor bits from the bank, overlaid on its
// current non-sensor bits, as StatusForPerspective reads an owner bank. ok is
// false for an allocation the last pass did not see — created since, or still
// inside creation (serial zero) — and the caller then seeds it as unit
// construction would for the viewer.
func (b *PresentationSensors) Status(id uint16, allocationSerial uint64, current uint32) (uint32, bool) {
	if b == nil || allocationSerial == 0 || int(id) >= len(b.rows) || b.rows[id].allocationSerial != allocationSerial {
		return current, false
	}
	return current&^perspectiveSensorMask | b.rows[id].status&perspectiveSensorMask, true
}

// SensorTickForPresentation runs the five sensor passes of SensorTick with
// viewer as the viewing player, into bank [03 R-VIS-01 §4]. units are the
// inputs the real pass is built from, with pointers to the units' own status
// words and cloak deadlines; neither is written. Each status is redirected
// into the bank, seeded from the unit's current word, and pass 4 is given no
// deadline to write. defeated is pass 1's defeated-viewer disjunct, decided by
// the caller as for the real pass; the single-viewer form shares no radar.
//
// The pass's diagnostic outputs (SensorInputs, SensorStatus) and its candidate
// index belong to the real pass, which simulation consumers may read: they
// are swapped out for the bank's own storage and restored before this
// returns, even if the pass panics. No other service state is written. A
// battle with one active player runs no pass, and the bank is left as the
// seeds.
func (s *Service) SensorTickForPresentation(bank *PresentationSensors, viewer PlayerID, defeated bool, activePlayers int, units []SensorUnit) {
	if s == nil || bank == nil || !validPlayer(viewer) {
		return
	}
	// Size the bank before taking any row pointers, so a later sparse slot
	// cannot move earlier rows away from the pointers the passes hold.
	size := 0
	for i := range units {
		if u := &units[i]; u.Alive && u.Status != nil && u.AllocationSerial != 0 && int(u.ID)+1 > size {
			size = int(u.ID) + 1
		}
	}
	if cap(bank.rows) < size {
		bank.rows = make([]perspectiveSensorStatus, size)
	} else {
		bank.rows = bank.rows[:size]
		clear(bank.rows)
	}
	bank.units = append(bank.units[:0], units...)
	if cap(bank.transient) < len(units) {
		bank.transient = make([]uint32, len(units))
	}
	bank.transient = bank.transient[:len(units)]
	for i := range bank.units {
		u := &bank.units[i]
		u.DecloakDeadline = nil
		if u.Status == nil {
			continue // no pass writes a missing status
		}
		seed := *u.Status
		if !u.Alive || u.AllocationSerial == 0 {
			// No bank identity: the pass writes a scratch word.
			bank.transient[i] = seed
			u.Status = &bank.transient[i]
			continue
		}
		row := &bank.rows[u.ID]
		*row = perspectiveSensorStatus{allocationSerial: u.AllocationSerial, status: seed}
		u.Status = &row.status
	}
	inputs, byID := s.sensorInputs, s.sensorStatusByID
	s.sensorInputs, s.sensorStatusByID = bank.inputs, bank.byID
	s.sensorIndex, bank.index = bank.index, s.sensorIndex
	defer func() {
		bank.inputs, bank.byID = s.sensorInputs, s.sensorStatusByID
		s.sensorInputs, s.sensorStatusByID = inputs, byID
		s.sensorIndex, bank.index = bank.index, s.sensorIndex
	}()
	// The tick feeds only pass 4's deadline write, which has no target here.
	s.sensorTick(0, activePlayers, bank.units, viewer, defeated, 0)
}
