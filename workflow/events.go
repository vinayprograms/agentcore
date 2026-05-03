package workflow

// Events fired by the workflow runtime. Consumers subscribe via Runtime.Hooks.
// All events are plain structs with no methods — they carry data, nothing else.

// WorkflowStarted fires when Workflow.Execute begins.
type WorkflowStarted struct {
	Name string
}

// WorkflowEnded fires when Workflow.Execute returns (success or error).
type WorkflowEnded struct {
	Name string
	Err  error
}

// GoalStarted fires when a Goal or Converge begins execution.
type GoalStarted struct {
	Name        string
	Description string
}

// GoalEnded fires when a Goal or Converge completes (success path only).
type GoalEnded struct {
	Name   string
	Output string
}

// SubagentSpawned fires when Goal.fanOut starts a child Step.
type SubagentSpawned struct {
	GoalName  string
	AgentName string
}

// SubagentCompleted fires when a child Step in a fan-out finishes.
type SubagentCompleted struct {
	GoalName  string
	AgentName string
	Output    string
	Err       error
}

// ConvergenceCapReached fires when Converge hits its WITHIN limit without
// the model emitting CONVERGED.
type ConvergenceCapReached struct {
	Name       string
	Cap        int
	LastOutput string
}

// PreflightFailed fires when Validate rejects the workflow before execution.
type PreflightFailed struct {
	Workflow string
	Err      error
}
