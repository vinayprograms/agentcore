package config

import (
	"maps"

	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// Merge returns base with override applied. Fields in override take
// precedence when non-zero; zero values leave the base field unchanged.
//
// Map fields (Profiles, MCPServers) are unioned: override entries win on
// name collision. SkillPaths are concatenated with override paths first so
// higher-priority skills shadow lower-priority ones on bare-name lookup.
//
// The supervisor default (SupervisorModel → DefaultModel when unset) is
// applied after merging so it reflects the merged DefaultModel, not either
// layer's individual DefaultModel.
func Merge(base, override Config) Config {
	result := base

	if override.Name != "" {
		result.Name = override.Name
	}
	if override.SecurityMode != "" {
		result.SecurityMode = override.SecurityMode
	}
	if override.DefaultModel.Service != "" {
		result.DefaultModel = override.DefaultModel
	}
	if override.SupervisorModel.Service != "" {
		result.SupervisorModel = override.SupervisorModel
	}

	if len(override.Profiles) > 0 {
		if result.Profiles == nil {
			result.Profiles = make(map[string]llm.Config, len(override.Profiles))
		} else {
			result.Profiles = maps.Clone(result.Profiles)
		}
		maps.Copy(result.Profiles, override.Profiles)
	}

	if len(override.MCPServers.Stdio) > 0 {
		if result.MCPServers.Stdio == nil {
			result.MCPServers.Stdio = make(map[string]mcp.ServerConfig, len(override.MCPServers.Stdio))
		} else {
			result.MCPServers.Stdio = maps.Clone(result.MCPServers.Stdio)
		}
		maps.Copy(result.MCPServers.Stdio, override.MCPServers.Stdio)
	}

	if len(override.MCPServers.HTTP) > 0 {
		if result.MCPServers.HTTP == nil {
			result.MCPServers.HTTP = make(map[string]mcp.HTTPConfig, len(override.MCPServers.HTTP))
		} else {
			result.MCPServers.HTTP = maps.Clone(result.MCPServers.HTTP)
		}
		maps.Copy(result.MCPServers.HTTP, override.MCPServers.HTTP)
	}

	result.SkillPaths = dedupPaths(override.SkillPaths, result.SkillPaths)

	// Apply supervisor default after merge so it uses the merged DefaultModel.
	if result.SupervisorModel.Service == "" {
		result.SupervisorModel = result.DefaultModel
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
