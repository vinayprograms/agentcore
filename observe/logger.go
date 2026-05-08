package observe

import (
	"context"
	"log/slog"
)

// Logger returns an EventSink that emits one structured slog record per
// event. The event itself supplies its name, level, attribute list, and
// (when applicable) error — see Event. A non-nil Err is attached as an
// "err" attribute so the structured log retains the failure cause.
//
// The sink is synchronous because slog handlers either are fast (text /
// JSON to a writer) or do their own batching. Wrapping in a goroutine here
// would only add overhead.
//
// New event types in any agentcore package automatically flow through this
// sink without code changes — the dispatch is data-driven via the Event
// interface.
func Logger(l *slog.Logger) EventSink {
	if l == nil {
		l = slog.Default()
	}
	return &logger{l: l}
}

type logger struct {
	l *slog.Logger
}

func (lg *logger) Fire(ctx context.Context, event any) {
	e, ok := event.(Event)
	if !ok {
		return
	}
	attrs := e.Attrs()
	if err := e.Err(); err != nil {
		attrs = append(attrs, slog.Any("err", err))
	}
	lg.l.LogAttrs(ctx, e.Level(), e.Name(), attrs...)
}
