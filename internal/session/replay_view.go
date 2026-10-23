package session

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// The playback perspective (docs/DESIGN_MULTIPLAYER.md §10 "Viewing is
// presentation"). In a single-player battle the local and viewing owners are
// simulation inputs: the viewing slot chooses whose sensor pass runs at its
// settlement deadline, whose units leave temporary sight and what `+view`
// moves, and the canonical checkpoint hashes both. A playback therefore never
// moves them. It sets this override instead, which only publication reads,
// after every authoritative read of the tick is done.

// PresentationPerspective chooses what playback draws without touching the
// simulation's local or viewing owner, which are simulation inputs in a
// single-player battle. Override false draws as the battle always has.
// RevealAll draws the whole map with no fog.
type PresentationPerspective struct {
	Owner     uint8
	Override  bool
	RevealAll bool
}

// SetPresentationPerspective sets the perspective publication draws from.
// It changes no random stream, checksum or canonical digest, and takes effect
// at the next publication; a paused playback shows it at once through
// RepublishPresentation. It may be called while a pump runs on another
// goroutine.
//
// An overridden owner publishes that player's coverage, line of sight, fog
// and contacts. Its fog is derived for it beside the simulation viewer's
// cache, which stays that viewer's. A single-player battle keeps one sensor
// bank on the units, the viewing owner's, so the overridden owner's contacts
// come from a presentation bank derived at the real pass's cadence; an
// online battle keeps one per seat already (DESIGN_MULTIPLAYER §16.4.1).
// Placement previews, the audio audience and the online effect filter keep
// the simulation's viewing owner.
func (s *Session) SetPresentationPerspective(p PresentationPerspective) error {
	if s == nil {
		return fmt.Errorf("nanolathe: presentation perspective refused: logical path session, providers searched [session], expected a session")
	}
	if p.Override && p.Owner > 9 {
		return fmt.Errorf("nanolathe: presentation perspective refused: logical path owner %d, providers searched [session], expected a player 0..9", p.Owner)
	}
	if !p.Override {
		p.Owner = 0
	}
	r := &s.seatCommands.replay
	r.mu.Lock()
	r.perspective = p
	r.mu.Unlock()
	return nil
}

// RepublishPresentation publishes the committed tick again from unchanged
// state, so a perspective change shows while playback is paused. It is the
// paused-input boundary's republication with no input applied
// (DESIGN_INTERFACE_HUD_INPUT §3.12): no tick runs, no input is drained and
// no authoritative state changes. Call it between pumps, as every session
// call that is not documented otherwise. It reports whether a frame was
// published.
func (s *Session) RepublishPresentation() bool {
	if s == nil || s.Snapshot == nil || s.Clock == nil || s.State != StateBattle {
		return false
	}
	if _, published := s.Snapshot.PublishedTick(); !published {
		return false
	}
	// The boundary's leading act: the interface facts staged for the last
	// publication were delivered with it and must not be delivered again.
	s.resetBigBrotherEvents()
	s.publishPausedSnapshot(s.Clock.GlobalTick)
	if s.publicationObserver != nil {
		s.publicationObserver(s.Snapshot.Current())
	}
	return true
}

// publicationPerspective is what one publication draws: the viewer whose
// sight it publishes and whether the whole map is revealed.
func (s *Session) publicationPerspective() (viewer uint8, reveal bool) {
	r := &s.seatCommands.replay
	r.mu.Lock()
	p := r.perspective
	r.mu.Unlock()
	viewer = s.ViewingOwner
	if p.Override {
		viewer = p.Owner
	}
	return viewer, p.RevealAll
}

// publishPerspectiveView finishes the published visibility channels for the
// perspective, after the authoritative visibility and fog work of the
// publication has run unchanged. Revealing publishes every cell visible and
// explored under an identity of its own, so no frame slot restores it as the
// viewer's real coverage and no presentation cache confuses the two; the fog
// is not drawn. Another viewer's fog was published by publishPerspectiveFog.
func (s *Session) publishPerspectiveView(published *frame.Frame, reveal bool) {
	if s.Vis == nil || !reveal {
		return
	}
	if w, h := s.Vis.GridDimensions(); w > 0 && h > 0 {
		n := int(w) * int(h)
		v := &published.Visibility
		v.Visible = fillBytesInto(v.Visible, n, 1)
		v.WordVisible = fillWordsInto(v.WordVisible, n, 0xffff)
		v.W, v.H = w, h
		v.CoverageBytes, v.Valid = true, true
		v.MappingSource, v.MappingVersion = 0, 0
		if source := s.Vis.PresentationIdentity(); source != 0 {
			// Real sources count up from one; the complement never
			// meets one.
			v.MappingSource, v.MappingVersion = ^source, 1
		}
	}
	published.Fog.Valid = false
	published.Fog.Version, published.Fog.Source = 0, 0
}

// perspectiveView is the storage a playback draws a viewer other than the
// simulation's viewing owner from: that viewer's fog and, in a single-player
// battle, its sensor contacts (DESIGN_MULTIPLAYER §10). It is presentation
// only: the simulation reads none of it, and none of it is saved,
// checkpointed or hashed. Publication, which is serialized with the ticks,
// owns it; the unit channel's workers only read its bank.
type perspectiveView struct {
	fog        visibility.FogCache
	fogViewer  uint8
	fogVersion uint64 // revision of the fog bytes and their viewer; zero before the first

	sensors      visibility.PresentationSensors
	sensorViewer uint8
	sensorsValid bool
	sensorsAt    uint64 // realPasses when the bank was derived
	// realPasses counts the simulation's own sensor passes
	// (stepSensorPhaseFor). Only the bank's cadence reads it.
	realPasses uint64
	// The bank pass's inputs, built as the real pass builds its own.
	units  []visibility.SensorUnit
	sliced []*units.Unit
	masks  []uint16
}

// perspectiveSensorBank readies the overridden viewer's presentation sensor
// bank for this publication's unit channel and reports whether the channel
// reads it. Only a single-player battle drawing a viewer other than its
// viewing owner has one: its units carry the viewing owner's sensor bits,
// which the simulation reads, while an online battle keeps a bank per seat
// already (DESIGN_MULTIPLAYER §16.4.1).
//
// The bank is the five sensor passes run for the viewer
// (visibility.SensorTickForPresentation) on the inputs the real pass builds,
// and it is latched as the real bits are: it is derived again only when a
// real pass has run since, when the viewer changes — a paused playback's
// view change republishes — or when there is none yet. It runs before the
// channel's workers start.
func (s *Session) perspectiveSensorBank(viewer uint8) bool {
	if viewer == s.ViewingOwner || viewer > 9 || s.onlineResults != nil || s.Vis == nil || s.Units == nil {
		return false
	}
	v := &s.unitChannel.perspective
	if v.sensorsValid && v.sensorViewer == viewer && v.sensorsAt == v.realPasses {
		return true
	}
	v.sensorsValid, v.sensorViewer, v.sensorsAt = true, viewer, v.realPasses
	active := s.activePlayerCount()
	if active <= 1 {
		// A one-player battle runs no pass: every unit keeps the bits its
		// construction gave it, which for this viewer are the seeds
		// (perspectiveView.sensorStatus).
		v.sensors.Clear()
		return true
	}
	v.units = s.appendSensorUnits(v.units[:0], &v.sliced, &v.masks)
	s.Vis.SensorTickForPresentation(&v.sensors, visibility.PlayerID(viewer), s.sensorViewerDefeated(int(viewer)), active, v.units)
	return true
}

// sensorStatus reads one unit's status word from the bank. A unit
// the last bank pass did not see reads as its construction seeds it for the
// viewer: no friendly pair and no jam marker, and the sonar exemption only on
// the viewer's own unit (initializeSensorStatus).
func (v *perspectiveView) sensorStatus(viewer uint8, u *units.Unit) uint32 {
	if status, ok := v.sensors.Status(uint16(u.Handle), u.AllocationSerial, u.Flags); ok {
		return status
	}
	return seededSensorStatus(u.Flags, u.Owner, viewer, true)
}

// publishPerspectiveFog publishes a viewer's own fog when it is not the
// simulation's viewing owner: the full-map fog the publication rebuilds for
// the visibility service's local player, derived from this viewer's grids
// into the playback's storage (visibility.DeriveFogFor). The service's fog
// cache, its revision and its cell marks stay the viewing owner's. The source
// is the complement of the visibility identity, which real sources, counting
// up from one, never meet, and the revision moves whenever the bytes or the
// viewer change, so no frame slot restores one fog as the other and no
// presentation cache confuses them.
func (s *Session) publishPerspectiveFog(published *frame.Frame, viewer uint8) {
	v := &s.unitChannel.perspective
	changed, ok := s.Vis.DeriveFogFor(&v.fog, visibility.PlayerID(viewer))
	source := s.Vis.PresentationIdentity()
	if !ok || source == 0 {
		published.Fog.Valid = false
		published.Fog.Version, published.Fog.Source = 0, 0
		return
	}
	if changed || v.fogViewer != viewer || v.fogVersion == 0 {
		v.fogViewer = viewer
		v.fogVersion++
	}
	source = ^source
	if !published.RestoreFog(source, v.fogVersion) {
		published.Fog.W, published.Fog.H = v.fog.Dimensions()
		published.Fog.OriginX, published.Fog.OriginZ = v.fog.Origin()
		// One copy into this frame slot's own storage, as the real fog's.
		published.Fog.Ch0, published.Fog.Ch1 = v.fog.CopyChannelsInto(published.Fog.Ch0, published.Fog.Ch1)
		published.Fog.Version, published.Fog.Source = v.fogVersion, source
	}
	published.Fog.Valid = true
}

func fillBytesInto(dst []uint8, n int, v uint8) []uint8 {
	if cap(dst) < n {
		dst = make([]uint8, n)
	}
	dst = dst[:n]
	for i := range dst {
		dst[i] = v
	}
	return dst
}

func fillWordsInto(dst []uint16, n int, v uint16) []uint16 {
	if cap(dst) < n {
		dst = make([]uint16, n)
	}
	dst = dst[:n]
	for i := range dst {
		dst[i] = v
	}
	return dst
}
