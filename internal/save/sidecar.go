package save

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nanolathe-gg/nanolathe/internal/community"
)

// The save sidecar is a Nanolathe file beside a retail bank that records the
// match selection the bank was written under: the mod, content profile, rule
// set, Community sources and entry table, unit limit, mutators, unit
// restrictions and Survival director (docs/DESIGN_MODS_MUTATORS.md §7,
// §15.5). The bank's bytes are unchanged; the retail enumerator lists only
// `*.SAV`, so the sidecar never shows as a save.

// SidecarSuffix is appended to the bank's full file name:
// `SAVEGAME/<name>.SAV` gets `SAVEGAME/<name>.SAV.nanolathe.json` (§7.1).
const SidecarSuffix = ".nanolathe.json"

// SidecarSchema is the layout of a sidecar without unit restrictions,
// official add-ons or a Survival director, so older builds keep reading
// those saves (§7.2).
const SidecarSchema = 1

// SidecarSchemaRestrictions is the layout of a sidecar that records unit
// restrictions (§15.5, proposal R-P7). A build that reads only schema 1
// refuses it, where it would otherwise load the bank without the
// restrictions and misread the definition indices it was written with.
// Later schemas also carry restrictions; an unsupported schema is refused
// rather than half-read (§7.3).
const SidecarSchemaRestrictions = 2

// SidecarSchemaAddons is the layout of a sidecar that records official
// add-ons, with or without unit restrictions (DESIGN_MODS_MUTATORS §5.7). A
// build that cannot restore them refuses it rather than loading the bank
// without the definitions it was written with.
const SidecarSchemaAddons = 3

// SidecarSchemaSurvival is the layout of a sidecar that records a Survival
// director, with or without unit restrictions and official add-ons (§7.2).
// A build that cannot restore the director refuses it rather than loading
// the bank as an ordinary skirmish.
const SidecarSchemaSurvival = 4

// SidecarSchemaGeneratedMap is the layout of a sidecar that records the
// recipe of the generated map its battle was played on, with or without unit
// restrictions, official add-ons and a Survival director
// (docs/DESIGN_MAP_GENERATOR.md §10). A build that cannot make the map again
// refuses it rather than loading the bank on whatever map has that name.
const SidecarSchemaGeneratedMap = 5

// SidecarSchemaOnlineReplay is the layout of a sidecar that records an
// offline save of a continued online replay (docs/DESIGN_MULTIPLAYER.md
// §10.1), with or without unit restrictions, official add-ons, a Survival
// director and a generated map's recipe. A build that cannot mount the
// room's content or restore its computers' difficulties refuses it rather
// than loading the bank on other content.
const SidecarSchemaOnlineReplay = 6

// sidecarMaxBytes bounds ordinary selection records. A Survival director also
// carries the whole planned wave and its history, so schema 4 has a larger
// bounded allowance (DESIGN_MODS_MUTATORS §7.2). Writers enforce the same bound
// as readers before replacing any file.
const sidecarMaxBytes = 1 << 20
const survivalSidecarMaxBytes = 16 << 20

func sidecarByteLimit(schema int) int {
	if schema == SidecarSchemaSurvival || schema == SidecarSchemaGeneratedMap || schema == SidecarSchemaOnlineReplay {
		return survivalSidecarMaxBytes
	}
	return sidecarMaxBytes
}

// SidecarMod names the installed mod a save was written under.
type SidecarMod struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256,omitempty"`
}

// SidecarAddon names an official add-on a save was written with: its id,
// version and content identity, the hex SHA-256 that addonlibrary.Digest
// gives its archives.
type SidecarAddon struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// SidecarOnlineReplay is what an offline save of a continued online replay
// records beyond an ordinary skirmish's sidecar (docs/DESIGN_MULTIPLAYER.md
// §10.1): the room content the battle ran on, each computer seat's own
// difficulty when the room's computers differed, and whether the room's teams
// shared sight, which a load keeps in every gameplay mode.
type SidecarOnlineReplay struct {
	Content     []SidecarRoomContent    `json:"content,omitempty"`
	Difficulty  []SidecarSeatDifficulty `json:"difficulty,omitempty"`
	SharedSight bool                    `json:"sharedSight,omitempty"`
}

// SidecarRoomContent is one room content entry (DESIGN_MULTIPLAYER §16.8):
// its kind (1 a base archive, 2 an official add-on, 3 a map package), name,
// version, archive size and hex SHA-256.
type SidecarRoomContent struct {
	Kind    uint8  `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Size    uint64 `json:"size"`
	SHA256  string `json:"sha256"`
}

// SidecarSeatDifficulty is one computer seat's own difficulty word, 0 easy
// to 2 hard.
type SidecarSeatDifficulty struct {
	Player     uint8 `json:"player"`
	Difficulty int   `json:"difficulty"`
}

// SidecarModRef is the sidecar's `mod` value: null for no mod, an object
// for an installed mod, and the string "custom" for a command line that
// stacked its own content roots (§7.2).
type SidecarModRef struct {
	Mod    *SidecarMod
	Custom bool
}

const sidecarCustomMod = "custom"

func (r SidecarModRef) MarshalJSON() ([]byte, error) {
	switch {
	case r.Custom:
		return json.Marshal(sidecarCustomMod)
	case r.Mod == nil:
		return []byte("null"), nil
	}
	return json.Marshal(r.Mod)
}

func (r *SidecarModRef) UnmarshalJSON(data []byte) error {
	*r = SidecarModRef{}
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("null")):
		return nil
	case len(data) > 0 && data[0] == '"':
		var word string
		if err := json.Unmarshal(data, &word); err != nil {
			return err
		}
		if word != sidecarCustomMod {
			return fmt.Errorf("mod is %q, expected null, an object or %q", word, sidecarCustomMod)
		}
		r.Custom = true
		return nil
	}
	var mod SidecarMod
	if err := json.Unmarshal(data, &mod); err != nil {
		return err
	}
	if mod.ID == "" || mod.Version == "" {
		return errors.New("mod names no id or version")
	}
	r.Mod = &mod
	return nil
}

// SidecarCommunitySources are the Community declarations the session
// resolved its tables from, in precedence order (DESIGN_COMMUNITY_PATCH §3.2).
type SidecarCommunitySources struct {
	Content     []community.Overrides `json:"content,omitempty"`
	Player      community.Overrides   `json:"player"`
	CommandLine []community.Overrides `json:"commandLine,omitempty"`
}

// SidecarCommunity records the session's sources and its battle-entry table,
// so a restored battle resolves and switches exactly as the saved one did.
type SidecarCommunity struct {
	Sources SidecarCommunitySources `json:"sources"`
	Entry   community.Features      `json:"entry"`
}

// Sidecar is the whole file (§7.2).
type Sidecar struct {
	Schema          int               `json:"schema"`
	Profile         string            `json:"profile"`
	Mod             SidecarModRef     `json:"mod"`
	ContentProfile  string            `json:"contentProfile,omitempty"`
	ContentManifest string            `json:"contentManifest,omitempty"`
	Catalog         string            `json:"catalog,omitempty"`
	Rules           string            `json:"rules"`
	Gameplay        string            `json:"gameplay"`
	Community       SidecarCommunity  `json:"community"`
	UnitLimit       int               `json:"unitLimit"`
	Mutators        map[string]string `json:"mutators"`
	// Restrictions are the battle's unit restrictions, canonical unit key to
	// count, e.g. {"armkrog": 0, "armpw": 20} (§15.5); absent when it had
	// none. Without add-ons or Survival, their presence makes the file schema 2.
	Restrictions map[string]int `json:"restrictions,omitempty"`
	// Addons are the official add-ons the battle's content mounted from the
	// add-on library (§5.7); absent when it had none. Their presence alone
	// makes the file schema 3 when there is no Survival director.
	Addons []SidecarAddon `json:"addons,omitempty"`
	// AI is the Modern AI controllers' record (session.AIControllers),
	// kept as raw JSON so this package stays ignorant of its shape; absent
	// when the battle had no computer player.
	AI json.RawMessage `json:"ai,omitempty"`
	// Survival is a Survival battle's wave director
	// (session.SurvivalRecord, docs/DESIGN_SURVIVAL.md §11), raw like AI;
	// absent in every other battle. Its presence makes the file schema 4.
	Survival json.RawMessage `json:"survival,omitempty"`
	// SurvivalBest is the host's personal-best eligibility record
	// (docs/DESIGN_SURVIVAL.md §11), raw like Survival; valid only with a
	// Survival director. The save package does not interpret its shape.
	SurvivalBest json.RawMessage `json:"survivalBest,omitempty"`
	// AlliedSight records that a skirmish's teams shared sight and radar
	// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern allied sight"); absent when
	// they did not. Like `ai` it leaves the schema alone: a build that does
	// not know it loads the battle with sight kept separate, which is a
	// presentation of the same simulation rather than a misread bank.
	AlliedSight bool `json:"alliedSight,omitempty"`
	// GeneratedMap is the recipe of the generated map the battle was played
	// on (docs/DESIGN_MAP_GENERATOR.md §10), raw like AI: the command owns
	// its shape. Its presence makes the file schema 5.
	GeneratedMap json.RawMessage `json:"generatedMap,omitempty"`
	// OnlineReplay marks an offline save of a continued online replay and
	// carries what its load needs beyond the other records. Its presence
	// makes the file schema 6.
	OnlineReplay *SidecarOnlineReplay `json:"onlineReplay,omitempty"`
}

// SidecarPath is the sidecar file of the bank at bankPath.
func SidecarPath(bankPath string) string { return bankPath + SidecarSuffix }

// WriteSidecar writes the sidecar of the bank at bankPath through a
// temporary file and a rename, so a reader never sees half a file (§7.1).
// It is schema 5 when it records a generated map's recipe, else schema 4
// when it records a Survival director, else schema 3 when it records
// official add-ons, else schema 2 when it records unit restrictions, and
// schema 1 otherwise (§7.2, §15.5, §5.7).
func WriteSidecar(bankPath string, s Sidecar) error {
	s.Schema = SidecarSchema
	if len(s.Restrictions) != 0 {
		s.Schema = SidecarSchemaRestrictions
	} else {
		s.Restrictions = nil
	}
	if len(s.Addons) != 0 {
		s.Schema = SidecarSchemaAddons
	} else {
		s.Addons = nil
	}
	if len(s.Survival) != 0 {
		s.Schema = SidecarSchemaSurvival
	}
	if len(s.GeneratedMap) != 0 {
		s.Schema = SidecarSchemaGeneratedMap
	}
	if s.OnlineReplay != nil {
		s.Schema = SidecarSchemaOnlineReplay
	}
	if err := validateSurvivalSidecar(s); err != nil {
		return sidecarError(SidecarPath(bankPath), err.Error())
	}
	if err := validateOnlineReplay(s.OnlineReplay); err != nil {
		return sidecarError(SidecarPath(bankPath), err.Error())
	}
	if s.Mutators == nil {
		s.Mutators = map[string]string{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	target := SidecarPath(bankPath)
	if len(data) > sidecarByteLimit(s.Schema) {
		return sidecarError(target, fmt.Sprintf("file is larger than %d MiB", sidecarByteLimit(s.Schema)>>20))
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err == nil {
		err = os.Rename(name, target)
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// ReadSidecar reads the sidecar of the bank at bankPath. A missing sidecar,
// as with every retail save and every older Nanolathe save, reports false
// and no error (§7.3 step 1). A sidecar that exists but cannot be read, or
// that another schema wrote, is an error: loading it as if it were absent
// would silently drop the selection it records. So is a schema 1 sidecar
// that names unit restrictions, a schema 2 one without a non-empty set
// (§15.5), and one that names add-ons in any schema but 3 or 4, or schema 3
// without them (§5.7). A Survival record requires schema 4 or 5, and schema
// 4 requires a non-null director; its host eligibility record requires that
// same director (§7.2). A generated map's recipe requires schema 5, and
// schema 5 requires one (DESIGN_MAP_GENERATOR §10).
func ReadSidecar(bankPath string) (Sidecar, bool, error) {
	path := SidecarPath(bankPath)
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Sidecar{}, false, nil
	}
	if err != nil {
		return Sidecar{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, survivalSidecarMaxBytes+1))
	if err != nil {
		return Sidecar{}, false, err
	}
	if len(data) > survivalSidecarMaxBytes {
		return Sidecar{}, false, sidecarError(path, "file is larger than 16 MiB")
	}
	// Decode just the schema before allocating the record's arrays and maps.
	// Older schemas retain their existing limit and compatibility guarantee.
	var header struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Sidecar{}, false, sidecarError(path, err.Error())
	}
	if len(data) > sidecarByteLimit(header.Schema) {
		return Sidecar{}, false, sidecarError(path, fmt.Sprintf("file is larger than %d MiB", sidecarByteLimit(header.Schema)>>20))
	}
	var s Sidecar
	if err := json.Unmarshal(data, &s); err != nil {
		return Sidecar{}, false, sidecarError(path, err.Error())
	}
	var present struct {
		Restrictions json.RawMessage `json:"restrictions"`
		Addons       json.RawMessage `json:"addons"`
	}
	if err := json.Unmarshal(data, &present); err != nil {
		return Sidecar{}, false, sidecarError(path, err.Error())
	}
	if err := validateSurvivalSidecar(s); err != nil {
		return Sidecar{}, false, sidecarError(path, err.Error())
	}
	switch {
	case s.Schema == SidecarSchema && present.Restrictions != nil:
		return Sidecar{}, false, sidecarError(path, "schema 1 with unit restrictions")
	case s.Schema == SidecarSchemaRestrictions && len(s.Restrictions) == 0:
		return Sidecar{}, false, sidecarError(path, "schema 2 without unit restrictions")
	case s.Schema != SidecarSchemaAddons && s.Schema != SidecarSchemaSurvival && s.Schema != SidecarSchemaGeneratedMap && s.Schema != SidecarSchemaOnlineReplay && present.Addons != nil:
		return Sidecar{}, false, sidecarError(path, fmt.Sprintf("schema %d with official add-ons", s.Schema))
	case s.Schema == SidecarSchemaAddons && len(s.Addons) == 0:
		return Sidecar{}, false, sidecarError(path, "schema 3 without official add-ons")
	case s.Schema != SidecarSchemaGeneratedMap && s.Schema != SidecarSchemaOnlineReplay && len(s.GeneratedMap) != 0:
		return Sidecar{}, false, sidecarError(path, fmt.Sprintf("schema %d with a generated map", s.Schema))
	case s.Schema == SidecarSchemaGeneratedMap && (len(s.GeneratedMap) == 0 || bytes.Equal(bytes.TrimSpace(s.GeneratedMap), []byte("null"))):
		return Sidecar{}, false, sidecarError(path, "schema 5 without a generated map")
	case s.Schema != SidecarSchemaOnlineReplay && s.OnlineReplay != nil:
		return Sidecar{}, false, sidecarError(path, fmt.Sprintf("schema %d with a continued online replay", s.Schema))
	case s.Schema == SidecarSchemaOnlineReplay && s.OnlineReplay == nil:
		return Sidecar{}, false, sidecarError(path, "schema 6 without a continued online replay")
	case s.Schema != SidecarSchema && s.Schema != SidecarSchemaRestrictions && s.Schema != SidecarSchemaAddons && s.Schema != SidecarSchemaSurvival && s.Schema != SidecarSchemaGeneratedMap && s.Schema != SidecarSchemaOnlineReplay:
		return Sidecar{}, false, sidecarError(path, fmt.Sprintf("schema %d", s.Schema))
	}
	for _, a := range s.Addons {
		if a.ID == "" || a.Version == "" || len(a.SHA256) != 64 {
			return Sidecar{}, false, sidecarError(path, "an official add-on without an id, version and SHA-256")
		}
	}
	if err := validateOnlineReplay(s.OnlineReplay); err != nil {
		return Sidecar{}, false, sidecarError(path, err.Error())
	}
	return s, true, nil
}

// validateSurvivalSidecar enforces the schema boundary without interpreting
// the director or host eligibility record. Their owners validate those
// records' versions and contents (docs/DESIGN_MODS_MUTATORS.md §7.2).
func validateSurvivalSidecar(s Sidecar) error {
	null := bytes.Equal(bytes.TrimSpace(s.Survival), []byte("null"))
	switch {
	case s.Schema != SidecarSchemaSurvival && s.Schema != SidecarSchemaGeneratedMap && s.Schema != SidecarSchemaOnlineReplay && len(s.Survival) != 0:
		return fmt.Errorf("schema %d with a Survival director", s.Schema)
	case s.Schema == SidecarSchemaSurvival && (len(s.Survival) == 0 || null):
		return errors.New("schema 4 without a non-null Survival director")
	case (s.Schema == SidecarSchemaGeneratedMap || s.Schema == SidecarSchemaOnlineReplay) && len(s.Survival) != 0 && null:
		return fmt.Errorf("schema %d with a null Survival director", s.Schema)
	case len(s.Survival) == 0 && len(s.SurvivalBest) != 0:
		return errors.New("survival best-score eligibility without a Survival director")
	}
	return nil
}

// validateOnlineReplay checks a continued online replay's record: every room
// content entry names a known kind, a name and a SHA-256, and every seat
// difficulty a player 0..9 and a word 0..2, each player once.
func validateOnlineReplay(r *SidecarOnlineReplay) error {
	if r == nil {
		return nil
	}
	for _, c := range r.Content {
		if _, err := hex.DecodeString(c.SHA256); c.Kind < 1 || c.Kind > 3 || c.Name == "" || len(c.SHA256) != 64 || err != nil {
			return errors.New("a room content entry without a known kind, a name and a SHA-256")
		}
	}
	var seen [10]bool
	for _, d := range r.Difficulty {
		if d.Player > 9 || d.Difficulty < 0 || d.Difficulty > 2 || seen[d.Player] {
			return errors.New("a computer seat difficulty outside players 1 to 10 and words 0 to 2, or named twice")
		}
		seen[d.Player] = true
	}
	return nil
}

// RemoveSidecar deletes the sidecar of the bank at bankPath, if any. It is
// called wherever the bank itself is deleted or overwritten (§7.1).
func RemoveSidecar(bankPath string) error {
	err := os.Remove(SidecarPath(bankPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func sidecarError(path, what string) error {
	return fmt.Errorf("nanolathe: save sidecar unreadable: %s: logical path %s, providers searched [save directory], expected a schema %d Nanolathe sidecar, schema %d with unit restrictions, schema %d with official add-ons, schema %d with a Survival director, schema %d with a generated map or schema %d for a continued online replay", what, path, SidecarSchema, SidecarSchemaRestrictions, SidecarSchemaAddons, SidecarSchemaSurvival, SidecarSchemaGeneratedMap, SidecarSchemaOnlineReplay)
}
