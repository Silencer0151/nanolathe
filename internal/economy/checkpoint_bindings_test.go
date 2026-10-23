package economy

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

func economyBoundCheckpointBytes(t *testing.T, s *Service, c *CheckpointContext) []byte {
	t.Helper()
	if n, err := s.CollectCheckpointReferences(c); err != nil || n != 0 {
		t.Fatalf("collect = %d, %v", n, err)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func economyBindingRefused(t *testing.T, s *Service, c *CheckpointContext, path string) {
	t.Helper()
	want := "nanolathe: checkpoint capture failed: logical path " + path + ","
	if n, err := s.CollectCheckpointReferences(c); n != 0 || err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("collect = %d, %v; want refusal at %s", n, err, path)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("write = %v; want refusal at %s", err, path)
	}
	if out.Len() != 0 {
		t.Fatal("refused bindings wrote partial state")
	}
}

func economyBoundContext(t *testing.T, s *Service, authority *checkpoint.BindingAuthority) *CheckpointContext {
	t.Helper()
	c := NewCheckpointContext(&world.CheckpointContext{Terrain: s.Terrain})
	if err := c.SetBindings(s, s.Wind, authority); err != nil {
		t.Fatal(err)
	}
	return c
}

// The slot adapters use real setters and getters; no test edits a proof to
// manufacture an admission. The panic callbacks also forbid capture invocation.
var economyCallbackSlots = []struct {
	name      string
	offset    int
	bind      func(*Service, *checkpoint.BindingAuthority)
	copyTo    func(dst, src *Service)
	clear     func(*Service)
	clearWith func(*Service, *checkpoint.BindingAuthority)
}{
	{
		"CloakCost", 0,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetCloakCostWithCheckpointBinding(func(*units.Unit) float32 { panic("capture called CloakCost") }, a)
		},
		func(dst, src *Service) { dst.SetCloakCost(src.CloakCostHook()) },
		func(s *Service) { s.SetCloakCost(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetCloakCostWithCheckpointBinding(nil, a) },
	},
	{
		"CloakDue", 1,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetCloakDueWithCheckpointBinding(func(*units.Unit) bool { panic("capture called CloakDue") }, a)
		},
		func(dst, src *Service) { dst.SetCloakDue(src.CloakDueHook()) },
		func(s *Service) { s.SetCloakDue(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetCloakDueWithCheckpointBinding(nil, a) },
	},
	{
		"EndCondition", 147,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetEndConditionWithCheckpointBinding(func(int, uint32) { panic("capture called EndCondition") }, a)
		},
		func(dst, src *Service) { dst.SetEndCondition(src.EndConditionHook()) },
		func(s *Service) { s.SetEndCondition(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetEndConditionWithCheckpointBinding(nil, a) },
	},
}

func TestCheckpointEconomyBindingPresenceVector(t *testing.T) {
	// Independent zero-state layout: cloak tags (2), Community (144), selector,
	// end-condition and network tags (3), players (10*203), reference (8),
	// terrain/wind tags (2), and physical bucket count (4).
	want := make([]byte, 2193)
	s := &Service{}
	a := checkpoint.NewBindingAuthority()
	c := economyBoundContext(t, s, a)
	if got := economyBoundCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatalf("registered absent payload = %x, want %x", got, want)
	}
	if got := economyCheckpointBytes(t, s); !bytes.Equal(got, want) {
		t.Fatal("unregistered absent payload changed")
	}
	for _, slot := range economyCallbackSlots {
		t.Run(slot.name, func(t *testing.T) {
			slot.bind(s, a)
			want[slot.offset] = 1
			if got := economyBoundCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
				t.Fatalf("presence payload = %x, want %x", got, want)
			}
			slot.clear(s)
			want[slot.offset] = 0
		})
	}
	s.Wind = &world.Wind{Strength: 700, NextChange: 123}
	c = economyBoundContext(t, s, a)
	want[2188] = 1
	if got := economyBoundCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatalf("wind presence payload = %x, want %x", got, want)
	}
}

func TestCheckpointEconomyCallbackInstallationProof(t *testing.T) {
	for slotIndex, slot := range economyCallbackSlots {
		t.Run(slot.name, func(t *testing.T) {
			s := &Service{}
			a := checkpoint.NewBindingAuthority()
			c := economyBoundContext(t, s, a)
			path := "economy.Service." + slot.name
			slot.bind(s, a)
			economyBoundCheckpointBytes(t, s, c)
			economyBindingRefused(t, s, NewCheckpointContext(c.World), path)

			// Extracting and reinstalling the exact function is still ordinary.
			slot.copyTo(s, s)
			economyBindingRefused(t, s, c, path)
			slot.bind(s, nil)
			economyBindingRefused(t, s, c, path)
			slot.bind(s, checkpoint.NewBindingAuthority())
			economyBindingRefused(t, s, c, path)
			slot.bind(s, a)
			economyBoundCheckpointBytes(t, s, c)

			copyService := *s
			copyContext := economyBoundContext(t, &copyService, a)
			economyBindingRefused(t, &copyService, copyContext, path)
			other := &Service{}
			slot.copyTo(other, s)
			economyBindingRefused(t, other, economyBoundContext(t, other, a), path)
			// Only a fresh attested installation belongs to the copied owner.
			slot.bind(&copyService, a)
			economyBoundCheckpointBytes(t, &copyService, copyContext)

			for _, clear := range []func(){func() { slot.clear(s) }, func() { slot.clearWith(s, a) }} {
				slot.bind(s, a)
				clear()
				if s.checkpointCallbacks[slotIndex] != (checkpointCallbackProof{}) {
					t.Fatal("nil installation kept proof")
				}
				economyBoundCheckpointBytes(t, s, c)
			}
		})
	}
}

func TestCheckpointEconomyCallbackProofIsPerSlot(t *testing.T) {
	s := &Service{}
	a := checkpoint.NewBindingAuthority()
	c := economyBoundContext(t, s, a)
	for _, slot := range economyCallbackSlots {
		slot.bind(s, a)
	}
	for index, slot := range economyCallbackSlots {
		before := s.checkpointCallbacks
		slot.copyTo(s, s)
		for other := range before {
			if other != index && s.checkpointCallbacks[other] != before[other] {
				t.Fatal("ordinary installation cleared another slot's proof")
			}
		}
		// Reattesting another slot must not bless this ordinary installation.
		economyCallbackSlots[(index+1)%len(economyCallbackSlots)].bind(s, a)
		economyBindingRefused(t, s, c, "economy.Service."+slot.name)
		slot.bind(s, a)
		economyBoundCheckpointBytes(t, s, c)
	}
}

func TestCheckpointEconomyContextRegistrationAtomicity(t *testing.T) {
	s := &Service{Wind: &world.Wind{Strength: 2}}
	a := checkpoint.NewBindingAuthority()
	c := economyBoundContext(t, s, a)
	before := *c
	if err := c.SetBindings(s, s.Wind, a); err != nil || *c != before {
		t.Fatalf("identical registration = %v, changed=%t", err, *c != before)
	}
	for name, args := range map[string]struct {
		service   *Service
		wind      *world.Wind
		authority *checkpoint.BindingAuthority
	}{
		"absent service":   {nil, s.Wind, a},
		"absent authority": {s, s.Wind, nil},
		"other service":    {&Service{}, s.Wind, a},
		"other wind":       {s, &world.Wind{Strength: 2}, a},
		"absent wind":      {s, nil, a},
		"other authority":  {s, s.Wind, checkpoint.NewBindingAuthority()},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.SetBindings(args.service, args.wind, args.authority); err == nil {
				t.Fatal("invalid registration accepted")
			}
			if *c != before {
				t.Fatal("failed registration changed context")
			}
			economyBoundCheckpointBytes(t, s, c)
		})
	}
	var absent *CheckpointContext
	if err := absent.SetBindings(s, s.Wind, a); err == nil {
		t.Fatal("nil context accepted")
	}
	// Even a wholly absent binding set cannot use another service's context.
	registered, other := &Service{}, &Service{}
	economyBindingRefused(t, other, economyBoundContext(t, registered, a), "economy.bindings")
}

func TestCheckpointEconomyWindAndTerrainIdentity(t *testing.T) {
	s := &Service{Wind: &world.Wind{Strength: 2}, Terrain: &world.Terrain{}}
	c := economyBoundContext(t, s, checkpoint.NewBindingAuthority())
	economyBoundCheckpointBytes(t, s, c)
	originalWind, originalTerrain := s.Wind, s.Terrain
	windCopy := *s.Wind
	s.Wind = &windCopy
	economyBindingRefused(t, s, c, "economy.Service.Wind")
	s.Wind = nil
	economyBindingRefused(t, s, c, "economy.Service.Wind")
	s.Wind = originalWind
	economyBindingRefused(t, s, NewCheckpointContext(c.World), "economy.Service.Wind")
	s.Terrain = &world.Terrain{}
	economyBindingRefused(t, s, c, "economy.Service.Terrain")
	s.Terrain = originalTerrain
	economyBoundCheckpointBytes(t, s, c)

	s.Wind = nil
	nilWind := economyBoundContext(t, s, checkpoint.NewBindingAuthority())
	economyBoundCheckpointBytes(t, s, nilWind)
	s.Wind = originalWind
	economyBindingRefused(t, s, nilWind, "economy.Service.Wind")
}

func TestCheckpointEconomyBoundCapturePurityAndSummary(t *testing.T) {
	s := &Service{Wind: &world.Wind{Strength: 123}, unitBuckets: make([]UnitEconomy, 3)}
	s.Players[7].Stock[Metal] = 31
	s.unitBuckets[2].Buckets[Energy].Carry = 19
	var wantSummary checkpoint.Summary
	if err := s.AppendCheckpointSummary(&wantSummary); err != nil {
		t.Fatal(err)
	}
	a := checkpoint.NewBindingAuthority()
	for _, slot := range economyCallbackSlots {
		slot.bind(s, a)
	}
	c := economyBoundContext(t, s, a)
	// Snapshot all retained values and private proof, omitting only functions
	// whose invocation is guarded by panic. Copy slice storage independently.
	state := func() Service {
		copyService := *s
		copyService.unitBuckets = slices.Clone(s.unitBuckets)
		copyService.cloakCost, copyService.cloakDue, copyService.endCondition = nil, nil, nil
		return copyService
	}
	before, beforeContext, beforeWind := state(), *c, *s.Wind
	first := economyBoundCheckpointBytes(t, s, c)
	if again := economyBoundCheckpointBytes(t, s, c); !bytes.Equal(first, again) {
		t.Fatal("repeated capture changed bytes")
	}
	var gotSummary checkpoint.Summary
	if err := s.AppendCheckpointSummary(&gotSummary); err != nil || gotSummary != wantSummary {
		t.Fatalf("summary = %#v, %v; want %#v", gotSummary, err, wantSummary)
	}
	if !reflect.DeepEqual(state(), before) || *c != beforeContext || *s.Wind != beforeWind {
		t.Fatal("capture changed service, context or wind")
	}
	// Refusal must also leave both source and registration intact.
	foreign := *s
	economyBindingRefused(t, &foreign, c, "economy.bindings")
	if !reflect.DeepEqual(state(), before) || *c != beforeContext || *s.Wind != beforeWind {
		t.Fatal("refused capture changed source or context")
	}
}

// Attestation is installation bookkeeping only. Both paths retain the deadline
// and per-unit callback order [05 "Authoritative settlement order"] and the
// end-condition position before settlement [08 R-TRIG-01 §6].
func TestCheckpointEconomyAttestedSettlementEquivalence(t *testing.T) {
	run := func(admitted bool) []byte {
		s := &Service{}
		p := settlingPlayer(s, 0)
		p.Stock[Energy] = 10
		w, handles := settleTestWorld(t, 2)
		sim := rng.NewSimulation(73)
		w.SetSimulationRNG(&sim)
		beforeRNG := sim
		var trace []string
		cost := func(u *units.Unit) float32 {
			trace = append(trace, fmt.Sprintf("cost:%d", u.Handle))
			return 6
		}
		due := func(u *units.Unit) bool {
			trace = append(trace, fmt.Sprintf("due:%d", u.Handle))
			return true
		}
		end := func(player int, tick uint32) {
			trace = append(trace, fmt.Sprintf("end:%d:%d:%d", player, tick, s.Players[player].UpdateTime))
		}
		if admitted {
			a := checkpoint.NewBindingAuthority()
			s.SetCloakCostWithCheckpointBinding(cost, a)
			s.SetCloakDueWithCheckpointBinding(due, a)
			s.SetEndConditionWithCheckpointBinding(end, a)
			economyBoundCheckpointBytes(t, s, economyBoundContext(t, s, a))
		} else {
			s.SetCloakCost(cost)
			s.SetCloakDue(due)
			s.SetEndCondition(end)
		}
		if len(trace) != 0 {
			t.Fatal("installation or capture invoked callback")
		}
		if !s.TickPlayer(0, 0, w, func() { trace = append(trace, "before") }) {
			t.Fatal("settlement deadline did not run")
		}
		if got := strings.Join(trace, ","); got != "before,end:0:0:30,due:1,cost:1,due:2,cost:2" {
			t.Fatalf("admitted=%t callback trace = %s", admitted, got)
		}
		if !w.Unit(handles[0]).Hidden || w.Unit(handles[1]).Hidden {
			t.Fatal("cloak shortage no longer respects unit order")
		}
		if sim != beforeRNG {
			t.Fatal("settlement consumed RNG")
		}
		s.SetCloakCost(nil)
		s.SetCloakDue(nil)
		s.SetEndCondition(nil)
		return economyCheckpointBytes(t, s)
	}
	if ordinary, admitted := run(false), run(true); !bytes.Equal(ordinary, admitted) {
		t.Fatal("attestation changed settled economy state")
	}
}
