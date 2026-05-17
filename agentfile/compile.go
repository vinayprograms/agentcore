package agentfile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vinayprograms/agentcore/workflow"
	"github.com/vinayprograms/agentcore/workflow/security"
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/tools"
)

// Compile lowers a parsed Spec to an executable workflow.Workflow plus a
// pre-populated workflow.Runtime base.
//
// defaultModel is used for the workflow's main model (passed through to
// security.Build when the Spec declares SECURITY) and for any AGENT that
// does not pin a REQUIRES profile. profiles maps every Spec.Profiles() name
// to a concrete llm.Model; the caller assembles the map by iterating
// spec.Profiles() and looking each name up in its own config.
//
// tools is required only when at least one skill AGENT declares allowed-tools
// in its SKILL.md frontmatter; pass nil otherwise. When required and nil (or
// missing one of the named tools), Compile returns an error.
//
// The returned *workflow.Runtime is always non-nil. Spec-derived fields
// (SECURITY → Guard, REQUIRES → per-agent Override.Model) are pre-populated
// when present; everything else is the zero value for the caller to fill in.
func Compile(spec *Spec, defaultModel llm.Model, profiles map[string]llm.Model, registry *tools.Registry) (*workflow.Workflow, *workflow.Runtime, error) {
	if err := validate(spec, profiles, registry); err != nil {
		return nil, nil, err
	}

	wf := workflow.New(spec.Name)
	if spec.Supervised {
		if spec.HumanOnly {
			wf.SuperviseByHuman()
		} else {
			wf.Supervise()
		}
	}
	if spec.SecurityMode != "" {
		mode, err := securityMode(spec.SecurityMode)
		if err != nil {
			return nil, nil, err
		}
		wf.Security(mode)
		if spec.SecurityScope != "" {
			wf.Scope(spec.SecurityScope)
		}
	}

	for _, in := range spec.Inputs {
		p := workflow.Parameter{Name: in.Name}
		if in.Default != nil {
			p.Default = *in.Default
		}
		wf.Input(p)
	}

	agents, err := buildAgents(spec, defaultModel, profiles, registry)
	if err != nil {
		return nil, nil, err
	}

	goals, err := buildGoals(spec, agents)
	if err != nil {
		return nil, nil, err
	}

	for _, step := range spec.Steps {
		seq := workflow.Sequence(step.Name)
		if step.Supervision == SupervisionEnabled {
			if step.HumanOnly {
				seq.SuperviseByHuman()
			} else {
				seq.Supervise()
			}
		}
		var children []workflow.Step
		for _, g := range step.UsingGoals {
			children = append(children, goals[g])
		}
		seq.Steps(children...)
		wf.Add(seq)
	}

	rt := runtimeFromSpec(spec, defaultModel)
	return wf, rt, nil
}

// validate performs semantic checks Compile owns. Syntactic validation is
// handled by the parser; cross-cutting workflow validation (variable resolution,
// output collisions, supervision wiring) is handled later by the workflow
// package's own Validate at Execute time.
func validate(spec *Spec, profiles map[string]llm.Model, registry *tools.Registry) error {
	var errs []error

	agentNames := make(map[string]bool, len(spec.Agents))
	for i := range spec.Agents {
		a := &spec.Agents[i]
		if a.FromPath != "" && a.Prompt == "" {
			errs = append(errs, fmt.Errorf("agent %q: FROM %q unresolved — use ParseFile to load external sources", a.Name, a.FromPath))
		}
		if agentNames[a.Name] {
			errs = append(errs, fmt.Errorf("agent %q: duplicate definition", a.Name))
		}
		agentNames[a.Name] = true
		if a.Requires != "" {
			if _, ok := profiles[a.Requires]; !ok {
				errs = append(errs, fmt.Errorf("agent %q: REQUIRES profile %q not in profiles map", a.Name, a.Requires))
			}
		}
		if len(a.AllowedTools) > 0 {
			if registry == nil {
				errs = append(errs, fmt.Errorf("agent %q: SKILL.md allowed-tools requires non-nil tools registry", a.Name))
			} else {
				for _, t := range a.AllowedTools {
					if !registry.Has(t) {
						errs = append(errs, fmt.Errorf("agent %q: SKILL.md allowed-tool %q not in registry", a.Name, t))
					}
				}
			}
		}
	}

	goalNames := make(map[string]bool, len(spec.Goals))
	for i := range spec.Goals {
		g := &spec.Goals[i]
		if goalNames[g.Name] {
			errs = append(errs, fmt.Errorf("goal %q: duplicate definition", g.Name))
		}
		goalNames[g.Name] = true
		if g.Outcome != "" && g.FromPath != "" && spec.BaseDir == "" {
			// Both set with no BaseDir means caller hand-built the Spec
			// inconsistently; with BaseDir set, loader populated Outcome
			// from FROM and that's expected.
			errs = append(errs, fmt.Errorf("goal %q: Outcome and FROM are mutually exclusive", g.Name))
		}
		if g.IsConverge {
			if g.WithinLimit != nil && g.WithinVar != "" {
				errs = append(errs, fmt.Errorf("goal %q: WITHIN literal and WITHIN $var are mutually exclusive", g.Name))
			}
			if g.WithinLimit == nil && g.WithinVar == "" {
				errs = append(errs, fmt.Errorf("goal %q: CONVERGE requires WITHIN", g.Name))
			}
			if g.WithinLimit != nil && *g.WithinLimit <= 0 {
				errs = append(errs, fmt.Errorf("goal %q: WITHIN must be > 0", g.Name))
			}
		}
		for _, name := range g.UsingAgent {
			if !agentNames[name] {
				errs = append(errs, fmt.Errorf("goal %q: USING %q is not a declared AGENT", g.Name, name))
			}
		}
	}

	stepNames := make(map[string]bool, len(spec.Steps))
	for i := range spec.Steps {
		s := &spec.Steps[i]
		if stepNames[s.Name] {
			errs = append(errs, fmt.Errorf("step %q: duplicate definition", s.Name))
		}
		stepNames[s.Name] = true
		for _, name := range s.UsingGoals {
			if !goalNames[name] {
				errs = append(errs, fmt.Errorf("step %q: USING %q is not a declared GOAL", s.Name, name))
			}
		}
	}

	if spec.SecurityMode == "research" && strings.TrimSpace(spec.SecurityScope) == "" {
		errs = append(errs, fmt.Errorf("SECURITY research requires a scope string"))
	}

	if spec.Supervised && spec.HumanOnly {
		for _, g := range spec.Goals {
			if g.Supervision == SupervisionDisabled {
				errs = append(errs, fmt.Errorf("goal %q: UNSUPERVISED cannot exist inside SUPERVISED HUMAN scope", g.Name))
			}
		}
		for _, a := range spec.Agents {
			if a.Supervision == SupervisionDisabled {
				errs = append(errs, fmt.Errorf("agent %q: UNSUPERVISED cannot exist inside SUPERVISED HUMAN scope", a.Name))
			}
		}
		for _, s := range spec.Steps {
			if s.Supervision == SupervisionDisabled {
				errs = append(errs, fmt.Errorf("step %q: UNSUPERVISED cannot exist inside SUPERVISED HUMAN scope", s.Name))
			}
		}
	}

	return errors.Join(errs...)
}

// buildAgents constructs one *workflow.Agent per Spec.Agent. Each agent
// receives its REQUIRES model and (for skill agents) the skill-dir
// SystemContext plus a tool subset for allowed-tools.
func buildAgents(spec *Spec, defaultModel llm.Model, profiles map[string]llm.Model, registry *tools.Registry) (map[string]workflow.Step, error) {
	out := make(map[string]workflow.Step, len(spec.Agents))
	for i := range spec.Agents {
		a := &spec.Agents[i]
		base := workflow.Agent(a.Name, a.Prompt)
		ov := workflow.Override{}
		if a.Requires != "" {
			ov.Model = profiles[a.Requires]
		} else if defaultModel != nil {
			ov.Model = defaultModel
		}
		if a.IsSkill {
			ov.SystemContext = skillContextBlurb(a)
			if len(a.AllowedTools) > 0 {
				sub, err := registry.Subset(a.AllowedTools)
				if err != nil {
					return nil, fmt.Errorf("agent %q: %w", a.Name, err)
				}
				ov.Tools = sub
			}
		}
		base.Customize(ov)
		if len(a.Outputs) > 0 {
			base.WithOutputs(a.Outputs...)
		}
		out[a.Name] = base
	}
	return out, nil
}

// buildGoals constructs one Goal/Convergence per Spec.Goal, wiring USING
// agents from the agents map. Each goal's children are clones of the
// canonical agent built once in buildAgents — workflow's clone-on-Using
// guarantees independence across goals.
func buildGoals(spec *Spec, agents map[string]workflow.Step) (map[string]workflow.Step, error) {
	out := make(map[string]workflow.Step, len(spec.Goals))
	for i := range spec.Goals {
		g := &spec.Goals[i]

		description := g.Outcome
		var node workflow.Step
		if g.IsConverge {
			within := 0
			if g.WithinLimit != nil {
				within = *g.WithinLimit
			}
			c := workflow.Convergence(g.Name, description, within)
			if g.WithinVar != "" {
				c.WithinVar(g.WithinVar)
			}
			if len(g.Outputs) > 0 {
				c.WithOutputs(g.Outputs...)
			}
			if g.Supervision == SupervisionEnabled {
				if g.HumanOnly {
					c.SuperviseByHuman()
				} else {
					c.Supervise()
				}
			}
			for _, name := range g.UsingAgent {
				c.Using(agents[name])
			}
			node = c
		} else {
			gw := workflow.Goal(g.Name, description)
			if len(g.Outputs) > 0 {
				gw.WithOutputs(g.Outputs...)
			}
			if g.Supervision == SupervisionEnabled {
				if g.HumanOnly {
					gw.SuperviseByHuman()
				} else {
					gw.Supervise()
				}
			}
			for _, name := range g.UsingAgent {
				gw.Using(agents[name])
			}
			node = gw
		}
		out[g.Name] = node
	}
	return out, nil
}

func skillContextBlurb(a *Agent) string {
	var b strings.Builder
	b.WriteString("Skill bundle resources are at: ")
	b.WriteString(a.SkillDir)
	b.WriteString("\nSubdirectories scripts/ and references/ (if present) contain materials you may read on demand using file-read tools.")
	return b.String()
}

func securityMode(name string) (security.Mode, error) {
	switch name {
	case "default":
		return security.Default, nil
	case "paranoid":
		return security.Paranoid, nil
	case "research":
		return security.Research, nil
	default:
		return 0, fmt.Errorf("unknown SECURITY mode: %q", name)
	}
}

// runtimeFromSpec produces the Compile-populated Runtime base. Always
// non-nil; populated fields depend on what the Spec declares (Guard from
// SECURITY, etc.). Callers layer their own fields on the returned value.
func runtimeFromSpec(spec *Spec, defaultModel llm.Model) *workflow.Runtime {
	rt := &workflow.Runtime{}
	if spec.SecurityMode != "" {
		mode, err := securityMode(spec.SecurityMode)
		if err == nil && defaultModel != nil {
			if g, err := security.Build(mode, spec.SecurityScope, defaultModel); err == nil {
				rt.Guard = g
			}
		}
	}
	return rt
}
