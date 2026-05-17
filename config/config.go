// Package config loads agent.toml into a typed Config.
//
// The package follows the same composable-primitives pattern as
// agentkit/credentials and agentkit/policy: individual sources are
// constructed explicitly, and the caller assembles a union when layering
// is needed. No discovery logic lives here — that belongs in the
// consuming application.
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
//	cred, _ := credStore.Get(cfg.DefaultModel.Service)
//	cfg.DefaultModel.APIKey = cred.APIKey
//	model, err := llm.New(cfg.DefaultModel)
package config

import (
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// Config holds the parsed contents of agent.toml as ready-to-wire agentkit
// types. Fields with empty/zero values were absent from the source file(s).
// SupervisorModel is defaulted to DefaultModel when not explicitly set.
type Config struct {
	// Name is the workflow identifier, matching the NAME directive.
	Name string

	// DefaultModel is the primary LLM config for the workflow.
	// Inject APIKey from credentials before calling llm.New.
	DefaultModel llm.Config

	// SupervisorModel is the LLM config for the supervision layer.
	// Defaults to DefaultModel when the source has no [supervisor] section.
	SupervisorModel llm.Config

	// Profiles maps REQUIRES profile names to their LLM configs.
	Profiles map[string]llm.Config

	// MCPServers holds configured MCP servers grouped by transport type.
	MCPServers MCPServers

	// SkillPaths are directories searched for skill bundles.
	SkillPaths []string

	// SecurityMode is the fallback content-guard mode when the Agentfile
	// has no SECURITY directive: "default", "paranoid", "research", or "".
	SecurityMode string
}

// MCPServers groups MCP server configs by transport. The MCP spec treats
// stdio and HTTP as distinct transports with separate configuration shapes.
type MCPServers struct {
	Stdio map[string]mcp.ServerConfig
	HTTP  map[string]mcp.HTTPConfig
}
