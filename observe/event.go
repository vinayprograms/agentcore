package observe

import (
	"context"
	"log/slog"
)

// Event is the contract every observed value satisfies. agentcore packages
// (workflow, future supervision, etc.) define their own concrete event
// types and implement this interface; observers in this package consume
// them through the interface and never need to switch on the concrete
// type. New event types added to any agentcore package flow through every
// observer here automatically.
//
// slog.Attr is the carrier vocabulary because it is stdlib, type-safe at
// construction, and already what the consumer's logging stack speaks.
// Tracer-style observers translate slog.Attr to OTel attribute.KeyValue at
// the boundary where they are needed.
type Event interface {
	Name() string       // stable identifier, e.g. "workflow.started"
	Level() slog.Level  // slog.LevelInfo / Warn / Error
	Attrs() []slog.Attr // structured fields, type-safe at construction
	Err() error         // nil unless the event represents a failure
}

// EventSink receives events from a producer (e.g. a running Workflow).
// Implementations must be safe for concurrent use — Fire may be called
// from multiple goroutines (parallel fan-out, async dispatchers).
//
// The interface is single-method on purpose: the producer fires
// fire-and-forget; the sink decides what to do (log, count, dispatch to
// handlers, drop). Slow or panicking sinks must not affect the producer.
type EventSink interface {
	Fire(ctx context.Context, event any)
}
