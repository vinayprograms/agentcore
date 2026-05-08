package observe

import (
	"context"
	"testing"
)

// observe_test.go covers observe.go: the Tee fan-out behaviour. Each
// downstream sink must see every event in declared order; an empty Tee is
// a valid no-op sink.

// recordSink captures every event it receives.
type recordSink struct {
	events []any
}

func (r *recordSink) Fire(_ context.Context, event any) {
	r.events = append(r.events, event)
}

func TestTee_FanOutToAllSinksInOrder(t *testing.T) {
	a, b, c := &recordSink{}, &recordSink{}, &recordSink{}
	sink := Tee(a, b, c)

	sink.Fire(context.Background(), fakeLifecycle{id: "1"})
	sink.Fire(context.Background(), fakeLifecycle{id: "2"})

	for i, s := range []*recordSink{a, b, c} {
		if len(s.events) != 2 {
			t.Errorf("sink %d: got %d events, want 2", i, len(s.events))
		}
	}
}

func TestTee_EmptyIsNoOp(t *testing.T) {
	sink := Tee()
	// Should not panic.
	sink.Fire(context.Background(), fakeLifecycle{id: "x"})
}

// Compile-time assertion that Tee returns an EventSink.
var _ EventSink = Tee()
