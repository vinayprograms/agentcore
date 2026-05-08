package observe

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// handlers_test.go covers handlers.go: typed registration via On[E],
// dispatch by event type, multiple handlers per type, drop-on-overflow
// + Dropped() counter, Close() drains the queue, and panic-recovery in
// handlers.
//
// Tests use locally-defined fake event types so this package's tests don't
// depend on workflow (which imports this package). Two fakes — fakeAlpha
// and fakeBeta — are enough to exercise type-keyed dispatch.

type fakeAlpha struct {
	id  string
	err error
}

func (e fakeAlpha) Name() string         { return "fake.alpha" }
func (e fakeAlpha) Level() slog.Level    { return slog.LevelInfo }
func (e fakeAlpha) Attrs() []slog.Attr   { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeAlpha) Err() error           { return e.err }

type fakeBeta struct {
	id string
}

func (e fakeBeta) Name() string       { return "fake.beta" }
func (e fakeBeta) Level() slog.Level  { return slog.LevelInfo }
func (e fakeBeta) Attrs() []slog.Attr { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeBeta) Err() error         { return nil }

// waitForOr fails the test if cond hasn't returned true within timeout.
// Used because handler dispatch is asynchronous.
func waitForOr(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func TestHandlers_OnRegistersTypedCallback(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()

	var got atomic.Int32
	On(h, func(_ context.Context, e fakeAlpha) {
		if e.id != "g" {
			t.Errorf("got id %q", e.id)
		}
		got.Add(1)
	})

	h.Fire(context.Background(), fakeAlpha{id: "g"})

	waitForOr(t, time.Second, func() bool { return got.Load() == 1 }, "handler should run once")
}

func TestHandlers_DispatchesByType(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()

	var alpha, beta atomic.Int32
	On(h, func(context.Context, fakeAlpha) { alpha.Add(1) })
	On(h, func(context.Context, fakeBeta) { beta.Add(1) })

	h.Fire(context.Background(), fakeAlpha{id: "1"})
	h.Fire(context.Background(), fakeBeta{id: "2"})
	h.Fire(context.Background(), fakeAlpha{id: "3"})

	waitForOr(t, time.Second, func() bool {
		return alpha.Load() == 2 && beta.Load() == 1
	}, "handlers should fire by event type")
}

func TestHandlers_MultipleHandlersForSameType(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()

	var a, b atomic.Int32
	On(h, func(context.Context, fakeAlpha) { a.Add(1) })
	On(h, func(context.Context, fakeAlpha) { b.Add(1) })

	h.Fire(context.Background(), fakeAlpha{id: "g"})

	waitForOr(t, time.Second, func() bool { return a.Load() == 1 && b.Load() == 1 },
		"both handlers should fire")
}

func TestHandlers_NoHandlersIsNoOp(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()
	// No handlers registered — Fire should not enqueue anything.
	h.Fire(context.Background(), fakeAlpha{id: "g"})
	if h.Dropped() != 0 {
		t.Errorf("Dropped should be 0 with no handlers; got %d", h.Dropped())
	}
}

func TestHandlers_DropOnOverflow(t *testing.T) {
	// Tiny queue + slow handler so we can deterministically force overflow.
	h := NewHandlers(Limits{QueueSize: 1})
	release := make(chan struct{})
	On(h, func(context.Context, fakeAlpha) { <-release })

	// Send 10 events. With queue=1 and the handler blocked, we should have
	// at most 2 in flight (one being processed, one queued); the rest get
	// dropped.
	for range 10 {
		h.Fire(context.Background(), fakeAlpha{id: "g"})
	}

	if h.Dropped() == 0 {
		t.Errorf("expected drops, got 0")
	}
	close(release)
	_ = h.Close()
}

func TestHandlers_CloseDrainsQueue(t *testing.T) {
	h := NewHandlers(Limits{QueueSize: 64})

	var fired atomic.Int32
	On(h, func(context.Context, fakeAlpha) {
		fired.Add(1)
	})

	for range 20 {
		h.Fire(context.Background(), fakeAlpha{id: "g"})
	}
	if err := h.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if fired.Load() != 20 {
		t.Errorf("Close should drain all events; fired=%d", fired.Load())
	}
}

func TestHandlers_CloseIsIdempotent(t *testing.T) {
	h := NewHandlers(Limits{})
	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close should be no-op, got: %v", err)
	}
}

func TestHandlers_PanicInHandlerRecovered(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()

	var fired atomic.Int32
	On(h, func(context.Context, fakeAlpha) { panic("boom") })
	On(h, func(context.Context, fakeAlpha) { fired.Add(1) })

	h.Fire(context.Background(), fakeAlpha{id: "g"})

	waitForOr(t, time.Second, func() bool { return fired.Load() == 1 },
		"second handler should still fire after first one panicked")
}

func TestHandlers_LimitsZeroOrNegativeQueueSizeUsesDefault(t *testing.T) {
	h := NewHandlers(Limits{QueueSize: 0})
	if h.queueSize != 1024 {
		t.Errorf("queueSize=0 should fall back to default; got %d", h.queueSize)
	}
	_ = h.Close()

	h2 := NewHandlers(Limits{QueueSize: -5})
	if h2.queueSize != 1024 {
		t.Errorf("queueSize=-5 should fall back to default; got %d", h2.queueSize)
	}
	_ = h2.Close()
}

// Concurrent Fire from multiple goroutines must be safe.
func TestHandlers_ConcurrentFireIsSafe(t *testing.T) {
	h := NewHandlers(Limits{QueueSize: 1024})
	defer h.Close()

	var got atomic.Int32
	On(h, func(context.Context, fakeAlpha) { got.Add(1) })

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			h.Fire(context.Background(), fakeAlpha{id: "g"})
		})
	}
	wg.Wait()

	waitForOr(t, time.Second, func() bool { return got.Load()+int32(h.Dropped()) == 50 },
		"all 50 events accounted for (fired + dropped)")
}

// Errored fakeAlpha flows through with the err preserved — exercises the
// fakeAlpha.err field so it doesn't show up as dead code.
func TestHandlers_ErroredEventFlowsThrough(t *testing.T) {
	h := NewHandlers(Limits{})
	defer h.Close()

	want := errors.New("boom")
	var seen atomic.Pointer[error]
	On(h, func(_ context.Context, e fakeAlpha) {
		if err := e.Err(); err != nil {
			seen.Store(&err)
		}
	})
	h.Fire(context.Background(), fakeAlpha{id: "g", err: want})

	waitForOr(t, time.Second, func() bool { return seen.Load() != nil }, "handler must observe err")
	if got := seen.Load(); !errors.Is(*got, want) {
		t.Errorf("err mismatch")
	}
}

// Compile-time assertion that *Handlers satisfies EventSink.
var _ EventSink = (*Handlers)(nil)
