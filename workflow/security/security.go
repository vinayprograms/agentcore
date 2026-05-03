// Package security defines the workflow's content-guard tier modes and the
// logic that turns a mode into a fully-configured *contentguard.Guard.
//
// The package owns BOTH the mode constants and the policy that maps each mode
// to a concrete pipeline of contentguard stages — there is no separate
// "constants here, behavior elsewhere" split.
package security

import (
	"github.com/vinayprograms/agentkit/contentguard"
	"github.com/vinayprograms/agentkit/llm"
)

// Mode controls how aggressively the content guard runs its tiers.
type Mode int

const (
	// Default runs deterministic tier-1 checks on every tool call. Higher
	// tiers fire only when tier-1 escalates (e.g., suspicious patterns or
	// untrusted content present). Lowest overhead.
	Default Mode = iota

	// Paranoid runs all tiers (deterministic + screener + reviewer) on every
	// tool call regardless of triggers. Highest overhead, highest assurance.
	// Deny-on-any-deny: any stage's deny verdict short-circuits the rest.
	Paranoid

	// Research permits security-relevant actions inside a free-text scope.
	// Same staging as Paranoid; the scope flows to the reviewer's system
	// prompt so it knows what is permitted within the engagement.
	Research
)

// Build constructs a *contentguard.Guard configured for the given mode.
//
// model is the LLM used by both the screener (cheap triage) and reviewer
// (full evaluation) stages. Consumers needing distinct models for each tier
// should construct their own guard instead of going through Build.
//
// scope is meaningful only for Research mode; it's ignored for the others.
func Build(mode Mode, scope string, model llm.Model) (*contentguard.Guard, error) {
	stages := []contentguard.Stage{
		contentguard.NewScreener(model),
		contentguard.NewReviewer(model),
	}

	cfg := contentguard.Defaults()

	var workflow contentguard.Workflow
	switch mode {
	case Paranoid:
		workflow = contentguard.Paranoid()
	case Research:
		workflow = contentguard.Paranoid()
		cfg.Context = map[string]string{"scope": scope}
	default:
		// Default mode: stages run only on escalation from tier-1.
		workflow = contentguard.Escalatory()
	}

	return contentguard.New(stages, workflow, cfg)
}
