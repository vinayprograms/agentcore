package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/vinayprograms/agentkit/contentguard"
	"github.com/vinayprograms/agentkit/llm"
)

// ctxKey is the unexported type for context values set by this package.
type ctxKey int

const (
	// ctxGoal carries the current goal's interpolated description into
	// executeTool, so the content guard can pass it as originalGoal.
	ctxGoal ctxKey = iota

	// ctxSupervision carries the inherited supervision mode through the
	// workflow → sequence → goal/convergence chain, so a workflow-level
	// or sequence-level Supervise() flows down to its descendants unless
	// they override.
	ctxSupervision
)

// effectiveSupervision returns the supervision a step should use: its own
// value if non-zero, otherwise the parent's mode read from ctx.
func effectiveSupervision(ctx context.Context, own supervision) supervision {
	if own != notSupervised {
		return own
	}
	if v, ok := ctx.Value(ctxSupervision).(supervision); ok {
		return v
	}
	return notSupervised
}

const mcpPrefix = "mcp_"

// allToolDefs returns the merged tool definitions from the built-in registry
// and any MCP servers wired into env.
func allToolDefs(rt *Runtime) []llm.ToolDef {
	var defs []llm.ToolDef
	if rt.Tools != nil {
		for _, d := range rt.Tools.Definitions() {
			defs = append(defs, llm.ToolDef{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.JSONSchema(),
			})
		}
	}
	if rt.MCP != nil {
		for _, t := range rt.MCP.AllTools() {
			defs = append(defs, llm.ToolDef{
				Name:        fmt.Sprintf("%s%s_%s", mcpPrefix, t.Server, t.Tool.Name),
				Description: fmt.Sprintf("[MCP:%s] %s", t.Server, t.Tool.Description),
				Parameters:  t.Tool.InputSchema,
			})
		}
	}
	return defs
}

// runTools executes tool calls in parallel and returns tool-result messages
// in the same order as the input calls.
func runTools(ctx context.Context, rt *Runtime, calls []llm.ToolCallResponse) ([]llm.Message, error) {
	type result struct {
		idx int
		msg llm.Message
		err error
	}

	ch := make(chan result, len(calls))
	for idx, tc := range calls {
		go func(idx int, tc llm.ToolCallResponse) {
			out, err := executeTool(ctx, rt, tc)
			if err != nil {
				ch <- result{idx: idx, err: err}
				return
			}
			ch <- result{idx: idx, msg: llm.Message{
				Role:       "tool",
				Content:    out,
				ToolCallID: tc.ID,
			}}
		}(idx, tc)
	}

	msgs := make([]llm.Message, len(calls))
	for range calls {
		r := <-ch
		if r.err != nil {
			return nil, r.err
		}
		msgs[r.idx] = r.msg
	}
	return msgs, nil
}

// executeTool dispatches a single tool call. When rt.Guard is non-nil the
// call is gated through the content guard and any external (MCP) result is
// ingested as untrusted content for downstream taint tracking.
func executeTool(ctx context.Context, rt *Runtime, tc llm.ToolCallResponse) (string, error) {
	if rt.Guard != nil {
		goal, _ := ctx.Value(ctxGoal).(string)
		res, err := rt.Guard.Check(ctx, tc.Name, tc.Args, goal)
		if err != nil {
			return "", fmt.Errorf("guard check %s: %w", tc.Name, err)
		}
		switch res.Verdict {
		case contentguard.Deny, contentguard.Modify:
			return "", fmt.Errorf("tool %s blocked by content guard: %s", tc.Name, res.Rationale)
		}
	}

	// MCP tool: name is "mcp_<server>_<tool>"
	if strings.HasPrefix(tc.Name, mcpPrefix) {
		if rt.MCP == nil {
			return "", fmt.Errorf("tool %s: no MCP manager", tc.Name)
		}
		server, found := rt.MCP.FindTool(tc.Name)
		if !found {
			return "", fmt.Errorf("tool %s: not found in MCP", tc.Name)
		}
		toolName := tc.Name[len(mcpPrefix)+len(server)+1:]
		result, err := rt.MCP.CallTool(ctx, server, toolName, tc.Args)
		if err != nil {
			return "", fmt.Errorf("mcp %s/%s: %w", server, toolName, err)
		}
		var b strings.Builder
		for _, c := range result.Content {
			b.WriteString(c.Text)
		}
		out := b.String()

		// MCP outputs are external — register as untrusted so subsequent
		// guard checks see the new content as part of the taint surface.
		if rt.Guard != nil {
			rt.Guard.Ingest(contentguard.Untrusted, contentguard.Data, true, out, tc.Name)
		}
		return out, nil
	}

	// Built-in tool.
	if rt.Tools == nil {
		return "", fmt.Errorf("tool %s: no tool registry", tc.Name)
	}
	return rt.Tools.Execute(ctx, tc.Name, tc.Args)
}
