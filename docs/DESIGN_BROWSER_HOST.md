# Browser host

## 1. Boundary

Nanolathe includes an experimental desktop browser host for the existing
Ebitengine game, using Go's js/wasm target. Chromium is the initial verified
browser. Safari, Firefox and mobile acceptance remain outside this first
release.

Browser launch, asset acquisition, storage and diagnostics are host policy.
They never select gameplay rules or compute a simulation tick. The Go VFS,
catalogs, ordinary campaign/skirmish composition and renderers remain the
content and game owners (ARCHITECTURE §2; DESIGN_CONTENT_VFS §2).
Windowed `--mission` entry now uses the existing headless mission request and
ordinary battle view. It skips menu navigation; the mission, triggers,
orders, campaign result and save codec retain their existing owners.

With imported retail content the browser build plays online: `MULTI`, its
lobby and its battles are the native ones (DESIGN_MULTIPLAYER §16.6), and the
relay client reaches the relay through the browser's WebSocket — a
`wss://host/relay` URL, or `ws://` on a numeric loopback address for a relay
on the same computer; native `host:port` TLS and a custom trust root are
unavailable there. Hidden-page play and the clipboard are host policy
(§4 contracts 9–10); neither changes what a tick computes.

## 2. Owned pieces

- `web/folder.js`: recursively drains every directory-reader batch; failed reads
  reject the import and source changes cancel stale results.
- `web/content.js`: folder import and verified, same-origin demo acquisition.
- `web/host.js`: one runtime per origin, module compilation and iframe lifecycle.
- `web/game.js`: immutable launch configuration, matching Go runtime, filesystem
  binding, command-line host options and tagged diagnostic messages.
- `web/film.js`: lazy, verified loading of the optional Chinese font atlases.
- `web/gestures.js`, `internal/platform/ebitenapp/scroll_js.go`: browser camera
  gestures, event ownership and the presentation-input bridge.
- `web/clipboard.js`, `internal/platform/ebitenapp/clipboard_js.go`: paste
  events and the asynchronous clipboard (contract 9).
- `web/frames.js`, `cmd/nanolathe/online_browser_js.go`: the frame gate and the
  online background step of a hidden page (contract 10).
- `web/fs.js`: Node-style filesystem adapter consumed by Go's js/wasm runtime.
- `web/storage.js`: committed IndexedDB settings/save transactions.
- `web/launcher.js`, `index.html`, `style.css`: player-facing launcher and diagnostics.
- `tools/browser-build`, `browser-serve`, `browser_assets.py`: static artifact,
  loopback preview and explicit demo allowlist. Python standard library only.
- `internal/platform/ebitenapp/browser_stats_*`: presentation-only telemetry,
  heap capture and the existing sparse-terrain preference.

## 3. Content and distribution

The supplied extracted demo's own `TADemoReadme.txt` says it contains three
single-player missions. Inspection of this archive through the existing VFS
and the landed demo compatibility work finds no skirmish maps. The launcher
therefore starts its first campaign mission; quick skirmish and stress entry
are unavailable for this source, and the main menu greys `MULTI`, whose rooms
are skirmish or Survival battles, by the same mounted-content check that greys
those entries. Retail folder import exposes the normal game menus, online play and those
development entries. No new demo-only catalog, rules,
replacement units or authored maps are introduced.

The default build contains engine/host code only. `--demo-root` is explicit
and admits only `TADemo.hpi` and the original `TADemoReadme.txt`. It never
packages the demo executable, a retail install, the user's compiled engine,
or the neighbouring checks/saves. The preview can serve those same two files
from an external directory without copying them into the artifact.

Manifest version 1 records kind `ta-demo`, original filename, byte size,
SHA-256 and a content-addressed relative URL for each file. The browser checks
the allowlist, bounds, duplicate names, origin, path, size and digest before
mounting bytes. Cache Storage contains only verified demo responses; cached
responses are verified again before use. Cache failures leave play available.
A missing manifest disables demo entry while retaining local import.

The supplied readme ends with a copyright notice and contains no
redistribution grant. Hosting the demo archive and readme on nanolathe.gg,
so that visitors can try the browser build without a game folder, is a
website decision recorded there (user-authorized 2026-10-03): the two files
are assets of the website repository's `demo` release, pinned by size and
SHA-256 in the website's `data/play.json`, and copied into the site at build
time so they stay same-origin with the launcher. Removing that release and
its pins withdraws the demo without changing this host.
**Unknown:** formal rights-holder permission for that distribution. The
original demo distribution agreement would settle it.

## 4. Runtime and storage contracts

1. Retail or demo content mounts read-only at `/game`. Local Files remain
   browser handles; there is no upload path. Go owns archive interpretation
   and overlay order.
2. Settings mount at `/settings`, saves at `/saves`, and temporary files at
   `/tmp`. Only settings/saves persist, on close/fsync or a committed atomic
   rename. Callback mutations serialize their commits; revisioned byte snapshots
   preserve synchronous writes made while IndexedDB commits. Failed flushes
   retain dirty data for retry. The adapter returns browser storage failures as known Go errno
   strings. Durable failures also publish a per-file warning to the launcher,
   because the retail bank writer may ignore close failures; successful sidecar
   writes do not clear a failed bank warning. The existing SAV and Nanolathe sidecar formats are preserved.
3. Demo and retail use separate IndexedDB databases. Retail retains the
   original prototype database to preserve existing saves. Demo uses
   `nanolathe-browser-demo-v1`. Both are local to the exact browser origin;
   changing domain or port changes storage. Different retail installations
   currently share the retail namespace; the engine's own content validation
   still applies.
4. Web Locks admits one runtime across tabs at an origin. The iframe is
   destroyed before releasing its runtime lease. A contender reports that
   another game is running. Exiting the engine releases the lease.
5. Every child message carries an unpredictable run identifier. The parent
   checks origin, current iframe source and identifier before accepting it.
   Replaced runtimes cannot update the current UI. Parent DOM controls are
   not runtime configuration; a child receives one launch snapshot.
6. Demo downloads are cancellable. Local import replaces the current source;
   returning to the launcher retires the iframe. Restart intentionally starts
   a fresh engine; completed saves persist.
7. Wasm compilation is reused within the launcher visit, with a fresh
   instance for each runtime. A failed fetch/compile clears the promise so
   restart can retry. The hashed Wasm and Go runtime filenames are paired in
   `build.json`; both come from the same build toolchain. The root index selects
   a content-hashed host directory containing its scripts, child page and build
   manifest. Restart uses that same directory, even after the root index changes.
   The optional Chinese font files live inside that hashed host directory too;
   their sizes and SHA-256 digests in its pinned `build.json` participate in
   the host hash. The root manifest lists their nested paths for deployment
   retention. Selecting Chinese starts the only font request: simultaneous
   requests share one attempt, and a successful attempt is kept for this runtime.
   All four files must pass size and digest checks before any bytes reach Go.
   A failed fetch, read or check reports failure to the language owner and the
   launcher; no partial font set is delivered. An attempt times out after 15 s.
   A later Apply can retry, bypassing cached responses after a failed attempt.
   Engine exit aborts pending acquisition and clears its timer.
   Deploy the new artifact before replacing the root index, and retain prior
   hashed directories/Wasm/runtime files for already-open launchers.
8. **Browser camera gestures (user-authorized 2026-10-04).** The child runtime
   observes gestures over its canvas before Ebitengine, cancels browser page
   scrolling/zoom there, and forwards them once to the existing camera controls
   (DESIGN_GPU_RENDERER §16.6). Pixel-mode wheel events pan both axes; line/page
   wheel events retain wheel zoom. DOM units do not identify devices, so mice
   reporting pixels also pan; no magnitude or timing heuristic guesses a device.
   GUI wheel deltas keep Ebitengine's signed browser units. Ctrl-wheel, including
   Chromium trackpad pinch, supplies magnification `-pixelDeltaY/200`, with line
   and page deltas converted using 16 CSS pixels/line and the viewport height.
   These conversions are host tuning choices. A Ctrl-wheel burst begins on its
   first event and ends after 180 ms of quiet, on ordinary scrolling, or on loss
   of focus; browsers supply no explicit finger-lift or momentum phase here, so
   inertial pixel scrolling pans too. Two canvas touches pan by their midpoint
   displacement and supply pinch magnification `log(newDistance/oldDistance)/2`,
   matching the existing sensitivity of 2. Lift/cancel retires the gesture; a
   third touch suspends it. Single touches issue no game commands. Touch input
   cannot synthesize selection clicks. CSS `touch-action:none` reserves canvas
   gestures. Positions and deltas convert through the centred letterbox into
   logical pixels; the camera divides panning by live zoom. Touchstart focuses
   the canvas. The latest gesture position survives idle refresh polls until
   mouse motion/button input updates it; each pinch start retains its own logical
   anchor through batching. Lifecycle-only end/cancel events do not move the
   pointer. Full-window tools retain their ordinary Ebitengine wheel stream;
   their ownership cancels and clears camera gestures before returning to battle.
   The existing focus, viewport, minimap, modal and UI ownership gates and zoom
   styles still apply.
   Classic retains its existing camera controls. This adds no gameplay seam,
   simulation state, RNG draws, resources or orders. Listener/timer cleanup runs
   on engine exit; iframe replacement destroys the whole adapter.
9. **Clipboard.** A page receives clipboard text only in a paste event, which
   the browser fires for its paste shortcut unless the key-down was cancelled,
   and Ebitengine cancels every key-down its canvas receives. The child page
   sets that cancellation aside for the engine's own paste keys (Ctrl+V and
   Shift+Insert, not repeated), so the engine still receives them and the
   browser still pastes. It keeps Command+V from the engine, which reads
   Command as no modifier in a browser and would type a V. Each paste event's
   plain text reaches the engine before its next update; a paste the engine
   has no key for — Command+V, or a browser menu — then reaches it as a
   synthetic Insert, its other paste key. The next paste token takes the text
   once, so the shared editor's paste fills the focused field, the room code
   included (DESIGN_INTERFACE_HUD_INPUT §3.2); a paste event without a text
   format leaves the field unchanged, and a paste key without a paste event
   finds the clipboard unavailable. **Copy** writes with
   `navigator.clipboard.writeText`. It is offered where the page has the
   asynchronous clipboard (secure origins, loopback included) and reports the
   browser's verdict, waited for at most 2 s inside the frame that pressed it.
10. **Online play in a hidden page.** The relay holds every seat within 30
   ticks of the slowest acknowledgement and ends a match after 10 seconds
   without progress (DESIGN_MULTIPLAYER §16.5.1–§16.5.2), and a browser makes
   no animation frames, so no Update or Draw, while its page is hidden or
   occluded. Before the Go runtime starts, the child page wraps
   `requestAnimationFrame` in a frame gate: an engine frame counts as running
   from its callback until it requests the next one, the gate records when
   the last one ended, and a held gate defers a frame's start to the next
   animation frame. While a room is opening or open, or an online battle
   runs, a js-only goroutine runs the shell's online step whenever no frame
   is running and the page is hidden (`document.visibilityState`) or has
   started no frame for 200 ms. The step holds the gate for its whole
   duration, including any wait inside it, so it never overlaps an Update or
   Draw. An online battle runs and acknowledges its granted ticks through the
   ordinary paced driver, exactly as the host step does; an open room is
   followed, so a Start the host makes meanwhile enters the prepared battle.
   The committed events of those ticks are dropped, so a page shown again
   resumes at its latest committed tick without replaying their cues or
   notices. A hidden page's timers are throttled to about one wake a second,
   but every grant's WebSocket message wakes the Go runtime, so the goroutine,
   sleeping 4 ms between steps, runs at each grant's arrival and keeps the
   relay's pace and reads every grant. While frames are presented it checks
   every 100 ms, and it ends once no room or online battle remains.
   Single-player battles keep today's behaviour, and native hosts have no
   background step. The step adds no simulation state, RNG draws or
   configuration. Measured in desktop Chromium against a native seat on a
   loopback relay: a page hidden before the host's Start entered the battle
   in the background, and pages hidden for 30 seconds to six and a half
   minutes mid-battle, kept the native seat at 30 ticks a second with no
   stall and every checksum agreed.

## 5. Delivery and measurements

`tools/browser-build` produces a static directory: host files, content-hashed
Wasm/runtime files, a reproducible gzip variant and `build.json`. Host scripts
and manifests must revalidate; content-addressed files can be immutable.
`tools/browser-serve` binds loopback, emits application/wasm, negotiates gzip,
and refuses directory listings. Production needs HTTPS for Web Crypto,
IndexedDB, Cache Storage and Web Locks. The Run locally link points to the
existing official installation page; the browser host adds no native binary hosting.

The lower-memory preset retains the existing sparse terrain atlas path and
GOGC=50, independently of gameplay. The collector shares the one browser
thread, so the demo defaults to the desktop preset and only retail imports
default to the lower one. Browser Modern rendering currently uses
full paused compositions, retaining the native paused-world optimization on
other targets. Editing a save-name field with browser reuse enabled exposed
blank modal backgrounds. The underlying WebGL resource discrepancy remains
unknown; compare captures of the cached paused snapshot with the ordinary
full-composition path before enabling browser reuse.

Browser Modern rendering records each presented frame in its own Draw; it never
launches the next frame's pre-record (DESIGN_GPU_RENDERER §13.10). Go's js/wasm
scheduler returns to the browser only once every goroutine has blocked, so a
pre-record ran in the same browser task as the Draw before it and overlapped
nothing, and each predicted miss recorded that frame a second time: 22% of
frames in the 750-unit stress fixture. The asynchronous simulation (§13.13 there)
is unchanged; switching it off measured no difference on one thread.

Feature-sprite banks prepare the same complete decoded images without keeping
the additional encoded RLE rasters that only direct-storage readers need
(DESIGN_PRESENTATION_CLIENT §1; [fmt gaf]). The Town & Country stress fixture's
feature banks contained about 98 MiB of those unused byte planes. Both ordinary
battles and settings-preview feature banks use the smaller product; model
textures, visibility masks and on-demand effects keep their direct views.
The dated browser comparison is in [web/README](../web/README.md).

Go's collector shares the thread too. Its idle mark worker is not interrupted by
browser events, so the rest of a cycle's marking finishes inside one browser
task. In the 750-unit stress fixture that task took 109–117 ms (176–224 ms
while the map catalog retained every parsed OTA document). How often it happens
follows the allocation rate, and how long follows the live heap's object count,
so allocation churn and retained small objects are browser frame-time costs even
where native profiles show them as cheap. Diagnostics remain outside the tick.
FPS counts composed frames; TPS uses committed tick deltas; Go heap is not
total browser/GPU memory; Wasm capacity can grow but cannot shrink per instance.
Capture with focus held and inspect samples: a lost-focus interval lowers TPS.
Demo RNG is not pinned by this launcher, so these are exploratory play captures,
not fingerprint comparisons.

## 6. Merge acceptance and release boundary

`tools/browser-check` locks folder traversal and batched drops, manifest bounds
and integrity, corrupt-cache recovery, failed acquisition, source/run message
isolation, lock admission/restart cancellation, Blob range reads, read-only
content, atomic persistence and retry behavior, gesture channel separation,
touch geometry and pinch lifetime/cancellation, paste-key handling and paste
delivery, and the frame gate's running and held states. Optional-font checks
lock lazy shared requests, complete verified delivery,
retry after failure or timeout, exit cancellation and the retained host's
atlas version. Go tests lock the
background step's hand-off (it runs only between frames, holding them), its
battle pump and dropped events, its lobby following, and paste into the room
code field. Authored fixtures contain no
retail bytes. Packaging checks preserve an older launcher's child/engine pairing.
The CI browser job runs these checks with Node 24/Python 3 and builds the
asset-free artifact and executes the existing numeric kernel vectors on Wasm
under Node with `GOMAXPROCS=1`. The retail direct-mission regression checks campaign
identity, preferences and the ordinary return-to-menu path in Go.

The landing owner must integrate current main, review the full diff, run
`tools/browser-check`, `tools/browser-build`, `tools/check`, `tools/check-retail`
and applicable visual/performance acceptance. Extended campaign/save checks
use `tools/check-retail --full`. The dated acceptance record and measurements
are in [web/README](../web/README.md). Builds alone do not establish playability.

The browser host includes engine/host code and local preview tooling. Public demo
asset distribution still needs the evidence in §3. Website hosting and demo
assets are not part of the merge. Desktop save downloads include the existing
sidecar; importing desktop saves, persistent installation access and mods
remain follow-ups, so a browser seat joins only rooms without a mod. Full
retail animation memory, longer matches, other browsers and mobile
input/memory budgets need further measurement. Online play is verified in
desktop Chromium only. Unverified: Safari's and Firefox's paste events on a
non-editable canvas, their clipboard-write activation rules and their
hidden-page WebSocket delivery; mobile browsers; and a page hidden long
enough for the browser to freeze or discard it (Chrome's Energy Saver and
Memory Saver), which stops every goroutine, so the relay ends the match. The paused WebGL snapshot discrepancy remains the explicit
unknown in §5; the browser uses full composition until it is resolved.
