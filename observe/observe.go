// Package observe provides default EventSink implementations for agentcore
// packages — structured logging via slog, OTel metric counters, and a
// typed-callback registry. Each implementation is a standalone EventSink;
// combine what you need with Tee.
//
//	sink := observe.Tee(
//	    observe.Logger(slog.Default()),
//	    observe.Counter(otel.Meter("agent")),
//	    observe.NewHandlers(),
//	)
//	rt := &workflow.Runtime{Model: m, Telemetry: sink}
//
// Logger and Counter are synchronous — slog and OTel batch downstream, so
// the extra goroutine layer is wasted overhead. The Handlers registry is
// async with a bounded queue (drop-on-overflow) so a slow handler can never
// block the producer.
//
// Tracing is NOT in this package: agentcore packages use OTel directly via
// otel.Tracer(...) at their internal boundaries (see workflow/tracing.go).
// observe is the channel for typed domain events; OTel is the channel for
// spans.
package observe

import "context"

// Tee fans an event out to every sink in declared order, synchronously.
// Each sink is fire-and-forget — failure or slowness in one does not affect
// the others (the EventSink contract has no error return).
//
// Use Tee to compose Logger + Counter + Handlers into the single sink that
// a producer's Telemetry field expects. A zero-arg Tee() is a valid no-op.
func Tee(sinks ...EventSink) EventSink {
	return tee(sinks)
}

type tee []EventSink

func (t tee) Fire(ctx context.Context, event any) {
	for _, s := range t {
		s.Fire(ctx, event)
	}
}
