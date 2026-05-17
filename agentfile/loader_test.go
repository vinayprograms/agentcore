package agentfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readAndParse reads an Agentfile from disk and parses it against a Root
// opened on the file's directory.
func readAndParse(t *testing.T, path string, cfg Config) (*Spec, error) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return Parse(string(content), root, cfg)
}

func writeAgentfile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "Agentfile")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write Agentfile: %v", err)
	}
	return path
}

func TestParse_PromptMarkdownFile(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "agents")
	os.MkdirAll(agents, 0755)
	os.WriteFile(filepath.Join(agents, "critic.md"), []byte("You are a critic."), 0644)

	path := writeAgentfile(t, dir, `NAME test
AGENT critic FROM agents/critic.md
GOAL main "Test"
RUN main USING main
`)

	spec, err := readAndParse(t, path, Config{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if spec.Agents[0].Prompt != "You are a critic." {
		t.Errorf("prompt=%q", spec.Agents[0].Prompt)
	}
	if spec.Agents[0].IsSkill {
		t.Error("IsSkill should be false for .md file")
	}
	if spec.BaseDir == "" || !filepath.IsAbs(spec.BaseDir) {
		t.Errorf("BaseDir should be absolute, got %q", spec.BaseDir)
	}
}

func TestParse_SkillDirectoryRelativeToAgentfile(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "code-review")
	os.MkdirAll(skill, 0755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(`---
name: code-review
description: Review code for bugs.
allowed-tools:
  - read
  - bash
---

# Instructions

Check thoroughness, correctness, style.
`), 0644)

	path := writeAgentfile(t, dir, `NAME test
AGENT reviewer FROM skills/code-review
GOAL main "Test"
RUN main USING main
`)

	spec, err := readAndParse(t, path, Config{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := spec.Agents[0]
	if !a.IsSkill {
		t.Error("IsSkill should be true")
	}
	if !filepath.IsAbs(a.SkillDir) {
		t.Errorf("SkillDir should be absolute: %q", a.SkillDir)
	}
	if !strings.Contains(a.Prompt, "Review code for bugs.") {
		t.Errorf("prompt missing description: %q", a.Prompt)
	}
	if !strings.Contains(a.Prompt, "Check thoroughness") {
		t.Errorf("prompt missing body: %q", a.Prompt)
	}
	if len(a.AllowedTools) != 2 || a.AllowedTools[0] != "read" || a.AllowedTools[1] != "bash" {
		t.Errorf("AllowedTools=%v", a.AllowedTools)
	}
}

func TestParse_SkillSearchPath(t *testing.T) {
	dir := t.TempDir()
	skillsRoot := filepath.Join(dir, "skill-store")
	skill := filepath.Join(skillsRoot, "testing")
	os.MkdirAll(skill, 0755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(`---
name: testing
description: Write tests.
---

Body.
`), 0644)

	agentDir := filepath.Join(dir, "agent")
	os.MkdirAll(agentDir, 0755)
	path := writeAgentfile(t, agentDir, `NAME test
AGENT tester FROM testing
GOAL main "Test"
RUN main USING main
`)

	skillRoot, err := os.OpenRoot(skillsRoot)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer skillRoot.Close()

	spec, err := readAndParse(t, path, Config{Skills: []*os.Root{skillRoot}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !spec.Agents[0].IsSkill {
		t.Error("IsSkill should be true")
	}
}

func TestParse_DirectoryWithoutSkillMd(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "bogus")
	os.MkdirAll(bogus, 0755)

	path := writeAgentfile(t, dir, `NAME test
AGENT x FROM bogus
GOAL main "Test"
RUN main USING main
`)

	_, err := readAndParse(t, path, Config{})
	if err == nil || !strings.Contains(err.Error(), "missing SKILL.md") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_AgentNotFound(t *testing.T) {
	dir := t.TempDir()
	path := writeAgentfile(t, dir, `NAME test
AGENT missing FROM nowhere
GOAL main "Test"
RUN main USING main
`)
	_, err := readAndParse(t, path, Config{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_RejectsDotAgentFROM(t *testing.T) {
	dir := t.TempDir()
	path := writeAgentfile(t, dir, `NAME test
AGENT nested FROM other.agent
GOAL main "Test"
RUN main USING main
`)
	_, err := readAndParse(t, path, Config{})
	if err == nil || !strings.Contains(err.Error(), ".agent file") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_PromptFileMustBeMD(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "persona.txt"), []byte("x"), 0644)
	path := writeAgentfile(t, dir, `NAME test
AGENT x FROM persona.txt
GOAL main "Test"
RUN main USING main
`)
	_, err := readAndParse(t, path, Config{})
	if err == nil || !strings.Contains(err.Error(), "must be .md") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_PathWithSlashNotInSearchPath(t *testing.T) {
	dir := t.TempDir()
	path := writeAgentfile(t, dir, `NAME test
AGENT x FROM agents/missing.md
GOAL main "Test"
RUN main USING main
`)
	// Even with an unrelated skill root, a slashed path that doesn't exist
	// in baseRoot must error rather than search the skill roots.
	other := t.TempDir()
	otherRoot, _ := os.OpenRoot(other)
	defer otherRoot.Close()
	_, err := readAndParse(t, path, Config{Skills: []*os.Root{otherRoot}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_GoalFROM(t *testing.T) {
	dir := t.TempDir()
	prompts := filepath.Join(dir, "prompts")
	os.MkdirAll(prompts, 0755)
	os.WriteFile(filepath.Join(prompts, "outline.md"), []byte("Outline this $topic."), 0644)

	path := writeAgentfile(t, dir, `NAME test
INPUT topic
GOAL outline FROM prompts/outline.md
RUN main USING outline
`)
	spec, err := readAndParse(t, path, Config{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if spec.Goals[0].Outcome != "Outline this $topic." {
		t.Errorf("goal outcome=%q", spec.Goals[0].Outcome)
	}
}

func TestParse_GoalFROMMissing(t *testing.T) {
	dir := t.TempDir()
	path := writeAgentfile(t, dir, `NAME test
GOAL outline FROM prompts/missing.md
RUN main USING outline
`)
	_, err := readAndParse(t, path, Config{})
	if err == nil || !strings.Contains(err.Error(), "load goal outcome") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_GoalFROMRequiresBaseRoot(t *testing.T) {
	src := `NAME test
GOAL outline FROM prompts/outline.md
RUN main USING outline
`
	_, err := Parse(src, nil, Config{})
	if err == nil || !strings.Contains(err.Error(), "baseRoot is nil") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_TraversalBlockedByRoot(t *testing.T) {
	// The OS-level Root protection should block a FROM that tries to
	// escape via "../".
	dir := t.TempDir()
	sibling := t.TempDir()
	os.WriteFile(filepath.Join(sibling, "evil.md"), []byte("escaped"), 0644)

	path := writeAgentfile(t, dir, `NAME test
AGENT x FROM ../`+filepath.Base(sibling)+`/evil.md
GOAL main "Test"
RUN main USING main
`)
	_, err := readAndParse(t, path, Config{})
	if err == nil {
		t.Error("expected traversal to be blocked")
	}
}

func TestParseSkillFile_MissingOpenDelimiter(t *testing.T) {
	_, _, err := parseSkillFile([]byte("name: x\n"))
	if err == nil || !strings.Contains(err.Error(), "must begin with ---") {
		t.Errorf("got: %v", err)
	}
}

func TestParseSkillFile_UnterminatedFrontmatter(t *testing.T) {
	_, _, err := parseSkillFile([]byte("---\nname: x\ndescription: y\n"))
	if err == nil || !strings.Contains(err.Error(), "not terminated") {
		t.Errorf("got: %v", err)
	}
}

func TestParseSkillFile_InvalidYAML(t *testing.T) {
	_, _, err := parseSkillFile([]byte("---\nname: [unclosed\n---\nbody\n"))
	if err == nil || !strings.Contains(err.Error(), "invalid frontmatter") {
		t.Errorf("got: %v", err)
	}
}

func TestParseSkillFile_MissingName(t *testing.T) {
	_, _, err := parseSkillFile([]byte("---\ndescription: y\n---\nbody\n"))
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("got: %v", err)
	}
}

func TestParseSkillFile_MissingDescription(t *testing.T) {
	_, _, err := parseSkillFile([]byte("---\nname: x\n---\nbody\n"))
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("got: %v", err)
	}
}

func TestParseSkillFile_CRLFDelimiter(t *testing.T) {
	fm, body, err := parseSkillFile([]byte("---\r\nname: x\r\ndescription: y\r\n---\nThe body.\n"))
	if err != nil {
		t.Fatalf("parseSkillFile: %v", err)
	}
	if fm.Name != "x" || fm.Description != "y" {
		t.Errorf("fm=%+v", fm)
	}
	if !strings.HasPrefix(body, "The body.") {
		t.Errorf("body=%q", body)
	}
}
