package workflow

import (
	"context"
	"testing"

	"github.com/vinayprograms/agentkit/mcp"
	"github.com/vinayprograms/agentkit/policy"
)

// runtime_test.go covers runtime.go: the Runtime / Override types, the merge
// algebra (every Override field, parent-empty SystemContext branch, identity
// path), and the Hooks fire helper.

func TestRuntime_MergeIdentityWhenAllZero(t *testing.T) {
	parent := &Runtime{Model: &countingModel{}, SystemContext: "ctx"}
	merged := parent.merge(Override{})
	if merged == parent {
		t.Errorf("merge should return a new pointer")
	}
	if merged.SystemContext != "ctx" {
		t.Errorf("SystemContext should pass through")
	}
}

func TestRuntime_MergeAllOverrides(t *testing.T) {
	parent := &Runtime{Model: &countingModel{}}
	mcpMgr := mcp.NewManager()
	pol := policy.New()
	merged := parent.merge(Override{
		MCP:           mcpMgr,
		Policy:        pol,
		SystemContext: "fresh", // parent has empty SystemContext
	})
	// MCP is now an interface; compare via the concrete *mcp.Manager value.
	if merged.MCP.(*mcp.Manager) != mcpMgr {
		t.Errorf("MCP override did not stick")
	}
	if merged.Policy != pol {
		t.Errorf("Policy override did not stick")
	}
	if merged.SystemContext != "fresh" {
		t.Errorf("SystemContext should be set when parent was empty; got %q", merged.SystemContext)
	}
}

func TestRuntime_MergeSystemContextAppendsToExisting(t *testing.T) {
	parent := &Runtime{Model: &countingModel{}, SystemContext: "L0"}
	merged := parent.merge(Override{SystemContext: "L1"})
	if merged.SystemContext != "L0\n\nL1" {
		t.Errorf("expected L0\\n\\nL1, got %q", merged.SystemContext)
	}
}

func TestRuntime_FireWithHooks(t *testing.T) {
	hooks := &hookSink{}
	rt := &Runtime{Model: &countingModel{}, Hooks: hooks}
	g := Goal("g", "do it")
	if err := g.Execute(context.Background(), rt, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(hooks.events) == 0 {
		t.Errorf("expected lifecycle events to fire")
	}
}

func TestRuntime_FireWithoutHooksIsNoOp(t *testing.T) {
	rt := &Runtime{} // Hooks is nil
	rt.fire(context.Background(), GoalStarted{Name: "g", Description: "d"})
	// no panic, no observable effect — the assertion is "didn't crash."
}
