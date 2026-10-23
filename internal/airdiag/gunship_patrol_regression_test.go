package airdiag

import (
	"fmt"
	"math"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Exercise the real command, flight, COB and weapon pipeline, rather than
// only checking the chosen standoff point (issue 121). Stock content and no
// mutators: DESIGN_MOVEMENT_PATH "Modern aircraft tactics", gunships.
func TestGunshipsSlideAndFireThroughRealFlight(t *testing.T) {
	root := testsupport.RetailRoot(t)
	// The fixture makes contacts available throughout; visibility is tested elsewhere.
	t.Setenv("NANOLATHE_AIRLAB_LOS", "0")
	const approachTicks, windowTicks, windows = 300, 240, 3
	for _, rules := range []string{"strict-3.1", "community-3.9", "modern"} {
		for _, scenario := range []string{"crystal-attack", "moving-attack", "crystal-patrol"} {
			t.Run(rules+"/"+scenario, func(t *testing.T) {
				h := labHarness(t, root, diagMap, 11)
				if err := h.Session.SetRules(rules); err != nil {
					t.Fatal(err)
				}
				w := h.Session.World
				c := labVec{float64(w.CellW) * 8, float64(w.CellH) * 8}
				s := newLabScene(h, scenario, c)
				targetName := "CORMEX"
				if scenario == "moving-attack" {
					targetName = "CORRAID"
				}
				target := s.holdPosition(s.spawn(t, 1, targetName, c))
				targets := []*units.Unit{target}
				if scenario == "crystal-patrol" {
					targets = append(targets, s.spawn(t, 1, "CORLLT", labVec{c.X + 160, c.Z + 160}))
				}
				// This is a firing fixture: use the ordinary Hold Fire stance
				// on its targets without healing away damage before the pilot
				// can observe it. Ordinary friendly damage remains possible.
				// The separate missile regression leaves both sides firing.
				for _, u := range targets {
					u.Flags &^= units.StandingFieldMask << units.StandingFireShift
				}
				// An authored, indestructible crystal blocks the westward firing
				// line. The test leaves physical shot admission intact: the pilot
				// must move around it, not fire through it.
				if scenario != "moving-attack" {
					crystal := h.Catalog.Features["crystalspire02"]
					if crystal == nil || h.Session.Features.PlaceAtWorld(labFixed(c.X-240), labFixed(c.Z-64), crystal) == nil {
						t.Fatal("stock crystal fixture could not be placed")
					}
				}
				air := make([]*units.Unit, 0, 3)
				for i := 0; i < 3; i++ {
					air = append(air, s.fireAtWill(s.spawn(t, 0, "ARMBRAWL", labVec{c.X - 640, c.Z + float64(i-1)*96})))
				}
				h.Step(30)
				if scenario == "moving-attack" {
					// Deliver the opponent patrol as its owner; the ordinary local
					// command boundary correctly rejects another owner's unit.
					h.Session.LocalOwner = 1
					s.order(t, target, input.LatchPatrol, nil, labVec{c.X + 480, c.Z + 160})
					h.Step(1)
					h.Session.LocalOwner = 0
				}
				for _, u := range air {
					if scenario == "crystal-patrol" {
						s.order(t, u, input.LatchPatrol, nil, labVec{c.X + 640, c.Z})
					} else {
						s.order(t, u, input.LatchAttack, target, labVec{})
					}
				}
				type observation struct {
					minHealth                                          int32
					bursts, lateral, radial, stationary, maxStationary int
					dry, maxDry                                        int
					pellets                                            [windows]int
					travel                                             float64
					prevReload                                         int32
					prev                                               labVec
				}
				obs := make([]observation, len(air))
				for i, u := range air {
					obs[i].prev = labPos(u)
					obs[i].prevReload = u.SlotAt(0).Reload
					obs[i].minHealth = u.Health
				}
				trace := openTraceFile(t, rules+"_"+scenario+"_gunships.csv")
				if trace != nil {
					fmt.Fprintln(trace, "tick,air,x,z,target_x,target_z,projectiles,lateral,radial")
				}
				participants := append(append([]*units.Unit{}, targets...), air...)
				targetPrev, targetTravel := labPos(target), 0.0
				for tick := 0; tick < approachTicks+windowTicks*windows; tick++ {
					// Keep only targets alive. Aircraft health must remain visible
					// to the pilot and ordinary reload calculation.
					for _, u := range targets {
						u.Health = u.Def.MaxDamage
					}
					h.Step(1)
					targetNow := labPos(target)
					targetTravel += targetNow.dist(targetPrev)
					targetPrev = targetNow
					for _, u := range participants {
						if !u.Alive || u.Dying {
							t.Fatalf("fixture lost %s at tick %d", u.Def.UnitName, tick)
						}
					}
					for i, u := range air {
						o := &obs[i]
						o.minHealth = min(o.minHealth, u.Health)
						pos := labPos(u)
						dx, dz := pos.X-o.prev.X, pos.Z-o.prev.Z
						pellets := 0
						// Inspect the completed projectile pass, counting only moving
						// pellets: parked burst anchors schedule later clones and
						// must not stand in for a successful emission [06 §4.3].
						for _, pr := range h.Session.Combat.Records[:h.Session.Combat.Count()] {
							if pr.Shooter == u.Handle && pr.CreationTick == h.Session.Clock.GlobalTick && pr.BurstRemaining == 0 && !pr.Dead {
								pellets++
							}
						}
						if tick >= approachTicks {
							o.pellets[(tick-approachTicks)/windowTicks] += pellets
							if pellets > 0 {
								o.dry = 0
							} else {
								o.dry++
								o.maxDry = max(o.maxDry, o.dry)
							}
							if u.SlotAt(0).Reload > o.prevReload {
								o.bursts++
							}
							// Ignore sub-quarter-unit settling when classifying travel.
							d := math.Hypot(dx, dz)
							o.travel += d
							if d > 0.25 {
								o.stationary = 0
							} else {
								o.stationary++
								if o.stationary > o.maxStationary {
									o.maxStationary = o.stationary
								}
							}
							tgt := h.Session.Units.Unit(u.SlotAt(0).Target.Unit)
							if tgt != nil && d > 0.25 {
								tx, tz := fx(tgt.X)-pos.X, fx(tgt.Z)-pos.Z
								// Compare tangential and radial displacement about the live target.
								if math.Abs(dx*tz-dz*tx) > math.Abs(dx*tx+dz*tz) {
									o.lateral++
								} else {
									o.radial++
								}
							}
						}
						if trace != nil {
							aimed := h.Session.Units.Unit(u.SlotAt(0).Target.Unit)
							if aimed == nil {
								aimed = target
							}
							fmt.Fprintf(trace, "%d,%d,%.3f,%.3f,%.3f,%.3f,%d,%d,%d\n", tick, i, pos.X, pos.Z, fx(aimed.X), fx(aimed.Z), pellets, o.lateral, o.radial)
						}
						o.prev = pos
						o.prevReload = u.SlotAt(0).Reload
					}
				}
				if scenario == "moving-attack" && targetTravel < float64(target.Def.FootprintX*16) {
					t.Fatalf("moving-target fixture travelled only %.1f world units", targetTravel)
				}
				for i, o := range obs {
					t.Logf("air%d bursts=%d pellets=%v lateral=%d radial=%d longestStop=%d longestDry=%d minHealth=%d travel=%.1f", i, o.bursts, o.pellets, o.lateral, o.radial, o.maxStationary, o.maxDry, o.minHealth, o.travel)
					// A group's total can hide one permanently blocked gunship.
					// Observe real pellets, not only reload or chosen waypoints.
					assertGunshipEmission(t, fmt.Sprintf("air%d", i), air[i], o.minHealth, windowTicks, o.pellets[:], o.maxDry)
					if o.lateral <= o.radial {
						t.Errorf("air%d mostly moved radially: lateral=%d radial=%d", i, o.lateral, o.radial)
					}
					// Permit the brief deceleration at a reversal, but not the
					// former Modern dwell's approximately one-second near-stop.
					if o.maxStationary > gunshipMaxNearStop {
						t.Errorf("air%d stayed still for %d ticks, limit %d", i, o.maxStationary, gunshipMaxNearStop)
					}
				}
			})
		}
	}
}
