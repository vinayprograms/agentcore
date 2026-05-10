package supervise

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vinayprograms/agentcore/workflow"
	"github.com/vinayprograms/agentkit/llm"
)

// Supervise calls a configured LLM to evaluate the agent's execution against
// its stated intent. It receives the original goal, the pre/post checkpoints,
// and any reconcile triggers. It renders a verdict: Continue, Reorient,
// AskHuman, or Halt.
//
// The model used is Config.Model (if non-nil) or req.Model (fallback). If
// both are nil, Supervise returns an error — there is no implicit default.
func (s *supervisor) Supervise(ctx context.Context, req workflow.SuperviseRequest) (*workflow.SuperviseResult, error) {
	m := s.cfg.Model
	if m == nil {
		m = req.Model
	}
	if m == nil {
		return nil, fmt.Errorf("supervise: no model configured")
	}

	prompt := buildPrompt(req)
	resp, err := m.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: supervisorSystem},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("supervise: model call: %w", err)
	}

	return parseVerdict(req.Pre.StepID, resp.Content)
}

const supervisorSystem = `You are a supervisor evaluating whether an agent's execution stayed faithful to its stated intent. Your job is not to judge whether the output is correct — it is to detect drift between what the agent SAID it would do and what it ACTUALLY did.

Examine the three records below:
1. INTENT — the agent's declared plan before acting
2. SELF-ASSESSMENT — the agent's honest reflection on its own work
3. DRIFT TRIGGERS — specific flags the system noticed

Respond with a JSON object containing:
{
  "verdict":    "continue|reorient|ask_human|halt",
  "correction": "what the agent should do differently (required for reorient, optional for ask_human)",
  "question":   "question for the human reviewer (required for ask_human)",
  "reason":     "why you reached this verdict (always required)"
}

Verdict guide:
- continue:   the agent did what it committed to; no meaningful drift
- reorient:   minor drift detected; the agent can fix it with your correction
- ask_human:  significant ambiguity; a human reviewer should decide
- halt:       the agent's behavior is unacceptable; stop immediately`

func buildPrompt(req workflow.SuperviseRequest) string {
	var b strings.Builder

	b.WriteString("ORIGINAL GOAL:\n")
	b.WriteString(req.OriginalGoal)
	b.WriteString("\n\n")

	b.WriteString("INTENT (COMMIT):\n")
	b.WriteString(fmt.Sprintf("Interpretation: %s\n", req.Pre.Interpretation))
	b.WriteString(fmt.Sprintf("Scope in:  %s\n", strings.Join(req.Pre.ScopeIn, ", ")))
	b.WriteString(fmt.Sprintf("Scope out: %s\n", strings.Join(req.Pre.ScopeOut, ", ")))
	b.WriteString(fmt.Sprintf("Approach:  %s\n", req.Pre.Approach))
	b.WriteString(fmt.Sprintf("Confidence: %s\n", req.Pre.Confidence))
	if len(req.Pre.Assumptions) > 0 {
		b.WriteString(fmt.Sprintf("Assumptions: %s\n", strings.Join(req.Pre.Assumptions, "; ")))
	}
	b.WriteString("\n")

	b.WriteString("SELF-ASSESSMENT (POST):\n")
	b.WriteString(fmt.Sprintf("Met commitment: %v\n", req.Post.MetCommitment))
	if len(req.Post.Deviations) > 0 {
		b.WriteString(fmt.Sprintf("Deviations: %s\n", strings.Join(req.Post.Deviations, "; ")))
	}
	if len(req.Post.Concerns) > 0 {
		b.WriteString(fmt.Sprintf("Concerns: %s\n", strings.Join(req.Post.Concerns, "; ")))
	}
	if len(req.Post.Unexpected) > 0 {
		b.WriteString(fmt.Sprintf("Unexpected: %s\n", strings.Join(req.Post.Unexpected, "; ")))
	}

	if len(req.Triggers) > 0 {
		b.WriteString("\nDRIFT TRIGGERS:\n")
		for _, t := range req.Triggers {
			b.WriteString(fmt.Sprintf("- %s\n", t))
		}
	}

	return b.String()
}

// parseVerdict extracts a SuperviseResult from a JSON response string. On
// parse failure, it degrades to an AskHuman verdict so a human can resolve
// the ambiguous output.
func parseVerdict(stepID, content string) (*workflow.SuperviseResult, error) {
	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return &workflow.SuperviseResult{
			StepID:  stepID,
			Verdict: workflow.VerdictAskHuman,
			Question: fmt.Sprintf("The supervisor produced an unparseable response:\n\n%s\n\nWhat should happen next?",
				truncate(content, 500)),
		}, nil
	}

	var data struct {
		Verdict    string `json:"verdict"`
		Correction string `json:"correction"`
		Question   string `json:"question"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return &workflow.SuperviseResult{
			StepID:  stepID,
			Verdict: workflow.VerdictAskHuman,
			Question: fmt.Sprintf("The supervisor produced malformed JSON:\n\n%s\n\nError: %v\n\nWhat should happen next?",
				truncate(content, 500), err),
		}, nil
	}

	v := parseVerdictString(data.Verdict)
	return &workflow.SuperviseResult{
		StepID:     stepID,
		Verdict:    v,
		Correction: data.Correction,
		Question:   data.Question,
		Reason:     data.Reason,
	}, nil
}

func parseVerdictString(s string) workflow.Verdict {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "reorient":
		return workflow.VerdictReorient
	case "ask_human":
		return workflow.VerdictAskHuman
	case "halt":
		return workflow.VerdictHalt
	default:
		return workflow.VerdictContinue
	}
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
