package observe

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
)

// Handlers is an EventSink that routes events to typed callbacks
// registered via the package-level On generic function. Registration is
// keyed by the event's Go type, so a callback registered for
// workflow.GoalEnded only fires on GoalEnded values — no type switches in
// consumer code. The registry doesn't need to know about specific producer
// packages; any value can be Fire'd, and any type used as E in On[E] will
// be routed.
//
// Dispatch is asynchronous via a bounded queue (default size 1024). When
// the queue is full, Fire returns immediately and the dropped event is
// counted in Dropped(). This is the explicit policy choice: a slow
// handler must never block the workflow's main thread.
//
// Handlers are invoked in registration order, one event at a time, on a
// dedicated goroutine. Handler panics are recovered and ignored — the
// goroutine never dies. Use Close to drain the queue and stop the worker
// at the end of a run.
type Handlers struct {
	queueSize int

	queue chan dispatch
	once  sync.Once
	stop  chan struct{}
	done  chan struct{}

	mu       sync.RWMutex
	handlers map[reflect.Type][]func(context.Context, any)

	dropped atomic.Int64
}

// dispatch is what we put on the queue: the context, the event, and the
// pre-resolved handler list captured at Fire time so a registration after
// Fire doesn't retroactively run.
type dispatch struct {
	ctx     context.Context
	event   any
	targets []func(context.Context, any)
}

// Limits caps the runtime resources a Handlers instance will consume.
// Today only the queue capacity matters; future fields (e.g. per-handler
// timeout, overflow action) belong here too. A zero value is valid and
// uses sensible defaults.
type Limits struct {
	// QueueSize is the bounded channel capacity for pending dispatches.
	// Values < 1 fall back to the default of 1024.
	QueueSize int
}

// NewHandlers builds a Handlers registry and starts its background
// dispatcher. Always pair with Close at the end of the run so the worker
// goroutine exits and the queue drains.
//
// Pass observe.Limits{} for defaults, or set QueueSize to size the
// drop-on-overflow buffer to your expected burst.
func NewHandlers(limits Limits) *Handlers {
	queueSize := limits.QueueSize
	if queueSize < 1 {
		queueSize = 1024
	}
	h := &Handlers{
		queueSize: queueSize,
		handlers:  make(map[reflect.Type][]func(context.Context, any)),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	h.queue = make(chan dispatch, h.queueSize)
	go h.run()
	return h
}

// On registers a typed handler keyed by the static type E. Multiple
// handlers can register for the same event type; they are invoked in
// registration order.
//
//	observe.On(reg, func(ctx context.Context, e workflow.GoalEnded) { ... })
func On[E any](h *Handlers, fn func(context.Context, E)) {
	var zero E
	t := reflect.TypeOf(zero)
	wrapped := func(ctx context.Context, ev any) {
		fn(ctx, ev.(E))
	}
	h.mu.Lock()
	h.handlers[t] = append(h.handlers[t], wrapped)
	h.mu.Unlock()
}

// Fire enqueues the event for asynchronous dispatch. Returns immediately;
// Dropped() increments if the queue is full.
func (h *Handlers) Fire(ctx context.Context, event any) {
	t := reflect.TypeOf(event)
	h.mu.RLock()
	targets := h.handlers[t]
	h.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	select {
	case h.queue <- dispatch{ctx: ctx, event: event, targets: targets}:
	default:
		h.dropped.Add(1)
	}
}

// Close signals the worker goroutine to stop, drains any queued events,
// and returns once the worker has exited. Calling Close more than once is
// safe — subsequent calls are no-ops.
func (h *Handlers) Close() error {
	h.once.Do(func() {
		close(h.stop)
		<-h.done
	})
	return nil
}

// Dropped returns the number of events the queue rejected because it was
// full. Useful as a metric / health signal.
func (h *Handlers) Dropped() int64 {
	return h.dropped.Load()
}

// run is the dispatcher loop. It exits when stop closes; before exiting it
// drains anything still in the queue so handlers registered for in-flight
// events still fire.
func (h *Handlers) run() {
	defer close(h.done)
	for {
		select {
		case d := <-h.queue:
			h.invoke(d)
		case <-h.stop:
			// Drain remaining events. The queue has a fixed capacity, so
			// this loop terminates.
			for {
				select {
				case d := <-h.queue:
					h.invoke(d)
				default:
					return
				}
			}
		}
	}
}

// invoke runs every captured target for one dispatch, recovering panics so
// a misbehaving handler can't kill the dispatcher.
func (h *Handlers) invoke(d dispatch) {
	for _, fn := range d.targets {
		func() {
			defer func() { _ = recover() }()
			fn(d.ctx, d.event)
		}()
	}
}
