package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vinayprograms/agentkit/llm"
)

// agent is a named persona with a fixed prompt. Implements both Node and Step.
// Constructed via workflow.Agent — never directly.
type agent struct {
	name    string
	prompt  string
	outputs []string

	// override layers on top of the parent runtime when this agent executes.
	// Set via .Customize().
	override Override

	// task is the per-invocation instruction the agent should perform.
	// Set via .Task() before .Execute(). Required.
	task string
}

// Agent constructs an agent with the given name and persona prompt.
// Each call returns an independent instance.
func Agent(name, prompt string) *agent {
	return &agent{name: name, prompt: prompt}
}

// Customize layers per-agent runtime overrides on top of whatever the
// parent step or workflow provided. Use it to pin a specific Model, narrow
// the tool registry, swap the MCP manager, tighten policy, or append agent-
// specific text to the system context. Fields left zero in o inherit from
// the parent. Calling Customize replaces any previously-set override.
//
//	critic := workflow.Agent("critic", "...").Customize(workflow.Override{
//	    Model: heavyModel,
//	    Tools: readOnlyRegistry,
//	})
func (a *agent) Customize(o Override) *agent {
	a.override = o
	return a
}

// Task sets the per-invocation instruction this agent will perform when
// .Execute() is called next. Required before Execute. Agents are otherwise
// stateless — Task is the per-call input, separate from the persona prompt
// (which describes who the agent IS, not what it should do this time).
//
// Goals and Convergences set Task automatically on their cloned children
// during fan-out (passing their interpolated description as the task).
// Standalone callers set it explicitly.
func (a *agent) Task(t string) *agent {
	a.task = t
	return a
}

// WithOutputs declares structured output field names for this agent.
func (a *agent) WithOutputs(fields ...string) *agent {
	a.outputs = append(a.outputs, fields...)
	return a
}

// Name implements Node.
func (a *agent) Name() string { return a.name }

// Kind implements Node.
func (a *agent) Kind() Kind { return kindOf(a) }

// Children implements Node. Agents are leaves in the tree.
func (a *agent) Children() []Node { return nil }

// Validate checks the agent's structural integrity. Task is per-invocation
// and is checked at Execute time, not here — Validate is for declarative
// fields only.
func (a *agent) Validate() error {
	var errs []error
	if strings.TrimSpace(a.name) == "" {
		errs = append(errs, fmt.Errorf("agent: name is required"))
	}
	if strings.TrimSpace(a.prompt) == "" {
		errs = append(errs, fmt.Errorf("agent %s: prompt is required", a.name))
	}
	for _, out := range a.outputs {
		if strings.TrimSpace(out) == "" {
			errs = append(errs, fmt.Errorf("agent %s: output has empty name", a.name))
		}
	}
	return errors.Join(errs...)
}

// clone returns an independent copy. All fields are copied, including task
// and override — so a clone's task and customizations start at whatever the
// source had at clone time. Goal / Convergence fan-out then overwrites task
// on their per-invocation clone.
func (a *agent) clone() Step {
	return &agent{
		name:     a.name,
		prompt:   a.prompt,
		outputs:  slices.Clone(a.outputs),
		override: a.override, // value-typed struct; pointer/interface fields share (external infra)
		task:     a.task,
	}
}

// Execute runs the agent's agentic loop. It uses a.task as the user message
// paired with a.prompt as the system message; the final response is written
// to state.Outputs under the agent's name.
//
// Both a.prompt and a.task are interpolated against state at Execute time —
// $var substrings resolve to State.Outputs first, then State.Inputs. The
// interpolation is idempotent: when invoked from a parent Goal or
// Convergence fan-out, the task has already been interpolated by the parent
// and re-interpolation is a no-op.
//
// Task must be set (via .Task) before calling Execute. Goal and Convergence
// fan-out set it automatically on their per-invocation clones; standalone
// callers set it explicitly:
//
//	critic := workflow.Agent("critic", "You are a rigorous critic.")
//	err := critic.Task("review this draft").Execute(ctx, rt, state)
func (a *agent) Execute(ctx context.Context, rt *Runtime, state *State) (err error) {
	// Structural validation: name, prompt, output declarations.
	if err = a.Validate(); err != nil {
		return err
	}
	// Per-invocation precondition: task must have been set via .Task().
	if strings.TrimSpace(a.task) == "" {
		return fmt.Errorf("agent %s: task is required (call .Task() before .Execute())", a.name)
	}

	ctx, end := trace(ctx, "agent.execute", attribute.String("agent", a.name))
	defer end(&err)

	// Apply this agent's overrides on top of the parent's runtime.
	rt = rt.merge(a.override)

	// Combine the agent's persona prompt with rt.SystemContext so any
	// workflow/sequence/goal-level system context flows through into the
	// agent's invocation.
	system := state.interpolate(a.prompt)
	if rt.SystemContext != "" {
		system = system + "\n\n" + rt.SystemContext
	}

	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: state.interpolate(a.task)},
	}
	toolDefs := allToolDefs(rt)

	for {
		resp, chatErr := rt.Model.Chat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		})
		if chatErr != nil {
			err = fmt.Errorf("agent %s: %w", a.name, chatErr)
			return err
		}
		if len(resp.ToolCalls) == 0 {
			state.Outputs[a.name] = resp.Content
			return nil
		}
		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		results, toolErr := runTools(ctx, rt, resp.ToolCalls)
		if toolErr != nil {
			err = fmt.Errorf("agent %s: %w", a.name, toolErr)
			return err
		}
		messages = append(messages, results...)
	}
}
