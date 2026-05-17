package config

import (
	"maps"

	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// Merge returns base with override applied. Fields in override take
// precedence when non-zero; zero values leave the base field unchanged.
//
// Models.Profiles and MCP maps are unioned: override entries win on name
// collision. Skills are concatenated with override paths first so
// higher-priority skills shadow lower-priority ones on bare-name lookup.
//
// The supervisor default (Models.Supervisor → Models.Default when unset)
// is applied after merging so it reflects the merged Default, not either
// layer's individual Default.
func Merge(base, override Config) Config {
	result := base

	if override.Name != "" {
		result.Name = override.Name
	}
	if override.Security.Level != "" {
		result.Security = override.Security
	}
	if override.Models.Default.Service != "" {
		result.Models.Default = override.Models.Default
	}
	if override.Models.Supervisor.Service != "" {
		result.Models.Supervisor = override.Models.Supervisor
	}

	if len(override.Models.Profiles) > 0 {
		if result.Models.Profiles == nil {
			result.Models.Profiles = make(map[string]llm.Config, len(override.Models.Profiles))
		} else {
			result.Models.Profiles = maps.Clone(result.Models.Profiles)
		}
		maps.Copy(result.Models.Profiles, override.Models.Profiles)
	}

	if len(override.MCP.Stdio) > 0 {
		if result.MCP.Stdio == nil {
			result.MCP.Stdio = make(map[string]mcp.ServerConfig, len(override.MCP.Stdio))
		} else {
			result.MCP.Stdio = maps.Clone(result.MCP.Stdio)
		}
		maps.Copy(result.MCP.Stdio, override.MCP.Stdio)
	}

	if len(override.MCP.HTTP) > 0 {
		if result.MCP.HTTP == nil {
			result.MCP.HTTP = make(map[string]mcp.HTTPConfig, len(override.MCP.HTTP))
		} else {
			result.MCP.HTTP = maps.Clone(result.MCP.HTTP)
		}
		maps.Copy(result.MCP.HTTP, override.MCP.HTTP)
	}

	result.Skills = dedupPaths(override.Skills, result.Skills)

	// Apply supervisor default after merge so it uses the merged Default.
	if result.Models.Supervisor.Service == "" {
		result.Models.Supervisor = result.Models.Default
	}

	return result
}

func dedupPaths(first, rest []string) []string {
	if len(first) == 0 {
		return rest
	}
	if len(rest) == 0 {
		return first
	}
	seen := make(map[string]bool, len(first)+len(rest))
	out := make([]string, 0, len(first)+len(rest))
	for _, p := range append(first, rest...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
