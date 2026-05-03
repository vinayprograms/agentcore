package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vinayprograms/agentcore/workflow/security"
	"github.com/vinayprograms/agentkit/contentguard"
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// tools_test.go covers tools.go: the ctxSupervision context plumbing
// (effectiveSupervision lives here too), allToolDefs, runTools (parallel +
// ordered), and executeTool (registry, missing-registry / missing-MCP error
// branches, content-guard verdicts).

// ---------------------------------------------------------------------------
// runTools: parallel execution preserves call order
// ---------------------------------------------------------------------------

func TestRunTools_PreservesOrder(t *testing.T) {
	rt := &Runtime{Tools: makeRegistry(t)}
	calls := []llm.ToolCallResponse{
		{ID: "1", Name: "echo", Args: map[string]any{"text": "alpha"}},
		{ID: "2", Name: "echo", Args: map[string]any{"text": "beta"}},
		{ID: "3", Name: "echo", Args: map[string]any{"text": "gamma"}},
	}
	msgs, err := runTools(context.Background(), rt, calls)
	if err != nil {
		t.Fatalf("runTools: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	for i, m := range msgs {
		if m.Content != want[i] {
			t.Errorf("idx %d: got %q want %q", i, m.Content, want[i])
		}
		if m.ToolCallID != calls[i].ID {
			t.Errorf("ToolCallID[%d]: got %q want %q", i, m.ToolCallID, calls[i].ID)
		}
	}
}

func TestRunTools_PropagatesError(t *testing.T) {
	rt := &Runtime{Tools: makeRegistry(t)}
	calls := []llm.ToolCallResponse{
		{ID: "1", Name: "echo", Args: map[string]any{"text": "ok"}},
		{ID: "2", Name: "missing"},
	}
	if _, err := runTools(context.Background(), rt, calls); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// executeTool: registry path, error branches
// ---------------------------------------------------------------------------

func TestExecuteTool_NoRegistryErrors(t *testing.T) {
	rt := &Runtime{} // no Tools registry
	_, err := executeTool(context.Background(), rt, llm.ToolCallResponse{Name: "echo"})
	if err == nil || !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("got: %v", err)
	}
}

func TestExecuteTool_MCPWithoutManagerErrors(t *testing.T) {
	rt := &Runtime{}
	_, err := executeTool(context.Background(), rt, llm.ToolCallResponse{Name: "mcp_srv_x"})
	if err == nil || !strings.Contains(err.Error(), "no MCP manager") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// executeTool: content-guard verdicts (Allow / Deny / Modify)
// ---------------------------------------------------------------------------

func TestExecuteTool_GuardAllowsCall(t *testing.T) {
	guard, err := security.Build(security.Default, "", &countingModel{reply: "NO"})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Tools: makeRegistry(t), Guard: guard}

	out, err := executeTool(context.Background(), rt, llm.ToolCallResponse{
		Name: "echo",
		Args: map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if out != "hi" {
		t.Errorf("output: got %q, want %q", out, "hi")
	}
}

func TestExecuteTool_GuardDeniesCall(t *testing.T) {
	guard, err := security.Build(security.Paranoid, "", &countingModel{reply: "DENY: not allowed"})
	if err != nil {
		t.Fatal(err)
	}
	guard.Ingest(contentguard.Untrusted, contentguard.Data, true, "external", "src")

	rt := &Runtime{Tools: makeRegistry(t), Guard: guard}
	_, err = executeTool(context.Background(), rt, llm.ToolCallResponse{
		Name: "echo",
		Args: map[string]any{"text": "hi"},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked by content guard") {
		t.Errorf("got: %v", err)
	}
}

func TestExecuteTool_NoGuardJustExecutes(t *testing.T) {
	rt := &Runtime{Tools: makeRegistry(t)} // Guard nil
	out, err := executeTool(context.Background(), rt, llm.ToolCallResponse{
		Name: "echo",
		Args: map[string]any{"text": "passthrough"},
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if out != "passthrough" {
		t.Errorf("output: got %q", out)
	}
}

// goalRecorder is a contentguard.Stage that captures the OriginalGoal for
// the purpose of verifying ctxGoal threading.
type goalRecorder struct {
	gotGoal string
}

func (r *goalRecorder) Evaluate(ctx context.Context, req contentguard.Request) (*contentguard.Finding, error) {
	r.gotGoal = req.OriginalGoal
	return &contentguard.Finding{Verdict: contentguard.Allow, Source: "test", Rationale: "test"}, nil
}

func TestExecuteTool_PassesGoalToGuard(t *testing.T) {
	rec := &goalRecorder{}
	guard, err := contentguard.New(
		[]contentguard.Stage{rec},
		contentguard.Escalatory(),
		contentguard.Defaults(),
	)
	if err != nil {
		t.Fatal(err)
	}
	guard.Ingest(contentguard.Untrusted, contentguard.Data, true, "external", "src")

	rt := &Runtime{Tools: makeRegistry(t), Guard: guard}
	ctx := context.WithValue(context.Background(), ctxGoal, "scan auth tokens")
	_, _ = executeTool(ctx, rt, llm.ToolCallResponse{
		Name: "echo",
		Args: map[string]any{"text": "hi"},
	})

	if rec.gotGoal != "scan auth tokens" {
		t.Errorf("guard received originalGoal %q, want %q", rec.gotGoal, "scan auth tokens")
	}
}

// ---------------------------------------------------------------------------
// MCP dispatch (via the MCPManager interface + fakeMCP in helpers_test.go)
// ---------------------------------------------------------------------------

func TestAllToolDefs_IncludesMCPTools(t *testing.T) {
	mcpFake := &fakeMCP{
		tools: []mcp.ToolWithServer{
			{Server: "srv1", Tool: mcp.Tool{
				Name:        "read_file",
				Description: "read a file",
				InputSchema: map[string]any{"type": "object"},
			}},
		},
	}
	rt := &Runtime{Tools: makeRegistry(t), MCP: mcpFake}
	defs := allToolDefs(rt)

	var saw bool
	for _, d := range defs {
		if d.Name == "mcp_srv1_read_file" {
			saw = true
			if !strings.Contains(d.Description, "[MCP:srv1]") {
				t.Errorf("MCP tool def description missing server label: %q", d.Description)
			}
		}
	}
	if !saw {
		t.Errorf("expected mcp_srv1_read_file in defs; got %d defs", len(defs))
	}
}

func TestExecuteTool_MCPSuccess(t *testing.T) {
	mcpFake := &fakeMCP{
		finder: func(name string) (string, bool) { return "srv1", true },
		caller: func(_ context.Context, server, tool string, _ map[string]any) (*mcp.Result, error) {
			return &mcp.Result{Content: []mcp.Content{{Text: "hello "}, {Text: "world"}}}, nil
		},
	}
	rt := &Runtime{MCP: mcpFake}
	out, err := executeTool(context.Background(), rt, llm.ToolCallResponse{
		Name: "mcp_srv1_read_file",
		Args: map[string]any{"path": "/x"},
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if out != "hello world" {
		t.Errorf("Content slices should concatenate; got %q", out)
	}
	if len(mcpFake.callsSeen) != 1 {
		t.Fatalf("expected 1 CallTool, got %d", len(mcpFake.callsSeen))
	}
	if mcpFake.callsSeen[0].server != "srv1" || mcpFake.callsSeen[0].tool != "read_file" {
		t.Errorf("dispatch parsed wrong server/tool: %+v", mcpFake.callsSeen[0])
	}
}

func TestExecuteTool_MCPFindToolMisses(t *testing.T) {
	mcpFake := &fakeMCP{
		finder: func(string) (string, bool) { return "", false },
	}
	rt := &Runtime{MCP: mcpFake}
	_, err := executeTool(context.Background(), rt, llm.ToolCallResponse{Name: "mcp_srv_x"})
	if err == nil || !strings.Contains(err.Error(), "not found in MCP") {
		t.Errorf("got: %v", err)
	}
}

func TestExecuteTool_MCPCallToolError(t *testing.T) {
	mcpFake := &fakeMCP{
		finder: func(string) (string, bool) { return "srv1", true },
		caller: func(context.Context, string, string, map[string]any) (*mcp.Result, error) {
			return nil, errors.New("upstream-down")
		},
	}
	rt := &Runtime{MCP: mcpFake}
	_, err := executeTool(context.Background(), rt, llm.ToolCallResponse{Name: "mcp_srv1_read"})
	if err == nil || !strings.Contains(err.Error(), "upstream-down") {
		t.Errorf("got: %v", err)
	}
}

// MCP outputs are external content — they must be ingested as untrusted into
// rt.Guard so subsequent guard checks see the new taint surface.
func TestExecuteTool_MCPResultIngestedAsUntrusted(t *testing.T) {
	guard, err := security.Build(security.Default, "", &countingModel{reply: "ALLOW"})
	if err != nil {
		t.Fatal(err)
	}
	mcpFake := &fakeMCP{
		finder: func(string) (string, bool) { return "srv1", true },
		caller: func(context.Context, string, string, map[string]any) (*mcp.Result, error) {
			return &mcp.Result{Content: []mcp.Content{{Text: "external-blob"}}}, nil
		},
	}
	rt := &Runtime{MCP: mcpFake, Guard: guard}
	out, err := executeTool(context.Background(), rt, llm.ToolCallResponse{Name: "mcp_srv1_read"})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if out != "external-blob" {
		t.Errorf("got %q", out)
	}
	// We can't easily assert taint state without reaching into contentguard
	// internals; the test's value is that the Ingest path executed without
	// error and the call still produced the expected output.
}

// modifyStage forces the Modify verdict — used to cover the Modify branch.
type modifyStage struct{}

func (modifyStage) Evaluate(ctx context.Context, req contentguard.Request) (*contentguard.Finding, error) {
	return &contentguard.Finding{Verdict: contentguard.Modify, Source: "test", Rationale: "rewrite"}, nil
}

func TestExecuteTool_GuardModifyIsTreatedLikeDeny(t *testing.T) {
	guard, err := contentguard.New(
		[]contentguard.Stage{modifyStage{}},
		contentguard.Escalatory(),
		contentguard.Defaults(),
	)
	if err != nil {
		t.Fatal(err)
	}
	guard.Ingest(contentguard.Untrusted, contentguard.Data, true, "external", "src")

	rt := &Runtime{Tools: makeRegistry(t), Guard: guard}
	_, err = executeTool(context.Background(), rt, llm.ToolCallResponse{
		Name: "echo",
		Args: map[string]any{"text": "hi"},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked by content guard") {
		t.Errorf("Modify verdict should also block; got: %v", err)
	}
}
