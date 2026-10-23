package main

// The online lobby (DESIGN_MULTIPLAYER §16.6, §16.6.2): the room code, a row
// per present player with their name, team, side, colour and readiness, a row
// per computer player the host added (§6.6), the host's settings — the rule
// set among them — until Start, the players' chat (online_lobby_chat.go),
// Ready, Start and Leave. It is the authored SKIRMISH.GUI window, whose ten
// runtime rows, side art, colours and allegiance symbols already fit a
// ten-player room, opened as the front end's skirmish screen. Its own controls
// replace the setup screen's handlers while it is open.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/relay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/survival"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

// The lobby's own controls, added to the SKIRMISH.GUI clone.
const (
	lobbyGameType  = "GAMETYPE"
	lobbyGameLabel = "GAMELABEL"
	lobbyLocLabel  = "LOCLABEL"
	lobbyAirLabel  = "AIRLABEL"
	lobbySeaLabel  = "SEALABEL"
	lobbyReady     = "READY"
	lobbyCopy      = "COPY"
	lobbyRoomLabel = "ROOMLABEL"
	lobbyCode      = "CODE"
	lobbyReadyHead = "READYHEAD"
	lobbyLevelHead = "LEVELHEAD"
	// lobbyRules is the host's rule-set control under its lobbyRulesLabel.
	lobbyRules      = "RULES"
	lobbyRulesLabel = "RULESLABEL"
	// lobbyChatLine is the chat's text line (online_lobby_chat.go).
	lobbyChatLine = "CHATLINE"
	// lobbyLevel names a row's difficulty caption, lobbyLevel+"n" over
	// Energy n.
	lobbyLevel  = "LEVEL"
	lobbyStatus = "HELPTEXT"
)

// onlineAddComputerCaption is the host's Add computer row: the name button of
// the row after the last player.
const onlineAddComputerCaption = "Add computer"

// onlineRoomState is the open room: the relay's lobby, the room's latest base
// configuration, and this seat's readiness.
type onlineRoomState struct {
	lobby    onlineLobby
	panel    *ui.Panel
	assets   *retailPanelAssets
	codeFont *formats.FNT
	address  string
	// cat is the unrestricted catalog this install compiled for the room:
	// its map headers, its capacity and the final composition read it.
	cat *content.Catalog

	base        session.EffectiveMatchConfig
	baseReq     session.MatchConfigRequest
	baseVersion uint32
	settings    onlineSettings
	mapMissing  bool
	capacity    int
	// archives are this install's base archives, and content this seat's
	// resolution of the base configuration's room content (§16.8).
	// looseFiles marks an install folder with loose game files an online
	// battle leaves out.
	archives   []localArchive
	known      []knownAddon
	content    roomContentPlan
	looseFiles bool
	// contentCS is the content set content was resolved on; a remount
	// resolves again (refreshOnlineRoomContent). fetch downloads a map
	// package the room names and this install lacks.
	contentCS *contentSet
	fetch     roomPackageFetch
	// addonFetch enables or downloads the official add-ons the room lists
	// and this seat's content does not mount.
	addonFetch roomAddonFetch
	state      relay.HostedLobbyState
	// notice is a one-off status line, such as why readiness was cleared;
	// a later change replaces it.
	notice      string
	choosingMap bool

	ready    bool
	readyJob *onlineReadyJob
	prepared *onlinePrepared
	readyErr error

	chat onlineLobbyChat
}

// onlineRoomKey is what the final configuration depends on: the base
// configuration and every seat's presence, team, side, colour and name. A
// prepared battle is valid only while the key is unchanged.
type onlineRoomKey struct {
	base  [32]byte
	seats [relay.HostedMaxSeats]relay.HostedSeatState
}

func (r *onlineRoomState) key() onlineRoomKey {
	k := onlineRoomKey{base: r.base.Digest()}
	for i, seat := range r.state.Seats {
		if seat.Present {
			k.seats[i] = relay.HostedSeatState{Present: true, Team: seat.Team, Side: seat.Side, Color: seat.Color, Name: onlineLobbyName(seat)}
		}
	}
	return k
}

func (g *gameShell) onlineLobbyActive() bool {
	return g != nil && g.online != nil && g.online.room.panel != nil && g.activePanel() == g.online.room.panel
}

// onlineSideCount is how many sides a player may choose: the room's catalog's
// (session.OnlineSides), or the setup screen's count before one is compiled.
func (g *gameShell) onlineSideCount() int {
	if g.online != nil && g.online.room.cat != nil {
		if n := len(session.OnlineSides(g.online.room.cat)); n > 0 {
			return n
		}
	}
	return g.skirmishSideCount()
}

// isHost reports whether this seat created the room.
func (r *onlineRoomState) isHost() bool { return r.lobby != nil && r.lobby.Seat() == 0 }

// presentSeats are the seats present now, in ascending seat order.
func (r *onlineRoomState) presentSeats() []int {
	var seats []int
	for i, seat := range r.state.Seats {
		if seat.Present {
			seats = append(seats, i)
		}
	}
	return seats
}

// onlineLobbyRow is one row of the lobby: a present human's seat, one of the
// host's computers, or neither, the host's Add computer row.
type onlineLobbyRow struct {
	seat     int // the relay seat of a human row, else -1
	computer int // the index in settings.computers of a computer row, else -1
}

// lobbyRows are the lobby's rows in the order every seat composes them: the
// present humans in seat order, then the host's computers in their stored
// order, then, for the host while one more computer fits, the Add computer
// row. A room that holds more than the ten rows, which no battle can seat,
// shows the first ten; the status line says it is too full.
func (r *onlineRoomState) lobbyRows() []onlineLobbyRow {
	var rows []onlineLobbyRow
	for _, seat := range r.presentSeats() {
		rows = append(rows, onlineLobbyRow{seat: seat, computer: -1})
	}
	for k := range r.settings.computers {
		rows = append(rows, onlineLobbyRow{seat: -1, computer: k})
	}
	if r.canAddComputer() {
		rows = append(rows, onlineLobbyRow{seat: -1, computer: -1})
	}
	return rows[:min(len(rows), relay.HostedMaxSeats)]
}

// canAddComputer reports whether the host may add a computer now that the
// room has these players (onlineCanAddComputer) and its rule set lets the
// host add another (onlineComputerCap). Before the relay's first state the
// room has no players, the host included, and no row.
func (r *onlineRoomState) canAddComputer() bool {
	humans := len(r.presentSeats())
	return r.isHost() && r.cat != nil && humans > 0 && onlineCanAddComputer(r.settings.survival, r.capacity, humans, len(r.settings.computers)) &&
		len(r.settings.computers) < onlineComputerCap(r.settings.rules)
}

// computerColors are the colours the host's computers take beside the
// players present now (onlineComputerColors).
func (r *onlineRoomState) computerColors() []uint8 {
	seats, _, _ := onlineSeatsOf(r.state, r.lobby.Seat(), r.settings.survival)
	return onlineComputerColors(seats, r.settings.computers)
}

// buildOnlineLobbyWindow shapes a SKIRMISH.GUI clone, with its ten runtime
// rows installed, into the lobby. Difficulty becomes the game type, Survival's
// controls are the Survival setup screen's clones, the Rules control follows
// them in their row as Select Map's clone — the stage buttons' art leaves
// "Community 3.9" no room beside its lights — the metal column shows
// readiness, the room code takes the free space beside the title, and the
// chat's text line runs along the bottom.
func buildOnlineLobbyWindow(window *gui.Window) {
	index := func(name string) int { return window.GadgetIndex(name) }
	labelOf := func(text string) int {
		for i, gad := range window.Gadgets {
			if gad.Kind == gui.KindLabel && gad.Text == text {
				return i
			}
		}
		return -1
	}
	start, mapping, difficulty, selectMap := index("StartLocation"), index("Mapping"), index("Difficulty"), index("SelectMap")
	location, diffLabel := labelOf("Location"), labelOf("Difficulty")
	if start < 0 || mapping < 0 || difficulty < 0 || selectMap < 0 || location < 0 || diffLabel < 0 {
		return
	}
	clone := func(template gui.Gadget, name, stages string, n uint8, help string) gui.Gadget {
		b := template
		b.Name, b.SourceName, b.Art = name, survivalSource, template.Name
		b.Text, b.Labels, b.Stages, b.Help = stages, splitStages(stages), n, help
		return b
	}
	label := func(name, text string, x, y, w int32) gui.Gadget {
		l := window.Gadgets[location]
		l.Name, l.SourceName, l.Text = name, survivalSource, text
		l.Rect.X, l.Rect.Y, l.Rect.W = x, y, w
		return l
	}
	button := func(name, text string, x, y int32) gui.Gadget {
		b := window.Gadgets[selectMap]
		b.Name, b.SourceName, b.Art, b.Text, b.QuickKey = name, survivalSource, "SelectMap", text, 0
		b.Rect.X, b.Rect.Y = x, y
		return b
	}
	game := clone(window.Gadgets[mapping], lobbyGameType, "Skirmish|Survival", 2, "Skirmish, or Survival against endless waves.")
	game.Rect = window.Gadgets[difficulty].Rect
	window.Gadgets[difficulty].Active = 0
	window.Gadgets[diffLabel].Name, window.Gadgets[diffLabel].Text = lobbyGameLabel, "Game"
	window.Gadgets[location].Name = lobbyLocLabel
	pace := clone(window.Gadgets[difficulty], survivalPaceButton, "Normal|Relaxed|Relentless", 3, "How quickly the waves grow.")
	pace.Rect = window.Gadgets[start].Rect
	added := []gui.Gadget{game, pace}
	const x0, pitch, labelY, buttonY = 45, 130, 290, 306
	for col, c := range []struct{ caption, name, labelName, help string }{
		{"Air Waves", survivalAirButton, lobbyAirLabel, "Whether waves may be airborne."},
		{"Naval Waves", survivalSeaButton, lobbySeaLabel, "Whether waves may come by sea."},
	} {
		b := clone(window.Gadgets[mapping], c.name, "On|Off", 2, c.help)
		b.Rect.X, b.Rect.Y = int32(x0+col*pitch), buttonY
		added = append(added, label(c.labelName, c.caption, int32(x0+col*pitch), labelY, 117), b)
	}
	// A skirmish room's Allied Sight takes the Air Waves slot, which only a
	// Survival room shows.
	added = append(added, alliedSightGadgets(window, x0)...)
	rules := button(lobbyRules, gameplayLabel(gameplay.Modern), x0+2*pitch, buttonY)
	added = append(added, label(lobbyRulesLabel, "Rules", x0+2*pitch, labelY, 117), rules)
	added = append(added,
		label(lobbyRoomLabel, "Room code", 300, 12, 160),
		label(lobbyCode, "", 300, 28, 170),
		label(lobbyReadyHead, "Ready", 287, 58, 60),
		label(lobbyLevelHead, "Difficulty", 337, 58, 60),
		button(lobbyCopy, "Copy", 480, 24),
		button(lobbyReady, "Ready", 263, 437),
	)
	// A computer's difficulty is written over its energy button in the
	// headings' face: the button keeps its art and its click, and "Medium"
	// in the button face is wider than the button.
	for row := range relay.HostedMaxSeats {
		suffix := strconv.Itoa(row)
		energy := index("Energy" + suffix)
		if energy < 0 {
			continue
		}
		r := window.Gadgets[energy].Rect
		level := label(lobbyLevel+suffix, "", r.X, r.Y+4, r.W)
		level.Rect.H = 13
		level.Attribs = level.Attribs&^4 | 2
		added = append(added, level)
	}
	window.Gadgets = append(window.Gadgets, added...)
	if i := index("PrevMenu"); i >= 0 {
		window.Gadgets[i].Text, window.Gadgets[i].Labels = "Leave", nil
	}
	// The status line takes two rows, and stops short of the right column,
	// so a longer reason or help reads whole.
	if i := index(lobbyStatus); i >= 0 {
		r := &window.Gadgets[i].Rect
		r.Y, r.W, r.H = onlineStatusY, onlineStatusW, onlineStatusH
	}
	buildOnlineChatLine(window)
}

// The lobby's status line: two rows of the label face between the settings
// band and the map field's heading, clear of the right-hand column.
const (
	onlineStatusY = 328
	onlineStatusW = 420
	onlineStatusH = 26
)

func splitStages(stages string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(stages); i++ {
		if i == len(stages) || stages[i] == '|' {
			out = append(out, stages[start:i])
			start = i + 1
		}
	}
	return out
}

// lobbyBackdrop copies the setup screen's backdrop and paints plain texture
// over its Metal and Energy headings, which the lobby does not use; the Ready
// heading is drawn in their place, and the Difficulty heading over the energy
// column while the room has a computer player. The Color heading stays over
// its column.
func lobbyBackdrop(source *formats.PCX) *formats.PCX {
	if source == nil {
		return nil
	}
	pcx := *source
	pcx.Pixels = append([]byte(nil), source.Pixels...)
	width := int(pcx.Width)
	patch := func(fromX, toX, y, w, h int) {
		for row := 0; row < h; row++ {
			src := (y+row)*width + fromX
			dst := (y+row)*width + toX
			if src >= 0 && dst >= 0 && src+w <= len(pcx.Pixels) && dst+w <= len(pcx.Pixels) {
				copy(pcx.Pixels[dst:dst+w], source.Pixels[src:src+w])
			}
		}
	}
	// The plain strip between the Energy heading and the Commander box
	// supplies the texture.
	const y, h = 50, 18
	patch(395, 283, y, 52, h)
	patch(395, 335, y, 52, h)
	return &pcx
}

// openOnlineLobby shows the room a job opened: the setup screen's window in
// the skirmish mode, replacing the online screen.
func (g *gameShell) openOnlineLobby(lobby onlineLobby, cat *content.Catalog, base session.EffectiveMatchConfig, address string, archives []localArchive, known []knownAddon) {
	s := g.online
	asset := g.assets.panel[modeMenuSkirmish]
	if asset == nil || asset.window == nil {
		_ = lobby.Close()
		g.onlineFail(fmt.Errorf("nanolathe: online lobby: logical path guis/skirmish.gui, providers searched [vfs], expected the authored setup window"))
		return
	}
	window := gui.CloneWindow(asset.window)
	slots := make([]ui.SkirmishSlot, relay.HostedMaxSeats)
	for i := range slots {
		slots[i] = ui.SkirmishSlot{SideStages: uint8(g.skirmishSideCount())}
	}
	ui.InstallSkirmishDynamicGadgets(window, slots)
	buildOnlineLobbyWindow(window)
	g.installRetailWindowButtonArt(window, asset.art)
	g.installRetailListScrollbars(window, asset.art)
	g.initializeRetailLabels(window)
	panel := ui.NewPanel(window)
	if panel == nil {
		_ = lobby.Close()
		g.onlineFail(fmt.Errorf("nanolathe: online lobby: panel construction failed"))
		return
	}
	room := onlineRoomState{lobby: lobby, panel: panel, address: address, cat: cat, archives: archives, known: known,
		looseFiles: looseSimulationFiles(g.cs.baseRoots),
		assets:     &retailPanelAssets{window: window, background: lobbyBackdrop(asset.background), art: asset.art}}
	if font, err := formats.LoadFNTFile(g.cs.fs, onlineCodeFont); err == nil {
		room.codeFont = font
	}
	s.room, s.phase, s.panel, s.status = room, onlineInLobby, nil, ""
	g.adoptOnlineBase(base)
	if room.looseFiles {
		s.room.notice = "Loose game files in your Total Annihilation folder are not used online."
	}
	g.frontend.Open(modeMenuSkirmish, panel, false)
	flushWindowTokens(clPtr)
	g.refreshOnlineLobby()
}

// adoptOnlineBase makes base the room's configuration: its settings, whether
// this install has its map, and how many players the map seats. The mod is
// the room's for good.
func (g *gameShell) adoptOnlineBase(base session.EffectiveMatchConfig) {
	r := &g.online.room
	r.base, r.baseReq = base, base.Request()
	r.settings = onlineSettingsOf(r.baseReq)
	r.content = resolveRoomContent(g.cs, r.archives, installedMapPackages(), r.known, r.baseReq.RoomContent)
	r.contentCS = g.cs
	g.startRoomPackageFetch()
	r.mapMissing = !g.hasSkirmishMap(r.baseReq.MapName)
	r.capacity = 0
	if !r.mapMissing && r.cat != nil {
		if n, err := onlineMapCapacity(r.cat, r.baseReq.MapName); err == nil {
			r.capacity = n
		}
	}
}

// closeOnlineRoom leaves the room: its lobby closes and any check stops.
func (g *gameShell) closeOnlineRoom() {
	if g.online == nil {
		return
	}
	r := &g.online.room
	if r.readyJob != nil {
		r.readyJob.abandon()
	}
	r.prepared.close()
	if r.lobby != nil {
		_ = r.lobby.Close()
	}
	g.online.room = onlineRoomState{}
}

// leaveOnlineLobby leaves the room for the online screen, which says why.
func (g *gameShell) leaveOnlineLobby(status string) {
	if g.online == nil {
		return
	}
	detail := "Server " + g.online.room.address
	g.closeOnlineRoom()
	g.online = nil
	g.openMenu(modeMenuMain)
	if err := g.openOnlineScreen(); err != nil {
		reportRetailMessageError(g.showRetailMessage(err.Error()))
		return
	}
	g.online.status, g.online.detail = status, detail
	g.refreshOnlinePanel()
}

// pollOnlineLobby follows the room each step: a failure returns to the online
// screen with its reason, a new base configuration is adopted, cleared
// readiness is explained, a finished check reports Ready, chat arrives, and
// Started enters the battle.
func (g *gameShell) pollOnlineLobby() {
	r := &g.online.room
	state, err := r.lobby.State()
	if err != nil {
		g.leaveOnlineLobby(onlineRefusalText(err))
		return
	}
	if state.Started {
		g.startOnlineBattle(state)
		return
	}
	if clPtr != nil {
		g.cancelOnlineChatOnRightPress(clPtr.Input())
	}
	changed := g.syncOnlineChatCapture()
	// A remount — a fetched map package, or one the host installed from
	// the map picker — resolves the room's content again (§16.8).
	if r.contentCS != g.cs {
		g.refreshOnlineRoomContent()
		changed = true
	}
	if g.pollRoomPackageFetch() {
		changed = true
	}
	g.startRoomPackageFetch()
	if g.pollRoomAddonFetch() {
		changed = true
	}
	changed = state != r.state || changed
	if state.ConfigVersion != r.baseVersion {
		r.baseVersion = state.ConfigVersion
		// The host already holds what it sent; an echo of an earlier change
		// must not undo a later one.
		if bytes := r.lobby.Configuration(); len(bytes) != 0 && !r.isHost() {
			base, err := session.DecodeMatchConfig(bytes)
			if err != nil || base.Request().Mod != r.baseReq.Mod {
				g.leaveOnlineLobby("The host's game settings could not be read. The host may be running a different release.")
				return
			}
			if base.Digest() != r.base.Digest() {
				g.adoptOnlineBase(base)
				changed = true
			}
		}
	}
	if changed {
		local := r.lobby.Seat()
		if r.ready && int(local) < len(state.Seats) && !state.Seats[local].Ready {
			r.ready, r.notice = false, onlineReadyClearedReason(r.state, state)
		}
		r.state = state
	}
	// Chat after the state, so a line names a sender who has just joined.
	if g.pollOnlineChat() {
		changed = true
	}
	if job := r.readyJob; job != nil {
		select {
		case result := <-job.done:
			r.readyJob = nil
			g.finishOnlineReady(result)
			changed = true
		default:
		}
	}
	if changed {
		g.refreshOnlineLobby()
	}
}

// onlineReadyClearedReason says why the relay cleared every seat's ready.
func onlineReadyClearedReason(before, after relay.HostedLobbyState) string {
	if before.ConfigVersion != after.ConfigVersion {
		return "The host changed the game settings. Choose Ready again."
	}
	for i := range before.Seats {
		if before.Seats[i].Present != after.Seats[i].Present {
			return "A player joined or left. Choose Ready again."
		}
	}
	for i := range before.Seats {
		b, a := before.Seats[i], after.Seats[i]
		if b.Team != a.Team || b.Side != a.Side || b.Color != a.Color {
			return "A player changed team, side or colour. Choose Ready again."
		}
	}
	return "A player joined, left or changed team, side or colour. Choose Ready again."
}

// onlineLobbyBlock is why the room cannot become ready now, for every seat,
// or "" when it can. The host's computers count as players against the map's
// start positions, ten players and Survival's three survivors, and join the
// one-team test; at least two humans are always needed.
func (g *gameShell) onlineLobbyBlock() string {
	r := &g.online.room
	seats, _, _ := onlineSeatsOf(r.state, r.lobby.Seat(), r.settings.survival)
	computers := len(r.settings.computers)
	players, limit := len(seats)+computers, onlinePlayerLimit(r.settings.survival, r.capacity)
	included := "."
	if computers > 0 {
		included = ", computers included."
	}
	switch {
	case r.fetch.want.Name != "" && roomEntryListed(r.content.missing, r.fetch.want):
		return g.roomFetchText()
	case r.addonFetch.want != nil:
		return g.roomAddonFetchText()
	case len(r.content.missing) > 0:
		return strings.TrimSpace(roomMissingText(r.content.missing) + " " + g.roomFetchFailures() + " " + g.roomAddonFailures())
	case r.mapMissing:
		return "The host chose " + r.baseReq.MapName + ", which you don't have."
	case len(seats) < onlineMinHumans:
		if r.isHost() {
			return "Send the room code to the other players."
		}
		return "Waiting for another player."
	case r.settings.survival && players > limit:
		return fmt.Sprintf("Survival allows up to %d players%s", limit, included)
	case players > limit && r.capacity > 0:
		return fmt.Sprintf("This map supports %d players%s", limit, included)
	case players > limit:
		return fmt.Sprintf("A game holds at most %d players%s", limit, included)
	case !r.settings.survival && onlineOneTeam(seats, r.settings.computers):
		return "Everyone is on one team. Someone needs another team, or none."
	}
	return ""
}

// onlineCanStart reports the host's Start: at least two present, every one
// ready with agreeing digests, and nothing blocking.
func (g *gameShell) onlineCanStart() bool {
	r := &g.online.room
	if !r.isHost() || r.state.Mismatch || g.onlineLobbyBlock() != "" {
		return false
	}
	for _, i := range r.presentSeats() {
		if !r.state.Seats[i].Ready {
			return false
		}
	}
	return true
}

// refreshOnlineLobby writes the lobby: the rows, the host's settings, the
// status line and what each button may do now.
func (g *gameShell) refreshOnlineLobby() {
	s := g.online
	if s == nil || s.room.panel == nil || s.room.lobby == nil {
		return
	}
	r := &s.room
	p := r.panel
	host := r.isHost()
	rows := r.lobbyRows()
	teams := r.lobbyTeams()
	colors := r.computerColors()
	for row := 0; row < relay.HostedMaxSeats; row++ {
		suffix := strconv.Itoa(row)
		shown := row < len(rows)
		for _, name := range []string{"Player", "Side", "Color", "Allies", "Metal", "Energy"} {
			p.SetActive(name+suffix, shown)
		}
		p.SetActive(lobbyLevel+suffix, false)
		retailGreyGadget(p.Window, "Player"+suffix, false)
		if !shown {
			continue
		}
		switch line := rows[row]; {
		case line.seat >= 0:
			g.refreshOnlineHumanRow(row, line.seat, teams)
		case line.computer >= 0:
			g.refreshOnlineComputerRow(row, line.computer, colors[line.computer], teams)
		default:
			g.refreshOnlineAddRow(row)
		}
	}
	p.SetActive(lobbyLevelHead, len(r.settings.computers) > 0)
	g.refreshOnlineLobbySettings(host)
	g.refreshOnlineRules(host)
	g.refreshOnlineChat()
	p.SetText("MapName", r.baseReq.MapName)
	p.SetActive(lobbyCopy, onlineClipboardAvailable())
	ready := "Ready"
	if r.ready {
		ready = "Not ready"
	}
	p.SetText(lobbyReady, ready)
	block := g.onlineLobbyBlock()
	retailGreyGadget(p.Window, lobbyReady, !r.ready && (r.readyJob != nil || block != ""))
	retailGreyGadget(p.Window, "Start", !g.onlineCanStart())
	g.setOnlineLobbyStatus(g.onlineLobbyStatus(block))
}

// lobbyEditable reports whether this seat may change its own row, and the
// host its computers, now: not ready and not checking.
func (r *onlineRoomState) lobbyEditable() bool { return !r.ready && r.readyJob == nil }

// lobbyTeams are the teams of the players present and the host's computers,
// the rows the allegiance symbols count.
func (r *onlineRoomState) lobbyTeams() []uint8 {
	var teams []uint8
	for _, i := range r.presentSeats() {
		teams = append(teams, r.state.Seats[i].Team)
	}
	for _, c := range r.settings.computers {
		teams = append(teams, c.Team)
	}
	return teams
}

// refreshOnlineHumanRow writes a present player's row: their name — the
// display name their hello carried, or "Player n" — marked (You) or (Host),
// their side, colour, team and readiness. A name too long for the button
// beside its mark is shortened with "..." so the mark stays; the help names
// the player in full.
func (g *gameShell) refreshOnlineHumanRow(row, seatIndex int, teams []uint8) {
	r := &g.online.room
	p := r.panel
	local := r.lobby.Seat()
	suffix := strconv.Itoa(row)
	seat := r.state.Seats[seatIndex]
	name := r.seatDisplayName(seatIndex)
	mark := ""
	switch {
	case uint8(seatIndex) == local:
		mark = " (You)"
	case seatIndex == 0:
		mark = " (Host)"
	}
	p.SetText("Player"+suffix, g.fitOnlineRowCaption(p, "Player"+suffix, name, mark))
	p.SetStatus("Player"+suffix, 0)
	p.SetStatus("Side"+suffix, 0)
	p.SetStageAt(p.Index("Side"+suffix), clampMenuStage(int(seat.Side), g.onlineSideCount()))
	p.SetStatus("Color"+suffix, int(seat.Color))
	p.SetStatus("Allies"+suffix, onlineTeamIcon(seat.Team, teams))
	p.SetActive("Allies"+suffix, !r.settings.survival)
	p.SetActive("Metal"+suffix, seat.Ready)
	p.SetActive("Energy"+suffix, false)
	p.SetText("Metal"+suffix, "Ready")
	own := uint8(seatIndex) == local
	switch {
	case own && r.lobbyEditable():
		p.SetHelp("Side"+suffix, "Click to choose your side.")
		p.SetHelp("Color"+suffix, "Click to choose your colour; right-click to step back.")
		p.SetHelp("Allies"+suffix, "Click to choose your team. Teammates are allies; Allied Sight says whether they share sight.")
	case own:
		p.SetHelp("Side"+suffix, "Choose Not ready to change your side.")
		p.SetHelp("Color"+suffix, "Choose Not ready to change your colour.")
		p.SetHelp("Allies"+suffix, "Choose Not ready to change your team.")
	default:
		p.SetHelp("Side"+suffix, "This player's side.")
		p.SetHelp("Color"+suffix, "This player's colour.")
		p.SetHelp("Allies"+suffix, "This player's team.")
	}
	p.SetHelp("Player"+suffix, onlinePlayerHelp(name, seatIndex == 0, own))
}

// onlinePlayerHelp is a player row's help: who the player is, by name, and
// what the host does.
func onlinePlayerHelp(name string, host, own bool) string {
	switch {
	case host && own:
		return "You are " + name + ", the host: you choose the settings and start the game."
	case host:
		return name + " is the host, who chooses the settings and starts the game."
	case own:
		return "This is you, " + name + "."
	}
	return "This is " + name + "."
}

// fitOnlineRowCaption is a player's name with its mark, in the width the
// row's name button gives a caption, the name shortened with "..." when the
// two do not fit.
func (g *gameShell) fitOnlineRowCaption(p *ui.Panel, gadget, name, mark string) string {
	i := p.Index(gadget)
	if i < 0 {
		return name + mark
	}
	gad := p.Window.Gadgets[i]
	measure, _ := g.retailTextMetrics(g.windowGadgetFont(p, gad))
	return onlineFitCaption(name, mark, measure, int(gad.Rect.W)-onlineCaptionInset)
}

// onlineCaptionInset is the part of a name button's width the caption keeps
// clear: the centred pen's offset and the art's bevel on either side.
const onlineCaptionInset = 8

// onlineFitCaption shortens name with "..." until name and mark fit width.
func onlineFitCaption(name, mark string, measure func(string) int, width int) string {
	if measure(name+mark) <= width {
		return name + mark
	}
	for cut := len(name) - 1; cut > 0; cut-- {
		short := strings.TrimRight(name[:cut], " ") + "..."
		if measure(short+mark) <= width {
			return short + mark
		}
	}
	return "..." + mark
}

// refreshOnlineComputerRow writes one of the host's computers: its AI named
// as the setup screen names a computer row (computerAICaption), numbered when
// the room has several, its side, the colour it plays in, its team and, in the
// energy column, its difficulty, captioned by the row's level label. The host
// edits them with its own row's controls while not ready; everyone else sees
// them.
func (g *gameShell) refreshOnlineComputerRow(row, k int, color uint8, teams []uint8) {
	r := &g.online.room
	p := r.panel
	suffix := strconv.Itoa(row)
	c := r.settings.computers[k]
	caption := computerAICaption(c.Kind)
	if len(r.settings.computers) > 1 {
		caption += " " + strconv.Itoa(k+1)
	}
	p.SetText("Player"+suffix, caption)
	p.SetStatus("Player"+suffix, 0)
	p.SetStatus("Side"+suffix, 0)
	p.SetStageAt(p.Index("Side"+suffix), clampMenuStage(int(c.Side), g.onlineSideCount()))
	p.SetStatus("Color"+suffix, int(color))
	p.SetStatus("Allies"+suffix, onlineTeamIcon(c.Team, teams))
	p.SetActive("Allies"+suffix, !r.settings.survival)
	p.SetActive("Metal"+suffix, false)
	p.SetText("Energy"+suffix, "")
	p.SetActive(lobbyLevel+suffix, true)
	p.SetText(lobbyLevel+suffix, onlineDifficultyName(session.DifficultyTier(r.baseReq.Community, int(c.Difficulty))))
	switch {
	case r.isHost() && r.lobbyEditable():
		if c.Kind == ai.ControllerModern {
			p.SetHelp("Player"+suffix, "The Modern AI plays this computer player. Click for the Classic AI.")
		} else {
			p.SetHelp("Player"+suffix, "The Classic AI plays this computer player. Click to remove it.")
		}
		p.SetHelp("Side"+suffix, "Click to choose this computer's side.")
		p.SetHelp("Color"+suffix, "Click to choose this computer's colour; right-click to step back.")
		p.SetHelp("Allies"+suffix, "Click to choose this computer's team. Teammates are allies.")
		p.SetHelp("Energy"+suffix, "Click to choose this computer's difficulty: Easy, Medium or Hard.")
	case r.isHost():
		for _, name := range []string{"Player", "Side", "Color", "Allies", "Energy"} {
			p.SetHelp(name+suffix, "Choose Not ready to change the computer players.")
		}
	default:
		who := "The Classic AI"
		if c.Kind == ai.ControllerModern {
			who = "The Modern AI"
		}
		p.SetHelp("Player"+suffix, who+" plays this computer player, which the host added.")
		p.SetHelp("Side"+suffix, "This computer's side.")
		p.SetHelp("Color"+suffix, "This computer's colour.")
		p.SetHelp("Allies"+suffix, "This computer's team.")
		p.SetHelp("Energy"+suffix, "This computer's difficulty.")
	}
	// The caption lies over the button, so hovering it shows the button's help.
	p.SetHelp(lobbyLevel+suffix, p.HelpOf("Energy"+suffix))
}

// refreshOnlineAddRow writes the host's Add computer row, greyed while the
// host is ready or checking.
func (g *gameShell) refreshOnlineAddRow(row int) {
	r := &g.online.room
	p := r.panel
	suffix := strconv.Itoa(row)
	for _, name := range []string{"Side", "Color", "Allies", "Metal", "Energy"} {
		p.SetActive(name+suffix, false)
	}
	p.SetText("Player"+suffix, onlineAddComputerCaption)
	p.SetStatus("Player"+suffix, 0)
	retailGreyGadget(p.Window, "Player"+suffix, !r.lobbyEditable())
	if r.lobbyEditable() {
		p.SetHelp("Player"+suffix, "Click to add a computer player, which the Modern AI plays.")
	} else {
		p.SetHelp("Player"+suffix, "Choose Not ready to add a computer player.")
	}
}

// onlineDifficultyName is a difficulty tier (session.DifficultyTier) in the
// setup screen's words.
func onlineDifficultyName(tier int) string {
	return gameOptionsDifficulty[min(max(tier, 0), len(gameOptionsDifficulty)-1)]
}

// refreshOnlineLobbySettings writes the host's settings with the setup
// screen's captions and help; only the host may change them.
func (g *gameShell) refreshOnlineLobbySettings(host bool) {
	r := &g.online.room
	p := r.panel
	set := r.settings
	p.SetActive("StartLocation", !set.survival)
	p.SetActive(survivalPaceButton, set.survival)
	for _, name := range []string{survivalAirButton, survivalSeaButton, lobbyAirLabel, lobbySeaLabel} {
		p.SetActive(name, set.survival)
	}
	// Strict 3.1 rooms keep every team's shared sight (SeatRules).
	offered := !set.survival && onlineSeatRules(set.rules).AlliedSightChoice()
	p.SetActive(alliedSightButton, offered)
	p.SetActive(alliedSightLabel, offered)
	p.SetStageAt(p.Index(alliedSightButton), boolInt(!set.separateSight))
	p.SetHelp(alliedSightButton, alliedSightHelp(!set.separateSight))
	p.SetText(lobbyLocLabel, "Location")
	if set.survival {
		p.SetText(lobbyLocLabel, "Wave Pace")
	}
	p.SetStageAt(p.Index(lobbyGameType), boolInt(set.survival))
	if set.location == 0 {
		p.SetStageAt(p.Index("StartLocation"), 1)
		p.SetHelp("StartLocation", "Commanders are randomly placed on the battle field.")
	} else {
		p.SetStageAt(p.Index("StartLocation"), 0)
		p.SetHelp("StartLocation", "Commanders are placed at pre-determined locations.")
	}
	if set.commanderDeath == 0 {
		p.SetStageAt(p.Index("CommanderDeath"), 1)
		p.SetHelp("CommanderDeath", "Game continues after Commander is destroyed.")
	} else {
		p.SetStageAt(p.Index("CommanderDeath"), 0)
		p.SetHelp("CommanderDeath", "Game ends when commander is destroyed.")
	}
	if set.mapping == 0 {
		p.SetStageAt(p.Index("Mapping"), 1)
		p.SetHelp("Mapping", "Terrain is visible.")
	} else {
		p.SetStageAt(p.Index("Mapping"), 0)
		p.SetHelp("Mapping", "Terrain is blacked out until explored.")
	}
	switch {
	case set.lineOfSight == 0:
		p.SetStageAt(p.Index("LineOfSight"), 0)
		p.SetHelp("LineOfSight", "All mapped terrain is visible.")
	case set.losType == 1:
		p.SetStageAt(p.Index("LineOfSight"), 1)
		p.SetHelp("LineOfSight", "Terrain elevations affect a unit's view.")
	default:
		p.SetStageAt(p.Index("LineOfSight"), 2)
		p.SetHelp("LineOfSight", "Terrain elevations do not affect a unit's view.")
	}
	p.SetStageAt(p.Index(survivalPaceButton), clampMenuStage(int(set.pace), 3))
	p.SetStageAt(p.Index(survivalAirButton), boolInt(set.noAir))
	p.SetStageAt(p.Index(survivalSeaButton), boolInt(set.noNaval))
	for _, name := range []string{lobbyGameType, "StartLocation", "CommanderDeath", "Mapping", "LineOfSight", survivalPaceButton, survivalAirButton, survivalSeaButton, alliedSightButton, "SelectMap"} {
		retailGreyGadget(p.Window, name, !host)
	}
}

// onlineRulesHelp describes each rule set, after the Nanolathe screen's words
// (nlscreen_pages.go), in the Rules control's stage order.
var onlineRulesHelp = [...]string{
	"Strict 3.1: the original 3.1 game, faults included. One computer per human, and only the host adds computers.",
	"Community 3.9: the original game plus the TA community patch's fixes and larger limits.",
	"Modern: Community 3.9 plus Nanolathe's own fixes for movement, building and targeting.",
}

// refreshOnlineRules writes the Rules control: the room's rule set, which the
// host may change and every guest sees. The mounted mod's minimum leaves
// lower stages out, and a host with one stage left has nothing to change.
func (g *gameShell) refreshOnlineRules(host bool) {
	r := &g.online.room
	p := r.panel
	stage := max(onlineRulesStage(r.settings.rules), 0)
	p.SetText(lobbyRules, gameplayLabel(gameplay.Mode(onlineRuleStages[stage])))
	lowest := onlineLowestRulesStage(g.cs)
	fixed := lowest == len(onlineRuleStages)-1
	retailGreyGadget(p.Window, lobbyRules, !host || fixed)
	help := onlineRulesHelp[stage]
	switch {
	case !host:
		help = "The host chose these rules. " + help
	case fixed:
		help = "This mod plays only Modern rules. " + help
	case lowest > 0:
		help = "Click for this mod's other rules. " + help
	default:
		help = "Click to change the rules. " + help
	}
	p.SetHelp(lobbyRules, help)
	p.SetHelp(lobbyRulesLabel, help)
}

// onlineTeamIcon is a row's allegiance symbol, as the setup screen draws an
// alliance: no team is blank, and teams 1..5 are the five symbols, joined
// when shared with another row of teams and split when alone
// (retailAllyIconFrame) [07 §4].
func onlineTeamIcon(team uint8, teams []uint8) int {
	const blank = 10
	if team == session.OnlineTeamNone || team >= onlineTeams {
		return blank
	}
	shared := 0
	for _, t := range teams {
		if t == team {
			shared++
		}
	}
	group := int(team) - 1
	if shared > 1 {
		return group * 2
	}
	return group*2 + 1
}

// onlineMismatchText is the lobby's line when every seat is ready with
// content that resolved alike and the Ready check still differs: the
// simulations themselves differ (§16.8).
const onlineMismatchText = "Your games computed the test battle differently. If everyone has the same version, please report this with your logs."

// onlineLobbyStatus is the status line, most pressing first.
func (g *gameShell) onlineLobbyStatus(block string) string {
	r := &g.online.room
	switch {
	case r.mapMissing || len(r.content.missing) > 0:
		return block
	case r.state.Mismatch && len(r.content.different) > 0:
		return roomDifferentText(r.content.different) + " That is why your games compute differently."
	case r.state.Mismatch:
		return onlineMismatchText
	case r.readyErr != nil:
		return "The check of your game failed: " + onlineRefusalText(r.readyErr)
	case r.readyJob != nil:
		return "Checking that your game matches..."
	case block != "":
		return block
	case r.notice != "":
		return r.notice
	case len(r.content.different) > 0:
		return roomDifferentText(r.content.different)
	}
	everyone := true
	for _, i := range r.presentSeats() {
		everyone = everyone && r.state.Seats[i].Ready
	}
	switch {
	case r.isHost() && everyone:
		return "Everyone is ready. Choose Start."
	case r.isHost():
		return "When everyone is ready, choose Start."
	case everyone:
		return "Waiting for the host to start the game."
	}
	return "Choose your team, side and colour, then Ready."
}

// setOnlineLobbyStatus keeps the status for the hover help to fall back to.
func (g *gameShell) setOnlineLobbyStatus(status string) {
	r := &g.online.room
	r.panel.SetHelp(lobbyStatus, status)
	r.panel.SetText(lobbyStatus, status)
}

// updateOnlineLobbyHelp shows the hovered control's help, or the status line.
func (g *gameShell) updateOnlineLobbyHelp(x, y int32) {
	p := g.online.room.panel
	help := ""
	if i := p.HitTest(x, y); i >= 0 {
		help = p.HelpAt(i)
	}
	if help == "" {
		help = p.HelpOf(lobbyStatus)
	}
	p.SetText(lobbyStatus, help)
}

// activateOnlineLobbyGadget routes a control of the lobby.
func (g *gameShell) activateOnlineLobbyGadget(name string) bool {
	if !g.onlineLobbyActive() {
		return false
	}
	r := &g.online.room
	if r.chat.swallowRight {
		// The right press that dropped the chat line does nothing else.
		r.chat.swallowRight = false
		return true
	}
	if name == lobbyChatLine {
		// Chat stays open to a ready seat: it changes no setting.
		g.playMenuCue("SmallButton")
		g.activateOnlineChatLine()
		return true
	}
	if r.readyJob == nil && r.ready && name != lobbyReady && name != lobbyCopy && name != "PrevMenu" && name != "Start" {
		// A ready seat changes nothing; the relay would clear every seat.
		return true
	}
	g.playMenuCue("SmallButton")
	if slot, kind, ok := dynamicSlot(name); ok {
		g.activateOnlineRow(slot, kind, 1)
		return true
	}
	set := r.settings
	switch name {
	case lobbyReady:
		g.setOnlineReady(!r.ready)
		return true
	case lobbyCopy:
		g.copyOnlineCode()
		return true
	case "Start":
		g.startOnlineMatch()
		return true
	case "PrevMenu":
		g.leaveOnlineLobby("You left the game.")
		return true
	case "SelectMap":
		g.openOnlineMapPicker()
		return true
	case lobbyGameType:
		set.survival = !set.survival
	case "StartLocation":
		set.location ^= 1
	case "CommanderDeath":
		set.commanderDeath ^= 1
	case "Mapping":
		set.mapping ^= 1
	case "LineOfSight":
		set.lineOfSight, set.losType = nextOnlineLineOfSight(set.lineOfSight, set.losType)
	case survivalPaceButton:
		set.pace = survival.Pace((int(set.pace) + 1) % 3)
	case survivalAirButton:
		set.noAir = !set.noAir
	case survivalSeaButton:
		set.noNaval = !set.noNaval
	case alliedSightButton:
		if !onlineSeatRules(set.rules).AlliedSightChoice() {
			return true
		}
		set.separateSight = !set.separateSight
	case lobbyRules:
		lowest := onlineLowestRulesStage(g.cs)
		if set.rules = onlineNextRules(set.rules, 1, lowest); set.rules == r.settings.rules {
			return true
		}
	default:
		return true
	}
	g.pushOnlineSettings(set)
	return true
}

// nextOnlineLineOfSight is the setup screen's line-of-sight cycle
// (cycleLineOfSight): True, Circular, Permanent.
func nextOnlineLineOfSight(lineOfSight, losType uint8) (uint8, uint8) {
	switch {
	case lineOfSight == 0:
		return 1, 1
	case losType == 1:
		return 1, 0
	}
	return 0, 1
}

// activateOnlineColorBack is a right click on a row's colour: the local
// player's own colour steps back while not ready, as the setup screen's
// right click does [08 R-SKIR-01 §1].
func (g *gameShell) activateOnlineColorBack(row int) {
	if !g.onlineLobbyActive() || g.online.room.readyJob == nil && g.online.room.ready {
		return
	}
	if r := &g.online.room; r.chat.swallowRight {
		r.chat.swallowRight = false
		return
	}
	g.playMenuCue("SmallButton")
	g.activateOnlineRow(row, "Color", -1)
}

// activateOnlineRow cycles the local player's own side, colour or team while
// not ready, and lets the host add and change its computers; another player's
// row does nothing. delta steps the colour, +1 for a left click and -1 for a
// right click.
func (g *gameShell) activateOnlineRow(row int, kind string, delta int) {
	r := &g.online.room
	rows := r.lobbyRows()
	if row >= len(rows) || !r.lobbyEditable() {
		return
	}
	switch line := rows[row]; {
	case line.seat >= 0:
		g.activateOnlineHumanRow(line.seat, kind, delta)
	case !r.isHost():
	case line.computer >= 0:
		g.activateOnlineComputerRow(line.computer, kind, delta)
	case kind == "Player":
		g.addOnlineComputer()
	}
}

// activateOnlineHumanRow cycles the local player's own side, colour or team.
func (g *gameShell) activateOnlineHumanRow(seatIndex int, kind string, delta int) {
	r := &g.online.room
	if uint8(seatIndex) != r.lobby.Seat() {
		return
	}
	seat := r.state.Seats[seatIndex]
	var err error
	switch kind {
	case "Side":
		err = r.lobby.SetSide(uint8((int(seat.Side) + 1) % g.onlineSideCount()))
	case "Color":
		computers := r.computerColors()
		next, ok := nextOnlineColor(seat.Color, delta, func(c uint8) bool {
			return onlineColorHeld(r.state, seatIndex, c) || slices.Contains(computers, c)
		})
		if !ok {
			return
		}
		err = r.lobby.SetColor(next)
	case "Allies":
		if r.settings.survival {
			return
		}
		err = r.lobby.SetTeam((seat.Team + 1) % onlineTeams)
	default:
		return
	}
	if err != nil {
		r.notice = onlineRefusalText(err)
	}
	g.refreshOnlineLobby()
}

// activateOnlineComputerRow is the host changing computer k with the controls
// of its own row: the name walks the setup screen's computer cycle, Modern AI
// to Classic AI and a Classic AI out of the room, as a Survival ally row does
// (computerAIHelp); side, team and colour cycle as its own do; the energy
// column steps the difficulty Easy, Medium, Hard and back to Easy, as the
// setup screen's Difficulty does. Each change is a new base configuration.
func (g *gameShell) activateOnlineComputerRow(k int, kind string, delta int) {
	r := &g.online.room
	computers := slices.Clone(r.settings.computers)
	c := &computers[k]
	switch kind {
	case "Player":
		if c.Kind == ai.ControllerModern {
			c.Kind = ai.ControllerClassic
		} else {
			computers = slices.Delete(computers, k, k+1)
		}
	case "Side":
		c.Side = uint8((int(c.Side) + 1) % g.onlineSideCount())
	case "Color":
		seats, _, _ := onlineSeatsOf(r.state, r.lobby.Seat(), r.settings.survival)
		shown := onlineComputerColors(seats, r.settings.computers)
		next, ok := nextOnlineColor(shown[k], delta, func(color uint8) bool {
			for _, s := range seats {
				if s.Color == color {
					return true
				}
			}
			for j, other := range shown {
				if j != k && other == color {
					return true
				}
			}
			return false
		})
		if !ok {
			return
		}
		c.Color = next
	case "Allies":
		if r.settings.survival {
			return
		}
		c.Team = (c.Team + 1) % onlineTeams
	case "Energy":
		c.Difficulty = uint8((int(c.Difficulty) + 1) % len(gameOptionsDifficulty))
	default:
		return
	}
	next := r.settings
	next.computers = computers
	g.pushOnlineSettings(next)
}

// addOnlineComputer is the host's Add computer: a new computer row after the
// others, played by the Modern AI as the setup screen's new computer rows are,
// at the host's own skirmish difficulty, with no team, the side its row's
// position alternates to and the lowest colour no player holds.
func (g *gameShell) addOnlineComputer() {
	r := &g.online.room
	if !r.canAddComputer() {
		return
	}
	seats, _, _ := onlineSeatsOf(r.state, r.lobby.Seat(), r.settings.survival)
	var held [relay.HostedColors]bool
	for _, s := range seats {
		held[s.Color%relay.HostedColors] = true
	}
	for _, c := range r.computerColors() {
		held[c%relay.HostedColors] = true
	}
	color := uint8(0)
	for color < relay.HostedColors-1 && held[color] {
		color++
	}
	row := len(seats) + len(r.settings.computers)
	next := r.settings
	next.computers = append(slices.Clone(r.settings.computers), session.OnlineComputer{
		Side:       uint8(row % max(g.onlineSideCount(), 1)),
		Color:      color,
		Kind:       ai.ControllerModern,
		Difficulty: uint8(min(max(g.setup.Difficulty, 0), len(gameOptionsDifficulty)-1)),
	})
	g.pushOnlineSettings(next)
}

// nextOnlineColor is the setup screen's colour step for the room: one logo
// frame per click from current, +1 or -1 modulo the room's colours, stepping
// again past a colour another player holds [08 R-SKIR-01 §1]. A room never
// has more players than colours, so a free one is always found; false means
// none is, or the step came back to current.
func nextOnlineColor(current uint8, delta int, held func(uint8) bool) (uint8, bool) {
	const colors = relay.HostedColors
	candidate := int(current)
	for range colors {
		candidate = ((candidate+delta)%colors + colors) % colors
		if !held(uint8(candidate)) {
			return uint8(candidate), uint8(candidate) != current
		}
	}
	return 0, false
}

// onlineColorHeld reports whether a present seat other than seat holds color.
func onlineColorHeld(state relay.HostedLobbyState, seat int, color uint8) bool {
	for i, other := range state.Seats {
		if i != seat && other.Present && other.Color == color {
			return true
		}
	}
	return false
}

// pushOnlineSettings is the host replacing the room's base configuration with
// new settings, rule set and computers included, which every seat adopts. Its
// frozen part — seeds, mod, mutators and restrictions — is unchanged; what
// the rule set decides is resolved again (onlineFrozen). Survival keeps one
// computer at most beside its two players, and Strict 3.1 one computer per
// human, so a switch to either with more is refused until the host removes
// them. A rule set below the mod's minimum is refused.
func (g *gameShell) pushOnlineSettings(next onlineSettings) {
	r := &g.online.room
	if !r.isHost() || r.cat == nil {
		return
	}
	if most := onlineMostComputers(next.survival, 0); len(next.computers) > most {
		r.notice = fmt.Sprintf("A game allows %d computer players. Remove the others first.", most)
		if next.survival {
			r.notice = "Survival allows one computer player. Remove the others first."
		}
		g.refreshOnlineLobby()
		return
	}
	if len(next.computers) > onlineComputerCap(next.rules) {
		r.notice = onlineStrictComputersText
		g.refreshOnlineLobby()
		return
	}
	if onlineRulesStage(next.rules) < onlineLowestRulesStage(g.cs) {
		r.notice = "This game's mod needs " + gameplayLabel(gameplay.Mode(onlineRuleStages[onlineLowestRulesStage(g.cs)])) + " rules or newer."
		g.refreshOnlineLobby()
		return
	}
	// The room content follows the map: a downloaded map brings its
	// package and the limits it needs (§16.8).
	frozen := onlineFrozenOf(g.cs, r.baseReq)
	roomContent, err := onlineRoomContent(g.cs, r.archives, r.known, next.mapName)
	if err == nil {
		frozen.room.RoomContent = roomContent
		frozen.options.ContentLimits = onlineRoomLimits(g.cs, roomContent)
	}
	var base session.EffectiveMatchConfig
	if err == nil {
		base, err = onlineBaseConfig(g.cs, r.cat, next, frozen)
	}
	if err == nil {
		var encoded []byte
		if encoded, err = session.EncodeMatchConfig(base); err == nil {
			if err = r.lobby.SetConfiguration(encoded); err == nil {
				g.adoptOnlineBase(base)
				r.notice = ""
			}
		}
	}
	if err != nil {
		r.notice = "That setting could not be used: " + onlineRefusalText(err)
	}
	g.refreshOnlineLobby()
}

// onlineStrictComputersText is why the host cannot switch to Strict 3.1 with
// more computers than it allows.
const onlineStrictComputersText = "Strict 3.1 allows one computer per human, and only the host adds computers for now. Remove the others first."

// openOnlineMapPicker opens the ordinary map selector over the lobby; its
// choice is the skirmish map, which the room then plays.
func (g *gameShell) openOnlineMapPicker() {
	r := &g.online.room
	if !r.isHost() {
		return
	}
	if len(g.maps) == 0 {
		r.notice = "There are no multiplayer maps to choose from."
		g.refreshOnlineLobby()
		return
	}
	r.choosingMap = true
	g.setSetupMap(r.settings.mapName)
	g.syncMapIndex()
	g.mapReturn = modeMenuSkirmish
	g.openMenu(modeMenuMap)
}

// leaveMapPicker is SELMAP's PREVMENU and LOAD: back to the screen that
// opened it. The lobby sits under the selector, so it is uncovered rather
// than rebuilt, and a new map becomes the room's.
func (g *gameShell) leaveMapPicker() {
	if s := g.online; s != nil && s.room.choosingMap {
		r := &s.room
		r.choosingMap = false
		if top := g.frontend.Panels.Top(); top != nil && top != r.panel {
			top.ResetPress()
			g.frontend.Panels.Pop()
		}
		g.frontend.SetMode(modeMenuSkirmish)
		flushWindowTokens(clPtr)
		if g.setup.MapName != r.settings.mapName {
			next := r.settings
			next.mapName = g.setup.MapName
			g.pushOnlineSettings(next)
			return
		}
		g.refreshOnlineLobby()
		return
	}
	g.openMenu(g.mapReturn)
}

// copyOnlineCode copies the room code where the host has a clipboard.
func (g *gameShell) copyOnlineCode() {
	r := &g.online.room
	r.notice = "Copied the room code " + r.lobby.Code() + "."
	if !onlineClipboardWrite(r.lobby.Code()) {
		r.notice = "The room code could not be copied. It is " + r.lobby.Code() + "."
	}
	g.refreshOnlineLobby()
}

// startOnlineMatch is the host's Start. A refused start leaves the room open.
func (g *gameShell) startOnlineMatch() {
	r := &g.online.room
	if !g.onlineCanStart() {
		return
	}
	if err := r.lobby.Start(); err != nil {
		g.leaveOnlineLobby(onlineRefusalText(err))
		return
	}
	r.notice = "Starting..."
	g.refreshOnlineLobby()
}

// drawOnlineLobbyHelp feeds the status line from the hovered control's help.
func (g *gameShell) drawOnlineLobbyHelp(c *client.Client) {
	mouse, _ := c.Input().PointerSample()
	g.updateOnlineLobbyHelp(int32(mouse.X), int32(mouse.Y))
}
