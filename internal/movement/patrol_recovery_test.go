package movement

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// The policy changes only an unusable flight request. In particular the air
// sector's strict far edge and raw fixed-point saved bias must agree with the
// marker, or a waypoint can remain outside vertical control after projection.
// DESIGN_MOVEMENT_PATH "Modern construction aircraft patrol recovery".
func TestConstructionPatrolRecoveryMarkerPolicy(t *testing.T) {
	at := numeric.FixedFromInt
	for _, mode := range []struct {
		name   string
		rules  Rules
		modern bool
	}{
		{"unbound", nil, false},
		{"strict", StrictRules{}, false},
		{"community", CommunityRules{}, false},
		{"modern", &ModernRules{}, true},
	} {
		for _, point := range []struct {
			name               string
			x, z, wantX, wantZ numeric.Fixed
			customBias         bool
		}{
			{"negative", at(-32), at(-32), at(16), at(24), false},
			{"near-footprint", at(1), at(1), at(16), at(24), false},
			{"far-equality", at(488), at(480), at(480), at(472), false},
			{"past-far-edge", at(540), at(540), at(480), at(472), false},
			{"valid-fractional", at(487) + at(1)/2, at(479) + at(1)/2, at(487) + at(1)/2, at(479) + at(1)/2, false},
			{"saved-bias", at(-32), at(540), at(20), at(476), true},
		} {
			t.Run(mode.name+"/"+point.name, func(t *testing.T) {
				s, _, u := airFixture(t)
				s.Rules = mode.rules
				coll := handleRow(s.Collisions, u.Handle)
				coll.FootPrintX, coll.FootPrintZ = 2, 3
				coll.SetHalfBias(int32(at(-8)), int32(at(-16)))
				if point.customBias {
					coll.SetHalfBias(int32(at(-12)), int32(at(-20)))
				}
				q := orders.QueueForUnit(u)
				ledger := &economy.Service{}
				ledger.Players[0].Stock = [2]float32{700, 900}
				q.Binding().Economy = ledger
				random, stock := *q.Binding().SimRNG, ledger.Players[0].Stock
				n := pushAirOrder(t, u, "VTOL_RepairPatrol", point.x, point.z)
				n.Phase = 1
				n.GoalY = at(86)
				before := Vec3{u.X, u.Y, u.Z}
				if !s.InstallAirGoal(orders.AirGoalRequest{Owner: u.Handle, Node: n, X: point.x, Y: at(60), Z: point.z, Flags: airMarkerExplicitAlt}) {
					t.Fatal("marker installation failed")
				}
				m := s.FlightCommandFor(u.Handle, u).Payload.(*airMarker)
				wantX, wantZ, wantMode := point.x, point.z, uint8(1)
				if mode.modern {
					wantX, wantZ, wantMode = point.wantX, point.wantZ, 2
					bx, bz := coll.HalfBias()
					anchor := QuantizedAnchor(int32(m.goal.X), int32(m.goal.Z), bx, bz)
					if s.AirSectors.sectorForStamp(m.goal.X, m.goal.Z, anchor, 2, 3) == &s.AirSectors.sentinel {
						t.Fatal("projected goal still selects the off-map sector")
					}
				}
				if m.goal.X != wantX || m.goal.Z != wantZ || u.Move.Mode != wantMode {
					t.Fatalf("marker=(%v,%v) mode=%d, want (%v,%v) mode=%d", m.goal.X, m.goal.Z, u.Move.Mode, wantX, wantZ, wantMode)
				}
				if n.GoalX != point.x || n.GoalY != at(86) || n.GoalZ != point.z || n.Phase != 1 || q.Head() != n || q.LenPrimary() != 1 {
					t.Fatal("recovery changed the retained route")
				}
				if (Vec3{u.X, u.Y, u.Z}) != before || u.Move.ModeMirror != 1 {
					t.Fatal("marker installation teleported or committed the aircraft")
				}
				if *q.Binding().SimRNG != random || ledger.Players[0].Stock != stock {
					t.Fatal("flight recovery consumed RNG or resources")
				}
			})
		}
	}
}

func TestConstructionPatrolRecoveryLeavesOtherMarkersAlone(t *testing.T) {
	for _, name := range []string{"takeoff", "work", "guard", "follow-marker", "attached"} {
		t.Run(name, func(t *testing.T) {
			s, _, u := airFixture(t)
			s.Rules = &ModernRules{}
			n := pushAirOrder(t, u, "VTOL_RepairPatrol", -32<<16, 128<<16)
			n.Phase = 1
			req := orders.AirGoalRequest{Owner: u.Handle, Node: n, X: n.GoalX, Y: 60 << 16, Z: n.GoalZ, Flags: airMarkerExplicitAlt}
			switch name {
			case "takeoff":
				n.Phase = 0
			case "work":
				n.ID = orders.Lookup("VTOL_HelpBuild")
			case "guard":
				n.ID = orders.Lookup("VTOL_Follow")
			case "follow-marker":
				req.Target = u.Handle
			case "attached":
				u.Attachment.Carrier = u.Handle + 1
			}
			if !s.InstallAirGoal(req) {
				t.Fatal("marker installation failed")
			}
			m := s.FlightCommandFor(u.Handle, u).Payload.(*airMarker)
			if u.Move.Mode != 1 || req.Target == 0 && (m.goal.X != req.X || m.goal.Z != req.Z) {
				t.Fatal("construction-patrol recovery changed another marker")
			}
		})
	}
}
