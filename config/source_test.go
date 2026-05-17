package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const fullTOML = `
name = "test-agent"
security_mode = "paranoid"

[model]
service = "anthropic"
model = "claude-opus-4-7"
max_tokens = 8192
max_retries = 3
max_backoff = "30s"
init_backoff = "1s"

[model.thinking]
level = "auto"
budget_tokens = 5000

[supervisor]
service = "anthropic"
model = "claude-haiku-4-5-20251001"
max_tokens = 2048

[profiles.reasoning-heavy]
service = "anthropic"
model = "claude-opus-4-7"
max_tokens = 8192
max_retries = 5

[profiles.fast]
service = "anthropic"
model = "claude-haiku-4-5-20251001"

[mcp.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]

[mcp.remote-tools]
endpoint = "https://tools.example.internal/mcp"

[skills]
paths = ["./skills", "~/.agent/skills"]
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// FromFile
// ---------------------------------------------------------------------------

func TestFromFile_Get_FullConfig(t *testing.T) {
	dir := t.TempDir()
	src := FromFile(writeFile(t, dir, "agent.toml", fullTOML))
	cfg, err := src.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "test-agent" {
		t.Errorf("Name=%q", cfg.Name)
	}
	if cfg.SecurityMode != "paranoid" {
		t.Errorf("SecurityMode=%q", cfg.SecurityMode)
	}
}

func TestFromFile_Get_DefaultModel(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	m := cfg.DefaultModel
	if m.Service != "anthropic" || m.Model != "claude-opus-4-7" {
		t.Errorf("DefaultModel: %q/%q", m.Service, m.Model)
	}
	if m.MaxTokens != 8192 {
		t.Errorf("MaxTokens=%d", m.MaxTokens)
	}
	if m.Retry.MaxRetries != 3 {
		t.Errorf("MaxRetries=%d", m.Retry.MaxRetries)
	}
	if m.Retry.MaxBackoff != 30*time.Second {
		t.Errorf("MaxBackoff=%v", m.Retry.MaxBackoff)
	}
	if m.Retry.InitBackoff != time.Second {
		t.Errorf("InitBackoff=%v", m.Retry.InitBackoff)
	}
	if m.Thinking.Level != llm.ThinkingLevel("auto") {
		t.Errorf("Thinking.Level=%q", m.Thinking.Level)
	}
	if m.Thinking.BudgetTokens != 5000 {
		t.Errorf("BudgetTokens=%d", m.Thinking.BudgetTokens)
	}
}

func TestFromFile_Get_SupervisorExplicit(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.SupervisorModel.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("SupervisorModel.Model=%q", cfg.SupervisorModel.Model)
	}
}

func TestFromFile_Get_SupervisorDefaultsToModel(t *testing.T) {
	const src = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
`
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", src)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.SupervisorModel.Model != cfg.DefaultModel.Model {
		t.Errorf("supervisor should default to model: got %q want %q",
			cfg.SupervisorModel.Model, cfg.DefaultModel.Model)
	}
}

func TestFromFile_Get_Profiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("Profiles count=%d", len(cfg.Profiles))
	}
	if rh := cfg.Profiles["reasoning-heavy"]; rh.Retry.MaxRetries != 5 {
		t.Errorf("reasoning-heavy MaxRetries=%d", rh.Retry.MaxRetries)
	}
}

func TestFromFile_Get_MCPServers(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stdio, ok := cfg.MCPServers.Stdio["filesystem"]
	if !ok {
		t.Fatal("filesystem stdio server missing")
	}
	if stdio.Command != "npx" {
		t.Errorf("command=%q", stdio.Command)
	}
	httpSrv, ok := cfg.MCPServers.HTTP["remote-tools"]
	if !ok {
		t.Fatal("remote-tools http server missing")
	}
	if httpSrv.Endpoint != "https://tools.example.internal/mcp" {
		t.Errorf("endpoint=%q", httpSrv.Endpoint)
	}
}

func TestFromFile_Get_NotFound(t *testing.T) {
	_, err := FromFile(filepath.Join(t.TempDir(), "absent.toml")).Get()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestFromFile_Get_InvalidDurationFails(t *testing.T) {
	const src = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
max_backoff = "not-a-duration"
`
	dir := t.TempDir()
	_, err := FromFile(writeFile(t, dir, "agent.toml", src)).Get()
	if err == nil || !strings.Contains(err.Error(), "max_backoff") {
		t.Errorf("expected max_backoff error, got: %v", err)
	}
}

func TestFromFile_Get_MCPBothTransportsError(t *testing.T) {
	const src = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
[mcp.bad]
command = "npx"
endpoint = "https://example.com"
`
	dir := t.TempDir()
	_, err := FromFile(writeFile(t, dir, "agent.toml", src)).Get()
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("expected transport-conflict error, got: %v", err)
	}
}

func TestFromFile_Get_MCPNeitherTransportError(t *testing.T) {
	const src = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
[mcp.empty]
`
	dir := t.TempDir()
	_, err := FromFile(writeFile(t, dir, "agent.toml", src)).Get()
	if err == nil || !strings.Contains(err.Error(), "set command") {
		t.Errorf("expected missing-transport error, got: %v", err)
	}
}

func TestFromFile_Get_EmptyMCPMapsNonNil(t *testing.T) {
	const src = `
name = "minimal"
[model]
service = "anthropic"
model = "claude-opus-4-7"
`
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", src)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.MCPServers.Stdio == nil {
		t.Error("MCPServers.Stdio should be non-nil")
	}
	if cfg.MCPServers.HTTP == nil {
		t.Error("MCPServers.HTTP should be non-nil")
	}
}

// ---------------------------------------------------------------------------
// NewUnion
// ---------------------------------------------------------------------------

const globalTOML = `
name = "global-agent"
security_mode = "default"

[model]
service = "anthropic"
model = "claude-haiku-4-5-20251001"
max_tokens = 2048

[supervisor]
service = "anthropic"
model = "claude-opus-4-7"

[mcp.shared-tools]
endpoint = "https://shared.example.internal/mcp"

[skills]
paths = ["~/.agent/skills"]
`

const projectTOML = `
name = "project-agent"

[model]
service = "anthropic"
model = "claude-opus-4-7"
max_tokens = 8192

[profiles.reasoning-heavy]
service = "anthropic"
model = "claude-opus-4-7"

[mcp.project-tools]
command = "npx"
args = ["-y", "@example/project-tools"]

[skills]
paths = ["./skills"]
`

func TestNewUnion_LayersMerge(t *testing.T) {
	dir := t.TempDir()
	globalPath := writeFile(t, dir, "global.toml", globalTOML)
	projectPath := writeFile(t, dir, "project.toml", projectTOML)

	cfg, err := NewUnion(FromFile(globalPath), FromFile(projectPath)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// project name overrides global
	if cfg.Name != "project-agent" {
		t.Errorf("Name=%q want project-agent", cfg.Name)
	}
	// project model overrides global
	if cfg.DefaultModel.Model != "claude-opus-4-7" {
		t.Errorf("DefaultModel.Model=%q want claude-opus-4-7", cfg.DefaultModel.Model)
	}
	// global security_mode preserved (project didn't set it)
	if cfg.SecurityMode != "default" {
		t.Errorf("SecurityMode=%q want default", cfg.SecurityMode)
	}
	// global explicitly set supervisor; project didn't — global's should win
	if cfg.SupervisorModel.Model != "claude-opus-4-7" {
		t.Errorf("SupervisorModel=%q want claude-opus-4-7 (from global)", cfg.SupervisorModel.Model)
	}
	// MCP is union of both
	if _, ok := cfg.MCPServers.HTTP["shared-tools"]; !ok {
		t.Error("shared-tools from global missing")
	}
	if _, ok := cfg.MCPServers.Stdio["project-tools"]; !ok {
		t.Error("project-tools from project missing")
	}
	// project skills precede global skills
	if len(cfg.SkillPaths) != 2 || cfg.SkillPaths[0] != "./skills" {
		t.Errorf("SkillPaths=%v", cfg.SkillPaths)
	}
}

func TestNewUnion_SkipsNotFound(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.toml")
	present := writeFile(t, dir, "agent.toml", fullTOML)

	cfg, err := NewUnion(FromFile(missing), FromFile(present)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "test-agent" {
		t.Errorf("Name=%q", cfg.Name)
	}
}

func TestNewUnion_AllNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := NewUnion(
		FromFile(filepath.Join(dir, "a.toml")),
		FromFile(filepath.Join(dir, "b.toml")),
	).Get()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestNewUnion_ProjectMCPCollisionOverridesGlobal(t *testing.T) {
	const overrideTOML = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
[mcp.shared-tools]
command = "local-override"
args = []
`
	dir := t.TempDir()
	cfg, err := NewUnion(
		FromFile(writeFile(t, dir, "global.toml", globalTOML)),
		FromFile(writeFile(t, dir, "project.toml", overrideTOML)),
	).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// project's stdio entry should replace global's HTTP entry for the same name
	if _, ok := cfg.MCPServers.Stdio["shared-tools"]; !ok {
		t.Error("overridden shared-tools should be stdio")
	}
}

func TestNewUnion_PropagatesParseError(t *testing.T) {
	const badTOML = `
[model
bad toml
`
	dir := t.TempDir()
	_, err := NewUnion(
		FromFile(writeFile(t, dir, "bad.toml", badTOML)),
	).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMerge_ScalarOverride(t *testing.T) {
	base := Config{Name: "base", SecurityMode: "default"}
	override := Config{Name: "override"}
	result := Merge(base, override)
	if result.Name != "override" {
		t.Errorf("Name=%q", result.Name)
	}
	if result.SecurityMode != "default" {
		t.Errorf("SecurityMode=%q (should be preserved from base)", result.SecurityMode)
	}
}

func TestMerge_ProfilesUnion(t *testing.T) {
	base := Config{Profiles: map[string]llm.Config{"fast": {Service: "anthropic"}}}
	override := Config{Profiles: map[string]llm.Config{"reasoning-heavy": {Service: "anthropic"}}}
	result := Merge(base, override)
	if len(result.Profiles) != 2 {
		t.Errorf("Profiles count=%d want 2", len(result.Profiles))
	}
}

func TestMerge_ProfileCollisionOverrideWins(t *testing.T) {
	base := Config{Profiles: map[string]llm.Config{"fast": {Service: "anthropic", Model: "slow"}}}
	override := Config{Profiles: map[string]llm.Config{"fast": {Service: "anthropic", Model: "faster"}}}
	result := Merge(base, override)
	if result.Profiles["fast"].Model != "faster" {
		t.Errorf("fast profile should use override, got %q", result.Profiles["fast"].Model)
	}
}

func TestMerge_MCPUnion(t *testing.T) {
	base := Config{MCPServers: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"global": {Endpoint: "https://global"}},
	}}
	override := Config{MCPServers: MCPServers{
		Stdio: map[string]mcp.ServerConfig{"local": {Command: "npx"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCPServers.HTTP["global"]; !ok {
		t.Error("global HTTP server missing after merge")
	}
	if _, ok := result.MCPServers.Stdio["local"]; !ok {
		t.Error("local stdio server missing after merge")
	}
}

func TestMerge_SkillPathsOverrideFirst(t *testing.T) {
	base := Config{SkillPaths: []string{"~/.agent/skills"}}
	override := Config{SkillPaths: []string{"./skills"}}
	result := Merge(base, override)
	if result.SkillPaths[0] != "./skills" {
		t.Errorf("override skills should be first, got %v", result.SkillPaths)
	}
}

func TestMerge_SkillPathsDeduplicated(t *testing.T) {
	base := Config{SkillPaths: []string{"./shared", "~/.agent/skills"}}
	override := Config{SkillPaths: []string{"./skills", "./shared"}}
	result := Merge(base, override)
	count := 0
	for _, p := range result.SkillPaths {
		if p == "./shared" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("./shared appears %d times, want 1", count)
	}
}

func TestMerge_SupervisorDefaultAfterMerge(t *testing.T) {
	// Neither layer sets supervisor. After merge, supervisor = merged DefaultModel.
	base := Config{DefaultModel: llm.Config{Service: "anthropic", Model: "haiku"}}
	override := Config{DefaultModel: llm.Config{Service: "anthropic", Model: "opus"}}
	result := Merge(base, override)
	if result.SupervisorModel.Model != "opus" {
		t.Errorf("supervisor should default to merged model opus, got %q", result.SupervisorModel.Model)
	}
}

func TestMerge_BaseMapsPreservedWithNoOverride(t *testing.T) {
	// Merge with an empty override should not nil out base maps.
	base := Config{
		Profiles:   map[string]llm.Config{"fast": {Service: "anthropic"}},
		MCPServers: MCPServers{Stdio: map[string]mcp.ServerConfig{"x": {Command: "y"}}},
	}
	result := Merge(base, Config{})
	if _, ok := result.Profiles["fast"]; !ok {
		t.Error("fast profile lost")
	}
	if _, ok := result.MCPServers.Stdio["x"]; !ok {
		t.Error("stdio server x lost")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage: specific branches
// ---------------------------------------------------------------------------

// thirdPartySource is a non-rawProvider Source used to exercise the
// Config-level merge path inside unionSource.
type thirdPartySource struct{ cfg Config }

func (s thirdPartySource) Get() (Config, error) { return s.cfg, nil }

func TestNewUnion_ThirdPartySourceMerged(t *testing.T) {
	dir := t.TempDir()
	file := FromFile(writeFile(t, dir, "agent.toml", `
name = "from-file"
[model]
service = "anthropic"
model = "claude-opus-4-7"
`))
	custom := thirdPartySource{cfg: Config{SecurityMode: "paranoid"}}

	cfg, err := NewUnion(file, custom).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "from-file" {
		t.Errorf("Name=%q", cfg.Name)
	}
	if cfg.SecurityMode != "paranoid" {
		t.Errorf("SecurityMode=%q want paranoid", cfg.SecurityMode)
	}
}

func TestNewUnion_ThirdPartySourceNotFound(t *testing.T) {
	custom := &notFoundSource{}
	dir := t.TempDir()
	file := FromFile(writeFile(t, dir, "agent.toml", `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
`))
	cfg, err := NewUnion(custom, file).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "x" {
		t.Errorf("Name=%q", cfg.Name)
	}
}

type notFoundSource struct{}

func (s *notFoundSource) Get() (Config, error) { return Config{}, ErrNotFound }

func TestNewUnion_ThirdPartySourceError(t *testing.T) {
	errSrc := &errorSource{}
	_, err := NewUnion(errSrc).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected propagated error, got: %v", err)
	}
}

type errorSource struct{}

func (s *errorSource) Get() (Config, error) {
	return Config{}, errors.New("source failed")
}

func TestMerge_SecurityModeOverride(t *testing.T) {
	base := Config{SecurityMode: "default"}
	override := Config{SecurityMode: "paranoid"}
	if Merge(base, override).SecurityMode != "paranoid" {
		t.Error("SecurityMode override did not apply")
	}
}

func TestMerge_SupervisorModelOverride(t *testing.T) {
	base := Config{
		DefaultModel:    llm.Config{Service: "anthropic", Model: "haiku"},
		SupervisorModel: llm.Config{Service: "anthropic", Model: "opus"},
	}
	override := Config{
		DefaultModel:    llm.Config{Service: "anthropic", Model: "sonnet"},
		SupervisorModel: llm.Config{Service: "anthropic", Model: "dedicated"},
	}
	result := Merge(base, override)
	if result.SupervisorModel.Model != "dedicated" {
		t.Errorf("SupervisorModel=%q want dedicated", result.SupervisorModel.Model)
	}
}

func TestMerge_NilBaseProfiles(t *testing.T) {
	base := Config{}
	override := Config{Profiles: map[string]llm.Config{"fast": {Service: "anthropic"}}}
	result := Merge(base, override)
	if _, ok := result.Profiles["fast"]; !ok {
		t.Error("fast profile missing after merge into nil-profile base")
	}
}

func TestMerge_NilBaseStdioMCP(t *testing.T) {
	base := Config{}
	override := Config{MCPServers: MCPServers{
		Stdio: map[string]mcp.ServerConfig{"x": {Command: "cmd"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCPServers.Stdio["x"]; !ok {
		t.Error("stdio server x missing after merge into nil stdio base")
	}
}

func TestMerge_NilBaseHTTPMCP(t *testing.T) {
	base := Config{}
	override := Config{MCPServers: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"x": {Endpoint: "https://example.com"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCPServers.HTTP["x"]; !ok {
		t.Error("http server x missing after merge into nil http base")
	}
}

func TestMerge_HTTPMapClonedWhenNonNil(t *testing.T) {
	base := Config{MCPServers: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"existing": {Endpoint: "https://base"}},
	}}
	override := Config{MCPServers: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"new": {Endpoint: "https://new"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCPServers.HTTP["existing"]; !ok {
		t.Error("existing http server lost after merge")
	}
	if _, ok := result.MCPServers.HTTP["new"]; !ok {
		t.Error("new http server missing after merge")
	}
}

func TestMerge_EmptyOverrideSkillPaths(t *testing.T) {
	base := Config{SkillPaths: []string{"./base"}}
	result := Merge(base, Config{})
	if len(result.SkillPaths) != 1 || result.SkillPaths[0] != "./base" {
		t.Errorf("SkillPaths=%v", result.SkillPaths)
	}
}

func TestMergeRaw_SecurityModeAndSupervisorOverride(t *testing.T) {
	base := rawConfig{SecurityMode: "default"}
	override := rawConfig{
		SecurityMode: "paranoid",
		Supervisor:   rawModel{Service: "anthropic", Model: "dedicated"},
	}
	merged := mergeRaw(base, override)
	if merged.SecurityMode != "paranoid" {
		t.Errorf("SecurityMode=%q", merged.SecurityMode)
	}
	if merged.Supervisor.Model != "dedicated" {
		t.Errorf("Supervisor.Model=%q", merged.Supervisor.Model)
	}
}

func TestMergeRaw_NilBaseMCP(t *testing.T) {
	base := rawConfig{}
	override := rawConfig{MCP: map[string]rawMCPEntry{"x": {Command: "cmd"}}}
	merged := mergeRaw(base, override)
	if _, ok := merged.MCP["x"]; !ok {
		t.Error("x entry missing after merge into nil MCP base")
	}
}

func TestToConfig_SupervisorInvalidDuration(t *testing.T) {
	r := rawConfig{
		Model:      rawModel{Service: "anthropic", Model: "opus"},
		Supervisor: rawModel{Service: "anthropic", Model: "haiku", InitBackoff: "bad"},
	}
	_, err := r.toConfig()
	if err == nil || !strings.Contains(err.Error(), "[supervisor]") {
		t.Errorf("expected supervisor error, got: %v", err)
	}
}

func TestToConfig_ProfileInvalidDuration(t *testing.T) {
	r := rawConfig{
		Model: rawModel{Service: "anthropic", Model: "opus"},
		Profiles: map[string]rawModel{
			"bad": {Service: "anthropic", Model: "opus", MaxBackoff: "bad"},
		},
	}
	_, err := r.toConfig()
	if err == nil || !strings.Contains(err.Error(), "[profiles.bad]") {
		t.Errorf("expected profile error, got: %v", err)
	}
}

func TestToLLMConfig_InitBackoffError(t *testing.T) {
	r := rawModel{Service: "anthropic", Model: "opus", InitBackoff: "bad"}
	_, err := r.toLLMConfig()
	if err == nil || !strings.Contains(err.Error(), "init_backoff") {
		t.Errorf("expected init_backoff error, got: %v", err)
	}
}

func TestMerge_NonNilBaseProfilesCloned(t *testing.T) {
	base := Config{Profiles: map[string]llm.Config{"existing": {Service: "anthropic"}}}
	override := Config{Profiles: map[string]llm.Config{"new": {Service: "anthropic"}}}
	result := Merge(base, override)
	// Both should be present; base must not be mutated.
	if _, ok := result.Profiles["existing"]; !ok {
		t.Error("existing profile lost")
	}
	if _, ok := result.Profiles["new"]; !ok {
		t.Error("new profile missing")
	}
	if _, ok := base.Profiles["new"]; ok {
		t.Error("base was mutated by Merge")
	}
}

func TestMerge_NonNilBaseStdioMCPCloned(t *testing.T) {
	base := Config{MCPServers: MCPServers{
		Stdio: map[string]mcp.ServerConfig{"existing": {Command: "npx"}},
	}}
	override := Config{MCPServers: MCPServers{
		Stdio: map[string]mcp.ServerConfig{"new": {Command: "new"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCPServers.Stdio["existing"]; !ok {
		t.Error("existing stdio server lost")
	}
	if _, ok := base.MCPServers.Stdio["new"]; ok {
		t.Error("base was mutated by Merge")
	}
}

func TestMergeRaw_NonNilBaseProfilesCloned(t *testing.T) {
	base := rawConfig{
		Profiles: map[string]rawModel{"existing": {Service: "anthropic"}},
	}
	override := rawConfig{
		Profiles: map[string]rawModel{"new": {Service: "anthropic"}},
	}
	merged := mergeRaw(base, override)
	if _, ok := merged.Profiles["existing"]; !ok {
		t.Error("existing profile lost")
	}
	if _, ok := merged.Profiles["new"]; !ok {
		t.Error("new profile missing")
	}
	if _, ok := base.Profiles["new"]; ok {
		t.Error("base profiles mutated by mergeRaw")
	}
}

func TestDedupPaths_EmptyRest(t *testing.T) {
	result := dedupPaths([]string{"./a", "./b"}, nil)
	if len(result) != 2 || result[0] != "./a" {
		t.Errorf("dedupPaths(first, nil)=%v", result)
	}
}

func TestDedupPaths_EmptyFirst(t *testing.T) {
	result := dedupPaths(nil, []string{"./a", "./b"})
	if len(result) != 2 || result[0] != "./a" {
		t.Errorf("dedupPaths(nil, rest)=%v", result)
	}
}

func TestNewUnion_ToConfigErrorPropagated(t *testing.T) {
	// Valid TOML but invalid duration — loadRaw succeeds, toConfig fails.
	const badDuration = `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
max_backoff = "not-a-duration"
`
	dir := t.TempDir()
	_, err := NewUnion(FromFile(writeFile(t, dir, "bad.toml", badDuration))).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected toConfig error, got: %v", err)
	}
}

func TestNewUnion_ThirdPartySourceGetError(t *testing.T) {
	// A third-party source (non-rawProvider) that returns a non-NotFound error
	// should abort the union.
	_, err := NewUnion(&errorSource{}).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected error propagation, got: %v", err)
	}
}

// Type assertions — MCPServers fields match agentkit types.
var _ map[string]mcp.ServerConfig = MCPServers{}.Stdio
var _ map[string]mcp.HTTPConfig = MCPServers{}.HTTP
