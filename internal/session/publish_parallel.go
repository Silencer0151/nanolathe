package session

import (
	"sync"
	"sync/atomic"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// The unit channel: every live unit's committed view, order-queue view and
// radar contact, built on joined worker goroutines while the calling
// goroutine builds the rest of the frame (INVARIANTS I1 "Parallel simulation
// exception", DESIGN_RUNTIME_DETERMINISM §2.5 "Parallel publication").
//
// The output is the serial builder's, byte for byte, whatever the workers'
// scheduling:
//
//   - Everything order-dependent is decided on the calling goroutine before a
//     worker starts: the live-unit list in pool-slot order (I1), each unit's
//     presentation identity in that order, which units publish an order queue,
//     and so the index of every unit's view, contact and queue view.
//   - The list is cut into fixed-size chunks, a function of the unit count
//     alone. A chunk writes only its own units' elements, at their final
//     indices, so the chunks are disjoint whichever goroutine claims one; the
//     calling goroutine claims the chunks no worker has reached once its own
//     part of the frame is built.
//   - Workers read only committed state that nothing writes until the frame is
//     committed: the tick is over, and the calling goroutine's overlapped work
//     writes the frame's other channels and its own caches, never a unit, a
//     queue, a route or a service a worker reads. The fog rebuild — the one
//     publication step that writes a word a worker's visibility sample reads —
//     runs before the channel starts (publishFrame), as does a playback's
//     presentation sensor pass, whose bank the workers read and which swaps
//     the visibility service's sensor diagnostics while it runs
//     (replay_view.go).
//   - The frame is committed only after every worker has joined.
//
// A session with too few units for the fork to pay builds the channel inline,
// through the same chunk loop, on the calling goroutine.

const (
	// publishChunkUnits is how many units one claim of the channel builds.
	publishChunkUnits = 32
	// publishWorkers is the most worker goroutines one publication starts;
	// the calling goroutine works beside them. Fixed, so the split never
	// depends on the host (I1). Four measured best on HugeBattle: two and
	// three left the publication longer, six spent more processor time
	// without shortening it (docs/SIM_BENCHMARK.md "Publication comparison").
	publishWorkers = 4
	// publishParallelMinUnits is the live-unit count below which the channel
	// is built inline: starting and joining workers costs more than they save.
	publishParallelMinUnits = 4 * publishChunkUnits
)

// unitChannel is one publication's unit channel and the storage it reuses
// from tick to tick. It belongs to the calling goroutine between
// publications; during one, workers read its inputs and write the elements of
// the chunks they claim.
type unitChannel struct {
	live    []*units.Unit // live units, pool slot ascending (I1)
	ids     []uint64      // presentation identity per live unit
	queued  []bool        // whether the unit's order queue is published
	queueAt []int32       // per chunk, the index of its first queue view

	views    []frame.UnitView
	queues   []frame.OrderQueueView
	contacts []frame.RadarContactView

	viewer uint8
	reveal bool
	// bank says this publication's status words come from the perspective's
	// presentation sensor bank, for a viewer other than the viewing owner
	// (replay_view.go), rather than from the simulation's own.
	bank   bool
	chunks int
	next   atomic.Int64 // the next unclaimed chunk

	// scratch is one staging record per executor: [0] for the calling
	// goroutine, [w+1] for worker w.
	scratch []unitPublishScratch
	wg      sync.WaitGroup
	// failure carries a worker's panic to the calling goroutine, which
	// raises it after the join exactly as the inline build would have.
	failure atomic.Pointer[workerPanic]
	pending bool

	// perspective is the storage a playback draws another viewer from
	// (replay_view.go). It lives with the channel, whose workers read its
	// sensor bank, and outlasts each publication.
	perspective perspectiveView
}

type workerPanic struct{ value any }

// startUnitChannel decides every order-dependent part of the unit channel on
// the calling goroutine and starts the workers. finishUnitChannel must follow
// before anything reads the frame's unit, order-queue or radar channels. bank
// selects the perspective's presentation sensor bank, which must be complete
// before this is called.
func (s *Session) startUnitChannel(published *frame.Frame, viewer uint8, reveal, bank bool) *unitChannel {
	c := &s.unitChannel
	publication := s.ensurePublicationState()
	// The Community reload table is built on first use; build it here so no
	// worker writes it (community_hud.go).
	s.communityReloadNames()
	c.live = s.Units.AppendLive(c.live[:0]) // pool slot ascending (I1)
	n := len(c.live)
	c.viewer, c.reveal, c.bank = viewer, reveal, bank
	c.ids = resizeKeep(c.ids, n)
	c.queued = resizeKeep(c.queued, n)
	c.chunks = (n + publishChunkUnits - 1) / publishChunkUnits
	c.queueAt = resizeKeep(c.queueAt, c.chunks)
	queues := int32(0)
	for i, u := range c.live {
		if i%publishChunkUnits == 0 {
			c.queueAt[i/publishChunkUnits] = queues
		}
		// Identities are allocated in slot order from one counter [I6].
		c.ids[i] = publication.unitIdentity(u)
		c.queued[i] = unitQueuePublished(u)
		if c.queued[i] {
			queues++
		}
	}
	// Grow each channel to its final length in place. The reservations keep
	// the bodies Frame.Reset retained beneath the truncated length, so every
	// element's nested piece, cargo, order and ring storage is reused.
	c.views = published.Units[:0]
	for len(c.views) < n {
		c.views = reserveUnitView(c.views)
	}
	c.queues = published.OrderQueues[:0]
	for len(c.queues) < int(queues) {
		c.queues = reserveOrderQueueView(c.queues)
	}
	c.contacts = published.Radar.Contacts[:0]
	for len(c.contacts) < n {
		c.contacts = reserveRadarContact(c.contacts)
	}
	workers := 0
	if n >= publishParallelMinUnits && s.publishWorkerLimit >= 0 {
		workers = min(publishWorkers, c.chunks-1)
		if s.publishWorkerLimit > 0 {
			workers = min(workers, s.publishWorkerLimit)
		}
	}
	if len(c.scratch) < workers+1 {
		c.scratch = append(c.scratch, make([]unitPublishScratch, workers+1-len(c.scratch))...)
	}
	c.next.Store(0)
	c.failure.Store(nil)
	c.pending = true
	if workers > 0 {
		c.wg.Add(workers)
		for w := 0; w < workers; w++ {
			go s.unitChannelWorker(c, &c.scratch[w+1])
		}
	}
	return c
}

// unitChannelWorker claims and builds chunks until none is left.
func (s *Session) unitChannelWorker(c *unitChannel, scratch *unitPublishScratch) {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.failure.CompareAndSwap(nil, &workerPanic{value: r})
			// Stop the other executors claiming further chunks; the frame
			// is abandoned with the panic.
			c.next.Store(int64(c.chunks))
		}
	}()
	s.buildUnitChunks(c, scratch)
}

// buildUnitChunks builds every chunk the caller can claim.
func (s *Session) buildUnitChunks(c *unitChannel, scratch *unitPublishScratch) {
	for {
		k := int(c.next.Add(1) - 1)
		if k >= c.chunks {
			return
		}
		lo := k * publishChunkUnits
		hi := min(lo+publishChunkUnits, len(c.live))
		q := int(c.queueAt[k])
		for i := lo; i < hi; i++ {
			u := c.live[i]
			// The sensor status word is a pure read of the latched sensor
			// pass, read once for the view and the contact.
			status := s.publishedSensorStatus(c, u)
			if queue, ok := s.fillUnitView(&c.views[i], u, c.ids[i], c.queued[i], c.viewer, c.reveal, status, scratch); ok {
				setOrderQueueView(c.queues, q, queue, s.Catalog)
				q++
			}
			s.fillUnitContact(&c.contacts[i], u, c.viewer, c.reveal, status)
		}
	}
}

// publishedSensorStatus is the status word the channel publishes for u: the
// perspective's presentation bank when the channel reads it, otherwise the
// simulation's own word for the viewer.
func (s *Session) publishedSensorStatus(c *unitChannel, u *units.Unit) uint32 {
	if c.bank {
		return c.perspective.sensorStatus(c.viewer, u)
	}
	return s.sensorStatus(c.viewer, u)
}

// finishUnitChannel builds the chunks no worker has claimed, joins the
// workers and installs the channel on the frame. Contacts holds the unit
// contacts only; the projectile contacts follow them.
func (s *Session) finishUnitChannel(published *frame.Frame, c *unitChannel) {
	if c == nil || !c.pending {
		return
	}
	c.pending = false
	s.buildUnitChunks(c, &c.scratch[0])
	c.wg.Wait()
	if p := c.failure.Load(); p != nil {
		panic(p.value)
	}
	published.Units = c.views
	published.OrderQueues = c.queues
	published.Radar.Contacts = c.contacts
}

// abandonUnitChannel joins a channel that was started but never finished:
// it stops further claims and waits for the workers, so none outlives the
// publication that started it.
func (s *Session) abandonUnitChannel(c *unitChannel) {
	if c == nil || !c.pending {
		return
	}
	c.pending = false
	c.next.Store(int64(c.chunks))
	c.wg.Wait()
}

// resizeKeep returns s with length n, reusing its storage when it is large
// enough. Every element below n is overwritten by the caller.
func resizeKeep[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// reserveOrderQueueView is reserveUnitView's form for the order-queue channel.
func reserveOrderQueueView(dst []frame.OrderQueueView) []frame.OrderQueueView {
	if i := len(dst); i < cap(dst) {
		return dst[:i+1]
	}
	return append(dst, frame.OrderQueueView{})
}

// reserveRadarContact is reserveUnitView's form for the radar contact channel.
func reserveRadarContact(dst []frame.RadarContactView) []frame.RadarContactView {
	if i := len(dst); i < cap(dst) {
		return dst[:i+1]
	}
	return append(dst, frame.RadarContactView{})
}

// setOrderQueueView writes one unit's queue view at its final index, keeping
// the order storage that element held.
func setOrderQueueView(dst []frame.OrderQueueView, i int, src orders.SnapshotQueue, cat *content.Catalog) {
	primary, secondary := dst[i].Primary, dst[i].Secondary
	dst[i] = frame.OrderQueueView{
		Unit: src.Unit, PrimaryTruncated: src.PrimaryTruncated, SecondaryTruncated: src.SecondaryTruncated,
		Primary:   snapshotOrderViewsInto(primary, src.Primary, cat),
		Secondary: snapshotOrderViewsInto(secondary, src.Secondary, cat),
	}
}
