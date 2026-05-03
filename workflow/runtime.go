package workflow

import (
	"context"

	"github.com/vinayprograms/agentkit/contentguard"
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
	"github.com/vinayprograms/agentkit/policy"
	"github.com/vinayprograms/agentkit/tools"
)

// MCPManager is the slice of agentkit's *mcp.Manager that the workflow
// package uses. Defining it as an interface here — consumer-defines-
// interfaces — lets tests fake MCP without standing up a real server;
// *mcp.Manager satisfies it as-is, so existing wiring keeps working.
type MCPManager interface {
	AllTools() []mcp.ToolWithServer
	FindTool(name string) (server string, found bool)
	CallTool(ctx context.Context, server, tool string, args map[string]any) (*mcp.Result, error)
}

// Runtime holds the immutable dependencies a workflow needs to execute.
// It is wired once by the caller and passed by pointer to Workflow.Execute.
//
// At each step's execution the package merges the step's Override (set via
// .Customize) on top of the parent's Runtime to produce an effective Runtime
// for that subtree. Children inherit unless they override in turn.
//
// Only Model is required. Everything else is optional and silently disabled
// when nil or unset.
type Runtime struct {
	// Model is the default LLM for goals, convergences, and agents that did
	// not customize a different model. Required.
	Model llm.Model

	// Tools is the built-in tool registry. Nil means no tools available.
	Tools *tools.Registry

	// MCP is the MCP server manager. Nil means no MCP tools. Typed as the
	// MCPManager interface so tests can substitute fakes; pass any
	// *mcp.Manager to satisfy it in production.
	MCP MCPManager

	// Policy enforces tool access rules. Nil means allow-all.
	Policy policy.Lookup

	// Guard is the contentguard instance for taint tracking and tier-based
	// security review. When the workflow declares a security mode and Guard
	// is nil, Workflow.Execute builds one from the declared mode using Model.
	Guard *contentguard.Guard

	// SystemContext is content appended to the system prompt of every Goal,
	// Convergence, and Agent invocation. Use it for any cross-cutting
	// information the LLM should see on every call: workspace layout,
	// behavior guidelines, environment notes, etc. Empty = nothing appended.
	//
	// The field is deliberately a free-form string. The package does not
	// inspect or validate its contents — whatever is supplied is concatenated
	// after the package's default system message.
	SystemContext string

	// Hooks receives workflow events. Nil means events are silently dropped.
	// The agentcore hooks package provides the canonical implementation.
	Hooks EventSink

	// Supervisor judges supervised steps' work. Required when any node in
	// the workflow has Supervise() or SuperviseByHuman() set; nil is fine
	// when no node is supervised. Validate enforces this.
	Supervisor Supervisor

	// CheckpointStore persists the four checkpoint records produced by the
	// supervision pipeline. Optional — nil means "don't persist."
	CheckpointStore CheckpointStore

	// HumanCh is a bidirectional channel used when a step set
	// SuperviseByHuman() and the supervisor returned VerdictPause. The
	// pipeline sends the supervisor's question on the channel and reads
	// the human's response. Required when any node has SuperviseByHuman();
	// Validate enforces this.
	HumanCh chan string

	// Debug enables verbose logging of prompts and responses.
	Debug bool
}

// EventSink receives workflow lifecycle events. Implementations must be
// safe for concurrent use — Fire may be called from multiple goroutines.
type EventSink interface {
	Fire(ctx context.Context, event any)
}

// fire calls Hooks.Fire if Hooks is non-nil, otherwise is a no-op.
func (rt *Runtime) fire(ctx context.Context, event any) {
	if rt.Hooks != nil {
		rt.Hooks.Fire(ctx, event)
	}
}

// Override is the curated subset of Runtime fields a step may customize for
// its own subtree. Set via (*goal).Customize / (*convergence).Customize /
// (*agent).Customize / (*sequence).Customize. Zero-valued fields inherit
// from the parent's Runtime; non-zero fields replace it.
//
// SystemContext is the one append-not-replace field: a step's SystemContext
// is concatenated to the parent's, separated by a blank line. This way each
// level can layer information for the LLM without erasing what the parent
// already declared.
//
// Hooks, Guard, Debug, and SecurityScope are deliberately NOT in Override —
// they are workflow-wide concerns (single sink for events, taint surface
// across the whole run, run-wide debug flag) and have no per-node meaning.
type Override struct {
	Model         llm.Model
	Tools         *tools.Registry
	MCP           MCPManager
	Policy        policy.Lookup
	SystemContext string
}

// merge returns a new *Runtime with o's non-zero fields applied on top of
// rt. The caller's *rt is not mutated. Used internally by each step's
// Execute to produce the effective Runtime for that subtree.
func (rt *Runtime) merge(o Override) *Runtime {
	cp := *rt
	if o.Model != nil {
		cp.Model = o.Model
	}
	if o.Tools != nil {
		cp.Tools = o.Tools
	}
	if o.MCP != nil {
		cp.MCP = o.MCP
	}
	if o.Policy != nil {
		cp.Policy = o.Policy
	}
	if o.SystemContext != "" {
		if cp.SystemContext == "" {
			cp.SystemContext = o.SystemContext
		} else {
			cp.SystemContext = cp.SystemContext + "\n\n" + o.SystemContext
		}
	}
	return &cp
}
