package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vinayprograms/agentkit/llm"
)

// supervision encodes the three valid supervision states. Two booleans were
// rejected because they admit a fourth, meaningless state ("human required
// but not supervised"). The zero value is unsupervised, so a freshly-built
// node carries no supervision until a method opts in.
type supervision uint8

const (
	notSupervised supervision = iota
	byLLM
	byHuman
)

// ----- Public types & interfaces (the integration contract) ------------------

// Verdict is the outcome of the SUPERVISE phase.
type Verdict string

const (
	VerdictContinue Verdict = "continue"
	VerdictReorient Verdict = "reorient"
	VerdictAskHuman Verdict = "ask_human"
	VerdictHalt     Verdict = "halt"
)

// PreCheckpoint records the agent's stated intent before EXECUTE runs.
// Built by the COMMIT phase via an LLM call against rt.Model.
type PreCheckpoint struct {
	StepID          string
	StepKind        string
	Instruction     string
	Interpretation  string
	ScopeIn         []string
	ScopeOut        []string
	Approach        string
	PredictedOutput string
	Confidence      string
	Assumptions     []string
	Timestamp       time.Time
}

// PostCheckpoint records the agent's self-assessment after EXECUTE.
// Built by the POST phase via an LLM call against rt.Model.
type PostCheckpoint struct {
	StepID        string
	ActualOutput  string
	MetCommitment bool
	Deviations    []string
	Concerns      []string
	Unexpected    []string
	Timestamp     time.Time
}

// ReconcileResult is the deterministic drift signal produced by Supervisor.
type ReconcileResult struct {
	StepID   string
	Triggers []string
	Escalate bool // whether SUPERVISE should fire
}

// SuperviseRequest is the input to the LLM-driven SUPERVISE phase.
type SuperviseRequest struct {
	OriginalGoal  string
	Pre           *PreCheckpoint
	Post          *PostCheckpoint
	Triggers      []string
	HumanRequired bool
	Model         llm.Model // fallback model; used when no dedicated supervisor model is configured
}

// SuperviseResult is the verdict and any correction/question.
type SuperviseResult struct {
	StepID     string
	Verdict    Verdict
	Correction string // populated when Verdict == Reorient or AskHuman
	Question   string // populated when Verdict == AskHuman; sent to human for approval
	Reason     string // populated when Verdict == Halt; explains why execution stopped
}

// Supervisor is the consumer-supplied policy that judges a step's work.
// Implementations live outside the workflow package (e.g., agentcore/supervision).
type Supervisor interface {
	Reconcile(pre *PreCheckpoint, post *PostCheckpoint) *ReconcileResult
	Supervise(ctx context.Context, req SuperviseRequest) (*SuperviseResult, error)
}

// ----- Pipeline orchestration ------------------------------------------------

// runWithSupervision orchestrates the four-phase pipeline when the step is
// supervised, or just calls execute directly when unsupervised.
//
// Phases when supervised:
//
//	COMMIT      LLM declares intent → PreCheckpoint
//	EXECUTE     execute() runs the actual work
//	POST        LLM self-assesses → PostCheckpoint
//	RECONCILE   Supervisor compares pre/post → triggers
//	SUPERVISE   Supervisor (LLM) renders Verdict (skipped if no triggers and not human-required)
//
// Verdict handling:
//
//	Continue → return output as-is
//	Reorient → re-run execute() once with the correction prepended to the instruction
//	AskHuman → send Question on HumanCh; treat non-empty response as approval
//	           (response becomes correction, execute re-runs). Empty/closed/
//	           cancelled → Halt.
//	Halt     → fail the step with the supervisor's Reason.
//
// Reorient is bounded by rt.MaxReorientAttempts (default 1, if zero).
// Exhaustion → AskHuman (if HumanCh wired) else Halt.
//
// byHuman steps skip the LLM-SUPERVISE call entirely: after RECONCILE, they
// route straight to askHuman with the reconcile triggers as context.
func runWithSupervision(
	ctx context.Context,
	rt *Runtime,
	stepID, stepKind, instruction string,
	mode supervision,
	execute func(ctx context.Context, instruction string) (output string, toolsUsed []string, err error),
) (string, error) {
	if mode == notSupervised {
		out, _, err := execute(ctx, instruction)
		return out, err
	}

	// COMMIT
	pre := runCommitPhase(ctx, rt, stepID, stepKind, instruction)

	// EXECUTE
	output, _, err := execute(ctx, instruction)
	if err != nil {
		return "", err
	}

	// POST
	post := runPostPhase(ctx, rt, pre, output)

	// RECONCILE
	reconcile := rt.Supervisor.Reconcile(pre, post)

	// byHuman: skip LLM-SUPERVISE, route straight to human handshake.
	if mode == byHuman {
		q := "Supervision triggers: " + strings.Join(reconcile.Triggers, ", ")
		if q == "Supervision triggers: " {
			q = "Review this step's output."
		}
		return askHuman(ctx, rt, stepID, stepKind, instruction,
			&SuperviseResult{Verdict: VerdictAskHuman, Question: q}, execute)
	}

	// Cost-saving short-circuit: skip SUPERVISE when no drift.
	if !reconcile.Escalate {
		return output, nil
	}

	// Build the supervise request once; reused across reorient attempts.
	req := SuperviseRequest{
		OriginalGoal: instruction,
		Pre:          pre,
		Post:         post,
		Triggers:     reconcile.Triggers,
		Model:        rt.Model,
	}

	max := rt.MaxReorientAttempts
	if max <= 0 {
		max = 1 // default
	}
	for attempt := 0; ; attempt++ {
		result, err := rt.Supervisor.Supervise(ctx, req)
		if err != nil {
			return "", fmt.Errorf("%s %s: supervise phase: %w", stepKind, stepID, err)
		}

		switch result.Verdict {
		case VerdictContinue:
			return output, nil

		case VerdictReorient:
			if attempt >= max {
				if rt.HumanCh != nil {
					return askHuman(ctx, rt, stepID, stepKind, instruction, result, execute)
				}
				return "", fmt.Errorf("%s %s: reorient budget exhausted (%d); halted: %s", stepKind, stepID, max, result.Reason)
			}
			corrected := instruction + "\n\n" + result.Correction
			output, _, err = execute(ctx, corrected)
			if err != nil {
				return "", err
			}
			post = runPostPhase(ctx, rt, pre, output)
			reconcile = rt.Supervisor.Reconcile(pre, post)
			req.Post = post
			req.Triggers = reconcile.Triggers

		case VerdictAskHuman:
			return askHuman(ctx, rt, stepID, stepKind, instruction, result, execute)

		case VerdictHalt:
			return "", fmt.Errorf("%s %s: halted by supervision: %s", stepKind, stepID, result.Reason)

		default:
			return "", fmt.Errorf("%s %s: unknown supervision verdict %q", stepKind, stepID, result.Verdict)
		}
	}
}

// askHuman sends the supervisor's Question on rt.HumanCh and waits for a
// response. A non-empty response is treated as approval — the response is used
// as a correction and execute re-runs. Empty, closed, or cancelled returns an
// error.
func askHuman(
	ctx context.Context,
	rt *Runtime,
	stepID, stepKind, instruction string,
	result *SuperviseResult,
	execute func(ctx context.Context, instruction string) (string, []string, error),
) (string, error) {
	if rt.HumanCh == nil {
		return "", fmt.Errorf("%s %s: AskHuman verdict but no HumanCh wired: %s", stepKind, stepID, result.Question)
	}
	rt.HumanCh <- result.Question
	select {
	case approval, ok := <-rt.HumanCh:
		if !ok || strings.TrimSpace(approval) == "" {
			return "", fmt.Errorf("%s %s: denied by human", stepKind, stepID)
		}
		corrected := instruction + "\n\n" + approval
		out, _, err := execute(ctx, corrected)
		return out, err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// runCommitPhase asks the LLM to declare its intent before EXECUTE runs.
// The structured response is parsed into a PreCheckpoint. A malformed
// response or model error is degraded gracefully — the PreCheckpoint is
// still produced with a low-confidence default rather than failing the
// whole step. Returning the checkpoint unconditionally keeps the pipeline
// honest about what was (and wasn't) committed to.
func runCommitPhase(ctx context.Context, rt *Runtime, stepID, stepKind, instruction string) *PreCheckpoint {
	prompt := fmt.Sprintf(`Before executing this %s, declare your intent:

%s: %s

Respond with a JSON object:
{
  "interpretation":   "How you understand this %s",
  "scope_in":         ["What you will do"],
  "scope_out":        ["What you will NOT do"],
  "approach":         "Your planned approach",
  "predicted_output": "What you expect to produce",
  "confidence":       "high|medium|low",
  "assumptions":      ["Assumptions you are making"]
}`, stepKind, strings.ToUpper(stepKind), instruction, stepKind)

	resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "You are declaring your intent before executing. Be specific and honest."},
			{Role: "user", Content: prompt},
		},
	})
	pre := &PreCheckpoint{
		StepID:      stepID,
		StepKind:    stepKind,
		Instruction: instruction,
		Confidence:  "low",
		Timestamp:   time.Now(),
	}
	if err != nil {
		pre.Assumptions = []string{"Failed to obtain commitment from agent: " + err.Error()}
		return pre
	}
	if jsonStr := extractJSONObject(resp.Content); jsonStr != "" {
		var data struct {
			Interpretation  string   `json:"interpretation"`
			ScopeIn         []string `json:"scope_in"`
			ScopeOut        []string `json:"scope_out"`
			Approach        string   `json:"approach"`
			PredictedOutput string   `json:"predicted_output"`
			Confidence      string   `json:"confidence"`
			Assumptions     []string `json:"assumptions"`
		}
		if json.Unmarshal([]byte(jsonStr), &data) == nil {
			pre.Interpretation = data.Interpretation
			pre.ScopeIn = data.ScopeIn
			pre.ScopeOut = data.ScopeOut
			pre.Approach = data.Approach
			pre.PredictedOutput = data.PredictedOutput
			if data.Confidence != "" {
				pre.Confidence = data.Confidence
			} else {
				pre.Confidence = "medium"
			}
			pre.Assumptions = data.Assumptions
		}
	}
	return pre
}

// runPostPhase asks the LLM to self-assess the work it just did. As with
// runCommitPhase, a model error or malformed response degrades gracefully
// rather than aborting the pipeline.
func runPostPhase(ctx context.Context, rt *Runtime, pre *PreCheckpoint, output string) *PostCheckpoint {
	prompt := fmt.Sprintf(`An execution completed. Assess it against the commitment that was declared beforehand:

ORIGINAL INSTRUCTION: %s

DECLARED COMMITMENT:
- Interpretation: %s
- In bounds:  %s
- Out of bounds: %s
- Approach: %s
- Predicted output: %s
- Confidence: %s
- Assumptions: %s

ACTUAL OUTPUT:
%s

Respond with a JSON object:
{
  "met_commitment": true/false,
  "deviations":     ["Any deviations from the plan"],
  "concerns":       ["Any concerns about the output"],
  "unexpected":     ["Anything unexpected that happened"]
}`,
		pre.Instruction,
		pre.Interpretation,
		strings.Join(pre.ScopeIn, ", "),
		strings.Join(pre.ScopeOut, ", "),
		pre.Approach,
		pre.PredictedOutput,
		pre.Confidence,
		strings.Join(pre.Assumptions, "; "),
		output)

	resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Assess the execution below against the commitment that was declared beforehand. Be honest about whether the work stayed on track."},
			{Role: "user", Content: prompt},
		},
	})
	post := &PostCheckpoint{
		StepID:        pre.StepID,
		ActualOutput:  output,
		MetCommitment: true, // optimistic default if assessment fails
		Timestamp:     time.Now(),
	}
	if err != nil {
		post.MetCommitment = false
		post.Concerns = []string{"Failed to obtain self-assessment: " + err.Error()}
		return post
	}
	if jsonStr := extractJSONObject(resp.Content); jsonStr != "" {
		var data struct {
			MetCommitment bool     `json:"met_commitment"`
			Deviations    []string `json:"deviations"`
			Concerns      []string `json:"concerns"`
			Unexpected    []string `json:"unexpected"`
		}
		if json.Unmarshal([]byte(jsonStr), &data) == nil {
			post.MetCommitment = data.MetCommitment
			post.Deviations = data.Deviations
			post.Concerns = data.Concerns
			post.Unexpected = data.Unexpected
		}
	}
	return post
}

// extractJSONObject finds the first { ... } JSON object in a string and
// returns its substring, or empty if none found.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}
