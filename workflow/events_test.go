package workflow

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/vinayprograms/agentcore/observe"
)

// events_test.go covers events.go. Two contracts matter:
//   1. Field shape — consumers pattern-match on these.
//   2. Event interface — every event type must satisfy observe.Event so
//      sinks can dispatch generically. The compile-time assertions below
//      mean a new event type forces the implementer to provide Name,
//      Level, Attrs, and Err at the moment it's declared.

// Compile-time interface conformance — adding a new event type without
// implementing observe.Event will fail to build.
var (
	_ observe.Event = WorkflowStarted{}
	_ observe.Event = WorkflowEnded{}
	_ observe.Event = GoalStarted{}
	_ observe.Event = GoalEnded{}
	_ observe.Event = SubagentSpawned{}
	_ observe.Event = SubagentCompleted{}
	_ observe.Event = ConvergenceCapReached{}
	_ observe.Event = PreflightFailed{}
)

func TestEvents_ConstructEverythingTheConsumerSeesByName(t *testing.T) {
	_ = WorkflowStarted{Workflow: "w"}
	_ = WorkflowEnded{Workflow: "w", Failure: errors.New("x")}
	_ = GoalStarted{Goal: "g", Description: "d"}
	_ = GoalEnded{Goal: "g", Output: "out"}
	_ = SubagentSpawned{Goal: "g", Agent: "a"}
	_ = SubagentCompleted{Goal: "g", Agent: "a", Output: "out", Failure: nil}
	_ = ConvergenceCapReached{Convergence: "c", Cap: 3, LastOutput: "last"}
	_ = PreflightFailed{Workflow: "w", Failure: errors.New("x")}
}

// TestEvents_NameAndLevelShape pins the user-facing names and levels so a
// refactor that accidentally renames "workflow.started" to something else
// is caught.
func TestEvents_NameAndLevelShape(t *testing.T) {
	cases := []struct {
		ev    observe.Event
		name  string
		level slog.Level
	}{
		{WorkflowStarted{}, "workflow.started", slog.LevelInfo},
		{WorkflowEnded{}, "workflow.ended", slog.LevelInfo},
		{WorkflowEnded{Failure: errors.New("x")}, "workflow.ended", slog.LevelError},
		{GoalStarted{}, "goal.started", slog.LevelInfo},
		{GoalEnded{}, "goal.ended", slog.LevelInfo},
		{SubagentSpawned{}, "subagent.spawned", slog.LevelInfo},
		{SubagentCompleted{}, "subagent.completed", slog.LevelInfo},
		{SubagentCompleted{Failure: errors.New("x")}, "subagent.completed", slog.LevelError},
		{ConvergenceCapReached{}, "convergence.cap_reached", slog.LevelWarn},
		{PreflightFailed{}, "preflight.failed", slog.LevelError},
	}
	for _, c := range cases {
		if got := c.ev.Name(); got != c.name {
			t.Errorf("%T name: got %q want %q", c.ev, got, c.name)
		}
		if got := c.ev.Level(); got != c.level {
			t.Errorf("%T level: got %v want %v", c.ev, got, c.level)
		}
	}
}

func TestEvents_AttrsCarryStructuredFields(t *testing.T) {
	cases := []struct {
		ev      observe.Event
		wantKey string
	}{
		{WorkflowStarted{Workflow: "w"}, "workflow"},
		{WorkflowEnded{Workflow: "w"}, "workflow"},
		{GoalStarted{Goal: "g", Description: "d"}, "goal"},
		{GoalEnded{Goal: "g", Output: "out"}, "goal"},
		{SubagentSpawned{Goal: "g", Agent: "a"}, "goal"},
		{SubagentCompleted{Goal: "g", Agent: "a", Output: "x"}, "goal"},
		{ConvergenceCapReached{Convergence: "c", Cap: 3}, "convergence"},
		{PreflightFailed{Workflow: "w"}, "workflow"},
	}
	for _, c := range cases {
		attrs := c.ev.Attrs()
		if len(attrs) == 0 {
			t.Errorf("%T: no attrs", c.ev)
			continue
		}
		if attrs[0].Key != c.wantKey {
			t.Errorf("%T: first attr key = %q, want %q", c.ev, attrs[0].Key, c.wantKey)
		}
	}

	// The goal attr-shape is the most consequential — pin it specifically.
	attrs := GoalStarted{Goal: "g", Description: "do it"}.Attrs()
	if len(attrs) != 2 {
		t.Fatalf("GoalStarted want 2 attrs, got %d", len(attrs))
	}
	if attrs[0].Key != "goal" || attrs[0].Value.String() != "g" {
		t.Errorf("goal attr: %+v", attrs[0])
	}
	if attrs[1].Key != "description" || attrs[1].Value.String() != "do it" {
		t.Errorf("description attr: %+v", attrs[1])
	}
}

func TestEvents_ErrAccessor(t *testing.T) {
	want := errors.New("boom")
	if got := (WorkflowEnded{Failure: want}).Err(); got != want {
		t.Errorf("WorkflowEnded.Err: got %v", got)
	}
	if got := (PreflightFailed{Failure: want}).Err(); got != want {
		t.Errorf("PreflightFailed.Err: got %v", got)
	}
	if got := (SubagentCompleted{Failure: want}).Err(); got != want {
		t.Errorf("SubagentCompleted.Err: got %v", got)
	}
	// Events that cannot fail always return nil from Err().
	if got := (WorkflowStarted{}).Err(); got != nil {
		t.Errorf("WorkflowStarted.Err should be nil, got %v", got)
	}
	if got := (GoalStarted{}).Err(); got != nil {
		t.Errorf("GoalStarted.Err should be nil, got %v", got)
	}
	if got := (GoalEnded{}).Err(); got != nil {
		t.Errorf("GoalEnded.Err should be nil, got %v", got)
	}
	if got := (SubagentSpawned{}).Err(); got != nil {
		t.Errorf("SubagentSpawned.Err should be nil, got %v", got)
	}
	if got := (ConvergenceCapReached{}).Err(); got != nil {
		t.Errorf("ConvergenceCapReached.Err should be nil, got %v", got)
	}
	// nil-Failure event also returns nil.
	if got := (WorkflowEnded{}).Err(); got != nil {
		t.Errorf("nil-Failure event should return nil")
	}
}
