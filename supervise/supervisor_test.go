package supervise

import (
	"context"
	"strings"
	"testing"

	"github.com/vinayprograms/agentcore/workflow"
)

func TestSupervise_UsesConfigModel(t *testing.T) {
	m := &scriptedModel{replies: []string{`{"verdict":"continue","reason":"ok"}`}}
	s := New(Config{Model: m})
	req := workflow.SuperviseRequest{
		OriginalGoal: "do it",
		Pre:          &workflow.PreCheckpoint{StepID: "s1", Interpretation: "I will do X"},
		Post:         &workflow.PostCheckpoint{MetCommitment: true},
		Triggers:     []string{"concern:something"},
		Model:        nil, // ensure fallback not needed
	}
	result, err := s.Supervise(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictContinue {
		t.Errorf("verdict: got %q, want continue", result.Verdict)
	}
}

func TestSupervise_FallsBackToReqModel(t *testing.T) {
	m := &scriptedModel{replies: []string{`{"verdict":"reorient","correction":"try harder","reason":"scope drift"}`}}
	s := New(Config{}) // no Config.Model
	req := workflow.SuperviseRequest{
		OriginalGoal: "do it",
		Pre:          &workflow.PreCheckpoint{StepID: "s1", Interpretation: "I will do X"},
		Post:         &workflow.PostCheckpoint{MetCommitment: false},
		Triggers:     []string{"commitment_not_met"},
		Model:        m,
	}
	result, err := s.Supervise(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictReorient {
		t.Errorf("verdict: got %q, want reorient", result.Verdict)
	}
}

func TestSupervise_NoModelReturnsError(t *testing.T) {
	s := New(Config{})
	req := workflow.SuperviseRequest{
		Pre:  &workflow.PreCheckpoint{StepID: "s1"},
		Post: &workflow.PostCheckpoint{},
	}
	_, err := s.Supervise(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("expected 'no model configured' error, got: %v", err)
	}
}

func TestSupervise_ModelErrorPropagates(t *testing.T) {
	m := &errorModel{err: "api down"}
	s := New(Config{Model: m})
	req := workflow.SuperviseRequest{
		Pre:  &workflow.PreCheckpoint{StepID: "s1"},
		Post: &workflow.PostCheckpoint{},
	}
	_, err := s.Supervise(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "supervise: model call") {
		t.Errorf("expected model-call error, got: %v", err)
	}
}

func TestSupervise_VerdictHalt(t *testing.T) {
	m := &scriptedModel{replies: []string{`{"verdict":"halt","reason":"agent attempted to delete files"}`}}
	s := New(Config{Model: m})
	req := workflow.SuperviseRequest{
		OriginalGoal: "do it",
		Pre:          &workflow.PreCheckpoint{StepID: "s1", Interpretation: "I will do X"},
		Post:         &workflow.PostCheckpoint{MetCommitment: false},
	}
	result, err := s.Supervise(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictHalt {
		t.Errorf("verdict: got %q, want halt", result.Verdict)
	}
	if result.Reason != "agent attempted to delete files" {
		t.Errorf("reason: %q", result.Reason)
	}
}

func TestSupervise_VerdictAskHuman(t *testing.T) {
	m := &scriptedModel{replies: []string{`{"verdict":"ask_human","question":"Should we proceed with this approach?","reason":"ambiguous scope"}`}}
	s := New(Config{Model: m})
	req := workflow.SuperviseRequest{
		OriginalGoal: "do it",
		Pre:          &workflow.PreCheckpoint{StepID: "s1", Interpretation: "I will do X"},
		Post:         &workflow.PostCheckpoint{MetCommitment: true, Deviations: []string{"changed approach"}},
	}
	result, err := s.Supervise(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictAskHuman {
		t.Errorf("verdict: got %q, want ask_human", result.Verdict)
	}
	if result.Question != "Should we proceed with this approach?" {
		t.Errorf("question: %q", result.Question)
	}
}

// ---------------------------------------------------------------------------
// parseVerdict: degradation paths.
// ---------------------------------------------------------------------------

func TestParseVerdict_UnparseableResponse(t *testing.T) {
	result, err := parseVerdict("s1", "i'm a teapot, not a supervisor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictAskHuman {
		t.Errorf("unparseable response should degrade to AskHuman, got %q", result.Verdict)
	}
	if result.Question == "" {
		t.Error("Question should be populated")
	}
}

func TestParseVerdict_NoBraces(t *testing.T) {
	result, err := parseVerdict("s1", "just some text, no json at all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictAskHuman {
		t.Errorf("no-braces response should degrade to AskHuman, got %q", result.Verdict)
	}
}

func TestParseVerdict_MalformedJSONInsideBraces(t *testing.T) {
	result, err := parseVerdict("s1", "here is {bad json} more text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictAskHuman {
		t.Errorf("malformed JSON should degrade to AskHuman, got %q", result.Verdict)
	}
}

func TestParseVerdict_ValidContinue(t *testing.T) {
	result, err := parseVerdict("s1", `{"verdict":"continue","reason":"on track"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictContinue {
		t.Errorf("got %q", result.Verdict)
	}
	if result.Reason != "on track" {
		t.Errorf("reason: %q", result.Reason)
	}
}

func TestParseVerdict_ValidReorient(t *testing.T) {
	result, err := parseVerdict("s1", `{"verdict":"reorient","correction":"focus on scope","reason":"drifted"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictReorient {
		t.Errorf("got %q", result.Verdict)
	}
	if result.Correction != "focus on scope" {
		t.Errorf("correction: %q", result.Correction)
	}
}

func TestParseVerdict_UnknownVerdictDefaultsToContinue(t *testing.T) {
	result, err := parseVerdict("s1", `{"verdict":"celebrate","reason":"party time"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != workflow.VerdictContinue {
		t.Errorf("unknown verdict should default to Continue, got %q", result.Verdict)
	}
}

func TestParseVerdict_Truncation(t *testing.T) {
	long := strings.Repeat("x", 600)
	result, err := parseVerdict("s1", long)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Question) >= len(long) {
		t.Error("long content should be truncated in degraded response")
	}
}

// ---------------------------------------------------------------------------
// parseVerdictString: case-normalization.
// ---------------------------------------------------------------------------

func TestParseVerdictString(t *testing.T) {
	cases := []struct {
		in   string
		want workflow.Verdict
	}{
		{"continue", workflow.VerdictContinue},
		{"CONTINUE", workflow.VerdictContinue},
		{"  continue  ", workflow.VerdictContinue},
		{"reorient", workflow.VerdictReorient},
		{"REORIENT", workflow.VerdictReorient},
		{"ask_human", workflow.VerdictAskHuman},
		{"ASK_HUMAN", workflow.VerdictAskHuman},
		{"halt", workflow.VerdictHalt},
		{"HALT", workflow.VerdictHalt},
		{"bogus", workflow.VerdictContinue},
		{"", workflow.VerdictContinue},
	}
	for _, c := range cases {
		got := parseVerdictString(c.in)
		if got != c.want {
			t.Errorf("parseVerdictString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// extractJSON.
// ---------------------------------------------------------------------------

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{`text {"a":1} more`, `{"a":1}`},
		{`no braces`, ""},
		{`}{`, ""},
		{`{`, ""},
	}
	for _, c := range cases {
		if got := extractJSON(c.in); got != c.want {
			t.Errorf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// truncate.
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 100, "short"},
		{"hello world", 5, "hello…"},
		{"exact", 5, "exact"},
		{"", 10, ""},
	}
	for _, c := range cases {
		got := truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// buildPrompt: includes all checkpoint fields.
// ---------------------------------------------------------------------------

func TestBuildPrompt_IncludesAllFields(t *testing.T) {
	req := workflow.SuperviseRequest{
		OriginalGoal: "summarize the report",
		Pre: &workflow.PreCheckpoint{
			StepID:         "s1",
			Interpretation: "extract key points",
			ScopeIn:        []string{"read report", "identify themes"},
			ScopeOut:       []string{"do not interpret", "do not add opinion"},
			Approach:       "scan sections, extract bullet points",
			Confidence:     "high",
			Assumptions:    []string{"report is in English", "length < 10 pages"},
		},
		Post: &workflow.PostCheckpoint{
			MetCommitment: true,
			Deviations:    []string{"added a summary paragraph"},
			Concerns:      []string{"may have missed section 3"},
			Unexpected:    []string{"report was 15 pages"},
		},
		Triggers: []string{"unexpected:report was 15 pages"},
	}
	prompt := buildPrompt(req)

	checks := []string{
		"summarize the report",
		"extract key points",
		"read report",
		"do not interpret",
		"scan sections",
		"high",
		"report is in English",
		"Met commitment: true",
		"added a summary paragraph",
		"may have missed section 3",
		"report was 15 pages",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing %q", check)
		}
	}
}

func TestBuildPrompt_OmitsEmptySections(t *testing.T) {
	req := workflow.SuperviseRequest{
		OriginalGoal: "do it",
		Pre:          &workflow.PreCheckpoint{StepID: "s1", Interpretation: "just do it"},
		Post:         &workflow.PostCheckpoint{MetCommitment: true},
	}
	prompt := buildPrompt(req)
	if strings.Contains(prompt, "Assumptions:") {
		t.Error("empty assumptions should be omitted from prompt")
	}
	if strings.Contains(prompt, "Deviations:") {
		t.Error("empty deviations should be omitted from prompt")
	}
}
