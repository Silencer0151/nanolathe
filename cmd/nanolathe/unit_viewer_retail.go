package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

// The native panel owns input in one logical coordinate system. Drawing maps
// these same rectangles and text metrics to the device-resolution settings
// style (DESIGN_DEVELOPER_TOOLS §7); no GPU upload occurs while building it.
func (s *toolsScreen) buildPanel() {
	if s.shell == nil {
		return
	}
	w := &gui.Window{Name: "nanolathe-unit-viewer", Rect: gui.Rect{W: unitViewerWidth, H: unitViewerHeight}, Gadgets: []gui.Gadget{{Kind: gui.KindPanel}}}
	add := func(kind gui.Kind, name, text string, x, y, width, height int32) int {
		w.Gadgets = append(w.Gadgets, gui.Gadget{Kind: kind, Name: name, Text: text, Active: 1, Rect: gui.Rect{X: x, Y: y, W: width, H: height}, Attribs: 1, ColorF: 9})
		return len(w.Gadgets) - 1
	}
	button := func(name, text string, x, y, width int32) {
		i := add(gui.KindButton, name, text, x, y, width, 36)
		w.Gadgets[i].Attribs = 0x20
	}
	if !s.viewer {
		button("VIEWER", "Unit viewer", 500, 392, 200)
		button("BACK", "Back", 1040, 26, 128)
	} else {
		i := add(gui.KindTextBox, "SEARCH", s.query, 32, 146, 234, 38)
		w.Gadgets[i].MaxChars = 127
		// Library rows hold a 48-pixel build picture beside the name; stock
		// pictures are 64x64, so this keeps them legible at every scale.
		i = add(gui.KindListBox, "UNITS", "", 32, 208, 210, unitViewerListHeight(unitViewerLibraryRows, unitViewerLibraryRowH))
		w.Gadgets[i].ItemHeight = unitViewerLibraryRowH
		w.Gadgets[i].Assoc = 1
		i = add(gui.KindScrollBar, "UNITSCROLL", "", 248, 208, 18, unitViewerListHeight(unitViewerLibraryRows, unitViewerLibraryRowH))
		w.Gadgets[i].Assoc, w.Gadgets[i].Attribs = 1, 0
		button("HISTBACK", "<", 1084, 102, 40)
		button("HISTFWD", ">", 1128, 102, 40)
		for _, tab := range []struct {
			name, text string
			x, w       int32
		}{{"TABSTATS", "Stats", 862, 96}, {"TABWEAPONS", "Weapons", 962, 104}, {"TABBUILD", "Build", 1070, 98}} {
			i = add(gui.KindButton, tab.name, tab.text, tab.x, 142, tab.w, 32)
			w.Gadgets[i].Attribs = 0x20
		}
		i = add(gui.KindListBox, "INFO", "", 862, 184, 284, unitViewerListHeight(unitViewerInfoRows, unitViewerInfoRowH))
		w.Gadgets[i].ItemHeight = unitViewerInfoRowH
		w.Gadgets[i].Assoc, w.Gadgets[i].Attribs = 2, 0x101
		i = add(gui.KindScrollBar, "INFOSCROLL", "", 1150, 184, 18, unitViewerListHeight(unitViewerInfoRows, unitViewerInfoRowH))
		w.Gadgets[i].Assoc, w.Gadgets[i].Attribs = 2, 0
		// The action row holds only the actions that apply to the selected
		// unit; refreshControls places them left to right at their caption
		// widths. The control row below is fixed.
		for _, a := range unitViewerActionButtons {
			button(a.name, a.labels[0], 306, unitViewerActionRowY, 60)
		}
		button("PAUSE", "Pause", 306, unitViewerControlRowY, 66)
		button("SPIN", "Rotate: on", 378, unitViewerControlRowY, 112)
		button("RESET", "Reset view", 496, unitViewerControlRowY, 108)
		button("WEAPON", "Weapon 1", 610, unitViewerControlRowY, 96)
		button("SEVERITY", "Severity 25", 712, unitViewerControlRowY, 122)
		button("BACK", "Back", 1040, 26, 128)
		addRestrictControls(add, button)
		// The ordinary slider builder synthesizes associated arrow buttons.
		// Settings-sized controls keep their hit targets and painted extents
		// identical; the native panel still computes thumb size and travel.
		for i, g := range w.Gadgets {
			if g.Kind == gui.KindScrollBar {
				bar, arrows := gui.BuildSlider(g, &gui.SliderArt{BaseExtent: 18, KnobExtent: 18, ArrowExtent: 18, ArrowCrossExtent: 18})
				w.Gadgets[i] = bar
				w.Gadgets = append(w.Gadgets, arrows...)
			}
		}
		s.listRect = screenkit.Rect{X: 32, Y: 208, W: 234, H: float64(unitViewerListHeight(unitViewerLibraryRows, unitViewerLibraryRowH))}
		s.infoRect = screenkit.Rect{X: 862, Y: 184, W: 306, H: float64(unitViewerListHeight(unitViewerInfoRows, unitViewerInfoRowH))}
		s.viewRect = screenkit.Rect{X: 306, Y: 180, W: 528, H: 418}
	}
	s.panel = ui.NewPanel(w)
	s.shell.frontend.Open(modeMenuSingle, s.panel, false)
	if s.viewer {
		s.refreshLists()
		s.refreshControls()
		s.panel.FocusEditor(s.panel.Index("SEARCH"))
	} else {
		s.panel.SetFocus(s.panel.Index("VIEWER"))
	}
	s.tokens = nil
	s.widgetHeld = false
}

// The native list service hit-tests (height - 2) / row height whole rows,
// below a two-pixel top inset, while its fill admits a row whenever the
// height still holds it [07 R-WGT-01 §4]. Sizing each list to exactly that
// many rows plus the inset keeps the rows the viewer paints, the rows a click
// can select and the furthest scroll in agreement; otherwise the last painted
// row could be scrolled into view but never clicked.
const (
	unitViewerLibraryRows, unitViewerLibraryRowH = 9, 56
	unitViewerInfoRows, unitViewerInfoRowH       = 20, 20
)

func unitViewerListHeight(rows, rowH int32) int32 { return rows*rowH + 2 }

// unitViewerListRows is the native service's whole-row count for a list.
func unitViewerListRows(g gui.Gadget) int {
	if g.ItemHeight <= 0 {
		return 1
	}
	return max(1, (int(g.Rect.H)-2)/int(g.ItemHeight))
}

func (s *toolsScreen) refreshLists() {
	if s.panel == nil || !s.viewer {
		return
	}
	rows := make([]string, len(s.filtered))
	for i, e := range s.filtered {
		rows[i] = unitViewerName(e.Def) + "\r" + strings.ToUpper(e.Key)
	}
	s.panel.FillTextListAt(s.panel.Index("UNITS"), rows, nil, unitViewerTextMetric)
	list := s.panel.Window.Gadgets[s.panel.Index("UNITS")]
	s.visible = unitViewerListRows(list)
	s.panel.SetListSelection("UNITS", s.selectionIndex(), s.visible)
	s.refreshInfo()
}

// refreshInfo rebuilds the selected tab's typed rows. The native list holds
// one plain row per typed row, so its scrolling, scrollbar and hit rows use
// the very rows drawInfo paints.
func (s *toolsScreen) refreshInfo() {
	if s.panel == nil || !s.viewer {
		return
	}
	index := s.panel.Index("INFO")
	s.infoLanguage = i18n.Language()
	width := float64(s.panel.Window.Gadgets[index].Rect.W) - 2*unitViewerTextInset
	switch s.infoTab {
	case unitViewerTabWeapons:
		s.infoRows = unitViewerWeaponRows(s.selected, width)
	case unitViewerTabBuild:
		s.infoRows = s.buildRows(s.selected, width)
	default:
		s.infoRows = unitViewerStatsRows(s.selected, width)
	}
	items := make([]string, len(s.infoRows))
	for i, row := range s.infoRows {
		items[i] = row.Label
	}
	s.panel.FillTextListAt(index, items, nil, unitViewerTextMetric)
}

// infoLinkAt is the build-tree entry under a logical point, using the list's
// own row origin and height.
func (s *toolsScreen) infoLinkAt(x, y float64) *content.UnitDef {
	if s.panel == nil || !s.viewer {
		return nil
	}
	index := s.panel.Index("INFO")
	if index < 0 || !s.panel.ActiveAt(index) {
		return nil
	}
	g := s.panel.Window.Gadgets[index]
	r := unitViewerRect(s.panel.Window.PlacedRect(index))
	_, _, top, ok := s.panel.ListValuesAt(index)
	if !ok || g.ItemHeight <= 0 || x < r.X || x >= r.X+r.W || y < r.Y+2 {
		return nil
	}
	row := top + int((y-r.Y-2)/float64(g.ItemHeight))
	rowY := r.Y + 2 + float64(row-top)*float64(g.ItemHeight)
	if row < 0 || row >= len(s.infoRows) || row-top >= unitViewerListRows(g) || rowY+unitViewerTextMetric > r.Y+r.H {
		return nil
	}
	return s.infoRows[row].Link
}

func (s *toolsScreen) serviceWidgets(inX, inY float64, down, pressed, released bool, dt float64) {
	p := s.panel
	if p == nil {
		return
	}
	f := ui.WidgetFrame{PointerX: int32(inX), PointerY: int32(inY), TokenMode: true, KeyNavigation: true, Tokens: s.tokens, TimerAdvanced: dt > 0}
	inside := inX >= 0 && inX < unitViewerWidth && inY >= 0 && inY < unitViewerHeight
	if pressed && inside {
		s.widgetHeld = true
	}
	if down && s.widgetHeld {
		f.HeldButtons = 1
	}
	if pressed && inside {
		f.PointerEvents = append(f.PointerEvents, input.PointerEvent{Kind: input.LeftDown, X: int32(inX), Y: int32(inY)})
	}
	if released && s.widgetHeld {
		f.PointerEvents = append(f.PointerEvents, input.PointerEvent{Kind: input.LeftUp, X: int32(inX), Y: int32(inY)})
		s.widgetHeld = false
	}
	result := p.ServiceFrame(f, ui.WidgetHooks{
		Metric: func(int) int { return unitViewerTextMetric },
		Measure: func(index int, text string) int {
			width := unitViewerTextWidth(text)
			if p.Window.Gadgets[index].Kind == gui.KindTextBox {
				width += 2 * unitViewerTextInset
			}
			return width
		},
		ArtFrames: func(int) int { return 4 },
		Change: func(index int) {
			if index == p.Index("UNITS") {
				i := p.ListAt(index).Selected()
				if i >= 0 && i < len(s.filtered) {
					s.visit(s.filtered[i].Def)
				}
			}
		},
	})
	s.tokens = s.tokens[min(len(s.tokens), result.ConsumedTokens):]
	if s.viewer && s.query != p.TextOf("SEARCH") {
		s.query = p.TextOf("SEARCH")
		s.filter()
	}
	s.searchFocus = p.EditorCaptured()
	if result.Fired {
		s.activateTool(p.Window.Gadgets[result.FiredIndex].Name)
	}
}

func (s *toolsScreen) activateTool(name string) {
	if s.shell != nil {
		s.shell.playMenuCue("SmallButton")
	}
	if s.activateRestrict(name) {
		s.refreshControls()
		return
	}
	switch name {
	case "BACK":
		s.back()
	case "VIEWER":
		s.openViewer()
	case "SPIN":
		s.spinning = !s.spinning
	case "RESET":
		s.resetView()
	case "PAUSE":
		s.animationPaused = !s.animationPaused
	case "IDLE", "FIRE", "HIT", "DEATH", "WRECK":
		s.chooseAnimation(string(unitViewerButtonAction(name, s.selected)))
	case "MOVE":
		// An aircraft's button takes off and lands within one preview, as
		// the battle's air orders do; choosing it from another action starts
		// a fresh takeoff.
		if a := s.model.anim; s.action == string(unitViewerFlying) && a != nil && a.fly != nil && !a.stopped {
			a.toggleFlight()
			s.model.refreshPose()
		} else {
			s.chooseAnimation(string(unitViewerButtonAction(name, s.selected)))
		}
	case "BUILD":
		// Build stops and restarts the same construction order.
		if a := s.model.anim; s.action == string(unitViewerBuilding) && a != nil && !a.stopped {
			a.toggleBuild()
			s.model.refreshPose()
		} else {
			s.chooseAnimation(string(unitViewerBuilding))
		}
	case "POWER":
		// On/Off records an explicit choice that later actions apply after
		// creation, and drives the live edge machine now.
		on := !s.model.anim.activated()
		s.power = -1
		if on {
			s.power = 1
		}
		s.model.power = s.power
		if a := s.model.anim; a != nil {
			a.setActivation(on)
			s.model.refreshPose()
		}
	case "SPEED":
		// The product cycle's preview speed; scripts keep 30 Hz.
		s.speed = (s.speed + 1) % len(unitViewerSpeeds)
		s.model.setSpeed(unitViewerSpeeds[s.speed])
	case "WEAPON":
		s.weapon = s.weapon%3 + 1
		s.model.setAnimation(unitViewerAction(s.action), s.weapon)
	case "SEVERITY":
		s.severity = (s.severity + 1) % len(unitViewerSeverities)
		s.model.severity = unitViewerSeverities[s.severity]
		if s.action == string(unitViewerDeath) || s.action == string(unitViewerWreck) {
			s.model.setAnimation(unitViewerAction(s.action), s.weapon)
		}
	case "HISTBACK":
		s.goBack()
	case "HISTFWD":
		s.goForward()
	case "TABSTATS", "TABWEAPONS", "TABBUILD":
		s.infoTab = unitViewerTabs[name]
		s.refreshInfo()
	}
	s.refreshControls()
}

func (s *toolsScreen) chooseAnimation(action string) {
	s.action, s.animationPaused = action, false
	s.model.severity, s.model.power = unitViewerSeverities[s.severity], s.power
	s.model.setAnimation(unitViewerAction(action), s.weapon)
}

// unitViewerActionButtons is the action row in display order. labels[0] is
// the caption; the row is sized for every caption a button can show.
var unitViewerActionButtons = []struct {
	name   string
	labels []string
}{
	{"IDLE", []string{"Idle"}},
	{"MOVE", []string{"Move", "Fly", "Land"}},
	{"FIRE", []string{"Fire"}},
	{"BUILD", []string{"Build", "Stop"}},
	{"SPEED", []string{"Speed 1x", "Speed 4x", "Speed 16x"}},
	{"HIT", []string{"Hit"}},
	{"DEATH", []string{"Death"}},
	{"WRECK", []string{"Wreck"}},
	{"POWER", []string{"On/Off"}},
}

const (
	unitViewerActionRowY  = 642
	unitViewerControlRowY = 688
	unitViewerRowLeft     = 306
	unitViewerRowWidth    = 528
)

// unitViewerButtonAction is the action a row button selects for the unit:
// the movement button flies an aircraft.
func unitViewerButtonAction(name string, def *content.UnitDef) unitViewerAction {
	switch name {
	case "MOVE":
		if def != nil && def.CanFly {
			return unitViewerFlying
		}
		return unitViewerMoving
	case "POWER", "SPEED":
		return ""
	}
	return unitViewerAction(strings.ToUpper(name[:1]) + strings.ToLower(name[1:]))
}

func (s *toolsScreen) buttonSelected(name string) bool {
	switch name {
	case "SPIN":
		return s.spinning
	case "APPLY":
		return true
	case "POWER":
		return s.model.anim.activated()
	case "SPEED":
		return false
	case "MOVE":
		return s.action == string(unitViewerMoving) || s.action == string(unitViewerFlying)
	}
	return strings.EqualFold(name, s.action)
}

func (s *toolsScreen) refreshControls() {
	if s.panel == nil || !s.viewer {
		return
	}
	spin := "off"
	if s.spinning {
		spin = "on"
	}
	s.panel.SetText("SPIN", "Rotate: "+spin)
	pause := "Pause"
	if s.animationPaused {
		pause = "Play"
	}
	s.panel.SetText("PAUSE", pause)
	s.panel.SetText("WEAPON", fmt.Sprintf("Weapon %d", s.weapon))
	s.panel.SetText("SPEED", fmt.Sprintf("Speed %dx", unitViewerSpeeds[s.speed]))
	s.panel.SetText("SEVERITY", fmt.Sprintf("Severity %d", unitViewerSeverities[s.severity]))
	s.refreshRestrictControls()
	grey := func(name string, off bool) {
		s.panel.Window.Gadgets[s.panel.Index(name)].GrayedOut = 0
		if off {
			s.panel.Window.Gadgets[s.panel.Index(name)].GrayedOut = 1
		}
	}
	grey("HISTBACK", len(s.histBack) == 0)
	grey("HISTFWD", len(s.histForward) == 0)
	def := s.selected
	grey("WEAPON", !unitViewerActionShown(def, unitViewerFiring))
	grey("SEVERITY", def == nil || def.Script == nil)
	move, build := "Move", "Build"
	if def != nil && def.CanFly {
		move = "Fly"
		if s.action == string(unitViewerFlying) && s.model.anim.airborne() {
			move = "Land"
		}
	}
	if s.action == string(unitViewerBuilding) && s.model.anim.buildEngaged() {
		build = "Stop"
	}
	s.panel.SetText("MOVE", move)
	s.panel.SetText("BUILD", build)
	// Lay the applicable actions out left to right at their caption widths,
	// tightening the padding when a full row would overflow.
	type slot struct {
		index int
		width float64
	}
	var row []slot
	total := 0.0
	for _, b := range unitViewerActionButtons {
		i := s.panel.Index(b.name)
		action := unitViewerButtonAction(b.name, def)
		shown := def != nil && def.Script != nil && unitViewerActionShown(def, action)
		available := b.name == "POWER" || unitViewerAnimationAvailable(def, action, s.weapon)
		switch b.name {
		case "POWER":
			shown = unitViewerHasPower(def)
		case "SPEED":
			// The product cycle's speed belongs to a factory's Build.
			shown = def != nil && def.Script != nil && unitViewerFactory(def) && unitViewerActionShown(def, unitViewerBuilding)
			available = unitViewerAnimationAvailable(def, unitViewerBuilding, s.weapon)
		}
		s.panel.SetActiveAt(i, shown)
		if !shown {
			continue
		}
		grey(b.name, !available)
		width := 0.0
		for _, label := range b.labels {
			width = max(width, unitViewerMeasure(label, unitViewerCaptionStyle(13), true))
		}
		row = append(row, slot{i, width})
		total += width
	}
	if len(row) == 0 {
		return
	}
	gap, widest := 6.0, 0.0
	for _, r := range row {
		widest = max(widest, r.width)
	}
	// Equal buttons when the row has room for them; otherwise each takes its
	// caption width with the padding that fits.
	uniform := (widest+18)*float64(len(row))+gap*float64(len(row)-1) <= unitViewerRowWidth
	pad := min(18, (unitViewerRowWidth-total-gap*float64(len(row)-1))/float64(len(row)))
	x := float64(unitViewerRowLeft)
	for _, r := range row {
		w := math.Round(r.width + max(4, pad))
		if uniform {
			w = math.Round(widest + 18)
		}
		g := &s.panel.Window.Gadgets[r.index]
		g.Rect.X, g.Rect.W = int32(x), int32(w)
		x += w + gap
	}
}

func (s *toolsScreen) initializeRetail(g *gameShell) {
	assets := g.assets
	if assets == nil {
		assets = loadMenuAssets(s.cs)
	}
	s.shell = &gameShell{cs: s.cs, assets: assets, font: assets.font, frontend: ui.NewFrontend(modeMenuSingle)}
	s.buildPanel()
}
