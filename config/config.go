// Package config loads agent.toml into a typed Config.
//
// The package follows the same composable-primitives pattern as
// agentkit/credentials and agentkit/policy: individual sources are
// constructed explicitly, and the caller assembles layers when needed.
// No discovery logic lives here — that belongs in the consuming application.
//
// Core API:
//
//	cfg, err := config.FromFile("agent.toml").Get()
//
//	cfg, err := config.NewUnion(
//	    config.FromFile("~/.agent/agent.toml"),
//	    config.FromFile("./agent.toml"),
//	).Get()
//
// Credentials are not in agent.toml. Inject APIKey from your credential
// store into each llm.Config before calling llm.New:
//
//	cred, _ := credStore.Get(cfg.Models.Default.Service)
//	cfg.Models.Default.APIKey = cred.APIKey
//	model, err := llm.New(cfg.Models.Default)
package config

import (
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// Config holds the parsed contents of agent.toml as ready-to-wire agentkit
// types. Fields with empty/zero values were absent from the source file(s).
//
// agent.toml is runtime configuration (models, MCP, security, skills).
// Workflow identity — name, inputs, goals — belongs in the Agentfile.
type Config struct {
	// Models holds all LLM configurations: the default, supervisor, and
	// named profiles. Inject APIKey from credentials before calling llm.New.
	Models Models

	// MCP holds configured MCP servers grouped by transport type.
	MCP MCPServers

	// Skills are directories searched for skill bundles. The caller opens
	// each path as an *os.Root before passing them to agentfile.Config.Skills.
	Skills []string

	// Security is the fallback content-guard configuration used when the
	// Agentfile has no SECURITY directive.
	Security Security
}

// Models holds all LLM model configurations for a workflow.
type Models struct {
	// Default is the primary model for the workflow.
	Default llm.Config

	// Supervisor is the model for the supervision layer. Defaults to Default
	// when no [supervisor] section is present in agent.toml.
	Supervisor llm.Config

	// Profiles maps REQUIRES profile names to their LLM configurations.
	Profiles map[string]llm.Config
}

// MCPServers groups MCP server configs by transport. The MCP spec treats
// stdio and HTTP as distinct transports with separate configuration shapes.
type MCPServers struct {
	Stdio map[string]mcp.ServerConfig
	HTTP  map[string]mcp.HTTPConfig
}

// Security describes the content-guard posture for the workflow. Level
// selects the inspection regime; Scope is required only for the research
// level and declares the boundary within which security-sensitive operations
// are permitted.
type Security struct {
	// Level is the inspection regime: "default" (cheap heuristics on triggers),
	// "paranoid" (full inspection on every tool call), or "research" (full
	// inspection + permitted scope). Empty means use the Agentfile's own
	// SECURITY directive, or "default" if the Agentfile has none.
	Level string

	// Scope describes permitted operations for the "research" level.
	// Must be non-empty when Level is "research".
	Scope string
}
