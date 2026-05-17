package config

import (
	"fmt"
	"maps"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
)

// ---------------------------------------------------------------------------
// Raw TOML shapes — intermediate types for decoding; not exported.
// ---------------------------------------------------------------------------

type rawConfig struct {
	Name         string                 `toml:"name"`
	SecurityMode string                 `toml:"security_mode"`
	Model        rawModel               `toml:"model"`
	Supervisor   rawModel               `toml:"supervisor"`
	Profiles     map[string]rawModel    `toml:"profiles"`
	MCP          map[string]rawMCPEntry `toml:"mcp"`
	Skills       rawSkills              `toml:"skills"`
}

// rawModel maps a flat TOML model section to llm.Config. Retry fields sit
// at the same level as service/model so each profile can tune them independently.
type rawModel struct {
	Service     string      `toml:"service"`
	Model       string      `toml:"model"`
	MaxTokens   int         `toml:"max_tokens"`
	BaseURL     string      `toml:"base_url"`
	MaxRetries  int         `toml:"max_retries"`
	MaxBackoff  string      `toml:"max_backoff"`
	InitBackoff string      `toml:"init_backoff"`
	Thinking    rawThinking `toml:"thinking"`
}

type rawThinking struct {
	Level        string `toml:"level"`
	BudgetTokens int64  `toml:"budget_tokens"`
}

// rawMCPEntry represents one [mcp.<name>] entry. Transport is determined by
// which field is set: Command → stdio, Endpoint → HTTP.
type rawMCPEntry struct {
	Command  string            `toml:"command"`
	Args     []string          `toml:"args"`
	Env      map[string]string `toml:"env"`
	Endpoint string            `toml:"endpoint"`
}

type rawSkills struct {
	Paths []string `toml:"paths"`
}

// loadRaw decodes a single TOML file without applying any defaults.
func loadRaw(path string) (rawConfig, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return rawConfig{}, err
	}
	return raw, nil
}

// mergeRaw applies override on top of base. Scalar fields (Name,
// SecurityMode, Model, Supervisor) take the override value when non-zero.
// Profiles, MCP, and SkillPaths are unioned; override wins on collision.
func mergeRaw(base, override rawConfig) rawConfig {
	result := base

	if override.Name != "" {
		result.Name = override.Name
	}
	if override.SecurityMode != "" {
		result.SecurityMode = override.SecurityMode
	}
	if override.Model.Service != "" {
		result.Model = override.Model
	}
	if override.Supervisor.Service != "" {
		result.Supervisor = override.Supervisor
	}

	if result.Profiles == nil {
		result.Profiles = make(map[string]rawModel, len(override.Profiles))
	} else {
		result.Profiles = maps.Clone(result.Profiles)
	}
	maps.Copy(result.Profiles, override.Profiles)

	if result.MCP == nil {
		result.MCP = make(map[string]rawMCPEntry, len(override.MCP))
	} else {
		result.MCP = maps.Clone(result.MCP)
	}
	maps.Copy(result.MCP, override.MCP)

	result.Skills.Paths = dedupPaths(override.Skills.Paths, base.Skills.Paths)

	return result
}

// toConfig converts a merged rawConfig to a Config, applying defaults and
// validating transport constraints. The supervisor default (using DefaultModel
// when the supervisor section is absent) is applied here so it reflects the
// fully merged model rather than any individual layer's model.
func (r rawConfig) toConfig() (Config, error) {
	defaultModel, err := r.Model.toLLMConfig()
	if err != nil {
		return Config{}, fmt.Errorf("config: [model]: %w", err)
	}

	supervisorSrc := r.Supervisor
	if supervisorSrc.Service == "" {
		supervisorSrc = r.Model
	}
	supervisorModel, err := supervisorSrc.toLLMConfig()
	if err != nil {
		return Config{}, fmt.Errorf("config: [supervisor]: %w", err)
	}

	profiles := make(map[string]llm.Config, len(r.Profiles))
	for name, raw := range r.Profiles {
		cfg, err := raw.toLLMConfig()
		if err != nil {
			return Config{}, fmt.Errorf("config: [profiles.%s]: %w", name, err)
		}
		profiles[name] = cfg
	}

	mcpServers, err := toMCPServers(r.MCP)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Name:            r.Name,
		SecurityMode:    r.SecurityMode,
		DefaultModel:    defaultModel,
		SupervisorModel: supervisorModel,
		Profiles:        profiles,
		MCPServers:      mcpServers,
		SkillPaths:      r.Skills.Paths,
	}, nil
}

func (r rawModel) toLLMConfig() (llm.Config, error) {
	cfg := llm.Config{
		Service:   r.Service,
		Model:     r.Model,
		MaxTokens: r.MaxTokens,
		BaseURL:   r.BaseURL,
		Thinking: llm.ThinkingConfig{
			Level:        llm.ThinkingLevel(r.Thinking.Level),
			BudgetTokens: r.Thinking.BudgetTokens,
		},
	}

	if r.MaxRetries != 0 || r.MaxBackoff != "" || r.InitBackoff != "" {
		retry := llm.RetryConfig{MaxRetries: r.MaxRetries}
		if r.MaxBackoff != "" {
			d, err := time.ParseDuration(r.MaxBackoff)
			if err != nil {
				return llm.Config{}, fmt.Errorf("max_backoff %q: %w", r.MaxBackoff, err)
			}
			retry.MaxBackoff = d
		}
		if r.InitBackoff != "" {
			d, err := time.ParseDuration(r.InitBackoff)
			if err != nil {
				return llm.Config{}, fmt.Errorf("init_backoff %q: %w", r.InitBackoff, err)
			}
			retry.InitBackoff = d
		}
		cfg.Retry = retry
	}

	return cfg, nil
}

func toMCPServers(raw map[string]rawMCPEntry) (MCPServers, error) {
	result := MCPServers{
		Stdio: make(map[string]mcp.ServerConfig),
		HTTP:  make(map[string]mcp.HTTPConfig),
	}
	for name, entry := range raw {
		hasCommand := entry.Command != ""
		hasEndpoint := entry.Endpoint != ""
		switch {
		case hasCommand && hasEndpoint:
			return MCPServers{}, fmt.Errorf("config: [mcp.%s]: set either command (stdio) or endpoint (http), not both", name)
		case !hasCommand && !hasEndpoint:
			return MCPServers{}, fmt.Errorf("config: [mcp.%s]: set command (stdio) or endpoint (http)", name)
		case hasCommand:
			result.Stdio[name] = mcp.ServerConfig{
				Command: entry.Command,
				Args:    entry.Args,
				Env:     entry.Env,
			}
		default:
			result.HTTP[name] = mcp.HTTPConfig{Endpoint: entry.Endpoint}
		}
	}
	return result, nil
}
