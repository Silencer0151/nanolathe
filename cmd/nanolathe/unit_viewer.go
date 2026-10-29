package main

// Tools and the unit viewer are host presentation, independent of gameplay
// mode. They borrow immutable content, never a battle or its random streams
// (DESIGN_DEVELOPER_TOOLS §7).

import (
	"math"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

var toolsScreenInst *toolsScreen

const unitViewerWidth, unitViewerHeight = 1200, 750

// frontendScreens keeps the host screens behind the existing full-window
// input boundary, including its release barrier on return to the retail menu.
type frontendScreens struct {
	settings  *nlScreen
	tools     *toolsScreen
	generator *mapgenScreen
}

func (s *frontendScreens) Active() bool {
	return s.tools.Active() || s.generator.Active() || s.settings.Active()
}
func (s *frontendScreens) Update() {
	switch {
	case s.tools.Active():
		s.tools.Update()
	case s.generator.Active():
		s.generator.Update()
	case s.settings.Active():
		s.settings.Update()
	}
}
func (s *frontendScreens) Draw(dst *ebiten.Image) {
	switch {
	case s.tools.Active():
		s.tools.Draw(dst)
	case s.generator.Active():
		s.generator.Draw(dst)
	case s.settings.Active():
		s.settings.Draw(dst)
	}
}
func (s *frontendScreens) OwnsPointer() bool {
	return !s.tools.Active() && !s.generator.Active() && s.settings.OwnsPointer()
}

// The unfinished viewer is an unadvertised main-menu preview. Keep its host
// shortcut out of ordinary widget quickkeys and the public keymap
// (DESIGN_DEVELOPER_TOOLS §7).
func (g *gameShell) openUnitViewerPreview(in *input.State) bool {
	if g == nil || g.frontend == nil || toolsScreenInst == nil || toolsScreenInst.Active() ||
		g.frontend.Mode != modeMenuMain || g.frontend.Panels.Len() != 1 || g.frontend.Panels.Modal() != nil ||
		in == nil || in.Kbd == nil || !in.Kbd.KeyHeld(input.KeyCtrl) || !in.Kbd.KeyDown(input.KeyU) ||
		in.Kbd.HasShift() || in.Kbd.KeyHeld(input.KeyAlt) {
		return false
	}
	p := g.activePanel()
	if p == nil || p.Window == nil || p.EditorCaptured() {
		return false
	}
	p.ResetPress()
	p.CancelScrollDrag()
	in.DiscardTokens(in.PendingTokens())
	toolsScreenInst.show(g)
	toolsScreenInst.openViewer()
	return true
}

type unitViewerLoad struct {
	entries  []unitViewerEntry
	tree     unitViewerTree
	features map[string]*content.FeatureDef
	names    map[string]unitViewerRestrictName
	keys     []string
	err      error
}

type toolsScreen struct {
	open, closing, viewer  bool
	cs                     *contentSet
	contentName            string
	shell                  *gameShell
	panel                  *ui.Panel
	fonts                  screenkit.Fonts
	art                    *nlArt
	uiClock                float64
	tokens                 []input.Token
	canvas                 screenkit.Rect
	animationPaused        bool
	widgetHeld             bool
	action                 string
	weapon                 int
	severity               int  // index into unitViewerSeverities
	speed                  int  // index into unitViewerSpeeds; kept across units
	power                  int8 // explicit On/Off choice: 0 none, +1 on, -1 off
	features               map[string]*content.FeatureDef
	last                   time.Time
	loading                chan unitViewerLoad
	loadErr                error
	entries, filtered      []unitViewerEntry
	selected               *content.UnitDef
	query                  string
	searchFocus, selectAll bool
	top, visible           int
	listRect, viewRect     screenkit.Rect
	infoRect               screenkit.Rect
	scale                  float64
	yaw, pitch, zoom       float64
	spinning, dragging     bool
	dragX, dragY           float64
	model                  unitViewerModel
	tree                   unitViewerTree
	histBack, histForward  []*content.UnitDef
	workCache              map[unitViewerWorkKey]unitViewerWork // lookup only
	pics                   unitViewerPictures
	picsPending            int // pictures the last Draw requested that were still decoding
	infoTab                int
	infoRows               []unitViewerRow
	infoLanguage           string // language used to lay out the retained rows
	altHeld, shiftHeld     bool
	pointerX, pointerY     float64 // logical pointer, for link hover
	linkPress              *content.UnitDef
	restrict               unitViewerRestrict // the restriction editor (unit_viewer_restrict.go)
	// field is the Death and Wreck field (unit_viewer_field.go). It stages
	// its battles with the shell's options and gameplay mode and draws them
	// with the player's presentation choices; it writes no settings.
	field         unitViewerField
	opts          Options
	fieldGameplay gameplay.Mode
	fieldRender   nlRender
}

func (s *toolsScreen) Active() bool { return s != nil && s.open }

func (s *toolsScreen) show(g *gameShell) {
	s.release()
	s.open, s.closing, s.viewer = true, false, false
	s.cs = g.cs
	s.opts, s.fieldGameplay = g.opts, g.gameplay.Normalize()
	s.fieldRender = unitViewerFieldRender(g.presentation, g.display)
	s.model.walkEnabled, s.model.walkSmooth = !s.fieldRender.classic, !s.fieldRender.classic
	s.contentName = "Total Annihilation"
	if g.cs != nil && g.cs.mod != nil {
		s.contentName = g.cs.mod.Name
	} else if g.cs != nil && g.cs.manualRoots {
		s.contentName = "Custom content"
	}
	s.last = time.Time{}
	s.action, s.weapon, s.animationPaused = "Idle", 1, false
	s.severity, s.power, s.speed = 0, 0, 0
	// Ctrl+U edits the running content's saved set, names it lacks included,
	// so Apply keeps them in the file (DESIGN_MODS_MUTATORS §15.9).
	s.restrict = unitViewerRestrict{route: unitViewerRestrictMenu, host: g, draft: g.restrictions.readable, saved: g.restrictions.readable}
	s.initializeRetail(g)
	s.query, s.top = "", 0
	s.searchFocus, s.selectAll, s.spinning = true, false, true
	s.resetView()
	g.playMenuCue("BigButton")
}

func (s *toolsScreen) openViewer() {
	s.viewer, s.searchFocus, s.dragging = true, true, false
	s.buildPanel()
	if s.entries != nil || s.loading != nil || s.loadErr != nil {
		return
	}
	// The content cannot change until this screen closes. Closing joins the
	// worker before the host may unmount it; only plain catalog data crosses.
	ch, cs := make(chan unitViewerLoad, 1), s.cs
	s.loading = ch
	go func() {
		cat, err := cs.nlPreviewCatalog()
		entries := unitViewerEntries(cat)
		var features map[string]*content.FeatureDef
		if cat != nil {
			features = cat.Features
		}
		names, keys := unitViewerRestrictNames(cat, entries)
		ch <- unitViewerLoad{entries: entries, tree: unitViewerBuildTree(cat, entries), features: features, names: names, keys: keys, err: err}
	}()
}

func (s *toolsScreen) pollLoad() {
	if s.loading == nil {
		return
	}
	select {
	case result := <-s.loading:
		s.loading = nil
		s.entries, s.tree, s.loadErr = result.entries, result.tree, result.err
		s.features = result.features
		s.model.features = s.features
		s.restrict.names, s.restrict.keys = result.names, result.keys
		s.filter()
	default:
	}
}

func (s *toolsScreen) release() {
	if s == nil {
		return
	}
	if s.loading != nil {
		<-s.loading
		s.loading = nil
	}
	// The field's staging reads the content too; join it before the host
	// may unmount it.
	s.field.release(s.cs)
	s.model.release()
	// The picture worker reads the content's archives; join it before the
	// host may unmount them.
	s.pics.release()
	s.entries, s.filtered, s.selected, s.cs, s.features = nil, nil, nil, nil, nil
	s.tree, s.histBack, s.histForward, s.workCache = unitViewerTree{}, nil, nil, nil
	s.infoRows, s.linkPress = nil, nil
	s.loadErr, s.open, s.closing = nil, false, false
	s.shell, s.panel, s.art = nil, nil, nil
	s.fonts, s.uiClock = screenkit.Fonts{}, 0
	s.tokens = nil
	s.restrict = unitViewerRestrict{}
}

func (s *toolsScreen) back() {
	s.dragging = false
	s.closing = true
	s.leaveRestrictions()
}

func (s *toolsScreen) resetView() {
	s.yaw, s.pitch, s.zoom = 40960, 8192, 1
	s.dragging = false
}

func (s *toolsScreen) filter() {
	defer s.refreshLists()
	s.filtered = filterUnitViewerEntries(s.entries, s.query)
	if s.restrict.only {
		s.filtered = s.restrict.keep(s.filtered)
	}
	s.top = 0
	for _, e := range s.filtered {
		if e.Def == s.selected {
			s.revealSelection()
			return
		}
	}
	// The search's first match replaces an excluded selection, which Back
	// can still return to; no match clears the preview.
	if len(s.filtered) > 0 {
		s.visit(s.filtered[0].Def)
	} else {
		s.selectUnit(nil)
	}
}

func (s *toolsScreen) selectUnit(def *content.UnitDef) {
	if s.selected == def {
		return
	}
	s.selected = def
	s.model.selectUnit()
	s.model.features = s.features
	s.model.builds, s.model.hidden = s.tree.builds[def], s.tree.hidden
	s.model.speed = unitViewerSpeeds[s.speed]
	s.action, s.weapon, s.animationPaused = "Idle", 1, false
	s.severity, s.power = 0, 0
	s.refreshInfo()
	s.refreshControls()
	s.resetView()
}

func (s *toolsScreen) selectionIndex() int {
	for i, e := range s.filtered {
		if e.Def == s.selected {
			return i
		}
	}
	return -1
}

func (s *toolsScreen) revealSelection() {
	i := s.selectionIndex()
	if i < 0 {
		return
	}
	if s.panel != nil {
		s.panel.SetListSelection("UNITS", i, max(1, s.visible))
	}
	n := max(1, s.visible)
	if i < s.top {
		s.top = i
	} else if i >= s.top+n {
		s.top = i - n + 1
	}
}

func (s *toolsScreen) moveSelection(delta int) {
	if len(s.filtered) == 0 {
		return
	}
	i := max(0, min(len(s.filtered)-1, s.selectionIndex()+delta))
	s.visit(s.filtered[i].Def)
	s.revealSelection()
}

func (s *toolsScreen) Update() {
	now := time.Now()
	dt := 0.0
	if !s.last.IsZero() {
		dt = max(0, min(0.1, now.Sub(s.last).Seconds()))
	}
	s.last = now
	s.pollLoad()
	s.field.poll()
	in := screenkit.ReadInput()
	shortcut := ebiten.IsKeyPressed(ebiten.KeyControl) || ebiten.IsKeyPressed(ebiten.KeyMeta)
	s.altHeld = ebiten.IsKeyPressed(ebiten.KeyAlt)
	s.shiftHeld = ebiten.IsKeyPressed(ebiten.KeyShift)
	s.updateInput(in, ebiten.AppendInputChars(nil), shortcut, dt)
}

func (s *toolsScreen) updateInput(in screenkit.Input, typed []rune, shortcut bool, dt float64) {
	if s.closing {
		if !in.Held && s.loading == nil {
			s.release()
		}
		return
	}
	if in.KeyPressed(ebiten.KeyEscape) {
		s.back()
		return
	}
	s.uiClock += max(0, dt)
	if s.scale > 0 {
		in.X = (in.X - s.canvas.X) / s.scale
		in.Y = (in.Y - s.canvas.Y) / s.scale
	}
	p := s.panel
	for _, pair := range []struct {
		from ebiten.Key
		to   input.Key
	}{{ebiten.KeyEnter, input.KeyEnter}, {ebiten.KeyTab, input.KeyTab}} {
		if in.KeyPressed(pair.from) {
			s.tokens = append(s.tokens, input.Token{Kind: input.TokenEdit, Key: pair.to})
		}
	}
	if !s.viewer {
		s.serviceWidgets(in.X, in.Y, in.Down, in.Pressed, in.Released, dt)
		return
	}
	if shortcut && in.KeyPressed(ebiten.KeyF) && p != nil {
		p.FocusEditor(p.Index("SEARCH"))
		s.selectAll = true
	}
	if p != nil {
		s.searchFocus = p.EditorCaptured()
	}
	if s.searchFocus && shortcut && in.KeyPressed(ebiten.KeyA) {
		s.selectAll = true
	}
	s.pointerX, s.pointerY = in.X, in.Y
	// History keys never reach the editor or the orbit: Alt+Left/Right always,
	// and Backspace while the search is not being edited.
	historyBack := (s.altHeld && in.KeyPressed(ebiten.KeyArrowLeft)) || (!s.searchFocus && in.KeyPressed(ebiten.KeyBackspace))
	historyForward := s.altHeld && in.KeyPressed(ebiten.KeyArrowRight)
	if in.Pressed {
		s.linkPress = s.infoLinkAt(in.X, in.Y)
	}
	if in.Pressed && s.viewRect.Contains(in.X, in.Y) {
		s.dragging, s.searchFocus = true, false
		if p != nil {
			p.SetFocus(-1)
		}
		s.dragX, s.dragY = in.X, in.Y
	}
	if s.dragging {
		if in.Down {
			s.yaw = math.Mod(s.yaw+(in.X-s.dragX)*90+65536, 65536)
			s.pitch = math.Mod(s.pitch+(in.Y-s.dragY)*90+65536, 65536)
			s.dragX, s.dragY = in.X, in.Y
		} else {
			s.dragging = false
		}
	}
	if in.WheelY != 0 {
		switch {
		case s.viewRect.Contains(in.X, in.Y):
			s.changeZoom(in.WheelY)
		case unitViewerRestrictStepper.Contains(in.X, in.Y):
			s.wheelRestrict(in.WheelY)
		case p != nil && s.listRect.Contains(in.X, in.Y):
			p.ScrollTextListAt(p.Index("UNITS"), float32(-in.WheelY*3))
		case p != nil && s.infoRect.Contains(in.X, in.Y):
			p.ScrollTextListAt(p.Index("INFO"), float32(-in.WheelY*3))
		}
	}
	if in.KeyPressed(ebiten.KeyArrowDown) {
		s.moveSelection(1)
	}
	if in.KeyPressed(ebiten.KeyArrowUp) {
		s.moveSelection(-1)
	}
	if in.KeyPressed(ebiten.KeyPageDown) {
		s.moveSelection(max(1, s.visible))
	}
	if in.KeyPressed(ebiten.KeyPageUp) {
		s.moveSelection(-max(1, s.visible))
	}
	if s.searchFocus && p != nil && s.selectAll && (len(typed) > 0 || in.KeyPressed(ebiten.KeyBackspace) || in.KeyPressed(ebiten.KeyDelete)) {
		p.SetText("SEARCH", "")
		p.FocusEditor(p.Index("SEARCH"))
		s.selectAll = false
	}
	if !shortcut {
		for _, r := range typed {
			if !s.searchFocus && r == ' ' {
				continue // Space belongs to the viewer's rotate shortcut.
			}
			s.tokens = append(s.tokens, input.Token{Kind: input.TokenText, Rune: r})
		}
	}
	for _, pair := range []struct {
		from ebiten.Key
		to   input.Key
	}{{ebiten.KeyBackspace, input.KeyBackspace}, {ebiten.KeyDelete, input.KeyDelete}, {ebiten.KeyHome, input.KeyHome}, {ebiten.KeyEnd, input.KeyEnd}, {ebiten.KeyArrowLeft, input.KeyLeft}, {ebiten.KeyArrowRight, input.KeyRight}} {
		if (!s.searchFocus || s.altHeld) && (pair.from == ebiten.KeyArrowLeft || pair.from == ebiten.KeyArrowRight) {
			continue // Orbit and history shortcuts must not also move native widget focus.
		}
		if !s.searchFocus && pair.from == ebiten.KeyBackspace {
			continue // Backspace outside the search is History back.
		}
		if in.KeyPressed(pair.from) {
			s.tokens = append(s.tokens, input.Token{Kind: input.TokenEdit, Key: pair.to})
		}
	}
	if !s.searchFocus {
		if in.KeyPressed(ebiten.KeySpace) {
			s.spinning = !s.spinning
		}
		if in.KeyPressed(ebiten.KeyR) {
			s.resetView()
		}
		if !s.altHeld && in.KeyPressed(ebiten.KeyArrowLeft) {
			s.yaw -= 2048
		}
		if !s.altHeld && in.KeyPressed(ebiten.KeyArrowRight) {
			s.yaw += 2048
		}
		if in.KeyPressed(ebiten.KeyEqual) {
			s.changeZoom(1)
		}
		if in.KeyPressed(ebiten.KeyMinus) {
			s.changeZoom(-1)
		}
	}
	wasViewer := s.viewer
	s.serviceWidgets(in.X, in.Y, in.Down, in.Pressed, in.Released, dt)
	if wasViewer != s.viewer || s.closing {
		return
	}
	if in.Released {
		// A build-tree link follows a press and release on the same entry,
		// measured in the same rows the column paints.
		if link := s.infoLinkAt(in.X, in.Y); link != nil && link == s.linkPress {
			s.navigate(link)
			s.refreshControls()
		}
		s.linkPress = nil
	}
	switch {
	case historyBack:
		s.goBack()
	case historyForward:
		s.goForward()
	}
	if s.spinning && !s.dragging {
		s.yaw += dt * 65536 / 18
	}
	s.yaw = math.Mod(s.yaw+65536, 65536)
	if !s.animationPaused {
		s.model.updateAnimation(dt)
	}
	// The field steps in Draw, which renders it; Pause holds it there too.
	s.syncField()
	if !s.animationPaused {
		s.field.dt += max(0, dt)
	}
}

func (s *toolsScreen) changeZoom(steps float64) {
	s.zoom = max(0.35, min(3, s.zoom*math.Pow(1.12, max(-20, min(20, steps)))))
}

func unitViewerName(def *content.UnitDef) string {
	if name := strings.TrimSpace(def.Name); name != "" {
		return name
	}
	return def.UnitName
}
