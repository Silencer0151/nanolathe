package economy

import (
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Physical unused rows retain all twelve floats. Their diagnostic paths must
// not allocate per row (DESIGN_MULTIPLAYER §16.3.81).
func TestCheckpointEconomyDenseAllocation(t *testing.T) {
	measure := func(size int) float64 {
		s := &Service{unitBuckets: make([]UnitEconomy, size)}
		c := NewCheckpointContext(&world.CheckpointContext{})
		e := checkpoint.NewEncoder(io.Discard)
		return testing.AllocsPerRun(5, func() {
			if err := s.WriteCheckpoint(e, c); err != nil {
				panic(err)
			}
		})
	}
	small, large := measure(16), measure(4096)
	t.Logf("economy: 16 rows %.0f allocations; 4096 rows %.0f", small, large)
	if large > small+1 {
		t.Fatalf("physical bucket paths allocate by row count: %g versus %g", small, large)
	}
}

type economyCheckpointFailAt struct {
	remaining int
	cause     error
}

func (w *economyCheckpointFailAt) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, w.cause
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestCheckpointEconomyDenseErrorPaths(t *testing.T) {
	// Authored byte offsets: the nil-binding/zero-table service prefix plus
	// ten 203-byte players ends at 2193, then each physical row is 48 bytes.
	for _, tc := range []struct {
		offset int
		path   string
	}{
		{2241, "Archived[0].Production"}, {2245, "Archived[0].Requested"},
		{2249, "Archived[1].Production"}, {2253, "Archived[1].Requested"},
		{2257, "Buckets[0].Accepted"}, {2261, "Buckets[0].Carry"},
		{2265, "Buckets[0].Production"}, {2269, "Buckets[0].Requested"},
		{2273, "Buckets[1].Accepted"}, {2277, "Buckets[1].Carry"},
		{2281, "Buckets[1].Production"}, {2285, "Buckets[1].Requested"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := &Service{unitBuckets: make([]UnitEconomy, 2)}
			cause := errors.New("authored sink failure")
			e := checkpoint.NewEncoder(&economyCheckpointFailAt{remaining: tc.offset, cause: cause})
			err := s.WriteCheckpoint(e, NewCheckpointContext(&world.CheckpointContext{}))
			want := "logical path economy.Service.unitBuckets[1]." + tc.path + ","
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v, want %s", err, want)
			}
		})
	}
	for _, tc := range []struct {
		path   string
		change func(*Service)
	}{
		{"Players[9].AIConsumption[1]", func(s *Service) { s.Players[9].AIConsumption[1] = math.Float32frombits(0x7fc01234) }},
		{"Players[9].ArchivedMirror[1].Requested", func(s *Service) { s.Players[9].ArchivedMirror[1].Requested = math.Float32frombits(0xff800001) }},
		{"Players[9].Mirror[1].Carry", func(s *Service) { s.Players[9].Mirror[1].Carry = math.Float32frombits(0x7fc00001) }},
		{"Players[9].Waste[1]", func(s *Service) { s.Players[9].Waste[1] = math.Float64frombits(0x7ff8000000000001) }},
		{"unitBuckets[1].Archived[1].Production", func(s *Service) { s.unitBuckets[1].Archived[1].Production = math.Float32frombits(0x7fc01234) }},
		{"unitBuckets[1].Buckets[1].Requested", func(s *Service) { s.unitBuckets[1].Buckets[1].Requested = math.Float32frombits(0xff800001) }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := &Service{unitBuckets: make([]UnitEconomy, 2)}
			tc.change(s)
			e := checkpoint.NewEncoder(io.Discard)
			err := s.WriteCheckpoint(e, NewCheckpointContext(&world.CheckpointContext{}))
			want := "logical path economy.Service." + tc.path + ","
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "NaN") {
				t.Fatalf("error=%v, want %s NaN", err, want)
			}
		})
	}
}
