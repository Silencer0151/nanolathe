package main

// The Enhanced economy explanation is host UI. It reads only committed
// observations and sends typed commands (DESIGN_INTERFACE_HUD_INPUT
// "Modern economy breakdown").

import (
	"cmp"
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/hud"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

type economyPanelState struct {
	open     bool
	resource economy.Res
	viewer   uint8
	last     [economy.ActivityCount]pool.UnitRef
	captured bool
	press    economyPanelHit
	message  string
}

type economyPanelHit struct {
	kind     uint8 // 1 resource, 2 close, 3 navigate, 4 pause, 5 background, 6 Smart makers
	resource economy.Res
	activity economy.Activity
	held     bool // retain the intent of the control that was pressed
}

type economyContributor struct {
	unit   frame.UnitView
	amount float32
}

type economyPanelRow struct {
	activity     economy.Activity
	production   bool
	amount       float32
	share        float32
	contributors []economyContributor
	held         bool
}

type economyPanelReport struct {
	rows                       []economyPanelRow
	produced, requested        float32
	otherProduced, otherDemand float32
	nonfinite                  bool
}

func economyActivityName(a economy.Activity) string {
	switch a {
	case economy.ActivityGeneration:
		return "Generation"
	case economy.ActivityConstruction:
		return "Construction"
	case economy.ActivityWeapons:
		return "Weapons & ammo"
	case economy.ActivityUpkeep:
		return "Upkeep"
	case economy.ActivityHealing:
		return "Healing"
	case economy.ActivityCloaking:
		return "Cloaking"
	case economy.ActivityReclaim:
		return "Reclaim"
	default:
		return "Unattributed"
	}
}

func economyFinite(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
}

// Totals include vanished contributors: the report explains a completed pass,
// while navigation can only act on allocations still owned by the viewer.
func economyReport(f *frame.Frame, resource economy.Res) economyPanelReport {
	var report economyPanelReport
	if f == nil || resource < economy.Metal || resource > economy.Energy {
		return report
	}
	rows := make([]economyPanelRow, economy.ActivityCount)
	for a := economy.ActivityGeneration; a < economy.ActivityCount; a++ {
		rows[a].activity = a
		rows[a].production = a == economy.ActivityGeneration || a == economy.ActivityReclaim
	}
	units := make(map[pool.UnitRef]frame.UnitView, len(f.Units))
	for _, u := range f.Units {
		if u.Owner == f.ViewingPlayer {
			units[unitViewRef(u)] = u
		}
	}
	for _, flow := range f.EconomyFlows {
		if flow.Player != f.ViewingPlayer {
			continue
		}
		made, demand := flow.Produced[resource], flow.Requested[resource]
		if !economyFinite(made) || !economyFinite(demand) {
			report.nonfinite = true
			continue
		}
		report.produced += made
		report.requested += demand
		a := economy.Activity(flow.Kind)
		if a <= economy.ActivityOther || a >= economy.ActivityCount {
			report.otherProduced += made
			report.otherDemand += demand
			continue
		}
		row := &rows[a]
		amount := demand
		if row.production {
			amount = made
			report.otherDemand += demand
		} else {
			report.otherProduced += made
		}
		row.amount += amount
		if u, ok := units[flow.Unit]; ok && amount != 0 {
			row.contributors = append(row.contributors, economyContributor{u, amount})
		}
	}
	for _, hold := range f.EconomyHolds {
		if hold.Player == f.ViewingPlayer && hold.Kind > 0 && int(hold.Kind) < len(rows) {
			rows[hold.Kind].held = true
		}
	}
	for a := economy.ActivityGeneration; a < economy.ActivityCount; a++ {
		row := &rows[a]
		denominator := report.requested
		if row.production {
			denominator = report.produced
		}
		if denominator > 0 {
			row.share = row.amount / denominator
		}
		slices.SortFunc(row.contributors, func(a, b economyContributor) int {
			if n := cmp.Compare(b.amount, a.amount); n != 0 {
				return n
			}
			if n := cmp.Compare(a.unit.Slot, b.unit.Slot); n != 0 {
				return n
			}
			return cmp.Compare(a.unit.AllocationSerial, b.unit.AllocationSerial)
		})
		report.rows = append(report.rows, *row)
	}
	slices.SortStableFunc(report.rows, func(a, b economyPanelRow) int {
		if a.production != b.production {
			if a.production {
				return -1
			}
			return 1
		}
		if n := cmp.Compare(b.amount, a.amount); n != 0 {
			return n
		}
		return cmp.Compare(a.activity, b.activity)
	})
	return report
}

// Resource anchors own the hit area as well as the readout. Including their
// text pens makes both the bar and its adjacent numbers clickable.
func (h *retailBattleHUD) economyResourceRect(resource economy.Res) image.Rectangle {
	if h == nil {
		return image.Rectangle{}
	}
	indexes := []int{hud.AnchorEnergyBar, hud.AnchorEnergyNum, hud.AnchorEnergyMax, hud.AnchorEnergy0, hud.AnchorEnergyProduced, hud.AnchorEnergyConsumed}
	if resource == economy.Metal {
		indexes = []int{hud.AnchorMetalBar, hud.AnchorMetalNum, hud.AnchorMetalMax, hud.AnchorMetal0, hud.AnchorMetalProduced, hud.AnchorMetalConsumed}
	}
	var out image.Rectangle
	for i, index := range indexes {
		r, ok := h.anchors.ByIndex(index)
		if !ok {
			continue
		}
		x1, y1, x2, y2 := r.Ordered()
		if i != 0 {
			if r == (hud.Rect{}) {
				continue
			}
			x2, y2 = x1+58, y1+10
			if i == 2 {
				x1, x2 = x1-58, x1
			}
		}
		out = out.Union(image.Rect(int(x1), int(y1), int(x2)+1, int(y2)+1))
	}
	return out.Union(h.economyPauseIndicatorRect(resource)).Inset(-2)
}

const economyPanelRowHeight = 31

// Smart is independent of activity holds, available in every gameplay mode.
func economySmartRect(row image.Rectangle) image.Rectangle {
	return image.Rect(row.Max.X-206, row.Min.Y, row.Max.X-132, row.Min.Y+16)
}

func economyPanelControlRect(row image.Rectangle) image.Rectangle {
	return image.Rect(row.Min.X, row.Min.Y+2, row.Min.X+28, row.Max.Y-2)
}

// Draw geometry rather than font glyphs: the authored console fonts need not
// contain media symbols. The triangle is the action to resume a paused row.
func drawEconomyMediaIcon(c *client.Client, x, y int, play bool, color byte) {
	if play {
		for dx := range 9 {
			inset := dx * 6 / 8
			c.UIFillRect(x+dx, y+inset, 1, 13-inset*2, color)
		}
		return
	}
	c.UIFillRect(x, y+1, 3, 11, color)
	c.UIFillRect(x+6, y+1, 3, 11, color)
}

// The dropdown must fit even when an explicitly magnified rail leaves too
// little virtual height. Its own integer region keeps text and hit testing
// together while allowing the whole explanation to remain on screen.
func (b *battleSession) economyPanelRegion() client.ChromeRegion {
	w, h := b.cl.Size()
	return client.ChromeRegion{Scale: max(1, min(b.chromeScale(), int32(w/580), int32(h/430)))}
}

func (b *battleSession) economyPanelRect() image.Rectangle {
	if b == nil || b.cl == nil || b.hud == nil {
		return image.Rectangle{}
	}
	w, h := b.cl.Size()
	region := b.economyPanelRegion()
	w, h = region.VirtualSize(w, h)
	width := min(442, w-12)
	anchor := b.hud.economyResourceRect(b.economyPanel.resource).Min.X * int(b.chromeScale()) / int(region.Scale)
	x := max(6, min(anchor, w-width-6))
	y := max(3, min(32*int(b.chromeScale())/int(region.Scale)+3, h-375))
	return image.Rect(x, y, x+width, y+367)
}

func economyPanelRowRect(panel image.Rectangle, i int) image.Rectangle {
	y := panel.Min.Y + 71 + i*economyPanelRowHeight
	if i >= 2 {
		y += 18
	}
	return image.Rect(panel.Min.X+8, y, panel.Max.X-8, y+economyPanelRowHeight-1)
}

func (b *battleSession) economyPanelCovers(x, y int32) bool {
	if b == nil || b.cl == nil || !b.cl.Enhanced() || !b.economyPanel.open {
		return false
	}
	mx, my := b.economyPanelRegion().ToVirtual(x, y)
	return image.Pt(int(mx), int(my)).In(b.economyPanelRect())
}

func (b *battleSession) economyPanelHit(f *frame.Frame, x, y int32) economyPanelHit {
	mx, my := b.railPointer(x, y)
	p := image.Pt(int(mx), int(my))
	// Wide text-pen bounds can reach into the adjacent resource's label.
	// Resolve the painted reminders first so each opens its own dropdown.
	for _, resource := range [...]economy.Res{economy.Metal, economy.Energy} {
		if p.In(b.hud.economyPauseIndicatorRect(resource)) {
			return economyPanelHit{kind: 1, resource: resource}
		}
	}
	for _, resource := range [...]economy.Res{economy.Metal, economy.Energy} {
		if p.In(b.hud.economyResourceRect(resource)) {
			return economyPanelHit{kind: 1, resource: resource}
		}
	}
	if !b.economyPanel.open {
		return economyPanelHit{}
	}
	mx, my = b.economyPanelRegion().ToVirtual(x, y)
	p = image.Pt(int(mx), int(my))
	panel := b.economyPanelRect()
	if !p.In(panel) {
		return economyPanelHit{}
	}
	if p.In(image.Rect(panel.Max.X-26, panel.Min.Y+4, panel.Max.X-4, panel.Min.Y+24)) {
		return economyPanelHit{kind: 2}
	}
	for i, row := range economyReport(f, b.economyPanel.resource).rows {
		r := economyPanelRowRect(panel, i)
		if p.In(r) {
			if b.economyPanel.resource == economy.Energy && row.activity == economy.ActivityUpkeep && p.In(economySmartRect(r)) {
				return economyPanelHit{kind: 6, held: b.smartMakersEnabled()}
			}
			kind := uint8(3)
			if p.In(economyPanelControlRect(r)) {
				kind = 4
			}
			return economyPanelHit{kind: kind, activity: row.activity, held: kind == 4 && row.held}
		}
	}
	return economyPanelHit{kind: 5}
}

func (b *battleSession) serviceEconomyPanel(in *input.State, cl *client.Client, mouse input.MouseState) bool {
	if b == nil || b.hud == nil || cl == nil || !cl.Enhanced() || !cl.IsFocused() {
		if b != nil {
			b.economyPanel = economyPanelState{}
		}
		return false
	}
	f, ok := b.currentSnapshot()
	if !ok {
		return false
	}
	state := &b.economyPanel
	if state.open && state.viewer != f.ViewingPlayer {
		*state = economyPanelState{}
	}
	if state.open && battleShortcutKeyboard(in).KeyDown(input.KeyEscape) {
		*state = economyPanelState{}
		return true
	}
	if !state.captured && (b.battleState().Input.DragActive || b.modernDrag != nil || b.palettePointerOwned) {
		return false
	}
	hit := b.economyPanelHit(f, int32(mouse.X), int32(mouse.Y))
	if state.captured {
		if mouse.Released(input.MouseButtonLeft) || mouse.Released(input.MouseButtonRight) {
			press := state.press
			state.captured = false
			if mouse.Released(input.MouseButtonLeft) && press == hit {
				b.activateEconomyPanel(f, hit)
			}
		}
		return true
	}
	if mouse.Pressed(input.MouseButtonLeft) || mouse.Pressed(input.MouseButtonRight) {
		if hit.kind != 0 || state.open {
			state.captured, state.press = true, hit
			if hit.kind == 0 || mouse.Pressed(input.MouseButtonRight) {
				state.open = false
				state.press = economyPanelHit{}
			}
			return true
		}
	}
	return hit.kind != 0 && state.open
}

func (b *battleSession) economyPanelCanControl(f *frame.Frame) bool {
	return b != nil && b.sess != nil && b.playback == nil && f != nil && f.EconomyHoldsAllowed &&
		f.ViewingPlayer == b.sess.LocalOwner && !localSlotWatching(f)
}

func (b *battleSession) activateEconomyPanel(f *frame.Frame, hit economyPanelHit) {
	state := &b.economyPanel
	switch hit.kind {
	case 1:
		open := !state.open || state.resource != hit.resource
		*state = economyPanelState{open: open, resource: hit.resource, viewer: f.ViewingPlayer}
		b.resourceClick = nil
		b.resourceQueueFeedback = nil
	case 2:
		state.open = false
	case 6:
		if state.resource == economy.Energy && b.smartMakersCanControl(f) {
			b.smartMakersEnabled()
			b.smartMakers.enabled = !hit.held
			b.smartMakers.observed = false
			state.message = ""
		}
	case 3, 4:
		for _, row := range economyReport(f, state.resource).rows {
			if row.activity != hit.activity {
				continue
			}
			if hit.kind == 4 {
				if b.economyPanelCanControl(f) {
					err := b.enqueueHumanCommand(session.HumanCommand{Kind: session.HumanEconomyActivity,
						EconomyActivity: session.HumanEconomyActivityCommand{Activity: row.activity, Pause: !hit.held}})
					if err != nil {
						state.message = "Activity command could not be sent."
					} else {
						state.message = ""
					}
				}
				return
			}
			if b.sess == nil || f.ViewingPlayer != b.sess.LocalOwner || len(row.contributors) == 0 {
				return
			}
			next := 0
			for i, c := range row.contributors {
				if unitViewRef(c.unit) == state.last[row.activity] {
					next = (i + 1) % len(row.contributors)
					break
				}
			}
			for n := 0; n < len(row.contributors); n++ {
				u := row.contributors[(next+n)%len(row.contributors)].unit
				if !b.ownSelectableUnit(f, u) {
					continue
				}
				state.last[row.activity] = unitViewRef(u)
				b.disarmPlacement()
				b.resetOrderLatch()
				b.commitSelection([]pool.Handle{u.Slot}, false)
				if b.cam != nil {
					b.cam.ClearFollow()
				}
				b.glideToUnit(u)
				return
			}
		}
	}
}

func (h *retailBattleHUD) drawEconomyPanel(c *client.Client, b *battleSession, f *frame.Frame) {
	if h == nil || h.console == nil || c == nil || !c.Enhanced() || b == nil || f == nil || !b.economyPanel.open || b.economyPanel.viewer != f.ViewingPlayer {
		return
	}
	panel := b.economyPanelRect()
	if panel.Empty() {
		return
	}
	c.BeginChromeRegion(b.economyPanelRegion())
	defer c.EndChromeRegion()
	bg, ink, muted := h.guiColor(0), h.guiColor(15), h.guiColor(7)
	good, warn := h.guiColor(10), h.guiColor(14)
	c.UIFillRect(panel.Min.X+3, panel.Min.Y+3, panel.Dx(), panel.Dy(), bg)
	c.UIFillRect(panel.Min.X, panel.Min.Y, panel.Dx(), panel.Dy(), muted)
	c.UIFillRect(panel.Min.X+1, panel.Min.Y+1, panel.Dx()-2, panel.Dy()-2, bg)
	text := func(s string, x, y, width int, color byte) {
		c.UITextWidthClipped(h.console, s, x, y, width, color, panel.Min.X+2, panel.Min.Y+2, panel.Dx()-4, panel.Dy()-4)
	}
	name := "ENERGY"
	if b.economyPanel.resource == economy.Metal {
		name = "METAL"
	}
	x, y := panel.Min.X+10, panel.Min.Y+8
	owner := "LOCAL ECONOMY"
	if b.sess != nil && f.ViewingPlayer != b.sess.LocalOwner {
		owner = fmt.Sprintf("PLAYER %d ECONOMY", f.ViewingPlayer+1)
	}
	text(name+"  /  "+owner, x, y, panel.Dx()-45, ink)
	text("x", panel.Max.X-20, y, 15, muted)
	subtitle := "Last settlement  |  click a type: largest unit first"
	canControl := b.economyPanelCanControl(f)
	if !canControl {
		subtitle = "View only  |  click a type: largest unit first"
	}
	if paused := economyPausedCount(f); paused > 0 {
		noun := "activities"
		if paused == 1 {
			noun = "activity"
		}
		subtitle = fmt.Sprintf("%d %s paused  |  Play to re-enable", paused, noun)
		if !canControl {
			subtitle = fmt.Sprintf("View only  |  %d %s paused", paused, noun)
		}
	}
	text(subtitle, x, y+19, panel.Dx()-20, muted)
	report := economyReport(f, b.economyPanel.resource)
	text(fmt.Sprintf("PRODUCED  +%.1f", report.produced), x, y+43, panel.Dx()-20, good)
	for i, row := range report.rows {
		r := economyPanelRowRect(panel, i)
		if i == 2 {
			text(fmt.Sprintf("REQUESTED  %.1f", report.requested), x, r.Min.Y-17, panel.Dx()-20, warn)
		}
		color := warn
		if row.production {
			color = good
		}
		if row.held {
			color = muted
		}
		contentX := r.Min.X + 36
		barWidth := r.Max.X - contentX
		c.UIFillRect(contentX, r.Max.Y-2, max(0, int(float32(barWidth)*min(1, max(0, row.share)))), 2, color)
		label := economyActivityName(row.activity)
		if row.held {
			label += " [paused]"
		}
		labelWidth := barWidth - 128
		if b.economyPanel.resource == economy.Energy && row.activity == economy.ActivityUpkeep {
			button := economySmartRect(r)
			labelWidth = button.Min.X - contentX - 4
			buttonInk, caption := muted, "Smart: --"
			if b.smartMakersCanControl(f) {
				buttonInk, caption = ink, "Smart: Off"
				if b.smartMakersEnabled() {
					buttonInk, caption = good, "Smart: On"
				}
			}
			c.UIFillRect(button.Min.X, button.Min.Y, button.Dx(), button.Dy(), buttonInk)
			c.UIFillRect(button.Min.X+1, button.Min.Y+1, button.Dx()-2, button.Dy()-2, bg)
			text(caption, button.Min.X+3, button.Min.Y+3, button.Dx()-6, buttonInk)
		}
		text(label, contentX, r.Min.Y, max(0, labelWidth), ink)
		amount := fmt.Sprintf("%.1f  %3.0f%%", row.amount, row.share*100)
		text(amount, r.Max.X-128, r.Min.Y, 128, color)
		detail := "No current contributors"
		if len(row.contributors) > 0 {
			lead := row.contributors[0]
			index := 1
			for n, candidate := range row.contributors {
				if unitViewRef(candidate.unit) == b.economyPanel.last[row.activity] {
					lead, index = candidate, n+1
					break
				}
			}
			unitName := lead.unit.DefName
			if b.cat != nil {
				if def, ok := b.cat.Unit(unitName); ok && def.Name != "" {
					unitName = def.Name
				}
			}
			detail = fmt.Sprintf("%d/%d  %s  %.1f", index, len(row.contributors), unitName, lead.amount)
		}
		text(detail, contentX, r.Min.Y+13, barWidth, muted)
		buttonColor, borderColor := ink, muted
		if row.held {
			buttonColor, borderColor = warn, warn
		}
		if !canControl {
			buttonColor, borderColor = muted, muted
		}
		button := economyPanelControlRect(r)
		c.UIFillRect(button.Min.X, button.Min.Y, button.Dx(), button.Dy(), borderColor)
		c.UIFillRect(button.Min.X+1, button.Min.Y+1, button.Dx()-2, button.Dy()-2, bg)
		drawEconomyMediaIcon(c, button.Min.X+9, button.Min.Y+6, row.held, buttonColor)
	}
	y = economyPanelRowRect(panel, len(report.rows)-1).Max.Y + 8
	text("Requested is demand, not necessarily paid.", x, y, panel.Dx()-20, muted)
	text("Upkeep: existing switchable structures. Debt remains.", x, y+13, panel.Dx()-20, muted)
	foot := "Pauses affect both resources. Generation keeps its costs."
	if report.otherProduced != 0 || report.otherDemand != 0 {
		foot = fmt.Sprintf("Unattributed / transfers: +%.1f  requested %.1f", report.otherProduced, report.otherDemand)
	} else if !f.EconomyHoldsAllowed {
		foot = "Activity controls require Modern gameplay."
	}
	if report.nonfinite {
		foot = "Non-finite ledger values cannot be graphed."
	}
	if b.economyPanel.message != "" {
		foot = b.economyPanel.message
	}
	text(foot, x, y+26, panel.Dx()-20, muted)
}

func economyPausedCount(f *frame.Frame) int {
	if f == nil {
		return 0
	}
	count := 0
	for _, hold := range f.EconomyHolds {
		if hold.Player == f.ViewingPlayer {
			count++
		}
	}
	return count
}

func (h *retailBattleHUD) drawEconomyPauseIndicators(c *client.Client, f *frame.Frame) {
	if !c.Enhanced() || economyPausedCount(f) == 0 {
		return
	}
	// The already sampled host instant gives a one-second pulse independent of
	// game speed, including a paused battle. Dim bars remain visible throughout.
	color := h.guiColor(14)
	if due := c.HostStepDue(); !due.IsZero() && due.UnixMilli()/500%2 != 0 {
		color = h.guiColor(7)
	}
	for _, resource := range [...]economy.Res{economy.Metal, economy.Energy} {
		r := h.economyPauseIndicatorRect(resource)
		if !r.Empty() {
			c.UIFillRect(r.Min.X, r.Min.Y, r.Dx(), r.Dy(), h.guiColor(0))
			drawEconomyMediaIcon(c, r.Min.X+2, r.Min.Y+1, false, color)
		}
	}
}

func (h *retailBattleHUD) economyPauseIndicatorRect(resource economy.Res) image.Rectangle {
	index := hud.AnchorEnergy0
	if resource == economy.Metal {
		index = hud.AnchorMetal0
	}
	r, ok := h.anchors.ByIndex(index)
	if !ok || r == (hud.Rect{}) {
		return image.Rectangle{}
	}
	// Leave the authored label and the zero/stock pens unobstructed. The badge
	// sits immediately before the label and joins the resource's click target.
	x, y := int(r.X1)-64, int(r.Y1)+5
	return image.Rect(x, y, x+13, y+15)
}
