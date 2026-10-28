package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The map generator's panel (docs/DESIGN_MAP_GENERATOR.md §10). Generate,
// beside More maps in the map picker, opens it: the world and the map's
// shape, the map code, each measurement of the map with its scope, and the
// map with its numbered starts. Every change generates again in the
// background (mapgen_job.go). Use mounts the shown map from memory and
// selects it; Save keeps it in the map library under a name and a
// description first.

var mapgenScreenInst *mapgenScreen

// The panel's steps.
var (
	mapgenSizes   = []int{6, 8, 10, 12, 14, 16, 20, 24}
	mapgenWaters  = []string{"None", "Lakes", "Seas"}
	mapgenReliefs = []string{"Plains", "Rolling", "Hills", "Mountains"}
	mapgenMetals  = []string{"Scarce", "Normal", "Rich"}
)

// mapgenNameRunes and mapgenTextRunes bound the Save form's fields.
const mapgenNameRunes, mapgenTextRunes = 40, 160

// mapgenScreen is the panel's state on the render thread.
type mapgenScreen struct {
	host func() *gameShell
	// kit is a Nanolathe screen used only for its widgets: plates, wells,
	// captions and hit regions. It is never the settings screen.
	kit  *nlScreen
	open bool
	p    mapgen.Params
	// world is the chosen world's canonical planet key.
	world string
	// edit is a map code being typed; editing while it is.
	edit    string
	editing bool
	// naming is the Save form; field is where typing goes: 0 the name,
	// 1 the description.
	naming            bool
	name, description string
	field             int
	message           string
	// pic is the picture of shown, the result it was made from.
	pic       *ebiten.Image
	shown     *mapgen.Map
	job       mapgenJob
	lastFrame time.Time
	// base is the base install the panel's art comes from, when the
	// settings screen has not loaded it already.
	base *vfs.FS
}

func newMapgenScreen(host func() *gameShell) *mapgenScreen {
	kit := newNLScreen(host)
	kit.open = true
	return &mapgenScreen{host: host, kit: kit}
}

// Active implements ebitenapp.FullScreen.
func (s *mapgenScreen) Active() bool { return s != nil && s.open }

// mapgenDefaultSize suits the player count: room for each base and the
// ground between them.
func mapgenDefaultSize(players int) int {
	switch {
	case players <= 2:
		return 8
	case players <= 4:
		return 10
	case players <= 6:
		return 14
	case players <= 8:
		return 16
	}
	return 20
}

func mapgenSeed() uint64 { return rand.Uint64() >> 24 }

// mapGeneratorAvailable reports whether Generate can open: content with the
// standard map directories. An online room's host may generate its map too,
// which the room carries as its recipe (DESIGN_MAP_GENERATOR §10).
func (g *gameShell) mapGeneratorAvailable() bool {
	if g == nil || g.cs == nil || mapgenScreenInst == nil {
		return false
	}
	return g.cs.config == nil || mapLibraryLayoutSupported(g.cs.config.Content.Layout())
}

// openMapGenerator opens the panel over the map picker: the mounted
// content's worlds are listed in the background, and the first map starts
// at once.
func (g *gameShell) openMapGenerator() {
	s := mapgenScreenInst
	if !g.mapGeneratorAvailable() || s.open {
		return
	}
	g.playMenuCue("BigButton")
	if s.p.Players == 0 {
		players := min(max(g.setup.NumPlayers, mapgen.MinPlayers), mapgen.MaxPlayers)
		s.p = mapgen.Params{Players: players, Size: mapgenDefaultSize(players), Water: 1, Relief: 1, Metal: 1, Seed: mapgenSeed()}
		if d := g.mapDataFor(g.setup.MapName); d != nil && d.ota != nil && d.ota.Global != nil {
			if planet, ok := d.ota.Global.StringValue("planet", ""); ok {
				s.world = content.CanonicalKey(planet)
			}
		}
	}
	s.open, s.editing, s.edit, s.naming, s.message = true, false, "", false, ""
	s.job.bind(g.cs)
	s.regenerate()
}

// closePanel closes the panel. The running generation stops, every
// goroutine reading the content is joined before the host may unmount it,
// and the job forgets the content, its worlds and the map it showed.
func (s *mapgenScreen) closePanel() {
	if !s.open {
		return
	}
	s.job.stop()
	s.job.bind(nil)
	if s.pic != nil {
		s.pic.Deallocate()
	}
	s.open, s.pic, s.shown, s.naming, s.editing = false, nil, nil, false, false
}

// closeMapGenerator closes the generator's panel, if it is open, before a
// remount closes the content its work reads.
func closeMapGenerator() {
	if mapgenScreenInst != nil {
		mapgenScreenInst.closePanel()
	}
}

// release closes the panel and forgets the content; the window calls it on
// the way out.
func (s *mapgenScreen) release() {
	if s == nil {
		return
	}
	s.closePanel()
	s.job.stop()
	s.job.bind(nil)
	if s.base != nil {
		s.base.Close()
		s.base = nil
	}
}

// chosenWorld is the chosen world among those listed, or the one with most
// maps; ready is false while the list is still being read.
func (s *mapgenScreen) chosenWorld() (worlds []mapgenWorldInfo, i int, ready bool) {
	worlds, ready, _ = s.job.listed()
	i = slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return content.CanonicalKey(w.Planet) == s.world })
	if i < 0 && len(worlds) > 0 {
		i = 0
		s.world = content.CanonicalKey(worlds[0].Planet)
	}
	return worlds, i, ready
}

// regenerate starts the map the settings name; a running one is cancelled
// and can no longer replace the picture.
func (s *mapgenScreen) regenerate() {
	s.p = s.p.Normalize()
	s.chosenWorld()
	if s.job.isDry(s.world) {
		s.p.Water = 0
	}
	s.job.start(s.world, s.p)
}

// change applies one setting's step and generates again.
func (s *mapgenScreen) change(apply func(p *mapgen.Params)) {
	if g := s.host(); g != nil {
		g.playMenuCue("SmallButton")
	}
	apply(&s.p)
	s.message = ""
	s.regenerate()
}

// teamSteps are the team counts that split the players evenly, free for all
// first.
func teamSteps(players int) []int {
	steps := []int{0}
	for t := 2; t <= mapgen.MaxTeams; t++ {
		if players%t == 0 && players != t {
			steps = append(steps, t)
		}
	}
	return steps
}

// stepIn moves v d steps through steps, wrapping; a value not among them
// starts from the first.
func stepIn(steps []int, v, d int) int {
	i := max(0, slices.Index(steps, v))
	return steps[(i+d+len(steps)*4)%len(steps)]
}

func teamsLabel(teams int) string {
	if teams == 0 {
		return "Free for all"
	}
	return fmt.Sprintf("%d teams", teams)
}

// applyCode makes the typed code the panel's settings, when it names a
// world that is listed.
func (s *mapgenScreen) applyCode() {
	s.editing = false
	slug, p, err := mapgen.ParseCode(s.edit)
	if err != nil {
		s.message = "That is not a map code of this generator"
		return
	}
	worlds, _, _ := s.chosenWorld()
	i := slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return mapgen.Slug(w.Planet) == slug })
	if i < 0 {
		s.message = "The code's world is not in this content"
		return
	}
	s.world, s.p, s.message = content.CanonicalKey(worlds[i].Planet), p.Normalize(), ""
	if g := s.host(); g != nil {
		g.playMenuCue("SmallButton")
	}
	s.regenerate()
}

// generatorReason is a failed generation said as a sentence.
func generatorReason(err error) string {
	text := strings.TrimPrefix(noticeReason(err), "mapgen: ")
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// ready is the shown result when Use and Save may take it: finished, and
// made from the settings beside it.
func (s *mapgenScreen) ready() *mapgenResult {
	r, running, _, _, _, _ := s.job.state()
	if r == nil || running {
		return nil
	}
	return r
}

// use mounts the shown map from memory and selects it, as the picker's
// Load does.
func (s *mapgenScreen) use(g *gameShell) {
	r := s.ready()
	if r == nil {
		return
	}
	made, err := newGeneratedMap(r.m, r.world, r.sources, mapgenName(r.world, r.m), mapgenDescription(r))
	if err != nil {
		s.message = noticeReason(err)
		return
	}
	if g.mapNameTaken(made) {
		s.message = "A map of that name is already listed"
		return
	}
	// The remount replaces the content the panel reads, so it closes first;
	// a failure is then the picker's to show.
	s.closePanel()
	if err := g.mountGeneratedMap(made); err != nil {
		reportRetailMessageError(g.showRetailMessage(noticeReason(err)))
		return
	}
	s.take(g, made)
}

// take commits a mounted generated map to the setup, as the picker's Load
// does.
func (s *mapgenScreen) take(g *gameShell, made *generatedMap) {
	g.playMenuCue("BigButton")
	g.setSetupMap(made.recipe.Name)
	g.syncMapIndex()
	g.saveSettings()
	g.leaveMapPicker()
}

// mapgenDescription is a generated map's default description: what it was
// made for and its code.
func mapgenDescription(r *mapgenResult) string {
	return mapgenTDFText(fmt.Sprintf("Generated for %d players on %s. Code: %s", r.m.Params.Players, r.world, r.m.Code))
}

// mapgenTDFText keeps typed text inside what a TDF value may hold.
func mapgenTDFText(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune("[]{};\r\n\t", r) {
			return ' '
		}
		return r
	}, text))
}

// mapNameTaken reports whether the picker lists another map under made's
// name, which would hide the one made below it: a generated map this run
// holds in memory under that name is replaced by made, and a map with
// made's very files is the same map.
func (g *gameShell) mapNameTaken(made *generatedMap) bool {
	name := made.recipe.Name
	if !slices.ContainsFunc(g.maps, func(m string) bool { return strings.EqualFold(m, name) }) {
		return false
	}
	if info, err := g.cs.unmappedMount.Stat("maps/" + name + ".tnt"); err == nil && strings.HasPrefix(info.Source.ProviderID(), "generated:") {
		return false
	}
	tnt, err := g.cs.fs.ReadFileLimit("maps/"+name+".tnt", int64(len(made.tnt)))
	if err != nil || mapgenSum(tnt) != made.recipe.TNT {
		return true
	}
	ota, err := g.cs.fs.ReadFileLimit("maps/"+name+".ota", int64(len(made.ota)))
	return err != nil || mapgenSum(ota) != made.recipe.OTA
}

// mapgenNameValid reports whether a typed name can be a map's file name:
// letters, digits, spaces and a little punctuation, as retail map names use.
func mapgenNameValid(name string) bool {
	if strings.TrimSpace(name) != name || name == "" || utf8.RuneCountInString(name) > mapgenNameRunes {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
		case strings.ContainsRune(" -_'().,!&+", r):
		default:
			return false
		}
	}
	return true
}

// startNaming opens the Save form with the default name and description.
func (s *mapgenScreen) startNaming() {
	r := s.ready()
	if r == nil {
		return
	}
	s.naming, s.field, s.message = true, 0, ""
	s.name, s.description = mapgenName(r.world, r.m), mapgenDescription(r)
}

// save keeps the shown map in the map library under the typed name and
// description, then takes it into the setup.
func (s *mapgenScreen) save(g *gameShell) {
	r := s.ready()
	if r == nil {
		return
	}
	name, description := strings.TrimSpace(s.name), mapgenTDFText(s.description)
	switch {
	case !mapgenNameValid(name):
		s.message = "Use letters, digits and spaces for the name"
		return
	case slices.ContainsFunc(g.maps, func(m string) bool { return strings.EqualFold(m, name) }):
		s.message = "A map of that name is already listed"
		return
	case strings.Contains(description, "//") || strings.Contains(description, "/*"):
		// Either opens a comment in the map's TDF file, which cannot
		// escape it [fmt tdf].
		s.message = "Leave // and /* out of the description"
		return
	}
	made, err := newGeneratedMap(r.m, r.world, r.sources, name, description)
	if err == nil {
		// The library refresh that follows the install closes the panel
		// before it replaces the content the panel reads.
		err = g.saveGeneratedMap(made)
	}
	if err != nil {
		if s.open {
			s.message = noticeReason(err)
		} else {
			reportRetailMessageError(g.showRetailMessage(noticeReason(err)))
		}
		return
	}
	s.closePanel()
	s.take(g, made)
}

func (s *mapgenScreen) OwnsPointer() bool { return false }

func (s *mapgenScreen) Update() {
	if !s.open {
		return
	}
	g := s.host()
	if g == nil || g.cs != s.job.cs {
		// The content changed under the panel; nothing it made applies.
		s.closePanel()
		return
	}
	in := screenkit.ReadInput()
	s.kit.hits.Update(in, 1.0/60)
	if s.job.waiting() {
		s.regenerate()
	}
	if s.job.isDry(s.world) {
		s.p.Water = 0
	}
	typed := ebiten.AppendInputChars(nil)
	enter := (in.KeyPressed(ebiten.KeyEnter) || in.KeyPressed(ebiten.KeyNumpadEnter)) && !ebiten.IsKeyPressed(ebiten.KeyAlt)
	if s.naming {
		field := &s.name
		limit := mapgenNameRunes
		if s.field == 1 {
			field, limit = &s.description, mapgenTextRunes
		}
		for _, r := range typed {
			if r >= ' ' && utf8.RuneCountInString(*field) < limit {
				*field += string(r)
			}
		}
		switch {
		case in.KeyPressed(ebiten.KeyEscape):
			s.naming, s.message = false, ""
		case in.KeyPressed(ebiten.KeyTab):
			s.field = 1 - s.field
		case in.KeyPressed(ebiten.KeyBackspace):
			if r := []rune(*field); len(r) > 0 {
				*field = string(r[:len(r)-1])
			}
		case enter:
			s.save(g)
		}
		return
	}
	for _, r := range typed {
		if r > ' ' {
			if !s.editing {
				s.editing, s.edit = true, ""
			}
			s.edit += string(r)
		}
	}
	switch {
	case s.editing && in.KeyPressed(ebiten.KeyEscape):
		s.editing, s.edit = false, ""
	case s.editing && in.KeyPressed(ebiten.KeyBackspace):
		if r := []rune(s.edit); len(r) > 0 {
			s.edit = string(r[:len(r)-1])
		}
	case s.editing && enter:
		s.applyCode()
	case in.KeyPressed(ebiten.KeyEscape):
		g.playMenuCue("SmallButton")
		s.closePanel()
	case enter:
		s.use(g)
	}
}

// picture is the shown map's minimap, made once per result.
func (s *mapgenScreen) picture(g *gameShell, m *mapgen.Map) *ebiten.Image {
	if m == s.shown && s.pic != nil {
		return s.pic
	}
	if s.pic != nil {
		s.pic.Deallocate()
		s.pic = nil
	}
	s.shown = m
	if m == nil || g.assets == nil || g.assets.pal == nil {
		return nil
	}
	// The map's own part of the minimap, without the pad round it.
	scale := max(m.Width, m.Height)
	w, h := max(1, m.Width*m.MinimapWidth/scale), max(1, m.Height*m.MinimapHeight/scale)
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := g.assets.pal.Base[m.Minimap[y*m.MinimapWidth+x]]
			i := (y*w + x) * 4
			rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3] = c[0], c[1], c[2], 255
		}
	}
	s.pic = ebiten.NewImageFromImage(rgba)
	return s.pic
}

// readyArt borrows the settings screen's fonts and art, or loads them from
// the base install the first time the panel opens before that screen has.
func (s *mapgenScreen) readyArt(g *gameShell) bool {
	k := s.kit
	if k.ready {
		return true
	}
	if nl := nlScreenInst; nl != nil && nl.ready {
		k.fonts, k.art, k.ready = nl.fonts, nl.art, true
		return true
	}
	if g == nil || g.cs == nil {
		return false
	}
	if s.base == nil {
		base := vfs.New()
		if err := base.MountGameDirectories(g.cs.baseRoots); err != nil {
			base.Close()
			return false
		}
		s.base = base
	}
	k.fonts, k.art, k.ready = screenkit.LoadFonts(), loadNLArt(s.base), true
	return true
}

func (s *mapgenScreen) Draw(screen *ebiten.Image) {
	g := s.host()
	screen.Fill(color.RGBA{14, 13, 10, 255})
	if !s.open || !s.readyArt(g) {
		return
	}
	k := s.kit
	now := time.Now()
	dt := 1.0 / 60
	if !s.lastFrame.IsZero() {
		dt = min(0.1, now.Sub(s.lastFrame).Seconds())
	}
	s.lastFrame = now
	b := screen.Bounds()
	nlScreenW, nlScreenH = float64(b.Dx()), float64(b.Dy())
	k.clock += dt
	k.dt = dt
	k.hits.Begin()
	defer k.hits.End()
	s.drawBackdrop(screen)
	s.drawPanel(screen, g)
	if s.naming {
		s.drawSaveForm(screen, g)
	}
}

// drawBackdrop is the Nanolathe screen's ground, without its scene.
func (s *mapgenScreen) drawBackdrop(screen *ebiten.Image) {
	k := s.kit
	u := k.u()
	if t := k.art.texture; t != nil {
		tb := t.Bounds()
		sc := math.Max(2, math.Round(u*2))
		for y := 0.0; y < k.h(); y += float64(tb.Dy()) * sc {
			for x := 0.0; x < k.w(); x += float64(tb.Dx()) * sc {
				screenkit.Image(screen, t, screenkit.Rect{X: x, Y: y, W: float64(tb.Dx()) * sc, H: float64(tb.Dy()) * sc}, 0.55, true)
			}
		}
	}
}

// plateButton is a Nanolathe plate with its caption.
func (s *mapgenScreen) plateButton(screen *ebiten.Image, id string, r screenkit.Rect, label string, size float64, selected, dim bool) {
	k := s.kit
	u := k.u()
	k.buttonPlate(screen, id, r, selected, dim)
	text := color.RGBA{244, 236, 206, 255}
	switch {
	case selected:
		text = nlCream
	case dim:
		text = nlDim
	}
	st := fitStyle(k.fonts.Display, label, screenkit.Style{Size: size, Tracking: 0.06, Top: text, Upper: true, Align: 1, Shadow: 0.1}, r.W-12*u, size*0.6)
	k.buttonCaption(screen, label, r.X+r.W/2, r.Y+r.H/2+st.Size/2, st)
}

// setting is one parameter: its label above a plate showing the value; a
// click steps forward, a right click back.
func (s *mapgenScreen) setting(screen *ebiten.Image, id string, r screenkit.Rect, label, value string, click, right func()) {
	k := s.kit
	u := k.u()
	df := k.fonts.Display
	df.Draw(screen, label, r.X+2*u, r.Y-8*u, fitStyle(df, label, screenkit.Style{Size: 10.5 * u, Tracking: 0.16, Top: nlKicker, Upper: true}, r.W, 8*u))
	s.plateButton(screen, id, r, value, 12*u, false, false)
	k.hits.Add(screenkit.Region{ID: id, Rect: r, Click: click, Right: right})
}

// drawPanel is the panel: the world and the map's shape on the left with
// the map code and the measurements, the map with its numbered starts on
// the right, and at the foot another map of the same settings, Close, Save
// and Use.
func (s *mapgenScreen) drawPanel(screen *ebiten.Image, g *gameShell) {
	k := s.kit
	u := k.u()
	w, h := 1160*u, 820*u
	r := screenkit.Rect{X: k.w()/2 - w/2, Y: k.h()/2 - h/2, W: w, H: h}
	k.well(screen, r, 1)
	df, bf := k.fonts.Display, k.fonts.Body
	df.Draw(screen, "Generate a map", r.X+32*u, r.Y+50*u, screenkit.Style{Size: 26 * u, Tracking: 0.04, Top: nlCream})
	res, running, stage, done, total, jobErr := s.job.state()
	var m *mapgen.Map
	if res != nil {
		m = res.m
	}
	worlds, wi, listed := s.chosenWorld()

	x, colW := r.X+32*u, 540*u
	half := (colW - 12*u) / 2
	y := r.Y + 100*u
	worldName := "Listing the worlds…"
	if listed && wi >= 0 && wi < len(worlds) {
		worldName = worlds[wi].Planet
	}
	stepWorld := func(d int) func() {
		return func() {
			if len(worlds) == 0 {
				return
			}
			next := worlds[(wi+d+len(worlds))%len(worlds)]
			s.change(func(*mapgen.Params) { s.world = content.CanonicalKey(next.Planet) })
		}
	}
	s.setting(screen, "mg-world", screenkit.Rect{X: x, Y: y, W: colW, H: 34 * u}, "World", worldName, stepWorld(1), stepWorld(-1))
	plate := func(id string, col, row int, label, value string, click, right func()) {
		rr := screenkit.Rect{X: x + float64(col)*(half+12*u), Y: y + float64(row)*70*u, W: half, H: 34 * u}
		s.setting(screen, id, rr, label, value, click, right)
	}
	p := &s.p
	players := func(d int) func() {
		return func() {
			s.change(func(p *mapgen.Params) {
				p.Players = min(max(p.Players+d, mapgen.MinPlayers), mapgen.MaxPlayers)
				if !slices.Contains(teamSteps(p.Players), p.Teams) {
					p.Teams = 0
				}
			})
		}
	}
	plate("mg-players", 0, 1, "Players", strconv.Itoa(p.Players), players(1), players(-1))
	teams := func(d int) func() {
		return func() { s.change(func(p *mapgen.Params) { p.Teams = stepIn(teamSteps(p.Players), p.Teams, d) }) }
	}
	plate("mg-teams", 1, 1, "Teams", teamsLabel(p.Teams), teams(1), teams(-1))
	size := func(d int) func() {
		return func() { s.change(func(p *mapgen.Params) { p.Size = stepIn(mapgenSizes, p.Size, d) }) }
	}
	plate("mg-size", 0, 2, "Size", fmt.Sprintf("%d x %d", p.Size, p.Size), size(1), size(-1))
	cycle := func(field *int, steps int, d int) func() {
		return func() { s.change(func(*mapgen.Params) { *field = (*field + d + steps) % steps }) }
	}
	plate("mg-water", 1, 2, "Water", mapgenWaters[p.Water], cycle(&p.Water, len(mapgenWaters), 1), cycle(&p.Water, len(mapgenWaters), -1))
	plate("mg-relief", 0, 3, "Relief", mapgenReliefs[p.Relief], cycle(&p.Relief, len(mapgenReliefs), 1), cycle(&p.Relief, len(mapgenReliefs), -1))
	plate("mg-metal", 1, 3, "Metal", mapgenMetals[p.Metal], cycle(&p.Metal, len(mapgenMetals), 1), cycle(&p.Metal, len(mapgenMetals), -1))

	// The map code: the settings in one line, or one being typed.
	cy := y + 4*70*u
	df.Draw(screen, "Map code", x+2*u, cy-8*u, screenkit.Style{Size: 10.5 * u, Tracking: 0.16, Top: nlKicker, Upper: true})
	field := screenkit.Rect{X: x, Y: cy, W: colW, H: 40 * u}
	screenkit.Fill(screen, field, color.RGBA{4, 8, 4, 240})
	screenkit.Outline(screen, field, 1.5*u, color.RGBA{90, 110, 86, 255})
	code, tone := "", nlBody
	switch {
	case s.editing:
		code, tone = s.edit, nlCream
		if int(k.clock*2)%2 == 0 {
			code += "|"
		}
	case m != nil:
		code = m.Code
	}
	bf.Draw(screen, code, field.X+12*u, field.Y+26*u, fitStyle(bf, code, screenkit.Style{Verbatim: true, Size: 14 * u, Top: tone}, field.W-24*u, 9*u))
	hint := "Type a map code and press Enter to make that map again."
	bf.Draw(screen, hint, x, field.Y+field.H+22*u, fitStyle(bf, hint, screenkit.Style{Size: 11.5 * u, Top: nlDim}, colW, 8*u))

	// What was measured, start by start, and what it covers.
	fy := field.Y + field.H + 66*u
	df.Draw(screen, "Measured on this map", x+2*u, fy, screenkit.Style{Size: 10.5 * u, Tracking: 0.16, Top: nlKicker, Upper: true})
	if m != nil {
		s.drawMeasurements(screen, res, x, fy+14*u, colW)
	}

	// The map, its starts numbered as the setup assigns them.
	px := x + colW + 40*u
	psize := 400 * u
	pic := screenkit.Rect{X: px, Y: r.Y + 70*u, W: psize, H: psize}
	screenkit.Fill(screen, pic, color.RGBA{0, 0, 0, 255})
	if img := s.picture(g, m); img != nil {
		// Letterboxed into the square, as the picker fits a minimap.
		scale := float64(max(m.Width, m.Height) * 16)
		iw, ih := float64(m.Width*16)/scale*psize, float64(m.Height*16)/scale*psize
		fit := screenkit.Rect{X: pic.X + (psize-iw)/2, Y: pic.Y + (psize-ih)/2, W: iw, H: ih}
		screenkit.Image(screen, img, fit, 1, false)
		for i, st := range m.Starts {
			sx, sy := fit.X+float64(st.X)/scale*psize, fit.Y+float64(st.Z)/scale*psize
			screenkit.Disc(screen, sx, sy, 9*u, color.RGBA{0, 0, 0, 200})
			screenkit.Disc(screen, sx, sy, 7.5*u, nlAmber)
			bf.Draw(screen, strconv.Itoa(i+1), sx, sy+4.5*u, screenkit.Style{Verbatim: true, Size: 11 * u, Top: color.RGBA{20, 16, 8, 255}, Align: 1})
		}
	}
	if running {
		screenkit.Fill(screen, pic, color.RGBA{0, 0, 0, 120})
		label := i18n.T(stage)
		if total > 0 {
			label = fmt.Sprintf("%s %d%%", label, done*100/total)
		}
		bf.Draw(screen, label, pic.X+psize/2, pic.Y+psize/2+5*u, screenkit.Style{Verbatim: true, Size: 14 * u, Top: nlCream, Align: 1, Shadow: 0.1})
	}
	screenkit.Outline(screen, pic, u, color.RGBA{90, 96, 80, 255})
	if m != nil {
		name := mapgenName(res.world, m)
		ty := pic.Y + pic.H + 34*u
		right := r.X + r.W - 32*u - px
		df.Draw(screen, name, px, ty, fitStyle(df, name, screenkit.Style{Verbatim: true, Size: 20 * u, Tracking: 0.04, Top: nlCream}, right, 12*u))
		facts := fmt.Sprintf("%s · %s · %s", i18n.T(fmt.Sprintf("Players: %d", len(m.Starts))), fmt.Sprintf("%d x %d", m.Params.Size, m.Params.Size), i18n.T(fmt.Sprintf("Metal deposits: %d", m.Deposits)))
		bf.Draw(screen, facts, px, ty+26*u, fitStyle(bf, facts, screenkit.Style{Verbatim: true, Size: 12 * u, Top: nlKicker}, right, 9*u))
		note := "Use plays it without keeping it; Save keeps it in the map library."
		bf.DrawWrapped(screen, note, px, ty+44*u, right, 1.45, screenkit.Style{Size: 12 * u, Top: alphaC(nlBody, 0.8)})
	}

	// The foot.
	by := r.Y + r.H - 64*u
	another := screenkit.Rect{X: r.X + 32*u, Y: by, W: 220 * u, H: 42 * u}
	s.plateButton(screen, "mg-another", another, "Another map", 14*u, false, false)
	k.hits.Add(screenkit.Region{ID: "mg-another", Rect: another, Click: func() {
		s.change(func(p *mapgen.Params) { p.Seed = mapgenSeed() })
	}})
	can := s.ready() != nil
	use := screenkit.Rect{X: r.X + r.W - 32*u - 170*u, Y: by, W: 170 * u, H: 42 * u}
	s.plateButton(screen, "mg-use", use, "Use", 15*u, can, !can)
	k.hits.Add(screenkit.Region{ID: "mg-use", Rect: use, Disable: !can, Click: func() { s.use(g) }})
	save := screenkit.Rect{X: use.X - 12*u - 170*u, Y: by, W: 170 * u, H: 42 * u}
	s.plateButton(screen, "mg-save", save, "Save…", 15*u, false, !can)
	k.hits.Add(screenkit.Region{ID: "mg-save", Rect: save, Disable: !can, Click: s.startNaming})
	cl := screenkit.Rect{X: save.X - 12*u - 150*u, Y: by, W: 150 * u, H: 42 * u}
	s.plateButton(screen, "mg-close", cl, "Close", 15*u, false, false)
	k.hits.Add(screenkit.Region{ID: "mg-close", Rect: cl, Click: func() {
		g.playMenuCue("SmallButton")
		s.closePanel()
	}})
	message := s.message
	if message == "" && jobErr != nil && !running {
		message = generatorReason(jobErr)
	}
	if message != "" && !s.naming {
		mx := another.X + another.W + 24*u
		st := screenkit.Style{Size: 12.5 * u, Top: nlAmber}
		lines := bf.Wrap(message, st, cl.X-24*u-mx)
		st.Verbatim = true
		for j, l := range lines[:min(len(lines), 2)] {
			bf.Draw(screen, l, mx, by+17*u+float64(j)*19*u, st)
		}
	}
}

// drawMeasurements is the measurements of §5 as a table, a column per start
// and a row per measurement with its spread, and what they cover.
func (s *mapgenScreen) drawMeasurements(screen *ebiten.Image, r *mapgenResult, x, y, width float64) {
	k := s.kit
	u := k.u()
	bf := k.fonts.Body
	m := r.m
	rows := []struct {
		label  string
		values []int64
		div    int64
		spread int
	}{
		{"Route to nearest enemy", m.Balance.Path.Values, mapgen.StepCost, m.Balance.Path.Spread},
		{"Building room", m.Balance.Room.Values, 1, m.Balance.Room.Spread},
		{"Metal in own zone", m.Balance.Metal.Values, 1, m.Balance.Metal.Spread},
	}
	labelW, spreadW := 170*u, 62*u
	col := (width - labelW - spreadW) / float64(max(1, len(m.Starts)))
	head := screenkit.Style{Verbatim: true, Size: 11 * u, Top: nlKicker, Align: 1}
	for i := range m.Starts {
		bf.Draw(screen, strconv.Itoa(i+1), x+labelW+col*(float64(i)+0.5), y+14*u, head)
	}
	bf.Draw(screen, "Spread", x+width-spreadW/2, y+14*u, screenkit.Style{Size: 11 * u, Top: nlKicker, Align: 1})
	for j, row := range rows {
		ry := y + 38*u + float64(j)*24*u
		bf.Draw(screen, row.label, x, ry, fitStyle(bf, row.label, screenkit.Style{Size: 12 * u, Top: nlBody}, labelW-8*u, 8*u))
		for i, v := range row.values {
			text := strconv.FormatInt(v/row.div, 10)
			bf.Draw(screen, text, x+labelW+col*(float64(i)+0.5), ry, fitStyle(bf, text, screenkit.Style{Verbatim: true, Size: 12 * u, Top: alphaC(nlBody, 0.9), Align: 1}, col-4*u, 7*u))
		}
		spread := fmt.Sprintf("%d%%", (row.spread+5)/10)
		tone := nlGreenText
		if row.spread >= 100 {
			tone = nlAmber
		}
		bf.Draw(screen, spread, x+width-spreadW/2, ry, screenkit.Style{Verbatim: true, Size: 12 * u, Top: tone, Align: 1})
	}
	scope := fmt.Sprintf("Measured for ground units of the %s movement class: the travel to the nearest enemy start, in cells; the level, dry, open cells within 30 cells' travel of a start; and the deposit metal in the part of the map a start reaches first. They compare this map's starts with each other and promise nothing beyond that.", strings.ToUpper(r.class))
	bf.DrawWrapped(screen, scope, x, y+38*u+3*24*u, width, 1.45, screenkit.Style{Size: 11.5 * u, Top: nlDim})
}

// drawSaveForm is the Save form over the panel: a name and a description,
// Cancel and Save.
func (s *mapgenScreen) drawSaveForm(screen *ebiten.Image, g *gameShell) {
	k := s.kit
	u := k.u()
	df, bf := k.fonts.Display, k.fonts.Body
	screenkit.Fill(screen, screenkit.Rect{W: k.w(), H: k.h()}, color.RGBA{0, 0, 0, 150})
	k.hits.Add(screenkit.Region{ID: "mg-save-block", Rect: screenkit.Rect{W: k.w(), H: k.h()}})
	w, h := 700*u, 380*u
	r := screenkit.Rect{X: k.w()/2 - w/2, Y: k.h()/2 - h/2, W: w, H: h}
	k.well(screen, r, 1)
	df.Draw(screen, "Save to the map library", r.X+32*u, r.Y+50*u, screenkit.Style{Size: 22 * u, Tracking: 0.04, Top: nlCream})
	fieldAt := func(i int, label, value string, y float64) {
		df.Draw(screen, label, r.X+34*u, y-8*u, screenkit.Style{Size: 10.5 * u, Tracking: 0.16, Top: nlKicker, Upper: true})
		f := screenkit.Rect{X: r.X + 32*u, Y: y, W: w - 64*u, H: 40 * u}
		screenkit.Fill(screen, f, color.RGBA{4, 8, 4, 240})
		edge := color.RGBA{90, 110, 86, 255}
		if s.field == i {
			edge = nlAmber
			if int(k.clock*2)%2 == 0 {
				value += "|"
			}
		}
		screenkit.Outline(screen, f, 1.5*u, edge)
		bf.Draw(screen, value, f.X+12*u, f.Y+26*u, fitStyle(bf, value, screenkit.Style{Verbatim: true, Size: 14 * u, Top: nlCream}, f.W-24*u, 8*u))
		k.hits.Add(screenkit.Region{ID: fmt.Sprintf("mg-field-%d", i), Rect: f, Click: func() { s.field = i }})
	}
	fieldAt(0, "Name", s.name, r.Y+104*u)
	fieldAt(1, "Description", s.description, r.Y+184*u)
	if s.message != "" {
		bf.Draw(screen, s.message, r.X+32*u, r.Y+252*u, fitStyle(bf, s.message, screenkit.Style{Size: 12.5 * u, Top: nlAmber}, w-64*u, 8*u))
	}
	bw := 170 * u
	ok := screenkit.Rect{X: r.X + w - 32*u - bw, Y: r.Y + h - 66*u, W: bw, H: 42 * u}
	s.plateButton(screen, "mg-save-ok", ok, "Save", 15*u, true, false)
	k.hits.Add(screenkit.Region{ID: "mg-save-ok", Rect: ok, Click: func() { s.save(g) }})
	no := screenkit.Rect{X: ok.X - 12*u - bw, Y: ok.Y, W: bw, H: 42 * u}
	s.plateButton(screen, "mg-save-no", no, "Cancel", 15*u, false, false)
	k.hits.Add(screenkit.Region{ID: "mg-save-no", Rect: no, Click: func() { s.naming, s.message = false, "" }})
}
