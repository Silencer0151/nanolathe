// Package lockstep drives a session from sealed relay grants. Socket timing is
// host state; it never enters the authoritative tick (DESIGN_MULTIPLAYER §16.4).
package lockstep

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/relay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

type localRead struct {
	grant relay.LocalGrant
	err   error
}

// Client carries the sealed stream. It supports one ReadGrant caller and
// serialized concurrent Submit/Acknowledge calls. ReadGrant returns io.EOF only
// for explicit normal completion; a lost connection must return another error.
// Close unblocks all operations. No client method accesses the session.
type Client interface {
	Submit([]byte) (uint64, error)
	ReadGrant() (relay.LocalGrant, error)
	Acknowledge(tick uint32, checksum [32]byte, ended, final bool) error
	Close() error
}

// LocalDriver has one host caller for Pump/Submit. Its reader owns no session
// pointers. Delivery is bounded while the window is busy: one entry for the
// local acknowledgment-paced prototype, 32 for continuous hosted grants.
type LocalDriver struct {
	sess                 *session.Session
	client               Client
	reads                chan localRead
	done                 chan struct{}
	closeOnce            sync.Once
	position             uint64
	appliedLocalSequence uint64 // host-only receipt watermark, not a stream position
	err                  error
	ended                bool
	playout              *playoutClock
	readError            chan error
	// The resource double-click producer has one pending first-click move.
	moveSequence, movePosition uint64
	cancel                     *session.SeatCommand
}

// AppliedLocalSequence is the last local client sequence whose grant has run.
// It lets host input assistants bound outstanding commands without mistaking a
// shared stream position for a client's sequence or inspecting live units.
func (d *LocalDriver) AppliedLocalSequence() uint64 {
	if d == nil {
		return 0
	}
	return d.appliedLocalSequence
}

func NewLocalDriver(s *session.Session, c *relay.LocalClient) (*LocalDriver, error) {
	if c == nil {
		return nil, localError("entry", "a prepared tick-zero online battle and connection")
	}
	return newDriver(s, c, nil)
}

// NewPacedDriver plays continuous grants at normal speed, with a small reserve
// against jitter. Monotonic timing remains host state (DESIGN_MULTIPLAYER §16.5.2).
func NewPacedDriver(s *session.Session, c Client) (*LocalDriver, error) {
	return newDriver(s, c, &playoutClock{now: time.Now})
}

func newDriver(s *session.Session, c Client, playout *playoutClock) (*LocalDriver, error) {
	if s == nil || c == nil || !s.OnlineCommandContext() || s.Clock == nil || s.Clock.GlobalTick != 0 || s.State != session.StateBattle || s.IsPendingBattle() {
		return nil, localError("entry", "a prepared tick-zero online battle and connection")
	}
	size := 1
	if playout != nil {
		size = pacedReceiveLimit
	}
	d := &LocalDriver{sess: s, client: c, reads: make(chan localRead, size), done: make(chan struct{}), playout: playout}
	if playout != nil {
		// A transport failure must not sit behind grants and let a failed room
		// keep advancing. Completion uses this lane too, after the final ACK.
		d.readError = make(chan error, 1)
	}
	go d.read()
	return d, nil
}

func (d *LocalDriver) read() {
	for {
		grant, err := d.client.ReadGrant()
		if err != nil && d.readError != nil {
			d.readError <- err
			return
		}
		select {
		case d.reads <- localRead{grant, err}:
		case <-d.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (d *LocalDriver) Close() error {
	if d == nil {
		return nil
	}
	var err error
	d.closeOnce.Do(func() { close(d.done); err = d.client.Close() })
	return err
}

// Completed reports explicit normal relay completion, after the shared final
// tick. A local result, explicit Close or transport failure alone cannot set it.
// The host reads it from the same caller that owns Pump/Submit.
func (d *LocalDriver) Completed() bool {
	return d != nil && d.ended
}

// Pump never advances without a complete grant and never executes a zero-tick
// pump. Session owns payload decoding, authorization and deterministic effects.
func (d *LocalDriver) Pump() (bool, error) {
	if d.err != nil {
		return false, d.err
	}
	if d.ended {
		return false, nil
	}
	select {
	case <-d.done:
		return false, io.ErrClosedPipe
	default:
	}
	if d.playout == nil {
		return d.step()
	}
	// The window host pumps once per 30 Hz step, so a step may owe two ticks;
	// each still runs through its own StepGranted (DESIGN_MULTIPLAYER §16.5.2).
	if stop, err := d.pacedStop(); stop {
		return false, err
	}
	advanced := false
	for i := range d.playout.due(len(d.reads)) {
		if i > 0 {
			if stop, err := d.pacedStop(); stop {
				return advanced, err
			}
		}
		ran, err := d.step()
		advanced = advanced || ran
		if err != nil || !ran {
			return advanced, err
		}
	}
	return advanced, nil
}

// pacedStop reports a transport outcome that overtakes buffered grants, or the
// shared terminal state after which no buffered grant may run.
func (d *LocalDriver) pacedStop() (bool, error) {
	select {
	case err := <-d.readError:
		if errors.Is(err, io.EOF) && d.sess.OnlineBattleEnded() {
			d.discardGrants()
			d.ended = true
			return true, nil
		}
		return true, d.fail(err)
	default:
	}
	if d.sess.OnlineBattleEnded() {
		// The relay may already have sealed more ticks when the final ACK
		// arrives. Drain them, without decoding or simulating any command,
		// until the relay explicitly confirms both replicas ended together.
		d.discardGrants()
		return true, nil
	}
	return false, nil
}

// step runs at most one received grant.
func (d *LocalDriver) step() (bool, error) {
	select {
	case r := <-d.reads:
		if r.err != nil {
			if errors.Is(r.err, io.EOF) && d.sess.OnlineBattleEnded() {
				d.ended = true
				return false, nil
			}
			return false, d.fail(r.err)
		}
		g := r.grant
		if g.Tick != d.sess.Clock.GlobalTick+1 || g.Position < d.position {
			return false, d.fail(localError("grant", "the next tick and a monotonic sealed stream"))
		}
		// Decode the complete grant before queueing any prefix of it. The
		// stamp is the relay's, so a broken one fails the battle. The payload
		// is the seat's: one this build cannot decode is skipped on every
		// replica alike, as an invalid command is rejected (§7.2).
		commands := make([]session.SeatCommand, len(g.Commands))
		decoded := make([]bool, len(g.Commands))
		pos := d.position
		for i, c := range g.Commands {
			if c.Seat >= relay.HostedMaxSeats || c.Sequence == 0 || c.Position != pos+1 || c.Position > g.Position {
				return false, d.fail(localError("grant command", "contiguous stream positions and admitted seats"))
			}
			var err error
			commands[i], err = session.DecodeSeatCommand(session.OnlineCommand, c.Payload)
			decoded[i] = err == nil
			pos = c.Position
		}
		if pos != g.Position {
			return false, d.fail(localError("grant seal", "the complete sealed command prefix"))
		}
		for i, c := range g.Commands {
			if !decoded[i] {
				continue
			}
			if err := d.sess.EnqueueSeatCommand(session.CommandStamp{Seat: c.Seat, Tick: g.Tick, Position: c.Position}, commands[i]); err != nil {
				return false, d.fail(err)
			}
			if c.Seat == d.sess.LocalOwner && c.Sequence == d.moveSequence {
				d.movePosition = c.Position
			}
		}
		if err := d.sess.StepGranted(g.Tick); err != nil {
			return false, d.fail(err)
		}
		d.position = g.Position
		for _, c := range g.Commands {
			if c.Seat == d.sess.LocalOwner {
				d.appliedLocalSequence = max(d.appliedLocalSequence, c.Sequence)
			}
		}
		var hash [32]byte
		if g.Tick%30 == 0 {
			hash = d.sess.UnitStateChecksum()
		}
		// A seat whose own result is final may leave a hosted match.
		final := d.sess.ResultForSeat(d.sess.LocalOwner).Ended
		if err := d.client.Acknowledge(g.Tick, hash, d.sess.OnlineBattleEnded(), final); err != nil {
			return true, d.fail(err)
		}
		if d.cancel != nil && d.movePosition != 0 && !d.sess.OnlineBattleEnded() {
			command := *d.cancel
			command.CancelQueuedMove.Sequence = d.movePosition
			if _, err := d.send(command); err != nil {
				return true, d.fail(err)
			}
			d.cancel = nil
			d.moveSequence, d.movePosition = 0, 0
		}
		return true, nil
	default:
		return false, nil
	}
}

// Submit captures references while the host owns the quiescent session. The
// relay chooses the tick and stream position; no optimistic world mutation.
func (d *LocalDriver) Submit(c session.HumanCommand) (uint64, error) {
	if d.err != nil {
		return 0, d.err
	}
	select {
	case <-d.done:
		return 0, io.ErrClosedPipe
	default:
	}
	if d.ended || d.sess.OnlineBattleEnded() {
		return 0, localError("command", "a running battle")
	}
	if c.Kind == session.HumanOrder && c.Order.TrackQueuedMove && d.cancel != nil {
		return 0, localError("tracked move", "the previous pending cancellation to finish")
	}
	waiting := false
	if c.Kind == session.HumanCancelQueuedMove {
		if c.CancelQueuedMove.Sequence == 0 || c.CancelQueuedMove.Sequence != d.moveSequence {
			return 0, localError("cancel move", "the current pending resource-click receipt")
		}
		waiting = d.movePosition == 0
		c.CancelQueuedMove.Sequence = d.movePosition
		if waiting {
			c.CancelQueuedMove.Sequence = 1
		} // validated now, replaced before sending
	}
	command, err := d.sess.CaptureOnlineCommand(c)
	if err != nil {
		return 0, err
	}
	if waiting {
		d.cancel = &command
		return 0, nil
	}
	seq, err := d.send(command)
	if err != nil {
		return 0, d.fail(err)
	}
	if c.Kind == session.HumanOrder && c.Order.TrackQueuedMove {
		d.moveSequence, d.movePosition = seq, 0
	}
	if c.Kind == session.HumanCancelQueuedMove {
		d.moveSequence, d.movePosition = 0, 0
	}
	return seq, nil
}

func (d *LocalDriver) send(c session.SeatCommand) (uint64, error) {
	payload, err := session.EncodeSeatCommand(session.OnlineCommand, c)
	if err != nil {
		return 0, err
	}
	return d.client.Submit(payload)
}
func (d *LocalDriver) fail(err error) error { d.err = err; _ = d.Close(); return err }
func localError(path, expected string) error {
	return fmt.Errorf("nanolathe: multiplayer lockstep failed: logical path %s, providers searched [relay grants], expected %s", path, expected)
}

// Bound the work even if the receiver is concurrently adding surplus grants.
func (d *LocalDriver) discardGrants() {
	for range pacedReceiveLimit {
		select {
		case <-d.reads:
		default:
			return
		}
	}
}
