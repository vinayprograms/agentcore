package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vinayprograms/agentkit/llm"
)

// goal is an agentic step: a full LLM loop that may issue tool calls until
// the model returns an empty tool-call list. When .Using is non-empty, the
// goal fans out to those Steps in parallel and synthesizes their outputs.
//
// Constructed via workflow.Goal — never directly.
type goal struct {
	name        string
	description string
	outputs     []string
	using       []Step

	override Override

	supervision supervision
}

// Goal constructs a goal with the given name and interpolated description.
// $var references in description resolve at execution time against State.
func Goal(name, description string) *goal {
	return &goal{name: name, description: description}
}

// Using attaches steps (typically agents) to this goal. They run in parallel
// and their outputs are synthesized. Each argument is deep-copied — passing
// the same agent variable to multiple goals is safe.
func (g *goal) Using(steps ...Step) *goal {
	for _, s := range steps {
		g.using = append(g.using, s.clone())
	}
	return g
}

// WithOutputs declares structured output field names for this goal.
func (g *goal) WithOutputs(fields ...string) *goal {
	g.outputs = append(g.outputs, fields...)
	return g
}

// Customize layers per-goal runtime overrides on top of whatever the parent
// workflow provided. Use it to scope a goal to a narrow tool registry, pin
// a specific model, swap MCP, tighten policy, or append goal-specific text
// to the system context. Fields left zero in o inherit from the parent.
// The override propagates to any agents/convergences this goal fans out to,
// unless they declare their own Customize on top.
func (g *goal) Customize(o Override) *goal {
	g.override = o
	return g
}

// Supervise marks this goal for LLM-driven supervision.
func (g *goal) Supervise() *goal {
	g.supervision = byLLM
	return g
}

// SuperviseByHuman marks this goal for supervision that requires human approval.
func (g *goal) SuperviseByHuman() *goal {
	g.supervision = byHuman
	return g
}

// Name implements Node.
func (g *goal) Name() string { return g.name }

// Kind implements Node.
func (g *goal) Kind() Kind { return kindOf(g) }

// Children implements Node.
func (g *goal) Children() []Node {
	nodes := make([]Node, 0, len(g.using))
	for _, s := range g.using {
		if n, ok := s.(Node); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// Validate checks the goal's structural integrity and recurses into its
// using-steps. Output-name collisions across the workflow are not checked
// here — that's a cross-tree concern handled by the package-level Validate.
func (g *goal) Validate() error {
	var errs []error
	if strings.TrimSpace(g.name) == "" {
		errs = append(errs, fmt.Errorf("goal: name is required"))
	}
	if strings.TrimSpace(g.description) == "" {
		errs = append(errs, fmt.Errorf("goal %s: description is required", g.name))
	}
	for _, out := range g.outputs {
		if strings.TrimSpace(out) == "" {
			errs = append(errs, fmt.Errorf("goal %s: output has empty name", g.name))
		}
	}
	for _, child := range g.using {
		if v, ok := child.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// clone returns an independent copy, including deep-copies of children.
func (g *goal) clone() Step {
	cp := &goal{
		name:        g.name,
		description: g.description,
		outputs:     slices.Clone(g.outputs),
		override:    g.override,
		supervision: g.supervision,
	}
	for _, s := range g.using {
		cp.using = append(cp.using, s.clone())
	}
	return cp
}

// Execute runs the goal: interpolates the description, runs the agentic loop
// (or fan-out + synthesis), parses structured outputs, and stores results.
func (g *goal) Execute(ctx context.Context, rt *Runtime, state *State) (err error) {
	if err = g.Validate(); err != nil {
		return err
	}

	ctx, end := trace(ctx, "goal.execute", attribute.String("goal", g.name))
	defer end(&err)

	// Apply this goal's overrides on top of the parent's runtime; the
	// merged runtime is what the goal's loop, fan-out, and synthesis use,
	// and is what flows into any child agents or nested goals/convergences.
	rt = rt.merge(g.override)

	description := state.interpolate(g.description)
	ctx = context.WithValue(ctx, ctxGoal, description)
	rt.fire(ctx, GoalStarted{Goal: g.name, Description: description})

	mode := effectiveSupervision(ctx, g.supervision)

	output, err := runWithSupervision(
		ctx, rt, g.name, "goal", description, mode,
		func(ctx context.Context, instruction string) (string, []string, error) {
			if len(g.using) > 0 {
				out, err := g.fanOut(ctx, rt, state, instruction)
				return out, nil, err
			}
			out, err := g.loop(ctx, rt, state, instruction)
			return out, nil, err
		},
	)
	if err != nil {
		return err
	}

	if len(g.outputs) > 0 {
		if parsed, parseErr := parseStructured(output, g.outputs); parseErr == nil {
			maps.Copy(state.Outputs, parsed)
		}
	}
	state.Outputs[g.name] = output

	rt.fire(ctx, GoalEnded{Goal: g.name, Output: output})
	return nil
}

// loop runs the single-agent agentic loop using Runtime.Model.
func (g *goal) loop(ctx context.Context, rt *Runtime, state *State, description string) (string, error) {
	messages := []llm.Message{
		{Role: "system", Content: buildSystemPrompt(rt)},
		{Role: "user", Content: buildPrompt(g.name, description, state, g.outputs)},
	}
	toolDefs := allToolDefs(rt)

	for {
		resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		})
		if err != nil {
			return "", fmt.Errorf("goal %s: %w", g.name, err)
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
			return "", fmt.Errorf("goal %s: %w", g.name, err)
		}
		messages = append(messages, results...)
	}
}

// fanOut runs all using-steps in parallel and synthesizes their outputs.
// Each child is cloned once more at execution time so the per-invocation
// task assignment doesn't mutate the agent stored in g.using; that keeps
// repeated invocations of the same goal isolated from each other.
func (g *goal) fanOut(ctx context.Context, rt *Runtime, state *State, description string) (string, error) {
	type agentOut struct {
		name   string
		output string
		err    error
	}
	ch := make(chan agentOut, len(g.using))

	var wg sync.WaitGroup
	for _, step := range g.using {
		wg.Add(1)
		go func(s Step) {
			defer wg.Done()
			invocation := s.clone()
			if a, ok := invocation.(*agent); ok {
				a.Task(description)
			}
			child := state.fork()
			name := stepName(invocation)
			rt.fire(ctx, SubagentSpawned{Goal: g.name, Agent: name})
			err := invocation.Execute(ctx, rt, child)
			out := child.Outputs[name]
			rt.fire(ctx, SubagentCompleted{
				Goal: g.name, Agent: name, Output: out, Failure: err,
			})
			ch <- agentOut{name: name, output: out, err: err}
		}(step)
	}
	wg.Wait()
	close(ch)

	var outputs []string
	for r := range ch {
		if r.err != nil {
			return "", fmt.Errorf("goal %s fan-out: %w", g.name, r.err)
		}
		outputs = append(outputs, fmt.Sprintf("[%s]: %s", r.name, r.output))
	}

	if len(outputs) == 1 {
		// Strip the "[name]: " prefix we just added — single-agent fan-out
		// shouldn't expose the synthesis label.
		_, after, _ := strings.Cut(outputs[0], "]: ")
		return after, nil
	}

	return g.synthesize(ctx, rt, outputs)
}

// synthesize asks the default LLM to merge multiple agent outputs.
func (g *goal) synthesize(ctx context.Context, rt *Runtime, outputs []string) (string, error) {
	prompt := "Synthesize these agent responses into a concise, coherent answer. Eliminate redundancy:\n\n" +
		strings.Join(outputs, "\n\n")

	resp, err := rt.Model.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "You are synthesizing multiple agent responses."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("goal %s synthesis: %w", g.name, err)
	}
	return resp.Content, nil
}

