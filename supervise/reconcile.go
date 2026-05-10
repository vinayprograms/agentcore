package supervise

import (
	"fmt"

	"github.com/vinayprograms/agentcore/workflow"
)

// Reconcile compares the pre-execution checkpoint against the post-execution
// self-assessment. It returns a ReconcileResult with a flat list of triggers
// and an Escalate flag. Any trigger sets Escalate to true.
//
// The check is deterministic — no LLM call. It examines four signals in the
// post-checkpoint: unmet commitment, deviations, concerns, and unexpected
// findings. Each non-empty signal becomes a trigger.
func (s *supervisor) Reconcile(pre *workflow.PreCheckpoint, post *workflow.PostCheckpoint) *workflow.ReconcileResult {
	var triggers []string

	if !post.MetCommitment {
		triggers = append(triggers, "commitment_not_met")
	}
	for _, d := range post.Deviations {
		if d != "" {
			triggers = append(triggers, fmt.Sprintf("deviation:%s", d))
		}
	}
	for _, c := range post.Concerns {
		if c != "" {
			triggers = append(triggers, fmt.Sprintf("concern:%s", c))
		}
	}
	for _, u := range post.Unexpected {
		if u != "" {
			triggers = append(triggers, fmt.Sprintf("unexpected:%s", u))
		}
	}

	return &workflow.ReconcileResult{
		StepID:   pre.StepID,
		Triggers: triggers,
		Escalate: len(triggers) > 0,
	}
}
