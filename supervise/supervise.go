// Package supervise provides a default workflow.Supervisor implementation for
// the five-phase supervision pipeline (COMMIT → EXECUTE → POST → RECONCILE →
// SUPERVISE). Reconcile does deterministic drift detection against the pre/post
// checkpoints. Supervise calls a configured LLM to render a verdict (Continue,
// Reorient, AskHuman, or Halt).
//
// Usage:
//
//	sup := supervise.New(supervise.Config{
//	    Model: supervisorModel, // optional; falls back to the agent's model
//	})
//	rt := &workflow.Runtime{
//	    Model:      agentModel,
//	    Supervisor: sup,
//	}
package supervise

import (
	"github.com/vinayprograms/agentcore/workflow"
	"github.com/vinayprograms/agentkit/llm"
)

// Config holds the optional settings for a Supervisor.
// Zero-value is usable — both fields default to reasonable values.
type Config struct {
	// Model is the dedicated LLM for the SUPERVISE phase. When nil, the
	// supervisor falls back to the model carried by workflow.SuperviseRequest
	// (the agent's main model). Separate models let the supervisor reason
	// independently — a cheaper or thinking-enabled model, for example.
	Model llm.Model
}

// New returns a workflow.Supervisor backed by the given Config. The returned
// value satisfies the two-method interface defined by the workflow package;
// its concrete type is unexported so consumers program to the interface.
func New(cfg Config) workflow.Supervisor {
	return &supervisor{cfg: cfg}
}

// supervisor is the unexported implementation of workflow.Supervisor.
type supervisor struct {
	cfg Config
}

// Compile-time assertion.
var _ workflow.Supervisor = (*supervisor)(nil)
