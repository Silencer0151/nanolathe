# Map generator

Nanolathe makes skirmish maps on demand (issue #145, user-authorized
2026-10-22). The player picks a world and the map's shape. The generator
makes a map that looks like that world's own maps and is fair by the
measurements it reports. The generator itself is `internal/mapgen` and
the TNT and TDF writers (§1–§9). The map picker, generated maps, their
recipes, saves and replays on them, and online rooms on them are §10.

- Terrain is natural-looking and fractal rather than mirrored; a symmetric
  "tournament" mode may follow.
- Fairness comes from where the starts and the metal go. It is then
  measured with the engine's own passability: several candidates are made
  and the fairest valid one is kept.
- Tiles are taken from the world's own maps.
- A map code makes the same map again.

## 1. What it is, and what it is not

The generator writes content: a TNT and an OTA. Nothing in it reaches a
battle except through those files, which the engine reads like any other
map. It adds no gameplay rule, no seam, no simulation state and no draw
from either simulation random stream
([INVARIANTS I4](INVARIANTS.md#i4--two-rng-streams-call-order-is-behavior)):
its randomness is a private SplitMix64 stream seeded from the map code. A
generated map plays the same in every gameplay mode.

Code: `internal/mapgen` (no dependency on the command) and the encoders
`formats/tnt_write.go` and `formats/tdf_write.go`.

## 2. The world

A `mapgen.World` is everything the generator knows of a world's look. The
caller reads it from that world's maps, so nothing about a world's look is
authored in the generator:

- **Sources.** The world's maps: each one's terrain heights, sea level, tile
  indices and tile art. The generator copies coherent patches of their art,
  except the cells the caller marks in the source's `Avoid`: its void
  (`0xFFFC`), legacy painted metal, the strips outside the play area and the
  art under every indestructible metal feature (and one cell round it), so
  no tile shows metal where there is none. The caller's order is part of
  the result: the same code makes the same map only from the same sources
  in the same order.
- **Features.** Kinds the world's maps place, each with members weighted by
  how often they are used: the **deposit** (a non-blocking, indestructible
  feature with metal, at least 2×2, whose sprite shows something), **trees**
  (block, hold energy), **foliage** (neither blocks nor holds metal) and
  **rocks** (reclaimable metal). A world whose maps draw no deposit gets
  none; its ground is its metal. Deposits seed per-cell metal at load
  [05 R-FEAT-01 §7], which is what makes the measured metal real.
- **Ground class.** The movement class whose passability decides whether a
  route exists: the most restrictive common land class.
- **Climate.** Wind, tidal strength, gravity, solar strength, surface and
  moho metal, and whether it is a lava world, for the OTA.
- **Palette.** The world's colours, to read water from the art (§6).

Choosing the sources is the caller's: the picker reads them from the
content's archives, in a deterministic order, and a saved recipe records
their identities (§10).

## 3. The parameters and the code

`mapgen.Params`: players 2–10; teams (0 free for all, or 2–5 dividing the
players with more than one per team); size 6–24 OTA units (512 pixels, 32
cells) a side; water none, lakes or seas; relief plains, rolling, hills or
mountains; metal scarce, normal or rich; and a seed.

The code is `world-players-teams-size-water-relief-metal-seed36-vN`, for
example `green_planet-4-0-10-1-2-1-1-v1`. `Version` moves whenever what any
step computes changes, so an old code is refused rather than silently
naming another map. The same code makes the same map for the same source
maps on every platform.

## 4. Making a candidate

A map is `Size×32+2` by `Size×32+8` cells, so the play area
(`PlayRight = W·16−32`, `PlayBottom = H·16−128` [03 §2.2]) is exactly the
size asked for.

1. **Starts.** Free for all puts them evenly round an ellipse, turned and
   jittered by the seed; teams put each team's members side by side across
   one point of it. Starts are numbered as retail's team maps are: team
   `t` holds StartPos `t+1`, `t+1+T`, … A candidate whose bases would
   overlap is dropped.
2. **Heights.** Domain-warped fractal value noise in 16.16 integers, with
   ridged noise blended in where a low-frequency mask asks for mountains;
   the middle 96% stretched over the relief's height range. With water,
   the coast follows only the coarse octaves and the fine ones fade in
   inland, the sea floor deepens with the square of the distance from the
   shore so shallows fringe every coast, and ponds and islets under 48
   cells are removed. Every base is levelled (radius 14, blended over 14
   more, kept 8 above the sea) and thermal erosion limits slopes to the
   relief's talus.
3. **Trees and foliage** grow in clusters a low-frequency field draws, on
   dry ground with a corner difference of 10 or less, never within 30 cells
   of a base, with a few rocks; each kind is picked by its weight. They go
   before the metal, so travel costs route round them.
4. **Zones and travel.** Every cell is classified as the engine's per-cell
   chain does for the ground class [04 §6.1 R-DOC04-B][04 R-SLOPE-01 §2]:
   blocking features and the load-time void block (the two right columns,
   the north and south height strips [03 R-TERR-01 §2], and on a lava world
   every cell at or below the sea [03 §2.2]); then the water depths and
   the land or water slope pair give clear, steep or blocked; a footprint
   takes the lowest tier inside it and is steep when any cell of the ring
   round it is below clear [04 R-SLOPE-01 §3]. Costs are the engine's 16
   straight, 22 diagonal and 30 more onto a steep anchor, and a diagonal
   step tests only the anchor it lands on [04 R-PATH-01 §3][04 §7.1]. The
   engine's turn and short-run penalties depend on a route's heading and
   are left out, so travel measures the ground, not the route a unit takes.
   Each cell belongs to the start that reaches it soonest. A candidate in
   which some start cannot reach every enemy is dropped.
5. **Metal.** The reach is half the shortest travel cost between enemy
   starts — the same for everyone. Each player gets the same deposits:
   for each band (near 22–42%, middle 50–80% of the reach) target costs are
   drawn once, and each player takes the free spot in their own zone whose
   cost is nearest each target, on dry ground no steeper than a corner
   difference of 24 over the footprint and its ring, which is then
   levelled into a pad. Shared deposits go beyond the reach (100–140%),
   where two neighbouring enemies are within 30 of the same cost.
   Few/normal/many is 2/3/4 near and middle, and 0/1/2 shared per pair.
6. **Measure** (§5). Per start: the travel cost to the nearest enemy, the
   building room and the metal within reach, each with its spread.
7. **Room.** A base short of room — under 96% of the largest — is widened
   at its own level by three cells of radius, every deposit pad is levelled
   again, and the candidate measured again, for up to four rounds.

8. **Validity.** A candidate is ranked only when it is valid, and the
   reason it is not is kept:
   - its bases stand apart (step 1);
   - every start reaches every enemy start by land (step 4, measured again
     after the room rounds);
   - every player's metal is placed (step 5);
   - every start is usable: its own cell is above the sea and its building
     room is at least `minRoom` (294 cells, about half the flat base every
     start is given).

Eight candidates are made per round, each from its own seed derived from the
code, and the valid one with the lowest score is kept. When a whole round is
invalid, up to two more rounds are made. When every round is invalid, the
error names the most common reason ("connected every base", "kept every base
apart", "placed every player's metal", "gave every start dry, level room to
build"). Parallelism cannot change the result: the seeds and the choice
depend only on candidate indices.

## 5. Fairness: what is measured

`Map.Balance` holds three measurements of the chosen layout. Each gives one
value per start, in start order, and their spread: (best − worst) per mille
of the mean. Each has a stated scope:

- **Path.** The travel cost from each start to the nearest enemy start (a
  teammate is no enemy), by the ground class's passability and the engine's
  step costs (§4, step 4).
- **Room.** Each start's building room: the cells within 30 cells' travel
  that no feature occupies and no deposit's 4-cell spacing ring keeps
  clear, above the sea, a 2×2 block counting when its heights differ by at
  most 2.
- **Metal.** The deposit metal in each start's own zone (the cells its units
  reach first) within 90% of the reach, the travel cost halfway to the
  nearest enemy.

The score, `4·path + 2·room + 3·metal` over the spreads, only ranks the
valid candidates of one code. It compares a layout's starts with each other
under the ground class, and promises nothing beyond these three
measurements. Other movement classes, air and naval routes, and
tournament symmetry are follow-ups (§10).

## 6. Painting

The kept candidate is painted with source tiles (`paint.go`).

- **Signature.** A 32×32 tile covers 2×2 cells. Its signature is which of
  them are water; the water's depth band or the land's height band (the
  map's land heights in thirds); its slope class over the 3×3 corners — a
  land vehicle's per-cell tiers doubled (6, 14, 30) — and, when steep,
  which way it faces. Retail water and cliffs are tile art [03 §6], so a
  tile whose signature matches the ground draws the shore and cliff in
  the right place.
- **Water from the art.** A source tile's water cells are read from its
  art, not its heights: a colour is water's when it is four times as
  common, by share, under the world's water as on its land, and land's the
  other way round; a quarter whose sampled pixels are mostly water's
  colours is water, mostly land's is land, and otherwise the heights
  decide. Authors' shores do not always follow their height maps, and a
  lake or ice painted on a plateau would otherwise land on dry ground.
- **Theme.** One source, picked by the seed among those whose tiles show
  nearly as much of the needed ground as the best one, and up to two
  more of like colour, so a map looks like one world rather than a quilt
  of every author's style. Other sources fill only what the theme cannot
  show.
- **Choice.** Tile by tile, row by row: the candidates are the
  continuations of the four painted neighbours (left, up and the two upper
  diagonals) in their own source maps, and samples of the theme's tiles
  matching exactly, by water and slope, and by water alone. The water
  cells must match. A tile costs the colour difference along its edges with
  the left and upper tiles (an edge it continues from the source costs
  nothing), less a bonus for every neighbour it continues and a little for
  reusing art already on the map, plus 60000 per height-band step, 150000
  per slope-class step and 50000 per facing difference from the ground
  under it, and 250000 from outside the theme. Patches of authored art
  therefore grow as far as the ground allows, and transition tiles win
  where exact matches would meet at a hard edge. When no source shows a
  shore at all, the nearest shore by cells is taken.
- **Tiles.** Every distinct tile is stored once.

## 7. The files

- **TNT** (`formats.EncodeTNT`): the canonical 0x2000 layout [fmt tnt];
  heights, the tile map and art, each feature's index in its anchor cell
  and `0xFFFE` in the rest of its footprint as retail files have it, and a
  252×252 minimap of the play area (`PlayRight` × `PlayBottom`) fitted by
  its long side, padded with index `0x64` [fmt tnt] "Minimap".
- **OTA** (`Map.OTA`, through `formats.EncodeTDF`) [fmt ota]: the global
  header with the world's planet and climate, `size`, `numplayers`, and
  `gravity` written explicitly (an omitted key reads as 0); one
  `Network 1` schema with the world's surface and moho metal, retail's
  1000 starting resources and the starts as `StartPosN` specials.

## 8. Determinism, workers and cancellation

- **Determinism.** Everything is integer arithmetic over the private stream,
  and nothing depends on a Go map's iteration order. The same code and world
  make the same files whatever the number of workers or cores and on every
  platform. `TestGeneratedMapIsPinned` pins the SHA-256 of one code's TNT
  and OTA on the authored test world; it also passes built for 386, where
  `int` is 32 bits.
- **Workers.** `Generator.Workers` bounds how many candidates are built at
  once; 0 uses `min(Candidates, GOMAXPROCS)`. A host keeping the game
  responsive while a preview generates sets it lower. The map is the same
  for any value.
- **Cancellation.** The context is checked between the steps of every
  candidate, before the chosen one is painted, per row while it is painted
  and once more when it is done; a cancelled `Generate` returns the
  context's error. A cancel that lands after the last check still returns
  the finished map, so a caller that must not show a stale one compares
  each result with the request it is waiting for (§10).

## 9. Tests

`internal/mapgen`, on an authored world (one source map of rolling land
round a lake, its tiles flat colours by what lies under them):

- one code makes one map whatever the parallelism and `Workers`; another
  seed makes another;
- the files read back with the starts, schema and deposits, every deposit on
  level ground;
- every player has the same metal in free for all, teams and every relief;
- the balance gives each measurement per start, and every start of the
  chosen layout is usable; `startUsable` refuses a wet start and a cramped
  one;
- a cancelled request returns no map, before any work or partway;
- the pinned code's hash (§8);
- codes round-trip and other versions are refused; a dry world refuses
  water; a world without a deposit makes maps without any.

`formats`: a written terrain file reads back cell for cell, and TDF text
reads back as written.

`cmd/nanolathe`, on authored content (palettes, a ground class, a tree, and
an archive of three maps the authored world made):

- the content's world is read with its sources in order, and a recipe makes
  its map again from them; a changed source, another generator version or
  other recorded hashes are refused, naming the difference;
- Use mounts the map from memory below everything else, its recipe is found
  by the map's name, and it feeds no generator;
- Save installs a package whose receipt records the recipe, removable from
  the picker; names and package ids stay within what files and TDF values
  hold; Generate stands beside More maps;
- a save's recipe finds its mounted map, makes a missing one again and
  refuses a map of that name with other files;
- a room on a generated map names the archive of its sources, which holds
  only maps, and its content serves the map from memory; the host's
  generated map becomes the room's with its recipe; a guest makes the map
  on the room's content and mounts it, and one whose source archive
  differs is told why it cannot.

The room tests add a second archive holding the authored support files, as
an install holds them, since a room's content mounts archives only.

`internal/save` and `internal/session`: sidecar schema 5 and extension 20
round-trip, and their refusals. The retail tier plays a map made from
Archipelago, its commanders on its starts, then loads the battle's save and
verifies its replay on content where the map is made again from the recipe;
and opens a room on such a map through a real local relay, where the guest
makes the map from the recipe, both seats compose the same battle, and the
battle starts and runs on it.

## 10. The map picker and generated maps

**Generate.** A Generate button beside More maps in the map picker opens the
generator's panel, a full-window screen in the Nanolathe screen's style
(`cmd/nanolathe/mapgen_screen.go`): the world, players, teams, size, water,
relief and metal; the map code, which can be typed to make that map again;
the measurements of §5, a column per start and a row per measurement with
its spread, under a paragraph saying what they measure and that they
promise nothing beyond it; and the map with its numbered starts. Another
map draws a new seed. Use plays the map without keeping it; Save… keeps it
under a typed name and description. An online room's host generates the
room's map the same way. `--nl-shot DIR --nl-shot-only generator` captures
the panel and its Save form for review.

**Sources.** The world's maps are the install's and the mod's archive maps
of that planet (providers of the HPI family): never loose files, maps held
in memory, or any package of the map library, whether downloaded or saved,
even when it is an archive. A code's result is thus tied to the content's
own maps, not to what one machine has downloaded, and no generated map
feeds the generator. They are ordered by the mount order of their archives,
which retail's archive precedence fixes from the archives' names, then by
name; the first 16 readable ones are the sources.
A world needs three to be offered. Each source's identity is the logical
path of its TNT, the archive that supplied it and the SHA-256 of the TNT
and OTA the generator read.

**Background work** (`mapgen_job.go`). The worlds are listed each time the
panel opens, and the chosen world's indexed art is kept while it is open.
Every change starts a request with a new serial number and cancels the
running one, whose result can no longer replace the picture; a failed
request clears it, so Use and Save always take the map shown, made from the
settings beside it. One world at most is indexed at a time: a request for
the world being indexed waits for that index, which is kept for it even
though an older request started it, and a request for another world
cancels it between source maps. `Generator.Workers` is the core count less
one, at least one, so the window stays responsive. Closing the panel
cancels the work, joins every goroutine reading the content, and forgets
the content, its worlds, the map shown and the indexed art. Every remount
that closes the content — a library refresh, a mod reload — closes the
panel first.

**Use.** The map is mounted from memory: its OTA and TNT as an HPI archive
in memory under the provider name `generated:<code>`, below every other
provider, so it adds its two files and overrides nothing. Every later mount
of the run holds the run's generated maps (at most six; the oldest the
setup does not use goes first), the mount is replaced as a library refresh
replaces it, as does the archive remount of a battle restart, and the
setup takes the map as the picker's Load does. Use refuses when the picker
lists another map under the map's name with other files, which would hide
it. Nothing is written to disk; a setting naming the map falls back the next
run, as for a removed map.

**Save.** The map is installed in the map library as a package of its two
files, under a name of letters, digits, spaces and a little punctuation
that no listed map has, and a description without `//` or `/*`, which would
open a comment in its OTA. The package's receipt names the code as a
download's names its URL (`generated:<code>`) and records the recipe, and
the picker removes the map as it removes a downloaded map. Keeping the files
is how a saved map survives generator updates: it plays whatever later
versions make of its code, and its recipe still says how it was made.

**The recipe** (`mapgen_recipe.go`): the recipe format and generator
versions, the code, the world, the name and description its OTA carries,
the sources with their identities in the order the generator read them, and
the SHA-256 of the TNT and OTA. Making the map again needs the same
generator version, reads exactly the recorded sources, each from the
archive it names and with the bytes it had (never another selection), and
compares the hashes; a difference is an error that names it. A recipe comes
from a save or a replay file, so one naming more than 16 sources, or a
source twice, is refused before anything is read.

**Saves and replays.** A battle's map must stay recoverable after its
memory mount is gone. A save's sidecar records the recipe as `generatedMap`,
which makes it schema 5, so an older build refuses the save
(DESIGN_MODS_MUTATORS §7.2). A replay records it as required configuration
extension 20 (DESIGN_MULTIPLAYER §8.6). Loading either uses a mounted map of
that name whose files have the recipe's hashes; with none, the map is made
again from the recipe and mounted first, and a map of that name with other
files is refused. A replay's content identity still covers the map's own
files, so a map made differently cannot verify.

**Online rooms** (`mapgen_online.go`, DESIGN_MULTIPLAYER §16.8). A room on
a generated map — one the host holds in memory or a saved one — carries its
recipe as extension 20, never its files or its package, and its content
names the archives the recipe's sources come from, even those that hold
only maps. Every seat holds the map before Ready. The host holds the one it
made. Any other seat makes it from the recipe in the background on the
room's own content — the archives the room names, as the battle mounts
them — checks the recipe's hashes, and mounts it with the run's generated
maps, so its lobby shows it and its battle draws it; meanwhile the lobby
says it is making the room's map, and Ready waits. A seat that cannot make
it, for missing or different sources or another generator version, is told
why and cannot be ready. The battle's content set and a recorded room's
playback mount the map from memory with the room's content. The identity
digests at Ready compare the map's files as for any map.

## 11. Later

- A symmetric tournament mode, validation with the engine's own pathfinder
  and more movement classes, and the official map editor's sections and
  tilesets as sources.
