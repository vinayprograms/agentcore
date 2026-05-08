package workflow

import "log/slog"

// Events fired by the workflow runtime. Consumers subscribe via
// Runtime.Telemetry, which receives values that satisfy observe.Event.
//
// Every event type below implements observe.Event (Name, Level, Attrs,
// Err) so observers in the agentcore/observe package consume them
// generically — adding a new event here automatically flows into Logger,
// Counter, Handlers, and any consumer-defined sink without code changes.
//
// slog.Attr is the carrier vocabulary because it is stdlib, type-safe at
// construction, and already what the consumer's logging stack speaks.
// observe.Counter and other sinks translate slog.Attr to whatever their
// downstream system needs at the boundary.

// ////////////////////////////////////////
// WorkflowStarted fires when Workflow.Execute begins.
type WorkflowStarted struct {
	Workflow string
}

func (e WorkflowStarted) Name() string     { return "workflow.started" }
func (e WorkflowStarted) Level() slog.Level { return slog.LevelInfo }
func (e WorkflowStarted) Attrs() []slog.Attr {
	return []slog.Attr{slog.String("workflow", e.Workflow)}
}
func (e WorkflowStarted) Err() error { return nil }

// ////////////////////////////////////////
// WorkflowEnded fires when Workflow.Execute returns (success or error).
type WorkflowEnded struct {
	Workflow string
	Failure  error
}

func (e WorkflowEnded) Name() string { return "workflow.ended" }
func (e WorkflowEnded) Level() slog.Level {
	if e.Failure != nil {
		return slog.LevelError
	}
	return slog.LevelInfo
}
func (e WorkflowEnded) Attrs() []slog.Attr {
	return []slog.Attr{slog.String("workflow", e.Workflow)}
}
func (e WorkflowEnded) Err() error { return e.Failure }

// ////////////////////////////////////////
// GoalStarted fires when a Goal or Convergence begins execution.
type GoalStarted struct {
	Goal        string
	Description string
}

func (e GoalStarted) Name() string     { return "goal.started" }
func (e GoalStarted) Level() slog.Level { return slog.LevelInfo }
func (e GoalStarted) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("goal", e.Goal),
		slog.String("description", e.Description),
	}
}
func (e GoalStarted) Err() error { return nil }

// ////////////////////////////////////////
// GoalEnded fires when a Goal or Convergence completes (success path only).
type GoalEnded struct {
	Goal   string
	Output string
}

func (e GoalEnded) Name() string     { return "goal.ended" }
func (e GoalEnded) Level() slog.Level { return slog.LevelInfo }
func (e GoalEnded) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("goal", e.Goal),
		slog.Int("output_len", len(e.Output)),
	}
}
func (e GoalEnded) Err() error { return nil }

// ////////////////////////////////////////
// SubagentSpawned fires when Goal.fanOut starts a child Step.
type SubagentSpawned struct {
	Goal  string
	Agent string
}

func (e SubagentSpawned) Name() string     { return "subagent.spawned" }
func (e SubagentSpawned) Level() slog.Level { return slog.LevelInfo }
func (e SubagentSpawned) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("goal", e.Goal),
		slog.String("agent", e.Agent),
	}
}
func (e SubagentSpawned) Err() error { return nil }

// ////////////////////////////////////////
// SubagentCompleted fires when a child Step in a fan-out finishes.
type SubagentCompleted struct {
	Goal    string
	Agent   string
	Output  string
	Failure error
}

func (e SubagentCompleted) Name() string { return "subagent.completed" }
func (e SubagentCompleted) Level() slog.Level {
	if e.Failure != nil {
		return slog.LevelError
	}
	return slog.LevelInfo
}
func (e SubagentCompleted) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("goal", e.Goal),
		slog.String("agent", e.Agent),
		slog.Int("output_len", len(e.Output)),
	}
}
func (e SubagentCompleted) Err() error { return e.Failure }

// ////////////////////////////////////////
// ConvergenceCapReached fires when Convergence hits its WITHIN limit
// without the model emitting CONVERGED.
type ConvergenceCapReached struct {
	Convergence string
	Cap         int
	LastOutput  string
}

func (e ConvergenceCapReached) Name() string     { return "convergence.cap_reached" }
func (e ConvergenceCapReached) Level() slog.Level { return slog.LevelWarn }
func (e ConvergenceCapReached) Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("convergence", e.Convergence),
		slog.Int("cap", e.Cap),
	}
}
func (e ConvergenceCapReached) Err() error { return nil }

// ////////////////////////////////////////
// PreflightFailed fires when Validate rejects the workflow before execution.
type PreflightFailed struct {
	Workflow string
	Failure  error
}

func (e PreflightFailed) Name() string     { return "preflight.failed" }
func (e PreflightFailed) Level() slog.Level { return slog.LevelError }
func (e PreflightFailed) Attrs() []slog.Attr {
	return []slog.Attr{slog.String("workflow", e.Workflow)}
}
func (e PreflightFailed) Err() error { return e.Failure }
