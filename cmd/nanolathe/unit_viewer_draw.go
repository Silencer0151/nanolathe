package main

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// Draw paints at device resolution using the settings screen's type and stock
// metal art. The panel remains in logical coordinates for input and scrolling.
func (s *toolsScreen) Draw(dst *ebiten.Image) {
	s.layout(float64(dst.Bounds().Dx()), float64(dst.Bounds().Dy()))
	s.preparePaint()
	if s.viewer && s.cs != nil {
		s.pics.bind(s.cs.fs)
	}
	s.drawBackground(dst)
	if s.panel == nil {
		return
	}
	s.refreshControls()
	s.drawHeader(dst)
	if s.viewer {
		s.drawViewer(dst)
	} else {
		s.label(dst, "Your loaded content, up close.", screenkit.Rect{X: 220, Y: 256, W: 760, H: 58}, screenkit.Style{Size: 38, Top: nlGoldTop, Bottom: nlGoldBottom, Align: 1}, true)
		s.label(dst, "Explore units, compare statistics and play their animations.", screenkit.Rect{X: 310, Y: 336, W: 580, H: 28}, screenkit.Style{Size: 14, Top: nlBody, Align: 1}, false)
	}
	for i, g := range s.panel.Window.Gadgets {
		if i == 0 || !s.panel.ActiveAt(i) {
			continue
		}
		r := unitViewerRect(s.panel.Window.PlacedRect(i))
		switch g.Kind {
		case gui.KindButton:
			switch {
			case strings.HasPrefix(g.Name, "TAB"):
				s.drawTab(dst, i, g, r)
			case g.Name == "RSWITCH":
				s.drawRestrictSwitch(dst, i, g, r)
			case g.Name == "RESTRONLY":
				s.drawRestrictOnly(dst, i, r)
			default:
				s.drawButton(dst, i, g, r)
			}
		case gui.KindTextBox:
			s.drawSearch(dst, i, r)
		case gui.KindListBox:
			if g.Name == "UNITS" {
				s.drawLibrary(dst, i, g, r)
			} else {
				s.drawInfo(dst, i, g, r)
			}
		case gui.KindScrollBar:
			s.drawScrollbar(dst, i, r)
		}
	}
	// Decoding continues on the loader's worker; nothing here waits for it.
	s.picsPending = s.pics.endFrame()
}

func (s *toolsScreen) drawBackground(dst *ebiten.Image) {
	full := screenkit.Rect{W: float64(dst.Bounds().Dx()), H: float64(dst.Bounds().Dy())}
	dst.Fill(color.RGBA{12, 15, 12, 255})
	if s.art.texture != nil {
		nlTileArt(dst, s.art.texture, full, max(0.5, s.scale))
	}
	screenkit.Fill(dst, full, color.RGBA{4, 9, 6, 214})
	screenkit.VGradient(dst, full, color.RGBA{0, 0, 0, 70}, color.RGBA{0, 0, 0, 170})
	if s.viewer {
		// Quiet side surfaces leave the selected model as the hero. There is
		// one original riveted frame around the stage, no nested list bevels.
		for _, r := range []screenkit.Rect{{X: 20, Y: 94, W: 258, H: 622}, {X: 850, Y: 94, W: 330, H: 622}} {
			screenkit.Fill(dst, s.deviceRect(r), color.RGBA{5, 9, 6, 100})
		}
	}
}

func (s *toolsScreen) drawHeader(dst *ebiten.Image) {
	if wm := s.art.wordmark; wm != nil {
		b := wm.Bounds()
		screenkit.Image(dst, wm, s.deviceRect(screenkit.Rect{X: 32, Y: 22, W: float64(b.Dx()) * 34 / float64(b.Dy()), H: 34}), 1, true)
	} else {
		s.label(dst, "NANOLATHE", screenkit.Rect{X: 32, Y: 24, W: 244, H: 40}, screenkit.Style{Size: 30, Tracking: 0.06, Top: nlGoldTop, Bottom: nlGoldBottom}, true)
	}
	// Opened from the Nanolathe screen's card, the viewer is that screen's
	// restriction editor and says so.
	title := "UNIT VIEWER"
	if s.restrict.route == unitViewerRestrictCard {
		title = "UNIT RESTRICTIONS"
	}
	ts := screenkit.Style{Size: 20, Tracking: 0.1, Top: nlCream}
	s.label(dst, title, screenkit.Rect{X: 306, Y: 27, W: 480, H: 29}, ts, true)
	s.label(dst, s.contentName, screenkit.Rect{X: 306, Y: 58, W: 590, H: 20}, screenkit.Style{Size: 12, Top: nlDim}, false)
	screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: 306, Y: 52, W: math.Round(unitViewerMeasure(title, ts, true)) - 4, H: 2}), nlGreenText)
	screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: 32, Y: 84, W: 1136, H: 1}), color.RGBA{123, 119, 86, 100})
}

func (s *toolsScreen) drawViewer(dst *ebiten.Image) {
	kicker := screenkit.Style{Size: 14, Tracking: 0.12, Top: nlKicker, Upper: true}
	s.label(dst, "Unit library", screenkit.Rect{X: 32, Y: 108, W: 234, H: 24}, kicker, true)
	s.label(dst, fmt.Sprintf("%d / %d units", len(s.filtered), len(s.entries)), screenkit.Rect{X: 34, Y: 191, W: 108, H: 16}, screenkit.Style{Size: 10, Top: nlDim}, false)
	s.label(dst, "Unit data", screenkit.Rect{X: 862, Y: 108, W: 210, H: 24}, kicker, true)
	screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: 862, Y: 175, W: 306, H: 1}), color.RGBA{123, 119, 86, 100})
	if s.selected != nil {
		s.label(dst, unitViewerName(s.selected), screenkit.Rect{X: 306, Y: 105, W: 528, H: 51}, screenkit.Style{Size: 40, Top: nlGoldTop, Bottom: nlGoldBottom, Shadow: 0.06, Upper: true}, true)
		s.label(dst, strings.ToUpper(s.selected.UnitName), screenkit.Rect{X: 308, Y: 157, W: 524, H: 19}, screenkit.Style{Size: 11, Tracking: 0.08, Top: nlGreenText}, true)
	}
	stage := s.deviceRect(s.viewRect)
	screenkit.Fill(dst, stage, color.RGBA{10, 14, 12, 255})
	// Death and Wreck play in the field, a small real battle drawn at the
	// stage's device size (unit_viewer_field.go); Wreck then hands the stage
	// back to the turntable's corpse, possibly within this very frame.
	if s.field.onStage() {
		if img, fade, alpha := s.fieldFrame(); img != nil {
			crisp := screenkit.Crisp(stage.W / float64(img.Bounds().Dx()))
			screenkit.Image(dst, img, stage, 1, crisp)
			if fade != nil {
				screenkit.Image(dst, fade, stage, alpha, crisp)
			}
		}
	}
	if !s.field.onStage() && s.selected != nil && s.loadErr == nil {
		k := min(1, 2048/max(stage.W, stage.H))
		if img := s.model.draw(s.cs, s.selected, s.yaw, s.pitch, s.zoom, max(1, int(stage.W*k)), max(1, int(stage.H*k))); img != nil {
			screenkit.Image(dst, img, stage, 1, false)
			if clipped := s.clip(dst, s.viewRect); clipped != nil {
				s.model.drawSpray(clipped, stage)
			}
		}
	}
	if s.art.frame != nil {
		screenkit.NineSlice(dst, s.art.frame, s.deviceRect(s.viewRect.Inset(-6)), nlFrameEdge, 0.5*s.scale, false)
	} else {
		screenkit.Outline(dst, stage, max(1, s.scale), color.RGBA{75, 79, 61, 255})
	}
	status := s.action
	if s.model.poseNote != "" {
		status = s.model.poseNote
	}
	if s.model.anim.wreckFeature() != nil && s.model.wreck.err != nil && s.model.wreck.feature == s.model.anim.wreckFeature() {
		status = "Wreck / model unavailable: " + s.model.wreck.err.Error()
	}
	fieldStatus, inField := s.fieldStatus()
	if inField {
		status = fieldStatus
	} else if a := s.model.anim; a != nil && unitViewerHasPower(s.selected) && !a.invalid && a.action != unitViewerWreck {
		// The Wreck view draws the corpse, which has no activation state.
		if a.activated() {
			status += " / on"
		} else {
			status += " / off"
		}
	}
	if s.animationPaused {
		status += " (paused)"
	}
	s.label(dst, status, screenkit.Rect{X: 312, Y: 611, W: 448, H: 24}, screenkit.Style{Size: 12, Top: nlDim}, false)
	if !inField {
		// The field's battle camera holds its own zoom.
		s.label(dst, fmt.Sprintf("%.0f%%", 100*s.zoom), screenkit.Rect{X: 766, Y: 611, W: 68, H: 24}, screenkit.Style{Size: 12, Top: nlKicker, Align: 2}, false)
	}
	message := ""
	switch {
	case s.loadErr != nil:
		message = "Catalog unavailable\r" + s.loadErr.Error()
	case s.loading != nil:
		message = "Loading units..."
	case s.selected == nil && s.restrict.only:
		message = "No restricted units match.\rTurn off Restricted only to list every unit."
	case s.selected == nil:
		message = "No matching units.\rTry another name or unit ID."
	case s.field.phase == unitViewerFieldStaging:
		message = "Staging..."
	case s.field.phase == unitViewerFieldFailed:
		message = "No field for this death\r" + s.field.err
	case !inField && s.model.err != nil:
		message = "Model unavailable\r" + s.model.err.Error()
	}
	if message != "" {
		// A carriage return starts a new line; each line wraps on its own.
		var lines []string
		for _, part := range strings.Split(message, "\r") {
			lines = append(lines, retailWrapLines(part, unitViewerTextWidth, 468)...)
		}
		for i, line := range lines {
			if i >= 10 {
				break
			}
			s.label(dst, line, screenkit.Rect{X: 336, Y: 300 + float64(i)*26, W: 468, H: 25}, screenkit.Style{Size: unitViewerBodySize, Top: nlBody, Align: 1}, false)
		}
	}
	s.drawRestrictBlock(dst)
	// The library's footer counts the restriction draft (§15.9).
	s.label(dst, s.restrict.footer(), screenkit.Rect{X: 32, Y: 732, W: 234, H: 18}, screenkit.Style{Size: 11, Top: nlKicker}, false)
	s.label(dst, "Arrows select  /  Drag to rotate  /  Wheel to zoom  /  Alt+Left or Backspace: back", screenkit.Rect{X: 306, Y: 734, W: 528, H: 16}, screenkit.Style{Size: 10, Top: nlDim}, false)
	s.label(dst, "Base values / before mutators", screenkit.Rect{X: 862, Y: 734, W: 306, H: 16}, screenkit.Style{Size: 10, Top: nlDim}, false)
}

func (s *toolsScreen) drawButton(dst *ebiten.Image, index int, g gui.Gadget, r screenkit.Rect) {
	p := s.panel
	device := s.deviceRect(r)
	disabled := g.GrayedOut != 0
	down := p.DownAt(index) != 0
	if !s.art.drawButton(dst, device, down, disabled) {
		screenkit.Fill(dst, device, color.RGBA{55, 59, 51, 255})
		screenkit.Bevel(dst, device, max(1, 2*s.scale), color.RGBA{139, 141, 121, 255}, color.RGBA{19, 24, 17, 255}, down)
	}
	if p.Hovered() == index && !disabled {
		screenkit.Fill(dst, s.deviceRect(r.Inset(3)), color.RGBA{255, 255, 255, 8})
	}
	if p.Focused() == index {
		screenkit.Outline(dst, device, max(1, s.scale), nlKicker)
	}
	if g.Attribs&0x1800 != 0 {
		// These are the builder's associated arrow controls, serviced by the
		// same panel as the track and thumb.
		x, y := device.X+device.W/2, device.Y+device.H/2
		dy := device.H * 0.18
		if g.Attribs&0x1000 != 0 {
			dy = -dy
		}
		screenkit.Poly(dst, []float64{x, y + dy, x - device.W*0.2, y - dy, x + device.W*0.2, y - dy}, nlBody)
		return
	}
	if g.Name == "RDOWN" || g.Name == "RUP" {
		s.drawStepGlyph(dst, r, g.Name == "RUP", disabled)
		return
	}
	selected := g.Name == "SPIN" && s.spinning || s.selected != nil && !disabled && s.buttonSelected(g.Name)
	if selected {
		screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: r.X + 7, Y: r.Y + r.H - 5, W: r.W - 14, H: 2}), nlGreenText)
	}
	c := nlCream
	if disabled {
		c = color.RGBA{118, 121, 101, 255}
	}
	// Compact controls shrink their caption a little, as long library names
	// do, rather than clip it.
	st := unitViewerCaptionStyle(14)
	st.Top = c
	for st.Size > 12 && unitViewerMeasure(p.TextAt(index), st, true) > r.W-12 {
		st.Size -= 0.5
	}
	caption := screenkit.Rect{X: r.X + 6, Y: r.Y + (r.H-st.Size)/2, W: r.W - 12, H: st.Size + 5}
	// The settings captions keep a dark outline over the original metal.
	outline := st
	outline.Top = color.RGBA{6, 9, 6, 240}
	for _, d := range [4][2]float64{{-0.7, 0}, {0.7, 0}, {0, -0.7}, {0, 0.7}} {
		run := caption
		run.X, run.Y = run.X+d[0], run.Y+d[1]
		s.label(dst, p.TextAt(index), run, outline, true)
	}
	s.label(dst, p.TextAt(index), caption, st, true)
}

// unitViewerCaptionStyle is the settings screen's button caption.
func unitViewerCaptionStyle(size float64) screenkit.Style {
	return screenkit.Style{Size: size, Tracking: 0.06, Top: nlCream, Upper: true, Align: 1}
}

func (s *toolsScreen) drawSearch(dst *ebiten.Image, index int, r screenkit.Rect) {
	focused := s.panel.EditorCaptured() && s.panel.EditorIndex() == index
	screenkit.Fill(dst, s.deviceRect(r), color.RGBA{5, 10, 6, 230})
	edge := color.RGBA{75, 82, 65, 255}
	if focused {
		edge = nlKicker
	}
	screenkit.Outline(dst, s.deviceRect(r), max(1, s.scale), edge)
	text := s.panel.TextAt(index)
	tr := screenkit.Rect{X: r.X + unitViewerTextInset, Y: r.Y + (r.H-unitViewerBodySize)/2 - 1, W: r.W - 2*unitViewerTextInset, H: unitViewerTextMetric}
	if text == "" {
		s.label(dst, "Search name or ID", tr, screenkit.Style{Size: unitViewerBodySize, Top: alphaC(nlDim, 0.7)}, false)
	}
	if focused && s.selectAll && text != "" {
		screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: tr.X, Y: tr.Y - 2, W: math.Min(tr.W, float64(unitViewerTextWidth(text))), H: unitViewerTextMetric + 2}), color.RGBA{64, 109, 57, 130})
	}
	// The query is the player's own typing: set it as typed (DESIGN_I18N).
	s.label(dst, text, tr, screenkit.Style{Size: unitViewerBodySize, Top: nlBody, Verbatim: true}, false)
	if focused && int(s.uiClock*2)%2 == 0 {
		caret := min(len(text), max(0, s.panel.EditorCaret()))
		x := tr.X + unitViewerMeasure(text[:caret], screenkit.Style{Size: unitViewerBodySize}, false)
		screenkit.Fill(dst, s.deviceRect(screenkit.Rect{X: min(x, tr.X+tr.W-1), Y: tr.Y - 1, W: 1.2, H: unitViewerBodySize + 4}), nlGreenText)
	}
}

func (s *toolsScreen) drawLibrary(dst *ebiten.Image, index int, g gui.Gadget, r screenkit.Rect) {
	rows, selected, top, ok := s.panel.ListValuesAt(index)
	if !ok {
		return
	}
	target := s.clip(dst, r)
	if target == nil {
		return
	}
	for i := top; i < len(rows) && i < len(s.filtered) && i-top < unitViewerListRows(g); i++ {
		y := r.Y + 2 + float64(i-top)*float64(g.ItemHeight)
		row := screenkit.Rect{X: r.X, Y: y, W: r.W, H: float64(g.ItemHeight) - 4}
		name, id, _ := strings.Cut(rows[i], "\r")
		def := s.filtered[i].Def
		// Every record carrying a name shows that name's restriction; a
		// removed name's row dims (DESIGN_MODS_MUTATORS §15.9).
		state := s.restrict.rowState(unitViewerRestrictKey(def))
		c, picAlpha := nlBody, 1.0
		if state.removed {
			c, picAlpha = color.RGBA{128, 122, 100, 255}, 0.35
		}
		if i == selected {
			screenkit.Fill(target, s.deviceRect(row), color.RGBA{34, 63, 30, 185})
			screenkit.Fill(target, s.deviceRect(screenkit.Rect{X: row.X, Y: row.Y, W: 3, H: row.H}), nlGreenText)
			c = nlCream
			if state.removed {
				c = color.RGBA{190, 180, 140, 255}
			}
		}
		s.drawPictureAlpha(target, def, screenkit.Rect{X: row.X + 8, Y: y + 2, W: 48, H: 48}, picAlpha)
		// Long names shrink a little, then take two lines above the ID.
		text := screenkit.Rect{X: row.X + 64, Y: y + 8, W: row.W - 70, H: 21}
		st := screenkit.Style{Size: 15, Top: c, Upper: true}
		for st.Size > 13 && unitViewerMeasure(name, st, true) > text.W {
			st.Size -= 0.5
		}
		idY := y + 30
		lines := []string{name}
		if unitViewerMeasure(name, st, true) > text.W {
			measure := func(s string) int { return int(math.Ceil(unitViewerMeasure(s, st, true))) }
			if wrapped := retailWrapLines(name, measure, int(text.W)); len(wrapped) > 1 {
				lines, text.Y, idY = []string{wrapped[0], strings.Join(wrapped[1:], " ")}, y+3, y+37
			}
		}
		for j, line := range lines {
			s.label(target, line, screenkit.Rect{X: text.X, Y: text.Y + float64(j)*16, W: text.W, H: 18}, st, true)
		}
		idW := text.W
		if used := s.drawRestrictRowState(target, state, text.X+text.W, idY); used > 0 {
			idW -= used + 6
		}
		// An ID beside a state shrinks a little before it would clip.
		ids := screenkit.Style{Size: 10, Top: nlDim}
		for ids.Size > 8.5 && unitViewerMeasure(id, ids, false) > idW {
			ids.Size -= 0.5
		}
		s.label(target, id, screenkit.Rect{X: text.X, Y: idY + (10-ids.Size)/2, W: idW, H: 14}, ids, false)
	}
	if s.panel.Focused() == index {
		screenkit.Outline(target, s.deviceRect(r), max(1, s.scale), color.RGBA{102, 101, 71, 200})
	}
}

// drawPicture draws a unit's build picture, or a neutral empty frame while it
// decodes or when the content has none. No substitute art is drawn.
func (s *toolsScreen) drawPicture(dst *ebiten.Image, def *content.UnitDef, r screenkit.Rect) {
	s.drawPictureAlpha(dst, def, r, 1)
}

// drawPictureAlpha is drawPicture at a reduced opacity, for a removed unit.
func (s *toolsScreen) drawPictureAlpha(dst *ebiten.Image, def *content.UnitDef, r screenkit.Rect, alpha float64) {
	device := s.deviceRect(r)
	screenkit.Fill(dst, device, color.RGBA{8, 13, 10, 230})
	if img := s.pics.image(def); img != nil {
		screenkit.Image(dst, img, device, alpha, false)
	}
	screenkit.Outline(dst, device, max(1, s.scale*0.75), color.RGBA{70, 76, 60, 220})
}

func (s *toolsScreen) drawTab(dst *ebiten.Image, index int, g gui.Gadget, r screenkit.Rect) {
	active := unitViewerTabs[g.Name] == s.infoTab
	st := screenkit.Style{Size: 14, Tracking: 0.12, Upper: true, Align: 1, Shadow: 0.1, Top: color.RGBA{183, 174, 140, 255}}
	switch {
	case active:
		st.Top = nlCream
	case s.panel.Hovered() == index:
		st.Top = color.RGBA{238, 232, 210, 255}
	}
	text := s.panel.TextAt(index)
	s.label(dst, text, screenkit.Rect{X: r.X, Y: r.Y + 5, W: r.W, H: 20}, st, true)
	if active {
		// The settings screen's active-page underline and glow.
		tw := unitViewerMeasure(text, st, true)
		bar := screenkit.Rect{X: r.X + (r.W-tw)/2, Y: r.Y + r.H - 6, W: tw, H: 3}
		screenkit.Glow(dst, s.deviceRect(bar.Inset(-6)), color.RGBA{61, 255, 92, 50})
		screenkit.VGradient(dst, s.deviceRect(bar), color.RGBA{184, 255, 194, 255}, color.RGBA{21, 168, 43, 255})
	}
	if s.panel.Focused() == index {
		screenkit.Outline(dst, s.deviceRect(r), max(1, s.scale), nlKicker)
	}
}

// drawInfo paints the typed rows at the native list's row origin and height,
// the same geometry infoLinkAt measures.
func (s *toolsScreen) drawInfo(dst *ebiten.Image, index int, g gui.Gadget, r screenkit.Rect) {
	_, _, top, ok := s.panel.ListValuesAt(index)
	if !ok {
		return
	}
	target := s.clip(dst, r)
	if target == nil {
		return
	}
	h := float64(g.ItemHeight)
	x0, w := r.X+unitViewerTextInset, r.W-2*unitViewerTextInset
	valueRight := x0 + w - unitViewerUnitWidth
	hover := s.infoLinkAt(s.pointerX, s.pointerY)
	start := top
	if start > 0 && start < len(s.infoRows) && s.infoRows[start].Kind == unitViewerRowLinkTail {
		start-- // keep a link's picture when only its second row is in view
	}
	body := screenkit.Style{Size: unitViewerBodySize, Top: nlBody}
	for i := start; i < len(s.infoRows) && i-top < unitViewerListRows(g); i++ {
		y := r.Y + 2 + float64(i-top)*h
		row := s.infoRows[i]
		if row.Card {
			screenkit.Fill(target, s.deviceRect(screenkit.Rect{X: r.X + 2, Y: y, W: r.W - 4, H: h}), color.RGBA{255, 255, 255, 7})
			screenkit.Fill(target, s.deviceRect(screenkit.Rect{X: r.X + 2, Y: y, W: 2, H: h}), alphaC(nlKicker, 0.55))
		}
		line := screenkit.Rect{X: x0, Y: y, W: w, H: unitViewerTextMetric + 2}
		switch row.Kind {
		case unitViewerRowHeading:
			s.label(target, row.Label, screenkit.Rect{X: x0, Y: y + 1, W: w, H: 18}, screenkit.Style{Size: 14, Tracking: 0.1, Top: nlGoldTop, Bottom: nlGoldBottom, Upper: true}, true)
			s.label(target, row.Value, screenkit.Rect{X: x0, Y: y + 4, W: w, H: 14}, screenkit.Style{Size: 10, Tracking: 0.08, Top: nlDim, Upper: true, Align: 2}, false)
			screenkit.Fill(target, s.deviceRect(screenkit.Rect{X: x0, Y: y + h - 1, W: w, H: 1}), color.RGBA{123, 119, 86, 110})
		case unitViewerRowText:
			s.label(target, row.Label, line, body, false)
		case unitViewerRowNote:
			s.label(target, row.Label, screenkit.Rect{X: x0, Y: y + 1, W: w, H: 16}, screenkit.Style{Size: unitViewerNoteSize, Top: nlDim}, false)
		case unitViewerRowPair:
			s.label(target, row.Label, line, screenkit.Style{Size: unitViewerBodySize, Top: nlDim}, false)
			right := valueRight
			if row.Wide {
				right = x0 + w
			}
			s.label(target, row.Value, screenkit.Rect{X: x0, Y: y, W: right - x0, H: line.H}, screenkit.Style{Size: unitViewerBodySize, Top: nlBody, Align: 2}, false)
			s.label(target, row.Unit, screenkit.Rect{X: valueRight + 5, Y: y + 3, W: unitViewerUnitWidth - 5, H: 15}, screenkit.Style{Size: unitViewerUnitSize, Top: nlDim}, false)
		case unitViewerRowCard:
			s.label(target, row.Value, screenkit.Rect{X: x0, Y: y + 4, W: w, H: 14}, screenkit.Style{Size: 10, Tracking: 0.08, Top: nlKicker, Upper: true, Align: 2}, false)
			slot := unitViewerMeasure(row.Value, screenkit.Style{Size: 10, Tracking: 0.08, Upper: true}, false)
			st := screenkit.Style{Size: 15, Top: nlCream}
			for st.Size > 12 && unitViewerMeasure(row.Label, st, true) > w-slot-8 {
				st.Size -= 0.5
			}
			s.label(target, row.Label, screenkit.Rect{X: x0, Y: y + 1 + (15-st.Size)/2, W: w - slot - 8, H: 19}, st, true)
		case unitViewerRowTags:
			x := x0
			for _, tag := range row.Tags {
				cw := unitViewerTagWidth(tag)
				chip := screenkit.Rect{X: x, Y: y + 3, W: cw, H: h - 6}
				screenkit.Fill(target, s.deviceRect(chip), color.RGBA{24, 36, 26, 220})
				screenkit.Outline(target, s.deviceRect(chip), max(1, s.scale*0.75), color.RGBA{86, 102, 74, 255})
				s.label(target, tag, screenkit.Rect{X: x, Y: y + 4, W: cw, H: 14}, screenkit.Style{Size: unitViewerTagSize, Tracking: 0.04, Top: nlBody, Upper: true, Align: 1}, false)
				x += cw + unitViewerTagGap
			}
		case unitViewerRowLink:
			s.drawInfoLink(target, row, screenkit.Rect{X: r.X + 2, Y: y, W: r.W - 4, H: 2*h - 2}, x0, w, row.Link == hover)
		}
	}
	if s.panel.Focused() == index {
		screenkit.Outline(target, s.deviceRect(r), max(1, s.scale), color.RGBA{102, 101, 71, 200})
	}
}

// drawInfoLink paints one two-row build-tree entry: picture and name, then
// the unit ID, an optional Modern marker and the work time.
func (s *toolsScreen) drawInfoLink(dst *ebiten.Image, row unitViewerRow, entry screenkit.Rect, x0, w float64, hover bool) {
	if hover {
		fill := color.RGBA{255, 255, 255, 12}
		if s.linkPress == row.Link {
			fill = color.RGBA{34, 63, 30, 185}
		}
		screenkit.Fill(dst, s.deviceRect(entry), fill)
	}
	s.drawPicture(dst, row.Link, screenkit.Rect{X: x0, Y: entry.Y + 2, W: 34, H: 34})
	tx := x0 + 44
	s.label(dst, unitViewerName(row.Link), screenkit.Rect{X: tx, Y: entry.Y + 1, W: x0 + w - tx, H: 18}, screenkit.Style{Size: unitViewerBodySize, Top: nlCream}, false)
	detail := screenkit.Style{Size: 13, Top: nlGreenText, Align: 2}
	right := x0 + w - unitViewerMeasure(row.Value, detail, false) - 8
	s.label(dst, row.Value, screenkit.Rect{X: tx, Y: entry.Y + 18, W: x0 + w - tx, H: 17}, detail, false)
	if row.Modern {
		const tag = "Modern"
		cw := unitViewerTagWidth(tag)
		chip := screenkit.Rect{X: right - cw, Y: entry.Y + 21, W: cw, H: 15}
		screenkit.Outline(dst, s.deviceRect(chip), max(1, s.scale*0.75), alphaC(nlKicker, 0.8))
		s.label(dst, tag, screenkit.Rect{X: chip.X, Y: chip.Y + 1, W: cw, H: 13}, screenkit.Style{Size: unitViewerTagSize, Tracking: 0.04, Top: nlKicker, Upper: true, Align: 1}, false)
		right = chip.X - 6
	}
	s.label(dst, strings.ToUpper(row.Link.UnitName), screenkit.Rect{X: tx, Y: entry.Y + 21, W: right - tx, H: 14}, screenkit.Style{Size: 10, Tracking: 0.04, Top: nlDim}, false)
}

func (s *toolsScreen) drawScrollbar(dst *ebiten.Image, index int, r screenkit.Rect) {
	screenkit.Fill(dst, s.deviceRect(r), color.RGBA{12, 19, 12, 220})
	// The very same native metrics and origin govern hit testing and dragging.
	size, _ := s.panel.SliderMetricsAt(index, unitViewerTextMetric)
	thumb := screenkit.Rect{X: r.X + 3, Y: r.Y + 2 + float64(s.panel.SliderKnobAt(index)), W: r.W - 6, H: float64(size)}
	screenkit.Fill(dst, s.deviceRect(thumb), color.RGBA{97, 133, 86, 255})
	if s.panel.Focused() == index || s.panel.Hovered() == index {
		screenkit.Outline(dst, s.deviceRect(thumb), max(1, s.scale), nlGreenText)
	}
}
