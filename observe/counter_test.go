package observe

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// counter_test.go covers counter.go: every event becomes a counter
// increment under "agentcore.<event-name>" with level/errored attrs;
// repeat events for the same name reuse the cached instrument; non-Event
// values and a nil meter are no-ops.

// newCountingMeter wires an OTel SDK MeterProvider with a manual reader so
// tests can introspect the recorded metric data.
func newCountingMeter(t *testing.T) (metric.Reader, *metric.MeterProvider) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return reader, mp
}

func collect(t *testing.T, reader metric.Reader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

// findCounter returns the int64 sum for the named instrument, plus the
// matching attribute set, or fails the test if not found.
func findCounter(t *testing.T, rm metricdata.ResourceMetrics, name string) []metricdata.DataPoint[int64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric %s: expected Sum[int64], got %T", name, m.Data)
				}
				return sum.DataPoints
			}
		}
	}
	t.Fatalf("metric %s not found in collected data", name)
	return nil
}

func TestCounter_IncrementsPerEventName(t *testing.T) {
	reader, mp := newCountingMeter(t)
	sink := Counter(mp.Meter("test"))

	sink.Fire(context.Background(), fakeAlpha{id: "1"})
	sink.Fire(context.Background(), fakeAlpha{id: "2"})
	sink.Fire(context.Background(), fakeBeta{id: "3"})

	rm := collect(t, reader)
	alphaPoints := findCounter(t, rm, "agentcore.fake.alpha")
	betaPoints := findCounter(t, rm, "agentcore.fake.beta")

	var alphaTotal int64
	for _, p := range alphaPoints {
		alphaTotal += p.Value
	}
	if alphaTotal != 2 {
		t.Errorf("agentcore.fake.alpha total: got %d want 2", alphaTotal)
	}

	var betaTotal int64
	for _, p := range betaPoints {
		betaTotal += p.Value
	}
	if betaTotal != 1 {
		t.Errorf("agentcore.fake.beta total: got %d want 1", betaTotal)
	}
}

func TestCounter_AttachesLevelAndErroredAttrs(t *testing.T) {
	reader, mp := newCountingMeter(t)
	sink := Counter(mp.Meter("test"))

	sink.Fire(context.Background(), fakeAlpha{id: "1"})                       // info, no err
	sink.Fire(context.Background(), fakeAlpha{id: "2", err: errors.New("x")}) // info, errored

	rm := collect(t, reader)
	points := findCounter(t, rm, "agentcore.fake.alpha")

	// Two distinct attribute sets: errored=false and errored=true.
	var sawClean, sawErrored bool
	for _, p := range points {
		errored, _ := p.Attributes.Value("errored")
		level, _ := p.Attributes.Value("level")
		if level.AsString() != "info" {
			t.Errorf("level attr: %q", level.AsString())
		}
		if !errored.AsBool() {
			sawClean = true
			if p.Value != 1 {
				t.Errorf("clean count: %d", p.Value)
			}
		} else {
			sawErrored = true
			if p.Value != 1 {
				t.Errorf("errored count: %d", p.Value)
			}
		}
	}
	if !sawClean || !sawErrored {
		t.Errorf("expected both errored=true and errored=false data points; got clean=%v errored=%v",
			sawClean, sawErrored)
	}
}

func TestCounter_LevelStringMaps(t *testing.T) {
	reader, mp := newCountingMeter(t)
	sink := Counter(mp.Meter("test"))

	sink.Fire(context.Background(), fakeWarn{id: "x"})         // warn
	sink.Fire(context.Background(), fakeFailure{id: "y"})      // error
	sink.Fire(context.Background(), fakeLifecycle{id: "z"})    // info
	sink.Fire(context.Background(), fakeDebug{id: "d"})        // debug
	sink.Fire(context.Background(), fakeOddLevel{id: "o"})     // out-of-band → "error"

	rm := collect(t, reader)
	for _, want := range []struct {
		metric, level string
	}{
		{"agentcore.fake.warn", "warn"},
		{"agentcore.fake.failure", "error"},
		{"agentcore.fake.lifecycle", "info"},
		{"agentcore.fake.debug", "debug"},
		{"agentcore.fake.oddlevel", "error"},
	} {
		points := findCounter(t, rm, want.metric)
		if len(points) != 1 {
			t.Fatalf("%s: %d points", want.metric, len(points))
		}
		level, _ := points[0].Attributes.Value("level")
		if level.AsString() != want.level {
			t.Errorf("%s level: got %q want %q", want.metric, level.AsString(), want.level)
		}
	}
}

func TestCounter_ReusesInstrumentForRepeatedEventName(t *testing.T) {
	reader, mp := newCountingMeter(t)
	sink := Counter(mp.Meter("test")).(*counter)

	for range 100 {
		sink.Fire(context.Background(), fakeAlpha{id: "x"})
	}
	rm := collect(t, reader)
	points := findCounter(t, rm, "agentcore.fake.alpha")
	var total int64
	for _, p := range points {
		total += p.Value
	}
	if total != 100 {
		t.Errorf("expected 100 total, got %d", total)
	}

	// And the cache contains exactly one entry for this event name.
	count := 0
	sink.instruments.Range(func(_, _ any) bool { count++; return true })
	if count != 1 {
		t.Errorf("expected 1 cached instrument, got %d", count)
	}
}

func TestCounter_IgnoresNonEventValues(t *testing.T) {
	reader, mp := newCountingMeter(t)
	sink := Counter(mp.Meter("test"))

	sink.Fire(context.Background(), struct{ X string }{X: "alien"})

	rm := collect(t, reader)
	for _, sm := range rm.ScopeMetrics {
		if len(sm.Metrics) != 0 {
			t.Errorf("non-Event value produced metrics: %+v", sm.Metrics)
		}
	}
}

func TestCounter_NilMeterIsNoOp(t *testing.T) {
	sink := Counter(nil)
	// Should not panic and should not blow up on a real event.
	sink.Fire(context.Background(), fakeAlpha{id: "x"})
}

// erroringMeter satisfies metric.Meter and forces every Int64Counter
// creation to fail. Used to exercise the failure path in instrumentFor /
// Fire so an SDK consumer with a misconfigured meter doesn't panic.
type erroringMeter struct {
	noop.Meter
}

func (erroringMeter) Int64Counter(string, ...otelmetric.Int64CounterOption) (otelmetric.Int64Counter, error) {
	return nil, errors.New("counter denied")
}

func TestCounter_FailedInstrumentRegistrationIsSilent(t *testing.T) {
	sink := Counter(erroringMeter{})
	// Must not panic, must not record anywhere, must not propagate the error.
	sink.Fire(context.Background(), fakeAlpha{id: "x"})
	sink.Fire(context.Background(), fakeAlpha{id: "y"})
	// Cache stays empty since registration failed.
	c := sink.(*counter)
	count := 0
	c.instruments.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("failed registration should not be cached; got %d entries", count)
	}
}

// silence imports — attribute is used implicitly via the SDK; ensure the
// import actually resolves a symbol so vet stays happy.
var _ = attribute.Bool
