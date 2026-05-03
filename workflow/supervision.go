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
	VerdictPause    Verdict = "pause"
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
	ToolsPlanned    []string
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
	ToolsUsed     []string
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
}

// SuperviseResult is the verdict and any correction/question.
type SuperviseResult struct {
	StepID     string
	Verdict    Verdict
	Correction string // populated when Verdict == Reorient
	Question   string // populated when Verdict == Pause and human approval is needed
}

// Supervisor is the consumer-supplied policy that judges a step's work.
// Implementations live outside the workflow package (e.g., agentcore/supervision).
type Supervisor interface {
	Reconcile(pre *PreCheckpoint, post *PostCheckpoint) *ReconcileResult
	Supervise(ctx context.Context, req SuperviseRequest) (*SuperviseResult, error)
}

// CheckpointStore is consumer-supplied persistence for the four checkpoint
// records produced by the pipeline. A nil store means "don't persist."
type CheckpointStore interface {
	SavePre(*PreCheckpoint) error
	SavePost(*PostCheckpoint) error
	SaveReconcile(*ReconcileResult) error
	SaveSupervise(*SuperviseResult) error
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
//	Pause    → if SuperviseByHuman + Runtime.HumanCh wired, dispatch Question and
//	           treat a non-empty response as approval (uses response as correction
//	           and re-runs execute). Otherwise, returns an error.
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
	if rt.CheckpointStore != nil {
		_ = rt.CheckpointStore.SavePre(pre)
	}

	// EXECUTE
	output, toolsUsed, err := execute(ctx, instruction)
	if err != nil {
		return "", err
	}

	// POST
	post := runPostPhase(ctx, rt, pre, output, toolsUsed)
	if rt.CheckpointStore != nil {
		_ = rt.CheckpointStore.SavePost(post)
	}

	// RECONCILE
	reconcile := rt.Supervisor.Reconcile(pre, post)
	if rt.CheckpointStore != nil {
		_ = rt.CheckpointStore.SaveReconcile(reconcile)
	}

	humanRequired := mode == byHuman
	// Cost-saving short-circuit: skip SUPERVISE when no drift AND no human
	// approval is required for this step.
	if !reconcile.Escalate && !humanRequired {
		return output, nil
	}

	// SUPERVISE
	result, err := rt.Supervisor.Supervise(ctx, SuperviseRequest{
		OriginalGoal:  instruction,
		Pre:           pre,
		Post:          post,
		Triggers:      reconcile.Triggers,
		HumanRequired: humanRequired,
	})
	if err != nil {
		return "", fmt.Errorf("%s %s: supervise phase: %w", stepKind, stepID, err)
	}
	if rt.CheckpointStore != nil {
		_ = rt.CheckpointStore.SaveSupervise(result)
	}

	switch result.Verdict {
	case VerdictContinue:
		return output, nil

	case VerdictReorient:
		corrected := instruction + "\n\nSupervisor correction: " + result.Correction
		out, _, err := execute(ctx, corrected)
		return out, err

	case VerdictPause:
		if humanRequired && rt.HumanCh != nil {
			rt.HumanCh <- result.Question
			select {
			case approval, ok := <-rt.HumanCh:
				if !ok || strings.TrimSpace(approval) == "" {
					return "", fmt.Errorf("%s %s: paused by supervision, denied by human", stepKind, stepID)
				}
				corrected := instruction + "\n\nHuman correction: " + approval
				out, _, err := execute(ctx, corrected)
				return out, err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "", fmt.Errorf("%s %s: paused by supervision: %s", stepKind, stepID, result.Question)

	default:
		return "", fmt.Errorf("%s %s: unknown supervision verdict %q", stepKind, stepID, result.Verdict)
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
  "tools_planned":    ["tools you expect to use"],
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
			ToolsPlanned    []string `json:"tools_planned"`
			PredictedOutput string   `json:"predicted_output"`
			Confidence      string   `json:"confidence"`
			Assumptions     []string `json:"assumptions"`
		}
		if json.Unmarshal([]byte(jsonStr), &data) == nil {
			pre.Interpretation = data.Interpretation
			pre.ScopeIn = data.ScopeIn
			pre.ScopeOut = data.ScopeOut
			pre.Approach = data.Approach
			pre.ToolsPlanned = data.ToolsPlanned
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
func runPostPhase(ctx context.Context, rt *Runtime, pre *PreCheckpoint, output string, toolsUsed []string) *PostCheckpoint {
	prompt := fmt.Sprintf(`You just completed a step. Assess your work:

ORIGINAL INSTRUCTION: %s

YOUR COMMITMENT:
- Interpretation: %s
- Approach: %s
- Predicted output: %s

ACTUAL OUTPUT:
%s

TOOLS USED: %s

Respond with a JSON object:
{
  "met_commitment": true/false,
  "deviations":     ["Any deviations from your plan"],
  "concerns":       ["Any concerns about your output"],
  "unexpected":     ["Anything unexpected that happened"]
}`,
		pre.Instruction, pre.Interpretation, pre.Approach, pre.PredictedOutput,
		output, strings.Join(toolsUsed, ", "))

	resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "You are honestly assessing whether your work met your commitment."},
			{Role: "user", Content: prompt},
		},
	})
	post := &PostCheckpoint{
		StepID:        pre.StepID,
		ActualOutput:  output,
		ToolsUsed:     toolsUsed,
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
