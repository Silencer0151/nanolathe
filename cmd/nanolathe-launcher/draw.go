package main

import (
	"errors"
	"image/color"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/launcher"
	"github.com/nanolathe-gg/nanolathe/internal/launcher/shell"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// The window's size in device-independent pixels.
const windowW, windowH = 760, 420

// candidateRows is how many game folders the folder list shows at once.
const candidateRows = 3

// The Nanolathe screen's palette (cmd/nanolathe nlscreen.go).
var (
	goldTop    = color.RGBA{255, 246, 216, 255}
	goldBottom = color.RGBA{168, 143, 70, 255}
	kicker     = color.RGBA{184, 166, 103, 255}
	body       = color.RGBA{226, 219, 190, 255}
	dim        = color.RGBA{159, 151, 122, 255}
	cream      = color.RGBA{255, 240, 184, 255}
	amber      = color.RGBA{255, 217, 122, 255}
	green      = color.RGBA{61, 255, 92, 255}
	alarm      = color.RGBA{255, 128, 104, 255}
)

func alpha(c color.RGBA, a float64) color.RGBA {
	return color.RGBA{c.R, c.G, c.B, uint8(float64(c.A) * math.Max(0, math.Min(1, a)))}
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}

// frame is one Draw's copy of the shared state.
type frame struct {
	view       view
	settings   launcher.Settings
	game       launcher.Installed
	hasGame    bool
	checking   bool
	offered    *launcher.Release
	offerErr   error
	busy       string
	progress   float64
	status     string
	failed     bool
	root       string
	appControl shell.AppControl
	dropdown   bool
	desktop    bool
	candidates []string
	searching  bool
	selected   int
	scroll     int
	launchNext bool
}

func (a *app) frame() frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	return frame{a.view, a.settings, a.game, a.hasGame, a.checking, a.offered, a.offerErr, a.busy, a.progress,
		a.status, a.failed, a.root, a.appControl, a.dropdown, a.desktop, append([]string(nil), a.candidates...),
		a.searching, a.selected, a.scroll, a.launchNext}
}

// Draw implements ebiten.Game.
func (a *app) Draw(screen *ebiten.Image) {
	if !a.loaded {
		return
	}
	u := a.scale
	w, h := float64(windowW)*u, float64(windowH)*u
	f := a.frame()
	a.hits.Begin()
	defer a.hits.End()

	screenkit.VGradient(screen, screenkit.Rect{W: w, H: h}, color.RGBA{24, 33, 27, 255}, color.RGBA{6, 9, 8, 255})
	screenkit.Glow(screen, screenkit.Rect{X: -120 * u, Y: -140 * u, W: 560 * u, H: 300 * u}, color.RGBA{61, 255, 92, 26})
	screenkit.Fill(screen, screenkit.Rect{X: 32 * u, Y: 108 * u, W: w - 64*u, H: max(1, u)}, alpha(kicker, 0.35))
	a.fonts.Display.Draw(screen, "NANOLATHE", 32*u, 68*u, screenkit.Style{Verbatim: true, Size: 40 * u, Tracking: 0.08, Top: goldTop, Bottom: goldBottom, Shadow: 0.06})
	a.text(screen, a.fonts.Body, "Total Annihilation, remastered", 34*u, 92*u, 12*u, dim, 0)

	switch f.view {
	case viewSetup:
		a.drawSetup(screen, f)
	case viewMain:
		a.drawMain(screen, f)
	case viewFolder:
		a.drawFolder(screen, f)
	}
	a.drawAction(screen, f)
	a.drawChannel(screen, f)
}

func (a *app) text(dst *ebiten.Image, font *screenkit.Font, s string, x, y, size float64, c color.RGBA, align int) float64 {
	return font.Draw(dst, s, x, y, screenkit.Style{Verbatim: true, Size: size, Tracking: 0.02, Top: c, Align: align, Shadow: 0.08})
}

func (a *app) label(dst *ebiten.Image, s string, x, y float64) {
	u := a.scale
	a.fonts.Display.Draw(dst, s, x, y, screenkit.Style{Verbatim: true, Size: 10 * u, Tracking: 0.16, Top: kicker, Upper: true})
}

// fit shortens a path from the middle until it fits width.
func (a *app) fit(font *screenkit.Font, s string, size, width float64) string {
	st := screenkit.Style{Verbatim: true, Size: size, Tracking: 0.02}
	if font.Measure(s, st) <= width {
		return s
	}
	r := []rune(s)
	for keep := len(r) - 1; keep > 4; keep-- {
		head := keep / 3
		candidate := string(r[:head]) + "..." + string(r[len(r)-(keep-head):])
		if font.Measure(candidate, st) <= width {
			return candidate
		}
	}
	return "..."
}

// paragraph sets body text wrapped to width, from the top-left, and returns
// the height used. A word too long for a line, such as a path, is shortened
// from the middle rather than drawn past the width. leading is a multiple
// of the text size.
func (a *app) paragraph(dst *ebiten.Image, text string, x, top, width, leading float64, st screenkit.Style) float64 {
	lines := a.fonts.Body.Wrap(text, st, width)
	y := top + st.Size
	for _, line := range lines {
		a.fonts.Body.Draw(dst, a.fit(a.fonts.Body, line, st.Size, width), x, y, st)
		y += st.Size * leading
	}
	return float64(len(lines)) * st.Size * leading
}

// button is a plain plate; click nil disables it.
func (a *app) button(dst *ebiten.Image, id string, r screenkit.Rect, label string, click func()) {
	u := a.scale
	hover := a.hits.HoverAmount(id)
	pressed := a.hits.Active() == id && a.hits.Hot() == id
	screenkit.Fill(dst, r, mix(color.RGBA{50, 54, 46, 255}, color.RGBA{72, 78, 66, 255}, hover))
	screenkit.Bevel(dst, r, max(1, u), color.RGBA{150, 150, 130, 255}, color.RGBA{20, 20, 18, 255}, pressed)
	c := color.RGBA{244, 236, 206, 255}
	if click == nil {
		c = dim
	}
	a.fonts.Display.Draw(dst, label, r.X+r.W/2, r.Y+r.H/2+5.5*u, screenkit.Style{Verbatim: true, Size: 11 * u, Tracking: 0.1, Top: c, Upper: true, Align: 1})
	a.hits.Add(screenkit.Region{ID: id, Rect: r, Click: click, Disable: click == nil})
}

// drawAction is the one large button: Install, Update or Play, with the
// status and progress beside it.
func (a *app) drawAction(dst *ebiten.Image, f frame) {
	u := a.scale
	w, h := float64(windowW)*u, float64(windowH)*u
	r := screenkit.Rect{X: w - 32*u - 210*u, Y: h - 32*u - 78*u, W: 210 * u, H: 78 * u}
	label, gold, click := "PLAY", false, (func())(nil)
	switch {
	case f.view == viewSetup:
		label, gold, click = "INSTALL", true, a.install
	case f.busy != "":
		label = f.busy
	case f.view == viewFolder:
	case !f.hasGame && f.offered != nil:
		label, gold, click = "INSTALL", true, a.update
	case !f.hasGame && f.checking:
		label = "CHECKING"
	case !f.hasGame:
	case f.offered != nil && !f.game.Current(*f.offered):
		label, gold, click = "UPDATE", true, a.update
	default:
		click = a.play
	}
	// Each state is its own region, so a press on Update that finishes
	// before the release does not release onto Play.
	id := "action-" + label
	hover := a.hits.HoverAmount(id)
	pressed := a.hits.Active() == id && a.hits.Hot() == id
	top, bottom, edge := color.RGBA{30, 92, 44, 255}, color.RGBA{12, 46, 22, 255}, green
	if gold {
		top, bottom, edge = color.RGBA{122, 98, 38, 255}, color.RGBA{58, 46, 16, 255}, amber
	}
	if click == nil {
		top, bottom, edge = color.RGBA{44, 48, 42, 255}, color.RGBA{26, 28, 25, 255}, dim
	}
	if click != nil {
		screenkit.Glow(dst, screenkit.Rect{X: r.X - 18*u, Y: r.Y - 14*u, W: r.W + 36*u, H: r.H + 28*u}, alpha(edge, 0.10+0.18*hover))
	}
	if pressed {
		top, bottom = bottom, top
	}
	screenkit.VGradient(dst, r, mix(top, color.RGBA{255, 255, 255, 255}, 0.08*hover), bottom)
	screenkit.Outline(dst, r, max(1, 1.5*u), alpha(edge, 0.85))
	text := cream
	if click == nil {
		text = dim
	}
	st := screenkit.Style{Verbatim: true, Size: 22 * u, Tracking: 0.12, Top: text, Upper: true, Align: 1, Shadow: 0.08}
	for st.Size > 10*u && a.fonts.Display.Measure(label, st) > r.W-24*u {
		st.Size -= u
	}
	a.fonts.Display.Draw(dst, label, r.X+r.W/2, r.Y+r.H/2+st.Size/2, st)
	a.hits.Add(screenkit.Region{ID: id, Rect: r, Click: click, Disable: click == nil})

	// Status and progress share the row to the button's left.
	left, width := 32*u, r.X-32*u-24*u
	status, c := f.status, body
	if f.failed {
		c = alarm
	}
	if f.busy != "" && status == "" {
		status = f.busy + "..."
	}
	a.paragraph(dst, status, left, h-82*u, width, 1.4, screenkit.Style{Verbatim: true, Size: 11 * u, Top: c, Shadow: 0.08})
	if f.busy != "" {
		bar := screenkit.Rect{X: left, Y: h - 44*u, W: width, H: 8 * u}
		screenkit.Fill(dst, bar, color.RGBA{16, 22, 18, 255})
		screenkit.Outline(dst, bar, max(1, u), alpha(dim, 0.4))
		if f.progress >= 0 {
			screenkit.HGradient(dst, screenkit.Rect{X: bar.X, Y: bar.Y, W: bar.W * f.progress, H: bar.H}, color.RGBA{17, 165, 42, 255}, green)
		} else {
			t := math.Mod(float64(time.Now().UnixMilli())/1400, 1)
			screenkit.HGradient(dst, screenkit.Rect{X: bar.X + (bar.W-bar.W/4)*t, Y: bar.Y, W: bar.W / 4, H: bar.H}, color.RGBA{17, 165, 42, 0}, green)
		}
	}
}

// drawChannel is the channel dropdown at the top right, drawn last so its
// open list lies over everything.
func (a *app) drawChannel(dst *ebiten.Image, f frame) {
	u := a.scale
	w := float64(windowW) * u
	r := screenkit.Rect{X: w - 32*u - 180*u, Y: 40 * u, W: 180 * u, H: 32 * u}
	a.label(dst, "Channel", r.X, r.Y-8*u)
	names := map[launcher.Channel]string{launcher.Stable: "Stable", launcher.Latest: "Latest"}
	enabled := f.busy == ""
	hover := a.hits.HoverAmount("channel")
	screenkit.Fill(dst, r, mix(color.RGBA{18, 24, 20, 255}, color.RGBA{30, 38, 32, 255}, hover))
	screenkit.Outline(dst, r, max(1, u), alpha(kicker, 0.6))
	a.text(dst, a.fonts.Body, names[f.settings.Channel], r.X+12*u, r.Y+r.H/2+5*u, 13*u, cream, 0)
	cx, cy := r.X+r.W-18*u, r.Y+r.H/2
	screenkit.Poly(dst, []float64{cx - 5*u, cy - 2.5*u, cx + 5*u, cy - 2.5*u, cx, cy + 3.5*u}, kicker)
	toggle := func() {
		a.mu.Lock()
		a.dropdown = !a.dropdown
		a.mu.Unlock()
	}
	if !enabled {
		toggle = nil
	}
	a.hits.Add(screenkit.Region{ID: "channel", Rect: r, Click: toggle, Disable: toggle == nil})
	if !f.dropdown || !enabled {
		return
	}
	// Clicking anywhere else closes the list.
	a.hits.Add(screenkit.Region{ID: "channel-dismiss", Rect: screenkit.Rect{W: w, H: float64(windowH) * u}, Click: func() {
		a.mu.Lock()
		a.dropdown = false
		a.mu.Unlock()
	}})
	a.hits.Add(screenkit.Region{ID: "channel", Rect: r, Click: toggle})
	notes := map[launcher.Channel]string{launcher.Stable: "Numbered releases", launcher.Latest: "Every change"}
	for i, ch := range []launcher.Channel{launcher.Stable, launcher.Latest} {
		row := screenkit.Rect{X: r.X, Y: r.Y + r.H + float64(i)*44*u, W: r.W, H: 44 * u}
		id := "channel-" + string(ch)
		screenkit.Fill(dst, row, mix(color.RGBA{14, 19, 16, 250}, color.RGBA{36, 48, 38, 255}, a.hits.HoverAmount(id)))
		screenkit.Outline(dst, row, max(1, u), alpha(kicker, 0.35))
		c := body
		if ch == f.settings.Channel {
			c = cream
		}
		a.text(dst, a.fonts.Body, names[ch], row.X+12*u, row.Y+19*u, 13*u, c, 0)
		a.text(dst, a.fonts.Body, notes[ch], row.X+12*u, row.Y+35*u, 10*u, dim, 0)
		ch := ch
		a.hits.Add(screenkit.Region{ID: id, Rect: row, Click: func() { a.setChannel(ch) }})
	}
}

func (a *app) drawSetup(dst *ebiten.Image, f frame) {
	u := a.scale
	x, width := 32*u, float64(windowW)*u-64*u
	a.label(dst, "Install", x, 140*u)
	para := screenkit.Style{Verbatim: true, Size: 12 * u, Top: body, Shadow: 0.08}
	y := 152 * u
	y += a.paragraph(dst, "Nanolathe installs into this folder, for this Windows user only. No administrator rights are needed.",
		x, y, width, 1.6, para)
	// The path is set on its own line, shortened from the middle if needed:
	// it has no spaces to wrap at.
	a.text(dst, a.fonts.Body, a.fit(a.fonts.Body, a.in.Base, 12*u, width), x, y+16*u, 12*u, cream, 0)
	y += 30 * u
	y += a.paragraph(dst, "You need your own installed copy of Total Annihilation. The launcher finds it after installing and never changes it.",
		x, y, width, 1.6, para)
	box := screenkit.Rect{X: x, Y: y + 10*u, W: 16 * u, H: 16 * u}
	screenkit.Fill(dst, box, color.RGBA{14, 19, 16, 255})
	screenkit.Outline(dst, box, max(1, u), alpha(kicker, 0.8))
	if f.desktop {
		screenkit.Line(dst, box.X+3.5*u, box.Y+8*u, box.X+7*u, box.Y+12*u, 2*u, green)
		screenkit.Line(dst, box.X+7*u, box.Y+12*u, box.X+13*u, box.Y+4*u, 2*u, green)
	}
	a.text(dst, a.fonts.Body, "Add a desktop shortcut", box.X+26*u, box.Y+12.5*u, 12*u, body, 0)
	a.hits.Add(screenkit.Region{ID: "desktop", Rect: screenkit.Rect{X: box.X, Y: box.Y - 4*u, W: 220 * u, H: 24 * u}, Click: func() {
		a.mu.Lock()
		a.desktop = !a.desktop
		a.mu.Unlock()
	}})
}

// channelOf names the channel a published release tag belongs to.
func channelOf(tag string) string {
	switch {
	case tag == "":
		return "built from source"
	case strings.HasPrefix(tag, "native-"):
		return "Latest"
	}
	return "Stable"
}

// releaseLabel names an offered release for the player.
func releaseLabel(rel *launcher.Release) string {
	if n, ok := strings.CutPrefix(rel.Tag, "native-"); ok {
		return "Latest build " + n
	}
	return "Version " + strings.TrimPrefix(rel.Tag, "v")
}

func (a *app) drawMain(dst *ebiten.Image, f frame) {
	u := a.scale
	x, valueX := 32*u, 140*u
	valueW := float64(windowW)*u - 32*u - valueX
	row := func(y float64, name, value string, c color.RGBA) {
		a.label(dst, name, x, y)
		a.text(dst, a.fonts.Body, a.fit(a.fonts.Body, value, 12*u, valueW), valueX, y+1*u, 12*u, c, 0)
	}
	installedText, installedColor := "Not installed yet", dim
	if f.hasGame {
		installedText, installedColor = f.game.Version+" ("+channelOf(f.game.Tag)+")", body
	}
	row(146*u, "Installed", installedText, installedColor)

	available, availableColor := "", body
	channel := map[launcher.Channel]string{launcher.Stable: "Stable", launcher.Latest: "Latest"}[f.settings.Channel]
	switch {
	case f.checking:
		available, availableColor = "Checking for updates...", dim
	case errors.Is(f.offerErr, launcher.ErrNoRelease):
		available, availableColor = "No "+channel+" release yet", amber
	case f.offerErr != nil:
		available, availableColor = "Could not check for updates; you can still play the installed version", amber
	case f.offered != nil && f.hasGame && f.game.Current(*f.offered):
		available, availableColor = "Up to date", green
	case f.offered != nil:
		available, availableColor = releaseLabel(f.offered)+" is ready to install", amber
	}
	row(172*u, "Available", available, availableColor)

	folder, folderColor := f.root, body
	if folder == "" {
		folder, folderColor = "Chosen when you first play", dim
	}
	row(198*u, "Game folder", folder, folderColor)

	// No channel's builds are signed yet, so the warning applies to both.
	note := ""
	switch f.appControl {
	case shell.AppControlOn:
		note = "Smart App Control is on. It blocks unsigned programs it does not recognize, and Nanolathe builds are not signed yet, so Windows may refuse to start them. Signed releases are planned."
	case shell.AppControlEvaluation:
		note = "Smart App Control is evaluating this PC. If Windows turns it on, it may block Nanolathe builds, which are not signed yet."
	}
	if note != "" {
		a.paragraph(dst, note, x, 228*u, float64(windowW)*u-64*u, 1.4, screenkit.Style{Verbatim: true, Size: 11 * u, Top: amber, Shadow: 0.08})
	}

	idle := f.busy == ""
	by := float64(windowH)*u - 140*u
	bx := x
	for _, b := range []struct {
		id, label string
		click     func()
	}{
		{"folder", "Game folder", func() { a.chooseFolder(false) }},
		{"logs", "Logs", func() { a.openFolder(a.in.Path("logs")) }},
		{"base", "Install folder", func() { a.openFolder(a.in.Base) }},
	} {
		click := b.click
		if (b.id == "folder" && (!f.hasGame || !idle)) || !idle && b.id != "logs" && b.id != "base" {
			click = nil
		}
		bw := a.fonts.Display.Measure(b.label, screenkit.Style{Verbatim: true, Size: 11 * u, Tracking: 0.1, Upper: true}) + 28*u
		a.button(dst, b.id, screenkit.Rect{X: bx, Y: by, W: bw, H: 28 * u}, b.label, click)
		bx += bw + 10*u
	}
}

func (a *app) drawFolder(dst *ebiten.Image, f frame) {
	u := a.scale
	x, width := 32*u, float64(windowW)*u-64*u
	a.label(dst, "Total Annihilation folder", x, 140*u)
	var prompt string
	switch len(f.candidates) {
	case 0:
		prompt = "Searching for Total Annihilation... If it is not found, choose the folder that holds totala1.hpi."
		if !f.searching {
			prompt = "Total Annihilation was not found. Choose the folder that holds totala1.hpi (for Steam: steamapps\\common\\Total Annihilation)."
		}
	case 1:
		prompt = "Found Total Annihilation. Use this folder?"
	default:
		prompt = "Found " + strconv.Itoa(len(f.candidates)) + " Total Annihilation folders. Choose one to play with."
	}
	a.paragraph(dst, prompt, x, 152*u, width, 1.4, screenkit.Style{Verbatim: true, Size: 12 * u, Top: body, Shadow: 0.08})
	y := 178 * u
	listW := width
	if len(f.candidates) > candidateRows {
		listW -= 34 * u // room for the scroll arrows
	}
	for i := f.scroll; i < len(f.candidates) && i < f.scroll+candidateRows; i++ {
		root := f.candidates[i]
		r := screenkit.Rect{X: x, Y: y, W: listW, H: 26 * u}
		id := "candidate-" + strconv.Itoa(i)
		screenkit.Fill(dst, r, mix(color.RGBA{14, 19, 16, 200}, color.RGBA{36, 48, 38, 255}, a.hits.HoverAmount(id)))
		cx, cy := r.X+13*u, r.Y+r.H/2
		screenkit.Ring(dst, cx, cy, 6*u, 1.5*u, kicker)
		if i == f.selected {
			screenkit.Disc(dst, cx, cy, 3*u, green)
		}
		a.text(dst, a.fonts.Body, a.fit(a.fonts.Body, root, 12*u, r.W-40*u), r.X+28*u, cy+4.5*u, 12*u, body, 0)
		i := i
		a.hits.Add(screenkit.Region{ID: id, Rect: r, Click: func() {
			a.mu.Lock()
			a.selected = i
			a.mu.Unlock()
		}})
		y += 30 * u
	}
	if len(f.candidates) > candidateRows {
		// The list scrolls by wheel or by these arrows.
		ax := x + width - 26*u
		for _, arrow := range []struct {
			id   string
			y    float64
			rows int
			pts  func(cx, cy float64) []float64
		}{
			{"scroll-up", 178 * u, -1, func(cx, cy float64) []float64 { return []float64{cx - 6*u, cy + 3*u, cx + 6*u, cy + 3*u, cx, cy - 4*u} }},
			{"scroll-down", 178*u + 60*u, 1, func(cx, cy float64) []float64 { return []float64{cx - 6*u, cy - 3*u, cx + 6*u, cy - 3*u, cx, cy + 4*u} }},
		} {
			r := screenkit.Rect{X: ax, Y: arrow.y, W: 26 * u, H: 26 * u}
			screenkit.Fill(dst, r, mix(color.RGBA{14, 19, 16, 200}, color.RGBA{36, 48, 38, 255}, a.hits.HoverAmount(arrow.id)))
			screenkit.Poly(dst, arrow.pts(r.X+r.W/2, r.Y+r.H/2), kicker)
			rows := arrow.rows
			a.hits.Add(screenkit.Region{ID: arrow.id, Rect: r, Click: func() {
				a.mu.Lock()
				a.scrollCandidates(rows)
				a.mu.Unlock()
			}})
		}
		shown := strconv.Itoa(f.scroll+1) + "-" + strconv.Itoa(min(f.scroll+candidateRows, len(f.candidates))) + " of " + strconv.Itoa(len(f.candidates))
		a.text(dst, a.fonts.Body, shown, ax+13*u, 178*u+47*u, 9*u, dim, 1)
	}
	idle := f.busy == ""
	by := float64(windowH)*u - 140*u
	bx := x
	var use, browse, cancel func()
	if idle && len(f.candidates) > 0 {
		root := f.candidates[f.selected]
		use = func() { a.useFolder(root) }
	}
	if idle {
		browse = a.browse
		cancel = func() {
			a.mu.Lock()
			a.view = viewMain
			a.mu.Unlock()
		}
	}
	for _, b := range []struct {
		id, label string
		click     func()
	}{{"use", "Use this folder", use}, {"browse", "Browse...", browse}, {"cancel", "Cancel", cancel}} {
		bw := a.fonts.Display.Measure(b.label, screenkit.Style{Verbatim: true, Size: 11 * u, Tracking: 0.1, Upper: true}) + 28*u
		a.button(dst, b.id, screenkit.Rect{X: bx, Y: by, W: bw, H: 28 * u}, b.label, b.click)
		bx += bw + 10*u
	}
}
