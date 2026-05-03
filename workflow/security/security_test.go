package security

import (
	"context"
	"strings"
	"testing"

	"github.com/vinayprograms/agentkit/contentguard"
	"github.com/vinayprograms/agentkit/llm"
)

// countingModel returns a fixed Chat response and counts invocations. Used
// to drive the screener and reviewer stages deterministically and to verify
// which stages the configured workflow actually runs.
type countingModel struct {
	calls int
	reply string
}

func (m *countingModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls++
	return &llm.ChatResponse{Content: m.reply}, nil
}

func TestBuild_ReturnsUsableGuardForEveryMode(t *testing.T) {
	model := &countingModel{reply: "ALLOW"}
	for _, mode := range []Mode{Default, Paranoid, Research} {
		g, err := Build(mode, "scope", model)
		if err != nil {
			t.Errorf("Build(%v): %v", mode, err)
		}
		if g == nil {
			t.Errorf("Build(%v) returned nil guard", mode)
		}
	}
}

// Default mode: deterministic check Allows when no untrusted content has been
// ingested, so no LLM stage runs.
func TestBuild_DefaultShortCircuitsWithoutUntrusted(t *testing.T) {
	m := &countingModel{reply: "NO"}
	g, err := Build(Default, "", m)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Check(context.Background(), "echo", map[string]any{"x": 1}, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Verdict != contentguard.Allow {
		t.Errorf("expected Allow short-circuit, got %s", res.Verdict)
	}
	if m.calls != 0 {
		t.Errorf("deterministic short-circuit should skip stages; got %d LLM calls", m.calls)
	}
}

// Default mode + untrusted content: escalatory workflow runs the screener
// first; on Allow it stops and the reviewer never runs.
func TestBuild_DefaultEscalatoryStopsOnFirstAllow(t *testing.T) {
	m := &countingModel{reply: "NO"} // screener "NO" → Allow
	g, err := Build(Default, "", m)
	if err != nil {
		t.Fatal(err)
	}
	g.Ingest(contentguard.Untrusted, contentguard.Data, true, "from web", "fetch")

	res, err := g.Check(context.Background(), "exec", map[string]any{}, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Verdict != contentguard.Allow {
		t.Errorf("expected Allow, got %s: %s", res.Verdict, res.Rationale)
	}
	if m.calls != 1 {
		t.Errorf("Default escalatory should run only the screener; got %d calls", m.calls)
	}
}

// Paranoid mode + untrusted content: paranoid workflow forces all stages to
// run even after an Allow verdict, so screener+reviewer = 2 LLM calls.
func TestBuild_ParanoidRunsAllStagesUnderEscalation(t *testing.T) {
	m := &countingModel{reply: "ALLOW"}
	g, err := Build(Paranoid, "", m)
	if err != nil {
		t.Fatal(err)
	}
	g.Ingest(contentguard.Untrusted, contentguard.Data, true, "from web", "fetch")

	res, err := g.Check(context.Background(), "exec", map[string]any{}, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Verdict != contentguard.Allow {
		t.Errorf("expected Allow, got %s: %s", res.Verdict, res.Rationale)
	}
	if m.calls != 2 {
		t.Errorf("Paranoid should run both stages; got %d calls", m.calls)
	}
}

// Paranoid mode: a Deny from the reviewer denies the call regardless of an
// earlier Allow-ish screener verdict.
func TestBuild_ParanoidDeniesOnAnyDeny(t *testing.T) {
	// Both stages get the same reply. The screener treats "DENY:" as
	// not-YES/NO → Escalate; the reviewer treats "DENY:" → Deny.
	m := &countingModel{reply: "DENY: blocked"}
	g, err := Build(Paranoid, "", m)
	if err != nil {
		t.Fatal(err)
	}
	g.Ingest(contentguard.Untrusted, contentguard.Data, true, "from web", "fetch")

	res, err := g.Check(context.Background(), "exec", map[string]any{}, "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Verdict != contentguard.Deny {
		t.Errorf("expected Deny, got %s: %s", res.Verdict, res.Rationale)
	}
}

// Research mode: scope is propagated into the guard's Context and reaches
// the reviewer's research-permissive system prompt, allowing actions that
// would otherwise be denied.
func TestBuild_ResearchPropagatesScope(t *testing.T) {
	m := &countingModel{reply: "ALLOW"}
	g, err := Build(Research, "auth-token-handling", m)
	if err != nil {
		t.Fatal(err)
	}
	g.Ingest(contentguard.Untrusted, contentguard.Data, true, "from web", "fetch")

	res, err := g.Check(context.Background(), "exec", map[string]any{"cmd": "ls"}, "scan auth tokens")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Verdict != contentguard.Allow {
		t.Errorf("expected Allow under Research, got %s: %s", res.Verdict, res.Rationale)
	}
}

// Build itself does not enforce non-empty scope under Research — that's the
// workflow package's Validate responsibility.
func TestBuild_ResearchAcceptsAnyScope(t *testing.T) {
	for _, scope := range []string{"", "auth", strings.Repeat("x", 200)} {
		_, err := Build(Research, scope, &countingModel{reply: "ALLOW"})
		if err != nil {
			t.Errorf("Build(Research, %q): %v", scope, err)
		}
	}
}
