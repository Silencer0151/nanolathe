package client

import (
	"github.com/nanolathe-gg/nanolathe/internal/camera"
	"github.com/nanolathe-gg/nanolathe/internal/drawlist"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	presentationrender "github.com/nanolathe-gg/nanolathe/internal/render"
)

// The carrier staging image [R-REN-03A §4].
//
// Retail does not blit a carrier and then paint its children over the top.
// Presentation of a unit that has attached children takes one of two paths,
// chosen on whether the carrier's own composition image has a key plane:
//
//   - **No key plane.** The cached image is blitted, and then every live piece
//     and every attached child is rasterized straight to the framebuffer in
//     painter order. Nothing resolves per pixel because there is no plane to
//     resolve against.
//   - **Key plane present.** A staging image is prepared whose box is the union
//     of the carrier's own box with the boxes of all its attached children,
//     offset by each child's world position relative to the carrier. The cached
//     image is copied into it, both planes. Each child is then composed into its
//     own two-plane image and composited into the staging image with the key
//     test, at the child's pixel offset and with the child's world-height
//     difference added to every key it contributes. The waterline and digger
//     passes run over the staging image, and the staging image is blitted once.
//
// The consequence is per-pixel occlusion between a carrier and its cargo: a
// transport's hull can stand in front of the unit it is carrying, and a
// factory's plate in front of the nanoframe on it, because the two resolve
// against one height plane rather than by draw order.

// stagingChild is one attached child ready to be composited: its finished
// composition — reveal and nanoframe outline already applied to its own image
// [R-COMP-01 §3] — and the key offset that puts its heights on the carrier's
// scale.
type stagingChild struct {
	model    composedModel
	keyDelta int32
	// Only the staging view moves; the child retains its shadow/trace anchor.
	rasterDX, rasterDY int32
}

func (child stagingChild) stagingTarget() modelTarget {
	if child.model.image == nil {
		return modelTarget{}
	}
	target := *child.model.image
	target.anchorX += child.rasterDX
	target.anchorY += child.rasterDY
	return target
}

// stagingDisplacement keeps classic composition in native raster space before
// its final scaled blit (DESIGN_GPU_RENDERER §14.2). Project the world anchors
// at raster scale: inverting their rounded screen difference loses pixels at
// fractional view scales. The carrier retains its actual framebuffer anchor.
func (c *Client) stagingDisplacement(parent, child composedModel) (int32, int32) {
	if c == nil || c.cam == nil || c.modelBlitScale().Native() {
		return 0, 0
	}
	rasterCamera := *c.cam
	rasterCamera.Scale = c.modelScale()
	p, ch := parent.draw.WorldPos, child.draw.WorldPos
	px, py := rasterCamera.WorldToScreen(p[0], p[1], p[2])
	cx, cy := rasterCamera.WorldToScreen(ch[0], ch[1], ch[2])
	ax, ay := c.modelAnchor(parent.draw)
	return ax + cx - px - child.image.anchorX,
		ay + cy - py - child.image.anchorY
}

// composeCarrier presents one unit together with its attached children.
//
// It returns whether the CARRIER itself was drawn, which is what the caller
// uses to decide selection chrome; a child that fails to compose is skipped
// exactly as it would be on its own.
func (c *Client) composeCarrier(v frame.UnitView, sx, sy int32, children []frame.UnitView) bool {
	if c == nil {
		return false
	}
	if c.geometryOnlyModels && len(children) != 0 {
		return c.recordCarrierGeometry(v, children)
	}
	if len(children) == 0 {
		return c.drawUnitModel(v, sx, sy)
	}
	carrier, cached, ok := c.prepareUnitModelState(v, false)
	if !ok {
		// The carrier has no resolvable model of its own. Its children are
		// still real units and still present, each on its own.
		for i := range children {
			c.drawChildModel(children[i])
		}
		return false
	}
	if carrier.direct {
		live, drawn := c.composeDirectLiveModel(carrier.draw, unitTeamColor(v), unitPresentationID(v), modelCursorUnit, carrier.directLane, c.selectedModelSkin(v.InstanceID, v.Owner))
		if drawn {
			c.finishModel(live, nil)
		}
		for i := range children {
			c.drawChildModel(children[i])
		}
		return drawn
	}
	if carrier.image == nil {
		return false
	}
	if carrier.image.height == nil {
		// No key plane: the cached image is blitted and each child is
		// rasterized straight to the framebuffer after it, in painter order.
		// The carrier's own live lane is part of that direct pass too; it is
		// separate from attached children but must still follow the cached body
		// [R-REN-03A §4].
		c.finishModel(carrier, nil)
		id := unitPresentationID(v)
		if live, ok := c.composeDirectLiveModel(carrier.draw, unitTeamColor(v), id, modelCursorUnit, presentationrender.PieceLaneLive, c.selectedModelSkin(v.InstanceID, v.Owner)); ok {
			c.emitModel(pendingModelCommit{m: live, blit: live.image, body: true, trace: true})
		}
		for i := range children {
			c.drawChildModel(children[i])
		}
		return true
	}
	// Key plane present: compose each child into its own image, then composite
	// the lot into one staging image and blit that [R-REN-03A §4].
	staged := make([]stagingChild, 0, len(children))
	for i := range children {
		child, ok := c.composeChildModel(children[i])
		if !ok {
			continue
		}
		dx, dy := c.stagingDisplacement(carrier, child)
		staged = append(staged, stagingChild{
			model:    child,
			rasterDX: dx, rasterDY: dy,
			// Retail adds only the child's world-height difference, even when
			// actual attachments have different Digger key bases. Independent
			// factory occupants must have matching bases before joining this
			// path [R-REN-03A §2][R-REN-03A §4]; presentation design §5.
			keyDelta: int32(int64(children[i].Y)>>16) - int32(int64(v.Y)>>16),
		})
	}
	if len(staged) == 0 {
		if cached {
			carrier = c.stageCachedUnitModel(carrier, v, false, nil)
		}
		c.finalizeModelImage(carrier.image, carrier.draw, v.Owner, modelCursorUnit)
		c.finishModel(carrier, nil)
		return true
	}
	var staging *modelTarget
	if cached {
		// Cargo and current parent geometry determine the final box before
		// the cached seed, reveal, or live raster writes [03 R-REN-03A §4].
		carrier = c.stageCachedUnitModel(carrier, v, false, staged)
		staging = carrier.image
	} else {
		// Standalone previews have already composed their all-piece image;
		// retain that adapter's covered-pixel copy instead of re-seeding it
		// as if it were a raw cached lane.
		staging = stagingBox(carrier.image, staged, c.borrowModelImage)
		staging.copyCoveredFrom(carrier.image)
	}
	// Freeze the parent's own shadow before children write into the union.
	// Its source remains the completed parent, as in the existing classic
	// shadow adapter; the later body command must not build it a second time.
	if carrier.draw.CastsShadow {
		c.emitModel(pendingModelCommit{m: carrier, shadow: true})
	}
	for i := range staged {
		target := staged[i].stagingTarget()
		staging.compositeChild(&target, staged[i].keyDelta)
		if carrier.geometry != nil && staged[i].model.geometry != nil {
			carrier.geometry.Children = append(carrier.geometry.Children, drawlist.ModelChild{Geometry: staged[i].model.geometry, KeyDelta: staged[i].keyDelta})
		}
	}
	c.finalizeModelImage(staging, carrier.draw, v.Owner, modelCursorUnit)
	c.emitModel(pendingModelCommit{m: carrier, blit: staging, body: true, trace: true})
	// A child's parity trace is resolved only now: the pixels it describes are
	// final once the staging image is on the framebuffer.
	for i := range staged {
		c.finishStagedChild(staged[i])
	}
	return true
}

// recordCarrierGeometry follows the classic preparation and shadow/body commit
// order, retaining only geometry. Keyless or missing carriers leave children
// in the ordinary painter path [03 R-REN-03A §4].
// CachedSeed on each ordinary keyed packet preserves the source dimensions
// before the executor chooses its final union [03 R-REN-03A §4].
// TODO(question): independently close Enhanced shadow-source chronology and
// changed cached-pose bounds through retail callers before changing either.
func (c *Client) recordCarrierGeometry(v frame.UnitView, children []frame.UnitView) bool {
	carrier, carrierLive := c.unitGeometryPair(v, false)
	if carrier == nil || !carrier.KeyPlane {
		if carrier != nil {
			c.list.RecordModel(drawlist.Model{Geometry: carrier})
		}
		if carrierLive != nil {
			c.list.RecordModel(drawlist.Model{Geometry: carrierLive})
		}
		for _, child := range children {
			g, live := c.unitGeometryPair(child, false)
			if g != nil {
				c.list.RecordModel(drawlist.Model{Geometry: g})
			}
			if live != nil {
				c.list.RecordModel(drawlist.Model{Geometry: live})
			}
		}
		return carrier != nil
	}
	for _, child := range children {
		g, _ := c.unitGeometryPair(child, true)
		if g == nil {
			continue
		}
		deferChildFinalPasses(g)
		c.list.RecordModel(drawlist.Model{Geometry: g, ShadowOnly: true})
		delta := int32(int64(child.Y)>>16) - int32(int64(v.Y)>>16)
		carrier.Children = append(carrier.Children, drawlist.ModelChild{Geometry: g, KeyDelta: delta})
	}
	c.list.RecordModel(drawlist.Model{Geometry: carrier})
	return true
}

// finishStagedChild resolves and emits a composited child's parity trace after
// the staging image reaches the framebuffer.
//
// Nothing of the child is drawn here. Retail composes a carried child into its
// own two-plane image, runs the nanoframe reveal AND the outline over that
// image, and only then composites it into the carrier's staging image under
// the key test — so the carrier's geometry occludes the wireframe of a product
// on its plate wherever it occludes the product's body [R-COMP-01 §3]. The
// outline therefore lives in composeModel, and an overdraw here after the blit
// would put it in front of geometry retail puts it behind.
func (c *Client) finishStagedChild(child stagingChild) {
	if c == nil || child.model.image == nil {
		return
	}
	if child.model.raster != nil && child.model.raster.trace != nil {
		// The trace reads c.indexed after the staging body reaches the framebuffer,
		// so it is recorded as a trace-only model command after the carrier's own
		// command; replaying resolves it against the committed pixels exactly as the
		// inline resolve did, without a direct read during the recording pass
		// (WU-1.8).
		c.emitModel(pendingModelCommit{m: child.model, trace: true})
	}
}

// composeUnitModel builds one unit's draw record and composes it, without
// committing. It is drawUnitModel's first half.
func (c *Client) composeUnitModel(v frame.UnitView) (composedModel, bool) {
	return c.composeUnitModelState(v, false, true)
}

// A child forces a two-plane image; a carrier defers its water/digger pass
// until attached children have joined it [03 R-REN-03A §4].
func (c *Client) composeUnitModelState(v frame.UnitView, child, finalPasses bool) (composedModel, bool) {
	m, cached, ok := c.prepareUnitModelState(v, child)
	if !ok {
		return composedModel{}, false
	}
	if cached {
		m = c.stageCachedUnitModel(m, v, child, nil)
	}
	if finalPasses && m.image != nil {
		c.finalizeModelImage(m.image, m.draw, v.Owner, modelCursorUnit)
	}
	return m, true
}

// prepareUnitModelState returns the read-only keyed cached image. Its caller
// places it and seeds a frame-owned staging target before any reveal
// or live drawing. Keyless and standalone adapters are already composed.
func (c *Client) prepareUnitModelState(v frame.UnitView, child bool) (result composedModel, cached, valid bool) {
	// Retail tints the cached/staged image blit; direct polygons remain their
	// ordinary lane [03 R-RAST-01 §7]. Developer modes take the same image
	// branch [03 §3.12]; neither belongs in retained pixels.
	defer func() { result.cloaked = (v.Cloaked || c.developer.Mode != 0) && !result.direct }()
	draw, ok := c.unitDrawFor(v)
	if !ok {
		return composedModel{}, false, false
	}
	if child {
		draw.KeyPlane = true
	}
	reveal, outline := c.unitNanoframeReveal(v)
	id := unitPresentationID(v)
	// Slot zero is the pool's null sentinel. Standalone preview/test poses may
	// intentionally use it without publication revisions, so they take the
	// non-retained all-piece adapter rather than pretending to be a live unit.
	if id == 0 || !c.modelScratch.active {
		m, ok := c.composeModelLane(draw, v.Owner, unitTeamColor(v), id, modelCursorUnit, reveal, outline, presentationrender.PieceLaneAll, false, c.selectedModelSkin(v.InstanceID, v.Owner))
		return m, false, ok
	}
	body := c.cachedBody(id)
	missing := body == nil || body.image == nil || body.cacheRevision != v.CacheRevision
	required := draw.Structure || draw.KeyPlane
	orient := c.orientationCache(id)
	if c.cachedBodyMustRebuild(body, v, draw, orient) || missing && required || draw.KeyPlane && body != nil && body.image != nil && body.image.height == nil {
		cached, built := c.composeModelLane(draw, v.Owner, unitTeamColor(v), id, modelCursorUnit, nil, 0, presentationrender.PieceLaneCached, false, c.selectedModelSkin(v.InstanceID, v.Owner))
		if !built {
			return composedModel{}, false, false
		}
		c.replaceCachedBody(id, v, draw, cached.image)
		body = c.cachedBody(id)
		missing = false
		// A settings or script rebuild may still use the retained orientation.
		// Advance its reference only when this draw crossed the orientation gate;
		// otherwise the key would describe angles never rasterized [03 §5.2].
		if draw.NeedsRebuild {
			orient.UpdateKey(draw.Model.Name, v.Heading, v.Pitch, v.Bank)
		}
	}
	if missing || body == nil || body.image == nil {
		// A valid image-less mobile with no required key plane takes the direct
		// all-piece route. The direct framebuffer target is
		// installed by the no-key consumer; until then retain its full-pose body
		// rather than inventing a key plane [03 R-REN-03A §4].
		// Do not advance the orientation reference here: it belongs to an actual
		// cached-body rebuild, and changing it for a direct pose would lose a
		// sequence of sub-threshold turns [03 §5.2].
		return composedModel{draw: draw, direct: true, directLane: presentationrender.PieceLaneAll}, false, true
	}
	if body.image.height == nil {
		base := c.cachedBodyImage(body, draw)
		return composedModel{image: base, raster: base, draw: draw}, false, base != nil
	}
	// Retained pixels remain immutable until the union copies them once
	// directly into the final staging allocation.
	return composedModel{image: body.image, draw: draw}, true, true
}

// stageCachedUnitModel seeds the final parent/live/cargo union once, then
// applies reveal and live geometry in their owning order [03 R-REN-03A §4].
func (c *Client) stageCachedUnitModel(m composedModel, v frame.UnitView, child bool, children []stagingChild) composedModel {
	draw := m.draw
	w, h, ox, oy, _ := c.projectedModelExtent(draw, presentationrender.PieceLaneAll, false)
	ax, ay := c.modelAnchor(draw)
	extent := modelTarget{width: w, heightPx: h, originX: ox, originY: oy, anchorX: ax, anchorY: ay}
	base := *m.image
	base.anchorX, base.anchorY, base.blit = ax, ay, camera.ViewScaleNative
	stage := stagingImage(&base, children, c.borrowModelImage, &extent)
	reveal, outline := c.unitNanoframeReveal(v)
	if !child {
		c.revealModelImage(stage, draw, reveal, outline)
	}
	if !(draw.Structure && draw.UnderConstruction) {
		c.drawLivePieces(stage, draw, unitTeamColor(v), unitPresentationID(v), c.selectedModelSkin(v.InstanceID, v.Owner))
	}
	if child {
		c.revealModelImage(stage, draw, reveal, outline)
	}
	m.image, m.raster = stage, stage
	return m
}

// drawLivePieces writes into the already-seeded union. A live key-colored
// texel still writes its key and color; no later staging copy may filter it
// as cached data [03 R-REN-03A §4/§5].
func (c *Client) drawLivePieces(stage *modelTarget, draw *presentationrender.UnitDraw, selector teamColor, id uint64, skins ...*ModelSkin) {
	polys := c.collectDrawPolysLane(draw, selector, id, modelCursorUnit, presentationrender.PieceLaneLive, skins...)
	placeFaces(polys, stage.originX, stage.originY, 1)
	for i := range polys {
		if polys[i].frame != nil {
			c.blitTexturedPolyTarget(stage, &polys[i], polys[i].frame, id)
		} else {
			c.fillPolyTarget(stage, &polys[i], polys[i].color, id)
		}
	}
}

// composeChildModel composes one attached child for the staging path.
//
// The child is given a key plane whether or not its own definition authors
// one: [R-REN-03A §4] step 2 composes each attached child into "its own
// two-plane image" before compositing it, and a child with no key plane would
// contribute no heights for the staging key test to resolve against.
func (c *Client) composeChildModel(v frame.UnitView) (composedModel, bool) {
	if c == nil || c.cam == nil {
		return composedModel{}, false
	}
	m, ok := c.composeUnitModelState(v, true, false)
	if !ok {
		return composedModel{}, false
	}
	// A carried child's own shadow is still its own subject's business, and it
	// is blitted to the framebuffer rather than into the staging image
	// [R-REN-03D §1]. It is recorded as a shadow-only model command here, before
	// the carrier's own command, so replaying the frame writes the child shadows
	// under the carrier body in the order the direct blits ran and the recording
	// pass touches c.indexed nowhere (WU-1.8). The shadow reads the child's
	// finished image, which the staging composite only reads and never mutates, so
	// a deferred replay sees the same pixels the inline blit did.
	deferChildFinalPasses(m.geometry)
	c.emitModel(pendingModelCommit{m: m, shadow: true})
	return m, true
}

// drawChildModel presents one attached child on its own, which is the painter
// path of [R-REN-03A §4] and the fallback whenever no staging image is built.
func (c *Client) drawChildModel(v frame.UnitView) {
	if c == nil || c.cam == nil {
		return
	}
	sx, sy := c.cam.WorldToScreen(v.X, v.Y, v.Z)
	c.drawUnitModel(v, sx-camera.OriginX, sy-camera.OriginY)
}

// stagingImage allocates the staging image and copies the carrier's cached
// image into it, both planes [R-REN-03A §4].
//
// The box is the union in composition raster space, anchored at the carrier.
// Each stagingTarget carries the child's relative raster displacement: at native
// scale this is already its screen-anchor offset; magnified classic composition
// normalizes that offset before the completed union is scaled on the blit.
//
// Recording passes a frame-owned image slot as allocate; standalone callers pass
// newModelImage for independently allocated storage. Both use the same union and
// composition.
func stagingImage(body *modelTarget, children []stagingChild, allocate func(int, int, int32, int32, int32, int32, bool, int32) *modelTarget, extra ...*modelTarget) *modelTarget {
	stage := stagingBox(body, children, allocate, extra...)
	stage.seedCachedFrom(body)
	return stage
}

func stagingBox(body *modelTarget, children []stagingChild, allocate func(int, int, int32, int32, int32, int32, bool, int32) *modelTarget, extra ...*modelTarget) *modelTarget {
	if body == nil {
		return nil
	}
	left, top := body.screenX(0), body.screenY(0)
	right, bottom := body.screenX(int32(body.width)-1), body.screenY(int32(body.heightPx)-1)
	extend := func(ch *modelTarget) {
		if ch == nil || ch.width == 0 || ch.heightPx == 0 {
			return
		}
		if l := ch.screenX(0); l < left {
			left = l
		}
		if t := ch.screenY(0); t < top {
			top = t
		}
		if r := ch.screenX(int32(ch.width) - 1); r > right {
			right = r
		}
		if b := ch.screenY(int32(ch.heightPx) - 1); b > bottom {
			bottom = b
		}
	}
	for i := range children {
		ch := children[i].stagingTarget()
		extend(&ch)
	}
	for _, ch := range extra {
		extend(ch)
	}
	// The staging image keeps the carrier's anchor, so its origin is whatever
	// puts the union's top-left corner at image pixel (0,0).
	originX := body.anchorX - left
	originY := body.anchorY - top
	staging := allocate(int(right-left+1), int(bottom-top+1), originX, originY, body.anchorX, body.anchorY, body.height != nil, 1)
	return staging
}

// seedCachedFrom follows the two plane-copy branches of [03 R-REN-03A §4].
// Equal dimensions copy raw bytes. A resized target copies each plane keyed
// independently against the source transparent index, including the key plane.
func (t *modelTarget) seedCachedFrom(src *modelTarget) {
	if t == nil || src == nil {
		return
	}
	if t.width == src.width && t.heightPx == src.heightPx {
		copy(t.color, src.color)
		copy(t.covered, src.covered)
		copy(t.height, src.height)
		return
	}
	for sy := 0; sy < src.heightPx; sy++ {
		iy := t.imageY(src.screenY(int32(sy)))
		for sx := 0; sx < src.width; sx++ {
			ix := t.imageX(src.screenX(int32(sx)))
			if ix < 0 || iy < 0 || ix >= int32(t.width) || iy >= int32(t.heightPx) {
				continue
			}
			si, di := sy*src.width+sx, int(iy)*t.width+int(ix)
			if src.color[si] != src.transparent {
				t.color[di], t.covered[di] = src.color[si], true
			}
			if t.height != nil && src.height != nil && src.height[si] != src.transparent {
				t.height[di] = src.height[si]
			}
		}
	}
}

// copyCoveredFrom retains the standalone all-piece preview adapter's copy.
// It receives a finished image, never a raw retained cached lane.
func (t *modelTarget) copyCoveredFrom(src *modelTarget) {
	if t == nil || src == nil {
		return
	}
	for sy := 0; sy < src.heightPx; sy++ {
		iy := t.imageY(src.screenY(int32(sy)))
		if iy < 0 || iy >= int32(t.heightPx) {
			continue
		}
		row := int(iy) * t.width
		base := sy * src.width
		for sx := 0; sx < src.width; sx++ {
			si := base + sx
			if !src.covered[si] {
				continue
			}
			ix := t.imageX(src.screenX(int32(sx)))
			if ix < 0 || ix >= int32(t.width) {
				continue
			}
			di := row + int(ix)
			t.color[di], t.covered[di] = src.color[si], true
			if t.height != nil && src.height != nil {
				t.height[di] = src.height[si]
			}
		}
	}
}

// compositeChild composites one child's finished image into the staging image
// with the key test [R-REN-03A §4] step 2.
//
// A child pixel is written when it is not the child image's transparent index
// and `stagingKey <= childKey + heightDelta`. That is the same comparison the
// span writers apply within one image, which is why it is the image's own
// admission and not a second rule: what makes it a cross-unit test is the
// height delta, which puts the child's keys on the carrier's scale.
//
// The comparison is made at full width — both stored bytes widened, the
// signed height delta added to the child's — and only the STORE narrows: the
// staging plane keeps the low byte of `childKey + heightDelta`, wrapping
// modulo 256 rather than saturating. Retail's composite forms the sum in a
// register and stores a byte add [R-REN-03A §4]. A child far enough above its
// carrier to leave the byte range therefore wins the comparison and then
// stores a small key, so later children and the digger/waterline passes see
// it as low; stock cargo and factory products sit within a few tens of world
// units of their carrier and never reach the boundary.
func (t *modelTarget) compositeChild(child *modelTarget, keyDelta int32) {
	if t == nil || child == nil {
		return
	}
	for cy := 0; cy < child.heightPx; cy++ {
		iy := t.imageY(child.screenY(int32(cy)))
		if iy < 0 || iy >= int32(t.heightPx) {
			continue
		}
		row := int(iy) * t.width
		base := cy * child.width
		for cx := 0; cx < child.width; cx++ {
			ci := base + cx
			if child.color[ci] == child.transparent {
				continue // the child image's own transparent index
			}
			ix := t.imageX(child.screenX(int32(cx)))
			if ix < 0 || ix >= int32(t.width) {
				continue
			}
			di := row + int(ix)
			if t.height == nil {
				t.color[di], t.covered[di] = child.color[ci], true
				continue
			}
			shifted := keyDelta
			if child.height != nil {
				shifted += int32(child.height[ci])
			}
			if int32(t.height[di]) > shifted {
				continue
			}
			t.height[di] = wrapKeyByte(shifted)
			t.color[di], t.covered[di] = child.color[ci], true
		}
	}
}

// wrapKeyByte narrows a shifted key to the plane's byte store the way retail's
// byte add does: the low eight bits, wrapping [R-REN-03A §4]. This replaces a
// saturating clamp that was this client's own choice before the store width
// was traced.
func wrapKeyByte(v int32) uint8 {
	return uint8(v)
}

// unitDrawFor builds one unit's draw record: the piece states, the authored
// gates the composer reads, and the nanoframe inputs. It is the part of
// drawUnitModel that precedes composition.
func (c *Client) unitDrawFor(v frame.UnitView) (*presentationrender.UnitDraw, bool) {
	return c.buildUnitDraw(v, false)
}

// unitDrawDeferred is unitDrawFor with the pieces' geometry left for the
// caller to materialize by lane (render.BuildUnitDrawDeferredInto).
func (c *Client) unitDrawDeferred(v frame.UnitView) (*presentationrender.UnitDraw, bool) {
	return c.buildUnitDraw(v, true)
}

func (c *Client) buildUnitDraw(v frame.UnitView, deferred bool) (*presentationrender.UnitDraw, bool) {
	if c == nil || c.cam == nil {
		return nil, false
	}
	if c.arrivalHidesUnit(v) {
		return nil, false
	}
	v = c.arrivalUnit(v)
	m := c.modelForUnit(v)
	if m == nil || m.compiled == nil {
		return nil, false
	}
	states := c.modelStates(m, v.Pieces)
	build := presentationrender.BuildUnitDrawInto
	if deferred {
		build = presentationrender.BuildUnitDrawDeferredInto
	}
	draw := build(m.compiled, states, v.Heading, v.Pitch, v.Bank, v, c.orientationCache(unitPresentationID(v)), c.borrowDrawScratch())
	// BMcode=0 is the structure class [R-RND-02A]; a unit under construction
	// always gets the height plane because the nanoframe reveal reads it
	// [R-REN-03A §2].
	draw.Structure = !v.BMCode
	draw.UnderConstruction = v.BuildRemaining > 0
	draw.KeyPlane = v.ZBuffer || v.BuildRemaining > 0
	// Digger selects its silhouette branch before the structure/mobile split;
	// even a structure-class Digger must pass the vehicle gates [R-REN-03D §1].
	draw.CastsShadow = c.castsModelShadow(v.NoShadow, v.CanHover, v.Floater, draw.Structure && !v.Digger)
	draw.GroundY = c.unitShadowGroundHeight(v.X, v.Z, v.MoverMode == 2)
	draw.Airborne = c.enhanced && v.MoverMode == 2
	draw.DiggerClip = v.Digger
	if v.Digger {
		// The Digger key bias is applied to every vertex, so the clip
		// threshold and the composed keys stay on one scale [R-REN-03A §8].
		draw.KeyPlane = true
	}
	return draw, true
}

// The child's own present is skipped; only its carrier clips the union
// [03 R-REN-03A §4][03 R-RAST-01 §7-A]. Reveal and shadow remain child inputs.
func deferChildFinalPasses(g *drawlist.ModelGeometry) {
	if g == nil {
		return
	}
	g.Waterline, g.WaterlineKey = drawlist.ModelWaterlineNone, 0
	g.Digger, g.DiggerKey = false, 0
	if g.Supersample != nil {
		deferChildFinalPasses(g.Supersample)
	}
}
