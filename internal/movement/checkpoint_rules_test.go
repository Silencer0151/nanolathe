package movement

import (
	"bytes"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
)

// The embedded nil interface panics if any gameplay method is called. The
// slice also prevents safely comparing this custom implementation by value.
type checkpointUnknownRules struct {
	Rules
	_ []int
}

func TestCheckpointRulesKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules Rules
		want  uint8
	}{
		{"nil", nil, 0},
		{"strict value", StrictRules{}, 1},
		{"strict pointer", &StrictRules{}, 1},
		{"community value", CommunityRules{}, 2},
		{"community pointer", &CommunityRules{}, 2},
		{"modern pointer", &ModernRules{}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckpointRulesKind(tc.rules)
			if err != nil || got != tc.want {
				t.Fatalf("kind = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	for _, rules := range []Rules{
		(*StrictRules)(nil), (*CommunityRules)(nil), (*ModernRules)(nil),
		checkpointUnknownRules{}, &checkpointUnknownRules{}, (*checkpointUnknownRules)(nil),
		struct{ StrictRules }{},
		&OverlapRules{}, (*OverlapRules)(nil),
	} {
		if got, err := CheckpointRulesKind(rules); err == nil || got != 0 {
			t.Fatalf("unreviewed %T = %d, %v", rules, got, err)
		}
	}
}

func TestCheckpointRulesOwnerTags(t *testing.T) {
	s := &System{}
	baseline := movementCheckpointBytes(t, s)
	// Two bindings, empty Collisions, Community (144), Damage, Profile
	// (16), empty Flights, Grid, Kernel, path limits, pilot ref, product
	// binding and empty Routes.
	const ruleOffset = 196
	if baseline[ruleOffset] != 0 {
		t.Fatal("nil rules lost their zero tag")
	}
	for i, rules := range []Rules{StrictRules{}, CommunityRules{}, &ModernRules{}} {
		s.Rules = rules
		want := bytes.Clone(baseline)
		want[ruleOffset] = byte(i + 1)
		if got := movementCheckpointBytes(t, s); !bytes.Equal(got, want) {
			t.Fatalf("rule %T changed bytes outside its tag\ngot  %x\nwant %x", rules, got, want)
		}
	}
}

func TestCheckpointRulesOwnerRefusesUnknown(t *testing.T) {
	c := movementCheckpointContext()
	s := &System{}
	for _, rules := range []Rules{(*StrictRules)(nil), (*CommunityRules)(nil), (*ModernRules)(nil), checkpointUnknownRules{}, struct{ StrictRules }{}} {
		s.Rules = rules
		if _, err := s.CollectCheckpointReferences(c); err == nil {
			t.Fatalf("collector accepted %T", rules)
		}
		var out bytes.Buffer
		if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || out.Len() != 0 {
			t.Fatalf("writer accepted %T or emitted bytes: %v, %x", rules, err, out.Bytes())
		}
	}
}
