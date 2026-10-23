package economy

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

func economyCheckpointBytes(t *testing.T, s *Service) []byte {
	t.Helper()
	c := NewCheckpointContext(&world.CheckpointContext{Terrain: s.Terrain})
	if n, err := s.CollectCheckpointReferences(c); err != nil || n != 0 {
		t.Fatalf("collect = %d, %v", n, err)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Independent authored schema vector: all player field families are distinct,
// resources are Metal/Energy, all ten fixed player slots are present, and the
// physical bucket array includes slot zero and an unused middle row.
func TestCheckpointEconomyVector(t *testing.T) {
	selector := -(1 << 40) + 5
	s := &Service{EconomySelector: &selector, ReferencePlayer: (1 << 42) + 3, Networked: true, Terrain: &world.Terrain{}}
	s.Players[0] = Player{
		AIConsumption: [2]float32{1, 2}, AIProduction: [2]float32{3, 4}, Allies: [10]bool{true, false, false, false, false, false, false, false, false, true},
		ArchivedMirror: [2]ArchivedBucket{{5, 6}, {7, 8}}, AutoShareEnergy: true, AutoShareSensor: true, Capacity: [2]float32{9, 10},
		CommanderKills: -2, CommanderLosses: 0x1234, ControllerState: 3, EndGameCountdown: -4, EnergyShareThreshold: 11,
		Exists: true, FullIncome: true, IsObserver: true, Kills: -5, Losses: 0x5678, MetalShareThreshold: 12,
		Mirror: [2]Bucket{{13, 14, 15, 16}, {17, 18, 19, 20}}, OptionKind: 2, PassConsumed: [2]float32{21, 22}, PassProduced: [2]float32{23, 24},
		RejectionReason: 0x9a, ResultAuxiliary: 0xfedcba98, Side: 7, Stock: [2]float32{25, 26}, StorageBonus: [2]float32{27, 28}, StorageBonusEnabled: true,
		TotalConsumed: [2]float64{math.Float64frombits(1 << 63), math.Inf(1)}, TotalProduced: [2]float64{1, -2}, UpdateTime: 0x87654321,
		Waste: [2]float64{math.SmallestNonzeroFloat64, math.MaxFloat64}, Watcher: true,
	}
	s.unitBuckets = make([]UnitEconomy, 3)
	s.unitBuckets[0] = UnitEconomy{Archived: [2]ArchivedBucket{{1, 2}, {3, 4}}, Buckets: [2]Bucket{{5, 6, 7, 8}, {9, 10, 11, 12}}}
	s.unitBuckets[2].Buckets[Metal].Production = -1
	decode := func(v string) []byte {
		t.Helper()
		b, err := hex.DecodeString(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// Two absent cloak ports, the shared table's 144-byte all-zero value,
	// selector presence/i64, absent end-condition port, networked Boolean.
	want := decode("0000")
	want = append(want, make([]byte, 144)...)
	want = append(want, decode("010500000000ffffff0001")...)
	const player = "0000803f00000040" + "0000404000008040" + "01000000000000000001" +
		"0000a0400000c0400000e04000000041" + "010001" + "0000104100002041" +
		"feff341203fcffffff0000304101010001fbff785600004041" +
		"00007041000080410000504100006041000098410000a0410000884100009041" +
		"02" + "0000a8410000b0410000b8410000c041" + "9a98badcfe07" +
		"0000c8410000d0410000d8410000e04101" +
		"0000000000000080000000000000f07f000000000000f03f00000000000000c0" +
		"21436587" + "0100000000000000ffffffffffffef7f01"
	want = append(want, decode(player)...)
	want = append(want, make([]byte, 9*203)...)
	want = append(want, decode("0300000000040000010003000000")...)
	const unit = "0000803f000000400000404000008040" +
		"0000e040000000410000a0400000c04000003041000040410000104100002041"
	want = append(want, decode(unit)...)
	want = append(want, make([]byte, 48)...)
	want = append(want, make([]byte, 24)...)
	want = append(want, decode("000080bf")...)
	want = append(want, make([]byte, 20)...)
	got := economyCheckpointBytes(t, s)
	if !bytes.Equal(got, want) {
		t.Fatalf("payload\ngot  %x\nwant %x", got, want)
	}
}

func TestCheckpointEconomyRetainedPlayerMutations(t *testing.T) {
	s := &Service{}
	baseline := economyCheckpointBytes(t, s)
	edits := map[string]func(*Player){
		"AIConsumption": func(p *Player) { p.AIConsumption[Energy] = 1 }, "AIProduction": func(p *Player) { p.AIProduction[Metal] = 1 },
		"Allies": func(p *Player) { p.Allies[0] = true }, "ArchivedMirror": func(p *Player) { p.ArchivedMirror[Energy].Requested = 1 },
		"AutoShareEnergy": func(p *Player) { p.AutoShareEnergy = true }, "AutoShareMetal": func(p *Player) { p.AutoShareMetal = true }, "AutoShareSensor": func(p *Player) { p.AutoShareSensor = true },
		"Capacity": func(p *Player) { p.Capacity[Energy] = 1 }, "CommanderKills": func(p *Player) { p.CommanderKills = -1 }, "CommanderLosses": func(p *Player) { p.CommanderLosses = -1 },
		"ControllerState": func(p *Player) { p.ControllerState = 3 }, "EndGameCountdown": func(p *Player) { p.EndGameCountdown = -1 }, "EnergyShareThreshold": func(p *Player) { p.EnergyShareThreshold = 1 },
		"Exists": func(p *Player) { p.Exists = true }, "FullIncome": func(p *Player) { p.FullIncome = true }, "GameEnded": func(p *Player) { p.GameEnded = true }, "IsObserver": func(p *Player) { p.IsObserver = true },
		"Kills": func(p *Player) { p.Kills = -1 }, "Losses": func(p *Player) { p.Losses = -1 }, "MetalShareThreshold": func(p *Player) { p.MetalShareThreshold = 1 },
		"Mirror": func(p *Player) { p.Mirror[Metal].Carry = 1 }, "OptionKind": func(p *Player) { p.OptionKind = 2 }, "PassConsumed": func(p *Player) { p.PassConsumed[Energy] = 1 }, "PassProduced": func(p *Player) { p.PassProduced[Metal] = 1 },
		"RejectionReason": func(p *Player) { p.RejectionReason = 3 }, "ResultAuxiliary": func(p *Player) { p.ResultAuxiliary = 1 }, "ShareRadar": func(p *Player) { p.ShareRadar = true }, "Side": func(p *Player) { p.Side = 2 },
		"Stock": func(p *Player) { p.Stock[Energy] = 1 }, "StorageBonus": func(p *Player) { p.StorageBonus[Metal] = 1 }, "StorageBonusEnabled": func(p *Player) { p.StorageBonusEnabled = true },
		"TotalConsumed": func(p *Player) { p.TotalConsumed[Energy] = 1 }, "TotalProduced": func(p *Player) { p.TotalProduced[Metal] = 1 }, "UpdateTime": func(p *Player) { p.UpdateTime = 1 }, "Waste": func(p *Player) { p.Waste[Energy] = 1 }, "Watcher": func(p *Player) { p.Watcher = true },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			s.Players[9] = Player{}
			edit(&s.Players[9])
			if bytes.Equal(baseline, economyCheckpointBytes(t, s)) {
				t.Fatal("retained mutation vanished")
			}
		})
	}
}

func TestCheckpointEconomyPhysicalBucketsAndServiceValues(t *testing.T) {
	s := &Service{unitBuckets: make([]UnitEconomy, 4)}
	baseline := economyCheckpointBytes(t, s)
	for _, resource := range []Res{Metal, Energy} {
		for _, field := range []string{"Production", "Requested", "Accepted", "Carry", "Archived.Production", "Archived.Requested"} {
			t.Run([]string{"Metal", "Energy"}[resource]+"."+field, func(t *testing.T) {
				s.unitBuckets[3] = UnitEconomy{}
				switch field {
				case "Production":
					s.unitBuckets[3].Buckets[resource].Production = 1
				case "Requested":
					s.unitBuckets[3].Buckets[resource].Requested = 1
				case "Accepted":
					s.unitBuckets[3].Buckets[resource].Accepted = 1
				case "Carry":
					s.unitBuckets[3].Buckets[resource].Carry = 1
				case "Archived.Production":
					s.unitBuckets[3].Archived[resource].Production = 1
				case "Archived.Requested":
					s.unitBuckets[3].Archived[resource].Requested = 1
				}
				if bytes.Equal(baseline, economyCheckpointBytes(t, s)) {
					t.Fatal("unused physical row vanished")
				}
			})
		}
	}
	s.unitBuckets[3] = UnitEconomy{}
	s.unitBuckets = s.unitBuckets[:3]
	if bytes.Equal(baseline, economyCheckpointBytes(t, s)) {
		t.Fatal("trailing unused row count vanished")
	}
	for name, edit := range map[string]func(*Service){
		"network": func(v *Service) { v.Networked = true }, "reference": func(v *Service) { v.ReferencePlayer = 1 << 42 },
		"terrain": func(v *Service) { v.Terrain = &world.Terrain{} }, "community flag": func(v *Service) { v.Community.AIDifficultyIncome = true },
		"community int": func(v *Service) { v.Community.UnitLimit = 1 << 40 }, "community nested": func(v *Service) { v.Community.RepairRate.SelfHealMultiplier = -3 },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := &Service{}, &Service{}
			edit(b)
			if bytes.Equal(economyCheckpointBytes(t, a), economyCheckpointBytes(t, b)) {
				t.Fatal("retained service mutation vanished")
			}
		})
	}
	// Storage order is physical handle order, independent of live allocation.
	s.unitBuckets = []UnitEconomy{{Buckets: [2]Bucket{{Production: 1}}}, {Buckets: [2]Bucket{{Production: 2}}}}
	first := economyCheckpointBytes(t, s)
	s.unitBuckets[0], s.unitBuckets[1] = s.unitBuckets[1], s.unitBuckets[0]
	if bytes.Equal(first, economyCheckpointBytes(t, s)) {
		t.Fatal("physical row order vanished")
	}
}

func TestCheckpointEconomySelectorPresenceAndIdentity(t *testing.T) {
	s := &Service{}
	absent := economyCheckpointBytes(t, s)
	zero := 0
	s.EconomySelector = &zero
	present := economyCheckpointBytes(t, s)
	if bytes.Equal(absent, present) {
		t.Fatal("absent selector collapsed into zero")
	}
	copyZero := 0
	s.EconomySelector = &copyZero
	if !bytes.Equal(present, economyCheckpointBytes(t, s)) {
		t.Fatal("selector address entered payload")
	}
	copyZero = 1 << 40
	if bytes.Equal(present, economyCheckpointBytes(t, s)) {
		t.Fatal("selector high bits vanished")
	}
}

func TestCheckpointEconomyExclusionsAndPurity(t *testing.T) {
	s := &Service{unitBuckets: make([]UnitEconomy, 2, 9)}
	s.Players[4].Stock = [2]float32{1, 2}
	s.unitBuckets[1].Buckets[Energy].Carry = -3
	baseline := economyCheckpointBytes(t, s)
	s.SensorShareCalls = 99
	for slot := range s.Players {
		p := &s.Players[slot]
		p.Name = "different"
		p.Logo = 8
		p.Rank = 7
		p.WinLoseTime = 99
		p.DisplayTimer = 98
		p.aiAggregatesPrepared = true
	}
	before := *s
	before.unitBuckets = append([]UnitEconomy(nil), s.unitBuckets...)
	if !bytes.Equal(baseline, economyCheckpointBytes(t, s)) {
		t.Fatal("excluded data changed payload")
	}
	if !reflect.DeepEqual(before, *s) {
		t.Fatal("capture changed service state")
	}
	// No capture grows the per-unit slice to the world pool size or strips its
	// unused rows; capacity itself has no reader beyond ordinary slice growth.
	if len(s.unitBuckets) != 2 || cap(s.unitBuckets) != 9 {
		t.Fatal("capture changed physical bucket storage")
	}
}

func TestCheckpointEconomyFloatBits(t *testing.T) {
	s := &Service{}
	// Standalone empty-service framing is 149 bytes before its first player;
	// Stock[Energy] is at byte 137, TotalConsumed[Energy] at byte 158 in a row.
	for _, bits := range []uint32{0, 1 << 31, 1, 0x007fffff, 0x7f7fffff, 0x7f800000, 0xff800000} {
		s.Players[0].Stock[Energy] = math.Float32frombits(bits)
		got := economyCheckpointBytes(t, s)
		if read := binary.LittleEndian.Uint32(got[149+137:]); read != bits {
			t.Fatalf("binary32 = %08x, want %08x", read, bits)
		}
	}
	for _, bits := range []uint64{0, 1 << 63, 1, 0x000fffffffffffff, 0x7fefffffffffffff, 0x7ff0000000000000, 0xfff0000000000000} {
		s.Players[0].TotalConsumed[Energy] = math.Float64frombits(bits)
		got := economyCheckpointBytes(t, s)
		if read := binary.LittleEndian.Uint64(got[149+158:]); read != bits {
			t.Fatalf("binary64 = %016x, want %016x", read, bits)
		}
	}
	for name, edit := range map[string]func(*Service){
		"player binary32": func(v *Service) { v.Players[7].EnergyShareThreshold = math.Float32frombits(0x7fc00001) },
		"player binary64": func(v *Service) { v.Players[9].Waste[Metal] = math.Float64frombits(0x7ff0000000000001) },
		"live bucket": func(v *Service) {
			v.unitBuckets = []UnitEconomy{{Buckets: [2]Bucket{{Carry: math.Float32frombits(0xff800001)}}}}
		},
		"archive": func(v *Service) { v.Players[1].ArchivedMirror[Energy].Requested = math.Float32frombits(0x7f800001) },
	} {
		t.Run(name, func(t *testing.T) {
			v := &Service{}
			edit(v)
			var out bytes.Buffer
			e := checkpoint.NewEncoder(&out)
			err := v.WriteCheckpoint(e, NewCheckpointContext(&world.CheckpointContext{}))
			if err == nil || !strings.Contains(err.Error(), "NaN binary") || !strings.HasPrefix(err.Error(), "nanolathe: ") {
				t.Fatalf("NaN capture = %v", err)
			}
			n := out.Len()
			e.U64(3)
			if out.Len() != n {
				t.Fatal("NaN failure was not sticky")
			}
		})
	}
}

func TestCheckpointEconomyBindingRefusals(t *testing.T) {
	for name, edit := range map[string]func(*Service, *CheckpointContext){
		"context": func(s *Service, c *CheckpointContext) { c.World = nil },
		"terrain": func(s *Service, c *CheckpointContext) { s.Terrain = &world.Terrain{} },
		"wind":    func(s *Service, c *CheckpointContext) { s.Wind = &world.Wind{} },
		"cost": func(s *Service, c *CheckpointContext) {
			s.SetCloakCost(func(*units.Unit) float32 { t.Error("cost called"); return 0 })
		},
		"due": func(s *Service, c *CheckpointContext) {
			s.SetCloakDue(func(*units.Unit) bool { t.Error("due called"); return false })
		},
		"end": func(s *Service, c *CheckpointContext) { s.SetEndCondition(func(int, uint32) { t.Error("end called") }) },
	} {
		t.Run(name, func(t *testing.T) {
			s := &Service{}
			c := NewCheckpointContext(&world.CheckpointContext{})
			edit(s, c)
			if _, err := s.CollectCheckpointReferences(c); err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint capture failed: logical path ") {
				t.Fatalf("collector = %v", err)
			}
			var out bytes.Buffer
			e := checkpoint.NewEncoder(&out)
			if err := s.WriteCheckpoint(e, c); err == nil || e.Err() == nil {
				t.Fatal("writer accepted unknown binding")
			}
			if out.Len() != 0 {
				t.Fatal("failed preflight wrote payload")
			}
		})
	}
	var absent *Service
	if _, err := absent.CollectCheckpointReferences(nil); err == nil {
		t.Fatal("nil service accepted")
	}
}

func economySummary(t *testing.T, s *Service) (uint64, uint64) {
	t.Helper()
	var result checkpoint.Summary
	if err := s.AppendCheckpointSummary(&result); err != nil {
		t.Fatal(err)
	}
	return result.Result()
}

// Raw subnormal bit words make every selected position independently visible
// without duplicating the production traversal or running a canonical encoder.
func TestCheckpointEconomySummaryVectorAndAppend(t *testing.T) {
	f := math.Float32frombits
	s := &Service{unitBuckets: make([]UnitEconomy, 2)}
	s.Players[0] = Player{Stock: [2]float32{f(1), f(2)}, Capacity: [2]float32{f(3), f(4)},
		Mirror:       [2]Bucket{{f(5), f(6), f(7), f(8)}, {f(9), f(10), f(11), f(12)}},
		AIProduction: [2]float32{f(13), f(14)}, AIConsumption: [2]float32{f(15), f(16)},
		UpdateTime: 17, GameEnded: true, EndGameCountdown: -19, Allies: [10]bool{true, false, false, false, false, false, false, false, false, true}}
	s.Players[9].Stock[Metal] = f(20)
	s.unitBuckets[0].Buckets = [2]Bucket{{f(21), f(22), f(23), f(24)}, {f(25), f(26), f(27), f(28)}}
	s.unitBuckets[1].Buckets[Energy].Carry = f(29)
	// A fixed player contributes 29 words. Ten rows, the variable length word,
	// and two eight-word unit rows total 307; no slot is filtered by Exists.
	var words [307]uint64
	for i := 0; i < 17; i++ {
		words[i] = uint64(i + 1)
	}
	words[17] = 1
	minus := int64(-19)
	words[18] = uint64(minus)
	words[19] = 1
	words[28] = 1
	words[261] = 20
	words[290] = 2
	for i := 0; i < 8; i++ {
		words[291+i] = uint64(21 + i)
	}
	words[306] = 29
	var want, sumWords uint64
	for i, v := range words {
		want += uint64(i+1) * v
		sumWords += v
	}
	count, got := economySummary(t, s)
	if count != uint64(len(words)) || got != want {
		t.Fatalf("summary = %d/%016x, want %d/%016x", count, got, len(words), want)
	}
	var appended checkpoint.Summary
	appended.Word(7)
	if err := s.AppendCheckpointSummary(&appended); err != nil {
		t.Fatal(err)
	}
	count, got = appended.Result()
	if count != 308 || got != want+sumWords+7 {
		t.Fatalf("append reset owner numbering: %d/%016x", count, got)
	}
}

func TestCheckpointEconomySummaryRelationships(t *testing.T) {
	s := &Service{}
	count, base := economySummary(t, s)
	if count != 291 || base != 0 {
		t.Fatalf("empty summary %d/%d", count, base)
	}
	full := economyCheckpointBytes(t, s)
	s.Players[2].Stock[Energy] = 1
	if n, changed := economySummary(t, s); n != count || changed == base {
		t.Fatal("selected stock fault escaped summary")
	}
	if bytes.Equal(full, economyCheckpointBytes(t, s)) {
		t.Fatal("selected stock fault escaped full payload")
	}
	s.Players[2] = Player{}
	s.Players[9].TotalProduced[Energy] = 17
	if n, changed := economySummary(t, s); n != count || changed != base {
		t.Fatal("unselected accounting entered cheap row")
	}
	if bytes.Equal(full, economyCheckpointBytes(t, s)) {
		t.Fatal("unselected accounting absent from full payload")
	}
	s.Players[9] = Player{}
	s.Players[0].DisplayTimer = 123
	if n, changed := economySummary(t, s); n != count || changed != base || !bytes.Equal(full, economyCheckpointBytes(t, s)) {
		t.Fatal("excluded presentation entered capture")
	}
	// This deliberate weighted collision is a required blind spot, not equality:
	// word 1 carrying 2 and word 2 carrying 1 have the same sum but different full
	// payloads. A ring match cannot replace the full digest.
	a, b := &Service{}, &Service{}
	a.Players[0].Stock[Metal] = math.Float32frombits(2)
	b.Players[0].Stock[Energy] = math.Float32frombits(1)
	_, sa := economySummary(t, a)
	_, sb := economySummary(t, b)
	if sa != sb || bytes.Equal(economyCheckpointBytes(t, a), economyCheckpointBytes(t, b)) {
		t.Fatal("summary collision/full-payload relationship changed")
	}
}

func TestCheckpointEconomySummaryNaNFailureIsAtomic(t *testing.T) {
	for name, edit := range map[string]func(*Service){
		"stock":       func(s *Service) { s.Players[8].Stock[Energy] = math.Float32frombits(0x7fc00001) },
		"capacity":    func(s *Service) { s.Players[2].Capacity[Metal] = math.Float32frombits(0x7f800001) },
		"mirror":      func(s *Service) { s.Players[6].Mirror[Energy].Carry = math.Float32frombits(0xffc00001) },
		"production":  func(s *Service) { s.Players[3].AIProduction[Energy] = math.Float32frombits(0x7fc00001) },
		"consumption": func(s *Service) { s.Players[9].AIConsumption[Metal] = math.Float32frombits(0x7fc00001) },
		"bucket":      func(s *Service) { s.unitBuckets[1].Buckets[Energy].Requested = math.Float32frombits(0x7fc00001) },
	} {
		t.Run(name, func(t *testing.T) {
			s := &Service{unitBuckets: make([]UnitEconomy, 2)}
			edit(s)
			var summary checkpoint.Summary
			summary.Word(99)
			before := summary
			err := s.AppendCheckpointSummary(&summary)
			if err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint capture failed: logical path economy.Service.") || !strings.Contains(err.Error(), "NaN binary32") {
				t.Fatalf("summary error = %v", err)
			}
			if summary != before {
				t.Fatal("failed summary partially appended")
			}
		})
	}
	// Unselected NaNs are a cheap-row blind spot and remain full-capture errors.
	s := &Service{}
	s.Players[0].Waste[Metal] = math.Float64frombits(0x7ff0000000000001)
	if _, sum := economySummary(t, s); sum != 0 {
		t.Fatal("unselected NaN entered summary")
	}
	var absent *Service
	var summary checkpoint.Summary
	if err := absent.AppendCheckpointSummary(&summary); err == nil {
		t.Fatal("nil owner accepted")
	}
	if err := s.AppendCheckpointSummary(nil); err == nil {
		t.Fatal("nil accumulator accepted")
	}
}

func TestCheckpointEconomySummaryPurityAndNoAllocations(t *testing.T) {
	s := &Service{unitBuckets: make([]UnitEconomy, 5), Wind: &world.Wind{}, Terrain: &world.Terrain{}}
	s.Players[0].Stock[Metal] = math.Float32frombits(1 << 31)
	s.Players[9].Capacity[Energy] = float32(math.Inf(-1))
	s.unitBuckets[4].Buckets[Energy].Carry = math.Float32frombits(1)
	s.SetCloakCost(func(*units.Unit) float32 { t.Fatal("summary invoked cost"); return 0 })
	s.SetCloakDue(func(*units.Unit) bool { t.Fatal("summary invoked due"); return false })
	s.SetEndCondition(func(int, uint32) { t.Fatal("summary invoked end condition") })
	beforePlayers := s.Players
	beforeBuckets := append([]UnitEconomy(nil), s.unitBuckets...)
	var last checkpoint.Summary
	allocations := testing.AllocsPerRun(50, func() {
		var next checkpoint.Summary
		if err := s.AppendCheckpointSummary(&next); err != nil {
			panic(err)
		}
		last = next
	})
	if allocations != 0 {
		t.Fatalf("summary allocated %g objects", allocations)
	}
	if !reflect.DeepEqual(beforePlayers, s.Players) || !reflect.DeepEqual(beforeBuckets, s.unitBuckets) {
		t.Fatal("summary changed stored state")
	}
	if words, _ := last.Result(); words != 331 {
		t.Fatalf("summary words = %d", words)
	}
	// Exact selected bits survive the fold; +0 versus -0 alone changes the sum.
	_, negative := last.Result()
	s.Players[0].Stock[Metal] = 0
	_, positive := economySummary(t, s)
	if negative-positive != 1<<31 {
		t.Fatalf("signed-zero delta = %x", negative-positive)
	}
}
