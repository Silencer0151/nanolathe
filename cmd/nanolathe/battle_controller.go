package main

import (
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/clock"
	committedframe "github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

// BattleMouseButtons is the logical mouse state consumed by the battle
// controller. Coordinates and button state are expressed in the negotiated
// 640x480 battle surface, not in device pixels [07 §1][07 §8].
type BattleMouseButtons = input.MouseButtons

// BattleModifiers is the platform-neutral modifier state. Keeping modifiers
// in the logical frame makes a replay independent of Ebitengine's key names.
type BattleModifiers = input.Modifiers

// BattleInputFrame is one presentation input sample. It is deliberately a
// value type: the Ebitengine adapter and deterministic replays both submit the
// same frame to BattleController.Step, while the controller alone decides
// which canonical battle command (if any) is issued [07 §8][07 §9].
//
// PressedKeys and HeldKeys carry the complete keyboard state so production
// hotkeys continue to use the existing input decision path. The slices are
// copied by the controller before use; callers may reuse their frame storage.
type BattleInputFrame = input.Sample

// monotonicMillisSource adapts Go's monotonic process clock to the wrapping
// 32-bit millisecond contract. time.Since uses the monotonic component of the
// captured start value when one is available, and the uint32 conversion is
// intentionally allowed to wrap [01 §4.1][01 §4.2].
type monotonicMillisSource struct {
	start time.Time
}

var monotonicHostStart = time.Now()

func newMonotonicMillisSource() *monotonicMillisSource {
	return &monotonicMillisSource{start: monotonicHostStart}
}

func (s *monotonicMillisSource) Millis32() uint32 {
	if s == nil {
		return 0
	}
	return uint32(time.Since(s.start) / time.Millisecond)
}

// BattleController is the single production/replay input seam. It owns only
// presentation input state and timing; authoritative mutation remains in the
// existing battleSession.handleInput and Session.Step calls.
type BattleController struct {
	shiftHeld bool

	battle            *battleSession
	millis            clock.MillisSource
	cursorScaled      int32
	cursorScaledValid bool
	// stepLocked says the budget's sample is locked to the host step
	// (stepScaled): on for the wall clock, off for an injected source.
	// lockedScaled is the last locked sample.
	stepLocked   bool
	lockedScaled int32
	lockedValid  bool
}

// NewBattleController constructs the production controller. An optional
// source is provided for deterministic replays and tests; omitted sources use
// the monotonic host clock. The variadic form preserves the existing call site
// while keeping source injection at the composition boundary.
func NewBattleController(b *battleSession, sources ...clock.MillisSource) *BattleController {
	var source clock.MillisSource
	if len(sources) > 0 {
		source = sources[0]
	}
	if source == nil && b != nil {
		source = b.millisSource
	}
	if source == nil {
		source = newMonotonicMillisSource()
	}
	if b != nil {
		b.millisSource = source
	}
	_, wall := source.(*monotonicMillisSource)
	return &BattleController{battle: b, millis: source, stepLocked: wall}
}

// stepScaled is the scaled time a host step's tick budget reads [01 §4.1].
//
// The host steps at 30 Hz of wall time and the scaled timebase is a 30 Hz
// count of the same milliseconds, so a step reads the timebase at whatever
// phase the two happen to have. Near a unit boundary a millisecond of timing
// jitter decides whether a step reads the old unit or the new one: sampled raw,
// one step released no tick and the next two, and the presentation froze for
// a tick and then skipped one — seen in 2 of 3 traced 30 s battles, every few
// seconds while the phases stayed close (DESIGN_GPU_RENDERER §13.5).
//
// On the wall clock the sample is therefore locked to the host step: each step
// reads one unit past the last. A step that finds the raw timebase still
// behind the last sample holds it, and one that finds the raw timebase two or
// more units ahead — the first step, or a stall longer than the host clock
// replays — takes the raw value. The budget's arithmetic, carry and clamp are
// untouched; only the phase of the sample moves, by less than a unit, and its
// rate is the wall clock's because the host clock's is. An injected source
// (shots, films, replays, tests) is read raw.
func (c *BattleController) stepScaled(raw int32) int32 {
	if !c.stepLocked {
		return raw
	}
	if !c.lockedValid {
		c.lockedScaled, c.lockedValid = raw, true
		return raw
	}
	next := c.lockedScaled + 1
	switch {
	case raw-c.lockedScaled >= 2:
		next = raw
	case raw < c.lockedScaled:
		next = c.lockedScaled
	}
	c.lockedScaled = next
	return next
}

// sampleMillis is the millisecond a host step's budget reads [01 §4.1]. On the
// wall clock it is the step's ideal instant when the window names one
// (client.HostStepDue): the step's body can run a refresh or two after it —
// deferred to a modern Draw's tail, behind a cap-skipped Draw or a refresh the
// display dropped — and read at the body, that lateness moved the sample
// across a unit boundary often enough that a late step released two ticks and
// the presented world leapt half a tick. Read at the ideal instant, the sample
// keeps the host clock's own phase however late the body runs. Like the lock,
// it moves only the phase of the sample. An injected source is read as it is.
func (c *BattleController) sampleMillis(cl *client.Client) uint32 {
	if src, ok := c.millis.(*monotonicMillisSource); ok && src != nil && c.stepLocked && cl != nil {
		if due := cl.HostStepDue(); !due.IsZero() && due.After(src.start) {
			return uint32(due.Sub(src.start) / time.Millisecond)
		}
	}
	return c.millis.Millis32()
}

// Step feeds one logical input frame through the production battle decision
// path and advances the existing presentation-to-simulation budget. Elapsed
// remains part of the input value for presentation callers, but is not a
// timing authority; every budget sample comes from Millis32 [01 §4.1].
func (c *BattleController) Step(frame BattleInputFrame, cl *client.Client) {
	if c == nil || c.battle == nil {
		return
	}
	c.battle.stepFollowCamera()
	state := input.StateFromSample(frame)
	if c.battle.sess != nil {
		if c.battle.sim != nil || c.battle.onlineBattle() {
			// Async batches are observed after joining; granted online ticks
			// are observed by the host pump before input. Neither uses the
			// synchronous wall-clock observer below.
			c.battle.sess.SetPublicationObserver(nil)
		} else {
			c.battle.sess.SetPublicationObserver(func(cur *committedframe.Frame) {
				c.battle.applyPublishedCamera(cur)
				// Observe every committed position before a catch-up tick replaces it.
				// Recording alone skips history at high speed or after a slow frame,
				// suppressing hover dust (DESIGN_GPU_RENDERER §26). Repeated capture
				// and draw observations are idempotent; this consumes only frames [I6].
				cl.ObserveCommittedTick()
			})
		}
		// Facts of ticks no observer saw (a host that stepped the session
		// itself) reach the local state before input reads it.
		c.battle.syncLocalInterface()
		shift := state.Kbd.HasShift()
		if cl != nil && cl.Input() != nil {
			shift = cl.Input().Kbd.HasShift()
		}
		if shift != c.shiftHeld {
			// Held Shift pauses BigBrother, local interface state
			// (DESIGN_MULTIPLAYER §7.1).
			if c.battle.enqueueHumanCommand(session.HumanCommand{Kind: session.HumanShiftState, ShiftHeld: shift}) == nil {
				c.shiftHeld = shift
			}
		}
	}
	// Whichever joined tick the next presentation pins carries the local
	// state this step left, composed while the simulation is quiescent.
	defer c.battle.finishLocalInterfaceStep()
	// Retail handles follow hotkeys after the sub-tick batch. Keep these
	// presentation-only requests pending while automatic cycles consume it.
	c.battle.deferFollowInput = true
	c.battle.handleInput(state, cl)
	c.battle.deferFollowInput = false
	c.battle.serviceSmartMakers()
	defer func() {
		if pending := c.battle.pendingFollowInput; pending != nil {
			c.battle.pendingFollowInput = nil
			if c.battle.sim != nil && c.battle.sim.pending != nil {
				// Retail handles these after the sub-tick batch, which now runs
				// after this step; the join that completes it runs them.
				c.battle.sim.followAfterBatch = pending
				return
			}
			pending()
		}
	}()
	if c.battle.ended || c.battle.sess == nil || c.battle.sess.Clock == nil {
		return
	}
	// Battle entry installs its loaded-model registry once. Client replacement
	// cannot change phase-7 ownership or pause model textures; the session
	// invokes that registry at every runnable sub-tick [01 §4.4][R-CRD-005 §1].
	scaled := int32(0)
	if c.millis != nil {
		scaled = clock.ScaledNow(c.sampleMillis(cl))
	}
	if cl != nil {
		if c.cursorScaledValid {
			delta := scaled - c.cursorScaled
			if delta > 0 {
				cl.StepCursorScaledDelta(delta)
			}
		} else {
			c.cursorScaledValid = true
		}
		c.cursorScaled = scaled
	} else {
		c.cursorScaledValid = false
	}
	if c.battle.onlineBattle() {
		return
	}
	budget := c.stepScaled(scaled)
	if c.battle.sim != nil {
		var due time.Time
		if cl != nil && c.stepLocked {
			due = cl.HostStepDue()
		}
		released := c.battle.prepareSimulationStep(budget, due)
		if cl != nil {
			cl.NoteTicksReleased(released)
		}
		return
	}
	c.battle.sess.Step(budget)
	c.battle.noteTickTiming()
}
