package movement

import (
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// recoverAirPatrolGoal applies Nanolathe Modern policy at a construction
// patrol's ordinary leg installation. A retained phase-one leg skips the
// takeoff preamble [04 R-ORD-01 §7]; an explicit-altitude goal outside the map
// cannot finish its climb because flight skips vertical control there
// [04 §10.1]. Keep the route records intact and repair only the flight request.
// DESIGN_MOVEMENT_PATH "Modern construction aircraft patrol recovery".
func (s *System) recoverAirPatrolGoal(u *units.Unit, x, z numeric.Fixed) (numeric.Fixed, numeric.Fixed) {
	if u.Attachment.Carrier != 0 {
		return x, z
	}
	if u.Move.Mode&3 == 1 {
		s.SetMoverMode(u, 2)
	}
	coll := handleRow(s.Collisions, u.Handle)
	if s.Terrain == nil || coll == nil {
		return x, z
	}
	bx, bz := coll.HalfBias()
	return patrolGoalAxis(x, s.Terrain.CellW, int32(coll.FootPrintX), bx), patrolGoalAxis(z, s.Terrain.CellH, int32(coll.FootPrintZ), bz)
}

// Only an axis whose footprint would select the off-map sector changes. Match
// sectorForStamp's strict far-edge bound [04 R-AIR-01 §5][04 R-COLL-01 §4],
// and target the centre of the nearest valid anchor cell rather than its edge,
// so braking toward it crosses back onto the map before arrival needs altitude.
func patrolGoalAxis(value numeric.Fixed, cells, footprint, bias int32) numeric.Fixed {
	if cells <= footprint {
		return value
	}
	biasFixed := numeric.Fixed(bias)
	anchor := world.WorldToCell(value + biasFixed)
	if anchor >= 0 && anchor+footprint < cells {
		return value
	}
	first := world.CellToWorld(1)/2 - biasFixed
	last := world.CellToWorld(cells-footprint-1) + first
	return max(first, min(value, last))
}
