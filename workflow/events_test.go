package workflow

import (
	"errors"
	"testing"
)

// events_test.go covers events.go. The events are pure data structs with no
// methods — the only contract worth pinning is the field shape consumers
// pattern-match against. Construct each event with realistic values to lock
// in the public surface.

func TestEvents_ConstructEverythingTheConsumerSeesByName(t *testing.T) {
	// If a field is renamed or removed, this file stops compiling — which is
	// exactly the early-warning a downstream consumer wants.
	_ = WorkflowStarted{Name: "w"}
	_ = WorkflowEnded{Name: "w", Err: errors.New("x")}
	_ = GoalStarted{Name: "g", Description: "d"}
	_ = GoalEnded{Name: "g", Output: "out"}
	_ = SubagentSpawned{GoalName: "g", AgentName: "a"}
	_ = SubagentCompleted{GoalName: "g", AgentName: "a", Output: "out", Err: nil}
	_ = ConvergenceCapReached{Name: "c", Cap: 3, LastOutput: "last"}
	_ = PreflightFailed{Workflow: "w", Err: errors.New("x")}
}
