package agentfile

// Spec is the AST root produced by Parse / ParseFile. It is a passive data
// shape — semantic validation runs in Compile, not at parse time.
type Spec struct {
	Name          string
	BaseDir       string // directory containing the Agentfile (set by ParseFile; empty for Parse)
	Supervised    bool   // global supervision (top-level SUPERVISED)
	HumanOnly     bool   // global human-only mode (top-level SUPERVISED HUMAN)
	SecurityMode  string // "default" | "paranoid" | "research" | "" (no SECURITY directive)
	SecurityScope string // scope description for research mode
	Inputs        []Input
	Agents        []Agent
	Goals         []Goal
	Steps         []Step
}


// Input represents an INPUT declaration.
type Input struct {
	Name    string
	Default *string // nil if no default
	Line    int
}


// Agent represents an AGENT declaration.
type Agent struct {
	Name         string
	FromPath     string          // path to prompt file or skill directory; empty for inline prompt
	Prompt       string          // resolved prompt content (description + body for skills; file content for .md; literal for inline)
	Requires     string          // capability profile name (REQUIRES "<profile>")
	Outputs      []string        // structured output field names (after ->)
	IsSkill      bool            // true if FromPath resolved to a skill directory
	SkillDir     string          // absolute path to skill directory (when IsSkill)
	AllowedTools []string        // SKILL.md frontmatter allowed-tools (empty when not a skill or unset)
	Supervision  SupervisionMode // inherit / supervised / unsupervised
	HumanOnly    bool            // SUPERVISED HUMAN modifier
	Line         int
}


// Goal represents a GOAL or CONVERGE declaration.
type Goal struct {
	Name        string
	Outcome     string          // inline description (mutually exclusive with FromPath)
	FromPath    string          // path to outcome file
	Outputs     []string        // structured output field names (after ->)
	UsingAgent  []string        // agent names for multi-agent fan-out
	IsConverge  bool            // true if CONVERGE
	WithinLimit *int            // numeric cap (nil if WithinVar set)
	WithinVar   string          // variable reference for cap (mutually exclusive with WithinLimit)
	Supervision SupervisionMode
	HumanOnly   bool
	Line        int
}


// Step represents a RUN step.
type Step struct {
	Type        StepType
	Name        string
	UsingGoals  []string
	Supervision SupervisionMode
	HumanOnly   bool
	Line        int
}


// IsSupervised reports whether this step is supervised, taking the workflow
// default into account.
func (s *Step) IsSupervised(spec *Spec) bool {
	return s.Supervision.Bool(spec.Supervised)
}

// RequiresHuman reports whether this step requires human supervision.
func (s *Step) RequiresHuman(spec *Spec) bool {
	if s.Supervision == SupervisionEnabled {
		return s.HumanOnly
	}
	if s.Supervision == SupervisionInherit && spec.Supervised {
		return spec.HumanOnly || s.HumanOnly
	}
	return false
}

// IsSupervised reports whether this goal is supervised.
func (g *Goal) IsSupervised(spec *Spec) bool {
	return g.Supervision.Bool(spec.Supervised)
}

// RequiresHuman reports whether this goal requires human supervision.
func (g *Goal) RequiresHuman(spec *Spec) bool {
	if g.Supervision == SupervisionEnabled {
		return g.HumanOnly
	}
	if g.Supervision == SupervisionInherit && spec.Supervised {
		return spec.HumanOnly || g.HumanOnly
	}
	return false
}

// IsSupervised reports whether this agent is supervised.
func (a *Agent) IsSupervised(spec *Spec) bool {
	return a.Supervision.Bool(spec.Supervised)
}

// RequiresHuman reports whether this agent requires human supervision.
func (a *Agent) RequiresHuman(spec *Spec) bool {
	if a.Supervision == SupervisionEnabled {
		return a.HumanOnly
	}
	if a.Supervision == SupervisionInherit && spec.Supervised {
		return spec.HumanOnly || a.HumanOnly
	}
	return false
}

// Profiles returns the unique set of REQUIRES profile names across all
// agents in this spec, preserving first-occurrence order. The caller uses
// this to know which profile names to resolve from its own config before
// invoking Compile.
func (s *Spec) Profiles() []string {
	var names []string
	seen := make(map[string]bool)
	for _, a := range s.Agents {
		if a.Requires == "" || seen[a.Requires] {
			continue
		}
		seen[a.Requires] = true
		names = append(names, a.Requires)
	}
	return names
}

// HasSupervision reports whether any supervised node exists at any level.
func (s *Spec) HasSupervision() bool {
	if s.Supervised {
		return true
	}
	for _, g := range s.Goals {
		if g.Supervision == SupervisionEnabled {
			return true
		}
	}
	for _, st := range s.Steps {
		if st.Supervision == SupervisionEnabled {
			return true
		}
	}
	for _, a := range s.Agents {
		if a.Supervision == SupervisionEnabled {
			return true
		}
	}
	return false
}

// HasHumanSupervision reports whether any node in the spec resolves to a
// human-required mode after propagation. Callers use this to decide whether
// to wire Runtime.HumanCh.
func (s *Spec) HasHumanSupervision() bool {
	if s.Supervised && s.HumanOnly {
		return true
	}
	for i := range s.Steps {
		if s.Steps[i].RequiresHuman(s) {
			return true
		}
	}
	for i := range s.Goals {
		if s.Goals[i].RequiresHuman(s) {
			return true
		}
	}
	for i := range s.Agents {
		if s.Agents[i].RequiresHuman(s) {
			return true
		}
	}
	return false
}
