package community

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
)

func TestCheckpointResolvedFeaturesVector(t *testing.T) {
	f := Features{
		AIApplianceEnergy: true, AIBuilderPlacementLimit: 1 << 40,
		AIDifficultyIncome: true, AIDifficultyReversed: true, DebrisCapacity: -2,
		RepairRate:         RepairRate{Enabled: true, RepairMultiplier: -3, SelfHealMultiplier: 1 << 41},
		WreckSnapRadiusMax: 1 << 42,
	}
	// Independent schema vector: explicit lexical positions, no JSON omission
	// of false fields, and full signed 64-bit host integers, even for values
	// that an ordinary profile does not select (DESIGN_MULTIPLAYER §16.3.13).
	const vector = "01" + "0000000000010000" + "0001" + "01" + "000000000000000000" +
		"feffffffffffffff" + "0000000000000000" + "0000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00" + "0000000000000000" + "00" +
		"01fdffffffffffffff0000000000020000" +
		"00000000" + "0000000000000000" + "000000" +
		"0000000000000000" + "00000000" + "0000000000000000" + "0000000000040000"
	want, err := hex.DecodeString(vector)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := f.WriteCheckpoint(checkpoint.NewEncoder(&out)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("payload = %x\nwant      %x", out.Bytes(), want)
	}
}

func TestCheckpointRetainsAIDifficultyReversed(t *testing.T) {
	write := func(f Features) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := f.WriteCheckpoint(checkpoint.NewEncoder(&out)); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	// Appliance byte, placement i64, stop-threshold byte and income byte
	// precede the retained reversal byte; false still occupies its position.
	want := make([]byte, 144)
	if got := write(Features{}); !bytes.Equal(got, want) {
		t.Fatalf("zero table = %x, want %x", got, want)
	}
	want[11] = 1
	if got := write(Features{AIDifficultyReversed: true}); !bytes.Equal(got, want) {
		t.Fatalf("reversed table = %x, want %x", got, want)
	}
}
