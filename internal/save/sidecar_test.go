package save

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/community"
)

// A sidecar round-trips every field, including the three spellings of `mod`
// (null, an object, "custom"), and a save without one reads as absent rather
// than as an error (docs/DESIGN_MODS_MUTATORS.md §7.2, §7.3 step 1).
func TestSidecarRoundTripsEveryModSpelling(t *testing.T) {
	dir := t.TempDir()
	enabled := true
	full := Sidecar{
		Profile:         "nanolathe-1.0",
		Mod:             SidecarModRef{Mod: &SidecarMod{ID: "prota", Version: "4.8", SHA256: strings.Repeat("ab", 32)}},
		ContentProfile:  "prota",
		ContentManifest: "manifest",
		Catalog:         "catalog",
		Rules:           "community-3.9",
		Gameplay:        "community-3.9",
		Community: SidecarCommunity{
			Sources: SidecarCommunitySources{Player: community.Overrides{Table: "community-3.9", Veterancy: &enabled}},
			Entry:   community.Features{UnitLimit: 1500, ConstructionKickout: true},
		},
		UnitLimit: 1500,
		Mutators:  map[string]string{"buildSpeed": "2", "health": "1.5"},
	}
	for name, mod := range map[string]SidecarModRef{
		"object": full.Mod,
		"none":   {},
		"custom": {Custom: true},
	} {
		bank := filepath.Join(dir, name+".SAV")
		want := full
		want.Mod = mod
		if err := WriteSidecar(bank, want); err != nil {
			t.Fatal(err)
		}
		got, ok, err := ReadSidecar(bank)
		if err != nil || !ok {
			t.Fatalf("%s: ReadSidecar = (%v, %v)", name, ok, err)
		}
		want.Schema = SidecarSchema
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: read back %+v, want %+v", name, got, want)
		}
	}
	raw, err := os.ReadFile(SidecarPath(filepath.Join(dir, "none.SAV")))
	if err != nil || !strings.Contains(string(raw), `"mod": null`) {
		t.Fatalf("no-mod sidecar = %s, %v; want mod null", raw, err)
	}
	raw, _ = os.ReadFile(SidecarPath(filepath.Join(dir, "custom.SAV")))
	if !strings.Contains(string(raw), `"mod": "custom"`) {
		t.Fatalf("custom sidecar = %s; want mod \"custom\"", raw)
	}
	// Only the sidecars remain: no temporary file outlives a write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".SAV"+SidecarSuffix) {
			t.Fatalf("stray file %s after writing", e.Name())
		}
	}

	if _, ok, err := ReadSidecar(filepath.Join(dir, "absent.SAV")); ok || err != nil {
		t.Fatalf("absent sidecar = (%v, %v), want (false, nil)", ok, err)
	}
	if err := RemoveSidecar(filepath.Join(dir, "none.SAV")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSidecar(filepath.Join(dir, "none.SAV")); err != nil {
		t.Fatalf("removing an absent sidecar = %v, want nil", err)
	}
}

// A sidecar that exists but cannot be honoured is an error, never read as
// absent: that would load the game with none of the selection it records.
func TestSidecarRefusesAnotherSchemaAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"schema":  `{"schema": 3, "rules": "modern", "mod": null}`,
		"json":    `{"schema": 1,`,
		"mod":     `{"schema": 1, "mod": "prota"}`,
		"modless": `{"schema": 1, "mod": {"id": "prota"}}`,
	} {
		bank := filepath.Join(dir, name+".SAV")
		if err := os.WriteFile(SidecarPath(bank), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ReadSidecar(bank); err == nil || ok {
			t.Fatalf("%s: ReadSidecar = (%v, %v), want an error", name, ok, err)
		}
	}
}

// The Modern AI record rides in the sidecar as raw JSON and comes back with
// the same content; a sidecar without it reads as having none
// (docs/DESIGN_MODS_MUTATORS.md §7.2).
func TestSidecarCarriesTheAIRecord(t *testing.T) {
	dir := t.TempDir()
	const record = `{"seed":7,"generators":[{"player":1,"position":9}]}`
	bank := filepath.Join(dir, "ai.SAV")
	if err := WriteSidecar(bank, Sidecar{Profile: "nanolathe-1.0", AI: json.RawMessage(record)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadSidecar(bank)
	if err != nil || !ok {
		t.Fatalf("read: ok %v, err %v", ok, err)
	}
	var want, have any
	if err := json.Unmarshal([]byte(record), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.AI, &have); err != nil {
		t.Fatalf("ai record does not parse after the round trip: %v", err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Fatalf("ai record %s, want %s", got.AI, record)
	}
	plain := filepath.Join(dir, "plain.SAV")
	if err := WriteSidecar(plain, Sidecar{Profile: "nanolathe-1.0"}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := ReadSidecar(plain); err != nil || len(got.AI) != 0 {
		t.Fatalf("a sidecar without the record read back %q (err %v)", got.AI, err)
	}
	raw, err := os.ReadFile(SidecarPath(plain))
	if err != nil || strings.Contains(string(raw), "survival") {
		t.Fatalf("plain sidecar = %s, %v; want no Survival fields", raw, err)
	}
}

// A sidecar is written as schema 2 exactly when it records unit
// restrictions, so every other save stays readable by a build that reads
// only schema 1, and that build refuses a restricted one rather than
// restoring its bank without them (docs/DESIGN_MODS_MUTATORS.md §15.5,
// proposal R-P7). This build reads both; a schema 1 sidecar naming
// restrictions, even as null, and a schema 2 one without a non-empty set
// are malformed.
func TestSidecarSchemaFollowsRestrictions(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.SAV")
	if err := WriteSidecar(plain, Sidecar{Profile: "nanolathe-1.0", Restrictions: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(SidecarPath(plain))
	if !strings.Contains(string(raw), `"schema": 1`) || strings.Contains(string(raw), "restrictions") {
		t.Fatalf("a sidecar without restrictions = %s, want schema 1 and no restrictions key", raw)
	}
	restricted := filepath.Join(dir, "restricted.SAV")
	want := map[string]int{"armkrog": 0, "armpw": 20}
	if err := WriteSidecar(restricted, Sidecar{Profile: "nanolathe-1.0", Restrictions: want}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(SidecarPath(restricted))
	if !strings.Contains(string(raw), `"schema": 2`) {
		t.Fatalf("a sidecar with restrictions = %s, want schema 2", raw)
	}
	got, ok, err := ReadSidecar(restricted)
	if err != nil || !ok || got.Schema != SidecarSchemaRestrictions || !reflect.DeepEqual(got.Restrictions, want) {
		t.Fatalf("read back %+v (%v, %v), want schema 2 with %v", got, ok, err, want)
	}
	if got, ok, err := ReadSidecar(plain); err != nil || !ok || got.Schema != SidecarSchema || got.Restrictions != nil {
		t.Fatalf("read back %+v (%v, %v), want schema 1 without restrictions", got, ok, err)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"schema 1 with":      {`{"schema": 1, "mod": null, "restrictions": {"armpw": 0}}`, "schema 1 with unit restrictions"},
		"schema 1 with null": {`{"schema": 1, "mod": null, "restrictions": null}`, "schema 1 with unit restrictions"},
		"schema 2 without":   {`{"schema": 2, "mod": null}`, "schema 2 without unit restrictions"},
		"schema 2 empty":     {`{"schema": 2, "mod": null, "restrictions": {}}`, "schema 2 without unit restrictions"},
	} {
		bank := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".SAV")
		if err := os.WriteFile(SidecarPath(bank), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ReadSidecar(bank); err == nil || ok || !strings.Contains(err.Error(), "unreadable: "+tc.want) {
			t.Fatalf("%s: ReadSidecar = (%v, %v), want a refusal for %s", name, ok, err, tc.want)
		}
	}
}

// A sidecar with official add-ons is schema 3, with or without unit
// restrictions, so a build that cannot restore them refuses it (§5.7).
func TestSidecarSchemaFollowsAddons(t *testing.T) {
	dir := t.TempDir()
	addons := []SidecarAddon{{ID: "cavedog-bonus-units", Version: "original", SHA256: strings.Repeat("ab", 32)}}
	for _, restrictions := range []map[string]int{nil, {"armpw": 20}} {
		bank := filepath.Join(dir, "addons.SAV")
		if err := WriteSidecar(bank, Sidecar{Profile: "nanolathe-1.0", Restrictions: restrictions, Addons: addons}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := ReadSidecar(bank)
		if err != nil || !ok || got.Schema != SidecarSchemaAddons || !reflect.DeepEqual(got.Addons, addons) || !reflect.DeepEqual(got.Restrictions, restrictions) {
			t.Fatalf("read back %+v (%v, %v), want schema 3 with %v", got, ok, err, addons)
		}
	}
	for name, tc := range map[string]struct{ body, want string }{
		"schema 1 with":    {`{"schema": 1, "mod": null, "addons": []}`, "schema 1 with official add-ons"},
		"schema 2 with":    {`{"schema": 2, "mod": null, "restrictions": {"armpw": 0}, "addons": [{"id": "a", "version": "1", "sha256": "` + strings.Repeat("0", 64) + `"}]}`, "schema 2 with official add-ons"},
		"schema 3 without": {`{"schema": 3, "mod": null}`, "schema 3 without official add-ons"},
		"no identity":      {`{"schema": 3, "mod": null, "addons": [{"id": "a", "version": "1"}]}`, "an official add-on without an id, version and SHA-256"},
	} {
		bank := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".SAV")
		if err := os.WriteFile(SidecarPath(bank), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ReadSidecar(bank); err == nil || ok || !strings.Contains(err.Error(), "unreadable: "+tc.want) {
			t.Fatalf("%s: ReadSidecar = (%v, %v), want a refusal for %s", name, ok, err, tc.want)
		}
	}
}

// A Survival director takes schema precedence over restrictions and add-ons,
// so an older build refuses the battle rather than silently restoring an
// ordinary skirmish. Its director and host eligibility record stay opaque
// to this package (docs/DESIGN_MODS_MUTATORS.md §7.2).
func TestSidecarSchemaFollowsSurvival(t *testing.T) {
	director := json.RawMessage(`{"version":1,"wave":7}`)
	best := json.RawMessage(`{"version":1,"eligible":true,"setup":"saved-setup"}`)
	addons := []SidecarAddon{{ID: "cavedog-bonus-units", Version: "original", SHA256: strings.Repeat("ab", 32)}}
	for _, tc := range []struct {
		name         string
		restrictions map[string]int
		addons       []SidecarAddon
		best         json.RawMessage
	}{
		{name: "director"},
		{name: "restrictions", restrictions: map[string]int{"armpw": 20}, best: best},
		{name: "addons", addons: addons, best: best},
		{name: "both", restrictions: map[string]int{"armpw": 20}, addons: addons, best: best},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "survival.SAV")
			want := Sidecar{
				Profile: "nanolathe-1.0", Mutators: map[string]string{},
				Restrictions: tc.restrictions, Addons: tc.addons,
				Survival: director, SurvivalBest: tc.best,
			}
			if err := WriteSidecar(bank, want); err != nil {
				t.Fatal(err)
			}
			got, ok, err := ReadSidecar(bank)
			if err != nil || !ok {
				t.Fatalf("ReadSidecar = (%v, %v)", ok, err)
			}
			// Indentation is a writer concern; the raw records' JSON values
			// survive without the save package knowing their shape.
			for _, record := range []struct {
				got  *json.RawMessage
				want json.RawMessage
			}{
				{&got.Survival, want.Survival},
				{&got.SurvivalBest, want.SurvivalBest},
			} {
				if len(record.want) == 0 {
					if len(*record.got) != 0 {
						t.Fatalf("read unexpected record %s", *record.got)
					}
					continue
				}
				var have, expected any
				if err := json.Unmarshal(*record.got, &have); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(record.want, &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(have, expected) {
					t.Fatalf("read record %s, want %s", *record.got, record.want)
				}
				*record.got = record.want
			}
			want.Schema = SidecarSchemaSurvival
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("read back %+v, want %+v", got, want)
			}
		})
	}
}

// Every earlier schema rejects even a null Survival field. Schema 4 requires
// the director, and the host eligibility record cannot exist without it
// (docs/DESIGN_MODS_MUTATORS.md §7.2).
func TestSidecarRefusesUnsupportedSurvivalCombinations(t *testing.T) {
	addon := `"addons":[{"id":"a","version":"1","sha256":"` + strings.Repeat("0", 64) + `"}]`
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"schema 1 director", `{"schema":1,"survival":{}}`, "schema 1 with a Survival director"},
		{"schema 1 null", `{"schema":1,"survival":null}`, "schema 1 with a Survival director"},
		{"schema 2 director", `{"schema":2,"restrictions":{"armpw":20},"survival":{}}`, "schema 2 with a Survival director"},
		{"schema 2 null", `{"schema":2,"restrictions":{"armpw":20},"survival":null}`, "schema 2 with a Survival director"},
		{"schema 3 director", `{"schema":3,` + addon + `,"survival":{}}`, "schema 3 with a Survival director"},
		{"schema 3 null", `{"schema":3,` + addon + `,"survival":null}`, "schema 3 with a Survival director"},
		{"schema 4 missing", `{"schema":4}`, "schema 4 without a non-null Survival director"},
		{"schema 4 null", `{"schema":4,"survival":null}`, "schema 4 without a non-null Survival director"},
		{"schema 1 best only", `{"schema":1,"survivalBest":{}}`, "survival best-score eligibility without a Survival director"},
		{"schema 1 null best", `{"schema":1,"survivalBest":null}`, "survival best-score eligibility without a Survival director"},
		{"schema 2 best only", `{"schema":2,"restrictions":{"armpw":20},"survivalBest":{}}`, "survival best-score eligibility without a Survival director"},
		{"schema 3 best only", `{"schema":3,` + addon + `,"survivalBest":{}}`, "survival best-score eligibility without a Survival director"},
		{"schema 4 best only", `{"schema":4,"survivalBest":{}}`, "schema 4 without a non-null Survival director"},
		{"future schema", `{"schema":6}`, "schema 6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "survival.SAV")
			if err := os.WriteFile(SidecarPath(bank), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := ReadSidecar(bank); err == nil || ok || !strings.Contains(err.Error(), "unreadable: "+tc.want) {
				t.Fatalf("ReadSidecar = (%v, %v), want a refusal for %s", ok, err, tc.want)
			}
		})
	}
}

func TestWriteSidecarRefusesInvalidSurvivalCombinations(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Sidecar
	}{
		{"null director", Sidecar{Survival: json.RawMessage(`null`)}},
		{"spaced null director", Sidecar{Survival: json.RawMessage(" \nnull\t ")}},
		{"best without director", Sidecar{SurvivalBest: json.RawMessage(`{}`)}},
		{"null best without director", Sidecar{SurvivalBest: json.RawMessage(`null`)}},
		{"best with null director", Sidecar{Survival: json.RawMessage(`null`), SurvivalBest: json.RawMessage(`{}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bank := filepath.Join(dir, "survival.SAV")
			if err := WriteSidecar(bank, tc.s); err == nil {
				t.Fatal("WriteSidecar succeeded, want a refusal")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("files after refused write: %v, err %v; want none", entries, err)
			}
		})
	}
}

// A generated map's recipe makes the file schema 5, whatever else it
// records, and comes back as written; schema 5 requires the recipe and no
// earlier schema may carry one (docs/DESIGN_MAP_GENERATOR.md §10).
func TestSidecarSchemaFollowsGeneratedMap(t *testing.T) {
	recipe := json.RawMessage(`{"format":1,"generator":1,"code":"test_world-2-0-6-1-1-1-b-v1"}`)
	for _, tc := range []struct {
		name string
		s    Sidecar
	}{
		{"recipe", Sidecar{GeneratedMap: recipe}},
		{"with the rest", Sidecar{GeneratedMap: recipe, Restrictions: map[string]int{"armpw": 20},
			Addons:   []SidecarAddon{{ID: "cavedog-bonus-units", Version: "original", SHA256: strings.Repeat("ab", 32)}},
			Survival: json.RawMessage(`{"version":1}`), SurvivalBest: json.RawMessage(`{"version":1}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "generated.SAV")
			if err := WriteSidecar(bank, tc.s); err != nil {
				t.Fatal(err)
			}
			got, ok, err := ReadSidecar(bank)
			if err != nil || !ok || got.Schema != SidecarSchemaGeneratedMap {
				t.Fatalf("ReadSidecar = (schema %d, %v, %v), want schema 5", got.Schema, ok, err)
			}
			var have, want any
			if json.Unmarshal(got.GeneratedMap, &have) != nil || json.Unmarshal(recipe, &want) != nil || !reflect.DeepEqual(have, want) {
				t.Fatalf("read recipe %s, want %s", got.GeneratedMap, recipe)
			}
			if len(tc.s.Survival) != 0 && len(got.Survival) == 0 {
				t.Fatal("the Survival director was dropped")
			}
		})
	}
	for _, tc := range []struct{ name, body, want string }{
		{"schema 5 without", `{"schema":5}`, "schema 5 without a generated map"},
		{"schema 5 null", `{"schema":5,"generatedMap":null}`, "schema 5 without a generated map"},
		{"schema 4 with", `{"schema":4,"survival":{},"generatedMap":{}}`, "schema 4 with a generated map"},
		{"schema 1 with", `{"schema":1,"generatedMap":{}}`, "schema 1 with a generated map"},
		{"schema 5 null director", `{"schema":5,"generatedMap":{},"survival":null}`, "schema 5 with a null Survival director"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "generated.SAV")
			if err := os.WriteFile(SidecarPath(bank), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := ReadSidecar(bank); err == nil || ok || !strings.Contains(err.Error(), "unreadable: "+tc.want) {
				t.Fatalf("ReadSidecar = (%v, %v), want a refusal for %s", ok, err, tc.want)
			}
		})
	}
}

// A continued online replay's offline save selects schema 6, beside any of
// the other records, and earlier schemas refuse its record
// (docs/DESIGN_MULTIPLAYER.md §10.1).
func TestSidecarSchemaFollowsOnlineReplay(t *testing.T) {
	record := &SidecarOnlineReplay{
		Content:     []SidecarRoomContent{{Kind: 1, Name: "totala1.hpi", Size: 7, SHA256: strings.Repeat("cd", 32)}},
		Difficulty:  []SidecarSeatDifficulty{{Player: 2, Difficulty: 0}, {Player: 3, Difficulty: 2}},
		SharedSight: true,
	}
	for _, s := range []Sidecar{
		{OnlineReplay: record},
		{OnlineReplay: record, GeneratedMap: json.RawMessage(`{"name":"gen"}`), Survival: json.RawMessage(`{"version":1}`),
			Addons: []SidecarAddon{{ID: "cavedog-bonus-units", Version: "original", SHA256: strings.Repeat("ab", 32)}}},
	} {
		bank := filepath.Join(t.TempDir(), "continued.SAV")
		if err := WriteSidecar(bank, s); err != nil {
			t.Fatal(err)
		}
		got, ok, err := ReadSidecar(bank)
		if err != nil || !ok || got.Schema != SidecarSchemaOnlineReplay || !reflect.DeepEqual(got.OnlineReplay, record) {
			t.Fatalf("ReadSidecar = (schema %d, %+v, %v, %v), want schema 6 with the record", got.Schema, got.OnlineReplay, ok, err)
		}
	}
	for _, tc := range []struct{ name, body, want string }{
		{"schema 6 without", `{"schema":6}`, "schema 6 without a continued online replay"},
		{"schema 5 with", `{"schema":5,"generatedMap":{},"onlineReplay":{}}`, "schema 5 with a continued online replay"},
		{"bad content", `{"schema":6,"onlineReplay":{"content":[{"kind":4,"name":"x","sha256":"` + strings.Repeat("ab", 32) + `"}]}}`, "a room content entry"},
		{"repeated seat", `{"schema":6,"onlineReplay":{"difficulty":[{"player":2,"difficulty":1},{"player":2,"difficulty":0}]}}`, "a computer seat difficulty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "continued.SAV")
			if err := os.WriteFile(SidecarPath(bank), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := ReadSidecar(bank); err == nil || ok || !strings.Contains(err.Error(), "unreadable: "+tc.want) {
				t.Fatalf("ReadSidecar = (%v, %v), want a refusal for %s", ok, err, tc.want)
			}
		})
	}
}
