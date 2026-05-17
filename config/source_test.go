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
skills = ["./skills", "~/.agent/skills"]

[security]
level = "paranoid"

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
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "test-agent" {
		t.Errorf("Name=%q", cfg.Name)
	}
	if cfg.Security.Level != "paranoid" {
		t.Errorf("Security.Level=%q", cfg.Security.Level)
	}
}

func TestFromFile_Get_DefaultModel(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	m := cfg.Models.Default
	if m.Service != "anthropic" || m.Model != "claude-opus-4-7" {
		t.Errorf("Models.Default: %q/%q", m.Service, m.Model)
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
	if cfg.Models.Supervisor.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Models.Supervisor.Model=%q", cfg.Models.Supervisor.Model)
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
	if cfg.Models.Supervisor.Model != cfg.Models.Default.Model {
		t.Errorf("supervisor should default to model: got %q want %q",
			cfg.Models.Supervisor.Model, cfg.Models.Default.Model)
	}
}

func TestFromFile_Get_Profiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(cfg.Models.Profiles) != 2 {
		t.Fatalf("Profiles count=%d", len(cfg.Models.Profiles))
	}
	if rh := cfg.Models.Profiles["reasoning-heavy"]; rh.Retry.MaxRetries != 5 {
		t.Errorf("reasoning-heavy MaxRetries=%d", rh.Retry.MaxRetries)
	}
}

func TestFromFile_Get_MCPServers(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stdio, ok := cfg.MCP.Stdio["filesystem"]
	if !ok {
		t.Fatal("filesystem stdio server missing")
	}
	if stdio.Command != "npx" {
		t.Errorf("command=%q", stdio.Command)
	}
	httpSrv, ok := cfg.MCP.HTTP["remote-tools"]
	if !ok {
		t.Fatal("remote-tools http server missing")
	}
	if httpSrv.Endpoint != "https://tools.example.internal/mcp" {
		t.Errorf("endpoint=%q", httpSrv.Endpoint)
	}
}

func TestFromFile_Get_Skills(t *testing.T) {
	dir := t.TempDir()
	cfg, err := FromFile(writeFile(t, dir, "agent.toml", fullTOML)).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(cfg.Skills) != 2 {
		t.Errorf("Skills count=%d", len(cfg.Skills))
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
	if cfg.MCP.Stdio == nil {
		t.Error("MCP.Stdio should be non-nil")
	}
	if cfg.MCP.HTTP == nil {
		t.Error("MCP.HTTP should be non-nil")
	}
}

// ---------------------------------------------------------------------------
// NewUnion
// ---------------------------------------------------------------------------

const globalTOML = `
name = "global-agent"
skills = ["~/.agent/skills"]

[security]
level = "default"

[model]
service = "anthropic"
model = "claude-haiku-4-5-20251001"
max_tokens = 2048

[supervisor]
service = "anthropic"
model = "claude-opus-4-7"

[mcp.shared-tools]
endpoint = "https://shared.example.internal/mcp"
`

const projectTOML = `
name = "project-agent"
skills = ["./skills"]

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
`

func TestNewUnion_LayersMerge(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewUnion(
		FromFile(writeFile(t, dir, "global.toml", globalTOML)),
		FromFile(writeFile(t, dir, "project.toml", projectTOML)),
	).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "project-agent" {
		t.Errorf("Name=%q want project-agent", cfg.Name)
	}
	if cfg.Models.Default.Model != "claude-opus-4-7" {
		t.Errorf("Default.Model=%q want claude-opus-4-7", cfg.Models.Default.Model)
	}
	if cfg.Security.Level != "default" {
		t.Errorf("Security.Level=%q want default (from global)", cfg.Security.Level)
	}
	// Global explicitly set supervisor; project didn't — global's should win.
	if cfg.Models.Supervisor.Model != "claude-opus-4-7" {
		t.Errorf("Supervisor=%q want claude-opus-4-7 (from global)", cfg.Models.Supervisor.Model)
	}
	if _, ok := cfg.MCP.HTTP["shared-tools"]; !ok {
		t.Error("shared-tools from global missing")
	}
	if _, ok := cfg.MCP.Stdio["project-tools"]; !ok {
		t.Error("project-tools from project missing")
	}
	if len(cfg.Skills) != 2 || cfg.Skills[0] != "./skills" {
		t.Errorf("Skills=%v", cfg.Skills)
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
	if _, ok := cfg.MCP.Stdio["shared-tools"]; !ok {
		t.Error("overridden shared-tools should be stdio")
	}
}

func TestNewUnion_PropagatesParseError(t *testing.T) {
	const badTOML = `
[model
bad toml
`
	dir := t.TempDir()
	_, err := NewUnion(FromFile(writeFile(t, dir, "bad.toml", badTOML))).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMerge_ScalarOverride(t *testing.T) {
	base := Config{Name: "base", Security: Security{Level: "default"}}
	override := Config{Name: "override"}
	result := Merge(base, override)
	if result.Name != "override" {
		t.Errorf("Name=%q", result.Name)
	}
	if result.Security.Level != "default" {
		t.Errorf("Security.Level=%q (should be preserved from base)", result.Security.Level)
	}
}

func TestMerge_SecurityOverride(t *testing.T) {
	base := Config{Security: Security{Level: "default"}}
	override := Config{Security: Security{Level: "paranoid"}}
	if Merge(base, override).Security.Level != "paranoid" {
		t.Error("Security.Level override did not apply")
	}
}

func TestMerge_SupervisorModelOverride(t *testing.T) {
	base := Config{
		Models: Models{
			Default:    llm.Config{Service: "anthropic", Model: "haiku"},
			Supervisor: llm.Config{Service: "anthropic", Model: "opus"},
		},
	}
	override := Config{
		Models: Models{
			Default:    llm.Config{Service: "anthropic", Model: "sonnet"},
			Supervisor: llm.Config{Service: "anthropic", Model: "dedicated"},
		},
	}
	result := Merge(base, override)
	if result.Models.Supervisor.Model != "dedicated" {
		t.Errorf("Supervisor=%q want dedicated", result.Models.Supervisor.Model)
	}
}

func TestMerge_ProfilesUnion(t *testing.T) {
	base := Config{Models: Models{Profiles: map[string]llm.Config{
		"fast": {Service: "anthropic"},
	}}}
	override := Config{Models: Models{Profiles: map[string]llm.Config{
		"reasoning-heavy": {Service: "anthropic"},
	}}}
	result := Merge(base, override)
	if len(result.Models.Profiles) != 2 {
		t.Errorf("Profiles count=%d want 2", len(result.Models.Profiles))
	}
}

func TestMerge_ProfileCollisionOverrideWins(t *testing.T) {
	base := Config{Models: Models{Profiles: map[string]llm.Config{
		"fast": {Service: "anthropic", Model: "slow"},
	}}}
	override := Config{Models: Models{Profiles: map[string]llm.Config{
		"fast": {Service: "anthropic", Model: "faster"},
	}}}
	if Merge(base, override).Models.Profiles["fast"].Model != "faster" {
		t.Error("profile collision: override should win")
	}
}

func TestMerge_NilBaseProfiles(t *testing.T) {
	base := Config{}
	override := Config{Models: Models{Profiles: map[string]llm.Config{
		"fast": {Service: "anthropic"},
	}}}
	result := Merge(base, override)
	if _, ok := result.Models.Profiles["fast"]; !ok {
		t.Error("fast profile missing after merge into nil-profile base")
	}
}

func TestMerge_MCPUnion(t *testing.T) {
	base := Config{MCP: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"global": {Endpoint: "https://global"}},
	}}
	override := Config{MCP: MCPServers{
		Stdio: map[string]mcp.ServerConfig{"local": {Command: "npx"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCP.HTTP["global"]; !ok {
		t.Error("global HTTP server missing after merge")
	}
	if _, ok := result.MCP.Stdio["local"]; !ok {
		t.Error("local stdio server missing after merge")
	}
}

func TestMerge_NilBaseStdioMCP(t *testing.T) {
	base := Config{}
	override := Config{MCP: MCPServers{Stdio: map[string]mcp.ServerConfig{"x": {Command: "cmd"}}}}
	result := Merge(base, override)
	if _, ok := result.MCP.Stdio["x"]; !ok {
		t.Error("stdio server x missing after merge into nil stdio base")
	}
}

func TestMerge_NilBaseHTTPMCP(t *testing.T) {
	base := Config{}
	override := Config{MCP: MCPServers{HTTP: map[string]mcp.HTTPConfig{"x": {Endpoint: "https://example.com"}}}}
	result := Merge(base, override)
	if _, ok := result.MCP.HTTP["x"]; !ok {
		t.Error("http server x missing after merge into nil http base")
	}
}

func TestMerge_HTTPMapClonedWhenNonNil(t *testing.T) {
	base := Config{MCP: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"existing": {Endpoint: "https://base"}},
	}}
	override := Config{MCP: MCPServers{
		HTTP: map[string]mcp.HTTPConfig{"new": {Endpoint: "https://new"}},
	}}
	result := Merge(base, override)
	if _, ok := result.MCP.HTTP["existing"]; !ok {
		t.Error("existing http server lost after merge")
	}
	if _, ok := result.MCP.HTTP["new"]; !ok {
		t.Error("new http server missing after merge")
	}
}

func TestMerge_SkillPathsOverrideFirst(t *testing.T) {
	base := Config{Skills: []string{"~/.agent/skills"}}
	override := Config{Skills: []string{"./skills"}}
	result := Merge(base, override)
	if result.Skills[0] != "./skills" {
		t.Errorf("override skills should be first, got %v", result.Skills)
	}
}

func TestMerge_SkillPathsDeduplicated(t *testing.T) {
	base := Config{Skills: []string{"./shared", "~/.agent/skills"}}
	override := Config{Skills: []string{"./skills", "./shared"}}
	result := Merge(base, override)
	count := 0
	for _, p := range result.Skills {
		if p == "./shared" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("./shared appears %d times, want 1", count)
	}
}

func TestMerge_EmptyOverrideSkillPaths(t *testing.T) {
	base := Config{Skills: []string{"./base"}}
	result := Merge(base, Config{})
	if len(result.Skills) != 1 || result.Skills[0] != "./base" {
		t.Errorf("Skills=%v", result.Skills)
	}
}

func TestMerge_SupervisorDefaultAfterMerge(t *testing.T) {
	base := Config{Models: Models{Default: llm.Config{Service: "anthropic", Model: "haiku"}}}
	override := Config{Models: Models{Default: llm.Config{Service: "anthropic", Model: "opus"}}}
	result := Merge(base, override)
	if result.Models.Supervisor.Model != "opus" {
		t.Errorf("supervisor should default to merged model opus, got %q", result.Models.Supervisor.Model)
	}
}

func TestMerge_BaseMapsPreservedWithNoOverride(t *testing.T) {
	base := Config{
		Models: Models{Profiles: map[string]llm.Config{"fast": {Service: "anthropic"}}},
		MCP:    MCPServers{Stdio: map[string]mcp.ServerConfig{"x": {Command: "y"}}},
	}
	result := Merge(base, Config{})
	if _, ok := result.Models.Profiles["fast"]; !ok {
		t.Error("fast profile lost")
	}
	if _, ok := result.MCP.Stdio["x"]; !ok {
		t.Error("stdio server x lost")
	}
}

func TestMerge_NonNilBaseProfilesCloned(t *testing.T) {
	base := Config{Models: Models{Profiles: map[string]llm.Config{"existing": {Service: "anthropic"}}}}
	override := Config{Models: Models{Profiles: map[string]llm.Config{"new": {Service: "anthropic"}}}}
	result := Merge(base, override)
	if _, ok := result.Models.Profiles["existing"]; !ok {
		t.Error("existing profile lost")
	}
	if _, ok := result.Models.Profiles["new"]; !ok {
		t.Error("new profile missing")
	}
	if _, ok := base.Models.Profiles["new"]; ok {
		t.Error("base was mutated by Merge")
	}
}

func TestMerge_NonNilBaseStdioMCPCloned(t *testing.T) {
	base := Config{MCP: MCPServers{Stdio: map[string]mcp.ServerConfig{"existing": {Command: "npx"}}}}
	override := Config{MCP: MCPServers{Stdio: map[string]mcp.ServerConfig{"new": {Command: "new"}}}}
	result := Merge(base, override)
	if _, ok := result.MCP.Stdio["existing"]; !ok {
		t.Error("existing stdio server lost")
	}
	if _, ok := base.MCP.Stdio["new"]; ok {
		t.Error("base was mutated by Merge")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage: specific branches
// ---------------------------------------------------------------------------

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
	custom := thirdPartySource{cfg: Config{Security: Security{Level: "paranoid"}}}

	cfg, err := NewUnion(file, custom).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "from-file" {
		t.Errorf("Name=%q", cfg.Name)
	}
	if cfg.Security.Level != "paranoid" {
		t.Errorf("Security.Level=%q want paranoid", cfg.Security.Level)
	}
}

type notFoundSource struct{}

func (s *notFoundSource) Get() (Config, error) { return Config{}, ErrNotFound }

func TestNewUnion_ThirdPartySourceNotFound(t *testing.T) {
	dir := t.TempDir()
	file := FromFile(writeFile(t, dir, "agent.toml", `
name = "x"
[model]
service = "anthropic"
model = "claude-opus-4-7"
`))
	cfg, err := NewUnion(&notFoundSource{}, file).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Name != "x" {
		t.Errorf("Name=%q", cfg.Name)
	}
}

type errorSource struct{}

func (s *errorSource) Get() (Config, error) { return Config{}, errors.New("source failed") }

func TestNewUnion_ThirdPartySourceGetError(t *testing.T) {
	_, err := NewUnion(&errorSource{}).Get()
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("expected error propagation, got: %v", err)
	}
}

func TestNewUnion_ToConfigErrorPropagated(t *testing.T) {
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

func TestMergeRaw_SecurityAndSupervisorOverride(t *testing.T) {
	base := rawConfig{Security: rawSecurity{Level: "default"}}
	override := rawConfig{
		Security:   rawSecurity{Level: "paranoid"},
		Supervisor: rawModel{Service: "anthropic", Model: "dedicated"},
	}
	merged := mergeRaw(base, override)
	if merged.Security.Level != "paranoid" {
		t.Errorf("Security.Level=%q", merged.Security.Level)
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

func TestMergeRaw_NonNilBaseProfilesCloned(t *testing.T) {
	base := rawConfig{Profiles: map[string]rawModel{"existing": {Service: "anthropic"}}}
	override := rawConfig{Profiles: map[string]rawModel{"new": {Service: "anthropic"}}}
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

// Type assertions — MCPServers fields match agentkit types.
var _ map[string]mcp.ServerConfig = MCPServers{}.Stdio
var _ map[string]mcp.HTTPConfig = MCPServers{}.HTTP
