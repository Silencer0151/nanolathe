package ai

import (
	"bytes"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
)

// Calling the embedded nil interface would panic. The slice makes this
// implementation noncomparable, so admission cannot use interface equality.
type checkpointUnknownPlanner struct {
	Planner
	_ []int
}

type checkpointUnknownConstructionRules struct{ construction.Rules }

func TestCheckpointPlannerKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		planner Planner
		want    uint8
	}{
		{"nil", nil, 0},
		{"retail value", RetailPlanner{}, 1},
		{"retail pointer", &RetailPlanner{}, 1},
		{"modern value", ModernPlanner{}, 2},
		{"modern pointer", &ModernPlanner{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckpointPlannerKind(tc.planner)
			if err != nil || got != tc.want {
				t.Fatalf("kind = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	for _, planner := range []Planner{
		(*RetailPlanner)(nil), (*ModernPlanner)(nil),
		checkpointUnknownPlanner{}, &checkpointUnknownPlanner{}, (*checkpointUnknownPlanner)(nil),
		struct{ RetailPlanner }{},
	} {
		if got, err := CheckpointPlannerKind(planner); err == nil || got != 0 {
			t.Fatalf("unreviewed %T = %d, %v", planner, got, err)
		}
	}
}

func TestCheckpointPlannerOwnerTags(t *testing.T) {
	c := aiCheckpointContext(t)
	m := &Manager{}
	baseline := aiCheckpointBytes(t, m, c)
	// BattleSeed, two bindings, Community (144) precede ConstructionRules.
	// The later Planner byte follows Controller, empty parameters, ten
	// deadlines, Ext, Factory ref, nine empty groups, IsAlliance,
	// JammerSuppresses, MissionGateFlag, OrderBinding, OriginX/Z and Passive.
	const rulesOffset, plannerOffset = 150, 263
	if baseline[rulesOffset] != 0 || baseline[plannerOffset] != 0 {
		t.Fatal("nil rules/planner lost their zero tags")
	}
	for ruleKind, rules := range []construction.Rules{nil, construction.StrictRules{}, construction.CommunityRules{}, &construction.ModernRules{}} {
		for plannerKind, planner := range []Planner{nil, RetailPlanner{}, ModernPlanner{}} {
			m.ConstructionRules, m.Planner = rules, planner
			want := bytes.Clone(baseline)
			want[rulesOffset], want[plannerOffset] = byte(ruleKind), byte(plannerKind)
			if got := aiCheckpointBytes(t, m, c); !bytes.Equal(got, want) {
				t.Fatalf("rules %T, planner %T changed bytes outside their tags\ngot  %x\nwant %x", rules, planner, got, want)
			}
		}
	}
}

func TestCheckpointPlannerOwnerRefusesUnknown(t *testing.T) {
	c := aiCheckpointContext(t)
	for _, m := range []*Manager{
		{Planner: (*RetailPlanner)(nil)}, {Planner: (*ModernPlanner)(nil)},
		{Planner: checkpointUnknownPlanner{}}, {Planner: struct{ RetailPlanner }{}},
		{ConstructionRules: (*construction.StrictRules)(nil)},
		{ConstructionRules: (*construction.CommunityRules)(nil)},
		{ConstructionRules: (*construction.ModernRules)(nil)},
		{ConstructionRules: checkpointUnknownConstructionRules{}},
	} {
		if _, err := m.CollectCheckpointReferences(c); err == nil {
			t.Fatalf("collector accepted rules %T, planner %T", m.ConstructionRules, m.Planner)
		}
		var out bytes.Buffer
		if err := m.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || out.Len() != 0 {
			t.Fatalf("writer accepted rules %T, planner %T or emitted bytes: %v, %x", m.ConstructionRules, m.Planner, err, out.Bytes())
		}
	}
}
