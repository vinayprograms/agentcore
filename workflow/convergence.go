package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/vinayprograms/agentkit/llm"
)

const convergedMarker = "CONVERGED"

// convergence is an iterative refinement step. It runs up to within iterations,
// stopping early when the model emits the literal "CONVERGED" anywhere in its
// response. The output is the last substantive iteration (the one before the
// CONVERGED signal).
//
// Constructed via workflow.Convergence — never directly.
type convergence struct {
	name        string
	description string
	within      int
	outputs     []string
	using       []Step

	override Override

	supervision supervision
}

// Convergence constructs a convergence with the given name, description, and
// iteration cap. within must be > 0; this is checked at Validate time.
func Convergence(name, description string, within int) *convergence {
	return &convergence{name: name, description: description, within: within}
}

// Using attaches steps (typically agents) to this convergence.
func (c *convergence) Using(steps ...Step) *convergence {
	for _, s := range steps {
		c.using = append(c.using, s.clone())
	}
	return c
}

// WithOutputs declares structured output field names extracted from the
// final substantive iteration.
func (c *convergence) WithOutputs(fields ...string) *convergence {
	c.outputs = append(c.outputs, fields...)
	return c
}

// Customize layers per-convergence runtime overrides on top of whatever the
// parent workflow provided. See workflow.Override for the customizable
// fields. The override propagates to any agents this convergence fans out
// to, unless they declare their own Customize on top.
func (c *convergence) Customize(o Override) *convergence {
	c.override = o
	return c
}

// Supervise marks this convergence for LLM-driven supervision.
func (c *convergence) Supervise() *convergence {
	c.supervision = byLLM
	return c
}

// SuperviseByHuman marks this convergence for supervision that requires human
// approval.
func (c *convergence) SuperviseByHuman() *convergence {
	c.supervision = byHuman
	return c
}

// Name implements Node.
func (c *convergence) Name() string { return c.name }

// Kind implements Node.
func (c *convergence) Kind() Kind { return kindOf(c) }

// Children implements Node.
func (c *convergence) Children() []Node {
	nodes := make([]Node, 0, len(c.using))
	for _, s := range c.using {
		if n, ok := s.(Node); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// Validate checks the convergence's structural integrity and recurses into
// its using-steps. Output-name collisions across the workflow are not
// checked here — that's a cross-tree concern handled by the package-level
// Validate.
func (c *convergence) Validate() error {
	var errs []error
	if strings.TrimSpace(c.name) == "" {
		errs = append(errs, fmt.Errorf("convergence: name is required"))
	}
	if strings.TrimSpace(c.description) == "" {
		errs = append(errs, fmt.Errorf("convergence %s: description is required", c.name))
	}
	if c.within <= 0 {
		errs = append(errs, fmt.Errorf("convergence %s: 'within' must be > 0", c.name))
	}
	for _, out := range c.outputs {
		if strings.TrimSpace(out) == "" {
			errs = append(errs, fmt.Errorf("convergence %s: output has empty name", c.name))
		}
	}
	for _, child := range c.using {
		if v, ok := child.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// clone returns an independent copy, including deep-copies of children.
func (c *convergence) clone() Step {
	cp := &convergence{
		name:        c.name,
		description: c.description,
		within:      c.within,
		outputs:     slices.Clone(c.outputs),
		override:    c.override,
		supervision: c.supervision,
	}
	for _, s := range c.using {
		cp.using = append(cp.using, s.clone())
	}
	return cp
}

// Execute runs the convergence loop and writes the final output (and optional
// structured fields) into state.
func (c *convergence) Execute(ctx context.Context, rt *Runtime, state *State) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// Apply this convergence's overrides on top of the parent's runtime.
	rt = rt.merge(c.override)

	description := state.interpolate(c.description)
	ctx = context.WithValue(ctx, ctxGoal, description)
	rt.fire(ctx, GoalStarted{Name: c.name, Description: description})

	mode := effectiveSupervision(ctx, c.supervision)

	output, err := runWithSupervision(
		ctx, rt, c.name, "convergence", description, mode,
		func(ctx context.Context, _ string) (string, []string, error) {
			// The convergence's iteration loop uses c.description directly
			// (history-driven prompt construction). Reorient corrections
			// from supervision are not threaded through individual
			// iterations in v1 — the loop is a single supervised unit.
			out, err := c.run(ctx, rt, state, description)
			return out, nil, err
		},
	)
	if err != nil {
		return err
	}

	if len(c.outputs) > 0 {
		if parsed, parseErr := parseStructured(output, c.outputs); parseErr == nil {
			maps.Copy(state.Outputs, parsed)
		}
	}
	state.Outputs[c.name] = output

	rt.fire(ctx, GoalEnded{Name: c.name, Output: output})
	return nil
}

// run executes the convergence loop, returning the last substantive output.
func (c *convergence) run(ctx context.Context, rt *Runtime, state *State, description string) (string, error) {
	var history []string
	var lastSubstantive string

	for range c.within {
		var iterOutput string
		var err error

		if len(c.using) > 0 {
			iterOutput, err = c.iterateFanOut(ctx, rt, state, description, history)
		} else {
			iterOutput, err = c.iterateSingle(ctx, rt, state, description, history)
		}
		if err != nil {
			return "", err
		}

		if strings.Contains(iterOutput, convergedMarker) {
			return lastSubstantive, nil
		}

		lastSubstantive = iterOutput
		history = append(history, iterOutput)
		state.Outputs[c.name] = iterOutput
	}

	rt.fire(ctx, ConvergenceCapReached{
		Name:       c.name,
		Cap:        c.within,
		LastOutput: lastSubstantive,
	})
	state.Failures[c.name] = c.within
	return lastSubstantive, nil
}

// iterateSingle runs one iteration with the default model.
func (c *convergence) iterateSingle(ctx context.Context, rt *Runtime, state *State, description string, history []string) (string, error) {
	messages := []llm.Message{
		{Role: "system", Content: buildSystemPrompt(rt)},
		{Role: "user", Content: buildConvergePrompt(c.name, description, state, history, c.outputs)},
	}

	for {
		resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    allToolDefs(rt),
		})
		if err != nil {
			return "", fmt.Errorf("convergence %s: %w", c.name, err)
		}
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}
		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		results, err := runTools(ctx, rt, resp.ToolCalls)
		if err != nil {
			return "", fmt.Errorf("convergence %s: %w", c.name, err)
		}
		messages = append(messages, results...)
	}
}

// iterateFanOut runs one iteration across multiple agents. Each child is
// cloned once more at execution time so the per-invocation task assignment
// doesn't mutate the agent stored in c.using; that keeps each iteration's
// children isolated from the previous iteration's mutations.
func (c *convergence) iterateFanOut(ctx context.Context, rt *Runtime, state *State, description string, history []string) (string, error) {
	task := buildConvergePrompt(c.name, description, state, history, nil)

	var agentOutputs []string
	for _, step := range c.using {
		invocation := step.clone()
		if a, ok := invocation.(*agent); ok {
			a.Task(task)
		}
		child := state.fork()
		if err := invocation.Execute(ctx, rt, child); err != nil {
			return "", fmt.Errorf("convergence %s: %w", c.name, err)
		}
		name := stepName(invocation)
		agentOutputs = append(agentOutputs, fmt.Sprintf("[%s]: %s", name, child.Outputs[name]))
	}

	if len(agentOutputs) == 1 {
		// Strip the "[name]: " prefix we just added — single-agent fan-out
		// shouldn't expose the synthesis label.
		_, after, _ := strings.Cut(agentOutputs[0], "]: ")
		return after, nil
	}

	prompt := "Synthesize these agent responses for this convergence iteration:\n\n" +
		strings.Join(agentOutputs, "\n\n")
	resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "You are synthesizing multiple agent responses for a convergence step."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("convergence %s synthesis: %w", c.name, err)
	}
	return resp.Content, nil
}

