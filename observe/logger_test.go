package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// logger_test.go covers logger.go: every event yields the expected log
// message + level, and Err-bearing events escalate to ERROR with the
// error attached. Tests use locally-defined fake event types so this
// package's tests don't depend on workflow.

// fakeLifecycle is a typical lifecycle event — INFO level, no error.
type fakeLifecycle struct{ id string }

func (e fakeLifecycle) Name() string       { return "fake.lifecycle" }
func (e fakeLifecycle) Level() slog.Level  { return slog.LevelInfo }
func (e fakeLifecycle) Attrs() []slog.Attr { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeLifecycle) Err() error         { return nil }

// fakeFailure has a non-nil Err and reports ERROR level — exercises the
// err-attribute attachment path.
type fakeFailure struct{ id string; err error }

func (e fakeFailure) Name() string         { return "fake.failure" }
func (e fakeFailure) Level() slog.Level    { return slog.LevelError }
func (e fakeFailure) Attrs() []slog.Attr   { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeFailure) Err() error           { return e.err }

// fakeWarn for the WARN-level branch.
type fakeWarn struct{ id string }

func (e fakeWarn) Name() string       { return "fake.warn" }
func (e fakeWarn) Level() slog.Level  { return slog.LevelWarn }
func (e fakeWarn) Attrs() []slog.Attr { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeWarn) Err() error         { return nil }

// fakeDebug for the DEBUG-level branch.
type fakeDebug struct{ id string }

func (e fakeDebug) Name() string       { return "fake.debug" }
func (e fakeDebug) Level() slog.Level  { return slog.LevelDebug }
func (e fakeDebug) Attrs() []slog.Attr { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeDebug) Err() error         { return nil }

// fakeOddLevel reports a level that isn't one of slog's four named values.
// Exercises the default-case mapping in counter.levelString — anything
// out-of-band reports as "error" so it surfaces loudly.
type fakeOddLevel struct{ id string }

func (e fakeOddLevel) Name() string       { return "fake.oddlevel" }
func (e fakeOddLevel) Level() slog.Level  { return slog.Level(2) } // between Info and Warn
func (e fakeOddLevel) Attrs() []slog.Attr { return []slog.Attr{slog.String("id", e.id)} }
func (e fakeOddLevel) Err() error         { return nil }

// captureLogger returns a slog.Logger that writes JSON records to a buffer
// and the buffer itself for inspection.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, &buf
}

// parseRecords splits a captureLogger buffer into one map per JSON record.
func parseRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal record %q: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

func TestLogger_NilLoggerFallsBackToDefault(t *testing.T) {
	// Should not panic when nil is passed.
	sink := Logger(nil)
	sink.Fire(context.Background(), fakeLifecycle{id: "x"})
}

func TestLogger_LifecycleEventsAtInfo(t *testing.T) {
	l, buf := captureLogger()
	sink := Logger(l)

	sink.Fire(context.Background(), fakeLifecycle{id: "1"})
	sink.Fire(context.Background(), fakeLifecycle{id: "2"})

	recs := parseRecords(t, buf)
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	for i, r := range recs {
		if r["msg"] != "fake.lifecycle" {
			t.Errorf("rec %d msg: %q", i, r["msg"])
		}
		if r["level"] != "INFO" {
			t.Errorf("rec %d level: %q", i, r["level"])
		}
	}
}

func TestLogger_ErrorEventEscalates(t *testing.T) {
	l, buf := captureLogger()
	sink := Logger(l)
	want := errors.New("boom")

	sink.Fire(context.Background(), fakeFailure{id: "x", err: want})

	recs := parseRecords(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0]["level"] != "ERROR" {
		t.Errorf("expected ERROR, got %q", recs[0]["level"])
	}
	errStr, ok := recs[0]["err"].(string)
	if !ok || !strings.Contains(errStr, "boom") {
		t.Errorf("err attribute missing or wrong: %v", recs[0]["err"])
	}
}

func TestLogger_FailureWithoutErrLogsAtErrorLevelButNoErrAttr(t *testing.T) {
	// fakeFailure reports LevelError unconditionally; with err==nil the log
	// should still be ERROR but carry no "err" attribute.
	l, buf := captureLogger()
	sink := Logger(l)
	sink.Fire(context.Background(), fakeFailure{id: "x"})

	recs := parseRecords(t, buf)
	if recs[0]["level"] != "ERROR" {
		t.Errorf("level: %q", recs[0]["level"])
	}
	if _, has := recs[0]["err"]; has {
		t.Errorf("err attribute should be absent when Err()==nil")
	}
}

func TestLogger_WarnLevel(t *testing.T) {
	l, buf := captureLogger()
	sink := Logger(l)
	sink.Fire(context.Background(), fakeWarn{id: "x"})

	recs := parseRecords(t, buf)
	if recs[0]["level"] != "WARN" {
		t.Errorf("level: %q", recs[0]["level"])
	}
}

func TestLogger_IgnoresNonEventValues(t *testing.T) {
	l, buf := captureLogger()
	sink := Logger(l)
	// Anything that doesn't satisfy Event is ignored — no panic, no log line.
	sink.Fire(context.Background(), struct{ X string }{X: "alien"})
	if buf.Len() != 0 {
		t.Errorf("non-Event value produced log: %q", buf.String())
	}
}

// Forward-compatibility: any Event flows through Logger without code
// changes here.
func TestLogger_DispatchesAnyEventInterfaceImpl(t *testing.T) {
	l, buf := captureLogger()
	sink := Logger(l)

	type custom struct{ count int }
	// Inline implementation; avoids polluting package-level types.
	_ = struct {
		Event
	}{} // keep imports happy

	sink.Fire(context.Background(), customCount{n: 7})

	recs := parseRecords(t, buf)
	if recs[0]["msg"] != "custom.count" {
		t.Errorf("name: %q", recs[0]["msg"])
	}
	if recs[0]["n"] != float64(7) {
		t.Errorf("n attr: %v", recs[0]["n"])
	}

	// silence unused-type warning
	_ = custom{}
}

type customCount struct{ n int }

func (e customCount) Name() string       { return "custom.count" }
func (e customCount) Level() slog.Level  { return slog.LevelInfo }
func (e customCount) Attrs() []slog.Attr { return []slog.Attr{slog.Int("n", e.n)} }
func (e customCount) Err() error         { return nil }
