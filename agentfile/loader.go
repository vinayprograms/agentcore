package agentfile

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config controls how Parse resolves external references.
type Config struct {
	// Skills are roots searched, in order, when an AGENT's FROM is a bare
	// identifier (no slash, no .md extension) that does not resolve inside
	// baseRoot. Each entry is an *os.Root opened by the caller.
	Skills []*os.Root
}

// Parse parses an Agentfile and resolves AGENT/GOAL FROM references against
// baseRoot. The caller reads the source bytes themselves (from a file,
// embed.FS, HTTP body, etc.); Parse only resolves filesystem references the
// source declares.
//
// baseRoot is the directory FROM paths are interpreted against. It must be
// non-nil when the source contains any FROM reference; otherwise pass nil.
// Path-traversal escapes via "../" are blocked by the OS at the *os.Root
// boundary, so a FROM cannot reach outside baseRoot.
//
// Skill directories have their SKILL.md frontmatter parsed and body loaded
// into the agent's prompt; supporting files (scripts/, etc.) stay on disk
// and are reached at runtime by the agent's file-read tools.
//
// Parse performs syntactic parsing and FROM resolution only. Semantic
// validation (undefined references, output-name collisions, supervision
// downgrade rules) runs inside Compile.
func Parse(text string, baseRoot *os.Root, cfg Config) (*Spec, error) {
	spec, err := newParser(newLexer(text)).parse()
	if err != nil {
		return nil, err
	}

	if baseRoot != nil {
		spec.BaseDir = baseRoot.Name()
	}

	for i := range spec.Agents {
		a := &spec.Agents[i]
		if a.FromPath == "" {
			continue
		}
		if baseRoot == nil {
			return nil, fmt.Errorf("line %d: agent %q has FROM %q but baseRoot is nil", a.Line, a.Name, a.FromPath)
		}
		if err := resolveAgentFrom(a, baseRoot, cfg.Skills); err != nil {
			return nil, fmt.Errorf("line %d: %w", a.Line, err)
		}
	}

	for i := range spec.Goals {
		g := &spec.Goals[i]
		if g.FromPath == "" {
			continue
		}
		if baseRoot == nil {
			return nil, fmt.Errorf("line %d: goal %q has FROM %q but baseRoot is nil", g.Line, g.Name, g.FromPath)
		}
		body, err := baseRoot.ReadFile(g.FromPath)
		if err != nil {
			return nil, fmt.Errorf("line %d: load goal outcome %q: %w", g.Line, g.FromPath, err)
		}
		g.Outcome = string(body)
	}

	return spec, nil
}

// resolveAgentFrom resolves an AGENT FROM reference. Per REFERENCE.md:
//  1. Path with .md extension inside baseRoot → inline prompt file.
//  2. Directory inside baseRoot with SKILL.md → skill bundle.
//  3. Bare identifier (no slash, no .md) → search cfg.Skills in order.
func resolveAgentFrom(a *Agent, baseRoot *os.Root, skillRoots []*os.Root) error {
	target := a.FromPath

	if strings.HasSuffix(target, ".agent") {
		return fmt.Errorf("agent FROM may not reference a .agent file: %s", target)
	}

	if info, err := baseRoot.Stat(target); err == nil {
		if !info.IsDir() {
			if !strings.HasSuffix(target, ".md") {
				return fmt.Errorf("agent prompt file must be .md: %s", target)
			}
			body, err := baseRoot.ReadFile(target)
			if err != nil {
				return fmt.Errorf("load agent prompt %q: %w", target, err)
			}
			a.Prompt = string(body)
			a.IsSkill = false
			return nil
		}
		sub, err := baseRoot.OpenRoot(target)
		if err != nil {
			return fmt.Errorf("open skill dir %q: %w", target, err)
		}
		defer sub.Close()
		return loadSkill(a, sub)
	}

	// Not found in baseRoot. Search skill roots only for bare identifiers.
	if strings.ContainsAny(target, "/.") {
		return fmt.Errorf("agent FROM not found: %s", target)
	}
	for _, root := range skillRoots {
		info, err := root.Stat(target)
		if err != nil || !info.IsDir() {
			continue
		}
		sub, err := root.OpenRoot(target)
		if err != nil {
			return fmt.Errorf("open skill dir %q: %w", target, err)
		}
		defer sub.Close()
		return loadSkill(a, sub)
	}
	return fmt.Errorf("agent FROM not found: %s (checked baseRoot and skill roots)", target)
}

// loadSkill reads SKILL.md from skillRoot, parses frontmatter, and populates
// the agent. skillRoot is the *os.Root for the skill directory itself (not
// its parent).
func loadSkill(a *Agent, skillRoot *os.Root) error {
	raw, err := skillRoot.ReadFile("SKILL.md")
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("directory %s is not a skill (missing SKILL.md)", skillRoot.Name())
		}
		return fmt.Errorf("read SKILL.md in %s: %w", skillRoot.Name(), err)
	}
	fm, body, err := parseSkillFile(raw)
	if err != nil {
		return fmt.Errorf("skill %s: %w", skillRoot.Name(), err)
	}
	a.IsSkill = true
	a.SkillDir = skillRoot.Name()
	a.AllowedTools = fm.AllowedTools
	a.Prompt = strings.TrimSpace(fm.Description) + "\n\n" + body
	return nil
}

// skillFrontmatter is the typed view of SKILL.md frontmatter. Per the
// Anthropic Agent Skills standard, only `name` and `description` are
// required; everything else is optional. License and other fields are
// accepted but not surfaced.
type skillFrontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
}

// parseSkillFile splits a SKILL.md file into its YAML frontmatter and body.
// The body is returned with leading whitespace trimmed; the frontmatter is
// decoded into a typed struct.
func parseSkillFile(raw []byte) (skillFrontmatter, string, error) {
	var fm skillFrontmatter
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		rest, ok = bytes.CutPrefix(raw, []byte("---\r\n"))
	}
	if !ok {
		return fm, "", fmt.Errorf("SKILL.md must begin with --- frontmatter delimiter")
	}
	yamlBlock, body, found := bytes.Cut(rest, []byte("\n---"))
	if !found {
		return fm, "", fmt.Errorf("SKILL.md frontmatter is not terminated by ---")
	}
	if err := yaml.Unmarshal(yamlBlock, &fm); err != nil {
		return fm, "", fmt.Errorf("invalid frontmatter: %w", err)
	}
	if fm.Name == "" {
		return fm, "", fmt.Errorf("frontmatter missing required field: name")
	}
	if fm.Description == "" {
		return fm, "", fmt.Errorf("frontmatter missing required field: description")
	}
	return fm, strings.TrimLeft(string(body), "\r\n"), nil
}
