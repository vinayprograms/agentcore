package workflow

import (
	"context"
	"strings"
	"testing"
)

// prompts_test.go covers prompts.go: buildSystemPrompt, buildPrompt,
// buildConvergePrompt, parseStructured, stepName.

// ---------------------------------------------------------------------------
// buildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_AppendsSystemContext(t *testing.T) {
	rt := &Runtime{SystemContext: "extra"}
	got := buildSystemPrompt(rt)
	if !strings.Contains(got, "extra") {
		t.Errorf("SystemContext not appended: %q", got)
	}
}

func TestBuildSystemPrompt_DefaultWhenNoContext(t *testing.T) {
	got := buildSystemPrompt(&Runtime{})
	if !strings.Contains(got, "helpful assistant") {
		t.Errorf("default base prompt missing: %q", got)
	}
}

// ---------------------------------------------------------------------------
// buildPrompt
// ---------------------------------------------------------------------------

func TestBuildPrompt_NoPriorOutputs(t *testing.T) {
	got := buildPrompt("g", "do it", NewState(nil), nil)
	if strings.Contains(got, "<prior-goals>") {
		t.Errorf("should omit prior-goals when none present: %q", got)
	}
	if !strings.Contains(got, "do it") {
		t.Errorf("description missing: %q", got)
	}
}

func TestBuildPrompt_WithPriorOutputsAndStructured(t *testing.T) {
	state := NewState(nil)
	state.Outputs["prior"] = "earlier"
	got := buildPrompt("g", "task", state, []string{"f1", "f2"})
	for _, want := range []string{"prior-goals", "earlier", "f1", "f2", "JSON object"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// buildConvergePrompt
// ---------------------------------------------------------------------------

func TestBuildConvergePrompt_WithHistoryAndOutputs(t *testing.T) {
	state := NewState(nil)
	state.Outputs["prior"] = "earlier"
	state.Outputs["c"] = "should-be-skipped" // own-name skip
	got := buildConvergePrompt("c", "refine", state, []string{"i1", "i2"}, []string{"verdict"})
	for _, want := range []string{"prior", "earlier", "convergence-history",
		"iteration n=1", "iteration n=2", "Refine the previous", "verdict"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "should-be-skipped") {
		t.Errorf("own-name output should be skipped from prior-goals")
	}
}

func TestBuildConvergePrompt_FirstIterationNoHistory(t *testing.T) {
	got := buildConvergePrompt("c", "task", NewState(nil), nil, nil)
	if !strings.Contains(got, "When satisfied") {
		t.Errorf("expected first-iteration prompt, got: %s", got)
	}
}

// ---------------------------------------------------------------------------
// parseStructured
// ---------------------------------------------------------------------------

func TestParseStructured_NoJSONReturnsError(t *testing.T) {
	if _, err := parseStructured("plain text", []string{"a"}); err == nil {
		t.Error("expected error for non-JSON output")
	}
}

func TestParseStructured_BrokenJSONReturnsError(t *testing.T) {
	if _, err := parseStructured("{not json}", []string{"a"}); err == nil {
		t.Error("expected unmarshal error")
	}
}

func TestParseStructured_MissingFieldErrors(t *testing.T) {
	_, err := parseStructured(`{"a":"x"}`, []string{"a", "missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("got: %v", err)
	}
}

func TestParseStructured_RemarshalsNonStringFields(t *testing.T) {
	m, err := parseStructured(`{"obj":{"k":1}}`, []string{"obj"})
	if err != nil {
		t.Fatalf("parseStructured: %v", err)
	}
	if m["obj"] != `{"k":1}` {
		t.Errorf("re-marshal: got %q", m["obj"])
	}
}

// ---------------------------------------------------------------------------
// stepName
// ---------------------------------------------------------------------------

func TestStepName_AgentReturnsName(t *testing.T) {
	if got := stepName(Agent("alpha", "p")); got != "alpha" {
		t.Errorf("got %q", got)
	}
}

func TestStepName_NonNodeReturnsUnknown(t *testing.T) {
	if got := stepName(&nameless{}); got != "unknown" {
		t.Errorf("got %q", got)
	}
}

// nameless is a Step that does NOT implement Node — used to trigger the
// fallback branch in stepName.
type nameless struct{}

func (nameless) Execute(ctx context.Context, rt *Runtime, state *State) error { return nil }
func (n *nameless) clone() Step                                               { return n }
