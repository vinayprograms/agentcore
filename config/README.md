# config

Loads `agent.toml` into a typed `Config`. Follows the same composable-primitives
pattern as `agentkit/credentials` and `agentkit/policy` — individual sources are
constructed explicitly, and the caller assembles layers when needed. No discovery
logic lives here; that belongs in the application.

## Core API

```go
// Single file
cfg, err := config.FromFile("agent.toml").Get()

// Layered (later sources override earlier ones)
cfg, err := config.NewUnion(
    config.FromFile("~/.agent/agent.toml"),
    config.FromFile("./agent.toml"),
).Get()

// Direct merge of two known Config values
a, _ := config.FromFile("base.toml").Get()
b, _ := config.FromFile("override.toml").Get()
cfg := config.Merge(a, b)
```

| Symbol | Role |
|---|---|
| `Source` | Interface every source implements: `Get() (Config, error)` |
| `FromFile(path)` | Source backed by a TOML file; returns `ErrNotFound` on Get() if absent |
| `NewUnion(sources...)` | Source that merges left to right; silently skips `ErrNotFound` |
| `Merge(base, override)` | Explicit Config merge — override wins on non-zero fields |
| `ErrNotFound` | Sentinel returned when the underlying file does not exist |

`NewUnion` skips missing files silently; any other error (parse error, invalid
duration) aborts. This lets callers include optional locations without
extra nil-checks:

```go
cfg, err := config.NewUnion(
    config.FromFile(globalPath),   // silently skipped if absent
    config.FromFile("./agent.toml"),
).Get()
```

## Credentials are not in agent.toml

API keys live in `credentials.toml` and are managed by `agentkit/credentials`.
Inject them into each `llm.Config` before calling `llm.New`:

```go
// Typical credential store: credentials.toml merged with env vars.
credStore := credentials.NewUnionStore(
    credentials.NewFileStore("credentials.toml"),
    credentials.NewEnvStore(),
)

cred, _ := credStore.Get(cfg.Models.Default.Service)
cfg.Models.Default.APIKey = cred.APIKey
model, err := llm.New(cfg.Models.Default)
```

Apply the same pattern to `cfg.Models.Supervisor` and each `cfg.Models.Profiles[name]`.

## Policy is not in agent.toml

Policy is sourced independently (CLI flag, env var, or by convention) and
loaded via `agentkit/policy`. This lets the caller choose enforcement behaviour:

```go
// fail-close
pol, err := policy.FromFile(policyPath, workspace, home)

// fail-open
pol, _ := policy.FromFile(policyPath, workspace, home)
if pol == nil { pol = policy.New() }
```

## Wiring into workflow.Runtime

```go
cfg, err := config.NewUnion(
    config.FromFile(globalPath),
    config.FromFile("./agent.toml"),
).Get()

// Build a credential store. Typically a union of a file store and env vars
// so that CI/CD environments can override credentials.toml entries via
// environment variables (e.g. ANTHROPIC_API_KEY).
credStore := credentials.NewUnionStore(
    credentials.NewFileStore("credentials.toml"),
    credentials.NewEnvStore(),
)

// Inject credentials and construct live objects
cred, _ := credStore.Get(cfg.Models.Default.Service)
cfg.Models.Default.APIKey = cred.APIKey
defaultModel, _ := llm.New(cfg.Models.Default)

supervisorCred, _ := credStore.Get(cfg.Models.Supervisor.Service)
cfg.Models.Supervisor.APIKey = supervisorCred.APIKey
supervisorModel, _ := llm.New(cfg.Models.Supervisor)

profiles := make(map[string]llm.Model, len(spec.Profiles()))
for _, name := range spec.Profiles() {
    pcfg := cfg.Models.Profiles[name]
    pc, _ := credStore.Get(pcfg.Service)
    pcfg.APIKey = pc.APIKey
    profiles[name], _ = llm.New(pcfg)
}

mgr := mcp.NewManager()
for name, srv := range cfg.MCP.Stdio {
    client, _ := mcp.Stdio(ctx, srv)
    mgr.Register(name, client)
}
for name, srv := range cfg.MCP.HTTP {
    client, _ := mcp.HTTP(ctx, srv)
    mgr.Register(name, client)
}

skillRoots := make([]*os.Root, len(cfg.Skills))
for i, p := range cfg.Skills {
    skillRoots[i], _ = os.OpenRoot(p)
}
```

## Implementing your own discovery (application concern)

`config` is a kit — it provides composable primitives. Discovery of *which* files
to load (filesystem hierarchy, env vars, command-line flags) is the application's
concern. Here is one typical implementation an application might write:

```go
// In your application — NOT in agentcore/config.
// Uses environment variable, project file, and user-global fallback
// to replicate Git-style config layering.
func loadAgentConfig() (config.Config, error) {
    var sources []config.Source

    // 1. User-global defaults (~/.agent/agent.toml)
    if home, err := os.UserHomeDir(); err == nil {
        sources = append(sources, config.FromFile(
            filepath.Join(home, ".agent", "agent.toml"),
        ))
    }

    // 2. Project-level overrides (./agent.toml)
    sources = append(sources, config.FromFile("agent.toml"))

    // 3. Explicit override via environment variable
    if v := os.Getenv("AGENT_CONFIG"); v != "" {
        sources = append(sources, config.FromFile(v))
    }

    return config.NewUnion(sources...).Get()
}
```

Other applications might use a `--config` flag, walk up the directory tree,
or fetch configuration from a remote store by implementing the `Source` interface:

```go
type remoteSource struct{ url string }

func (r remoteSource) Get() (config.Config, error) {
    // fetch from URL, unmarshal, return config.Config
}

cfg, err := config.NewUnion(
    config.FromFile("./agent.toml"),
    remoteSource{url: "https://config.internal/agent"},
).Get()
```

## agent.toml reference

```toml

# skills must appear before any [section] headers (TOML table scope rule)
skills = ["./skills", "~/.agent/skills"]

[security]
level = "default"   # "default" | "paranoid" | "research"; fallback when Agentfile has no SECURITY directive
# scope = "..."     # required when level = "research"

[model]
service = "anthropic"       # required
model = "claude-opus-4-7"   # required
max_tokens = 8192
base_url = ""               # optional; empty = provider default
max_retries = 3
max_backoff = "30s"
init_backoff = "1s"

[model.thinking]
level = "auto"              # "auto" | "off" | "low" | "medium" | "high"
budget_tokens = 5000

[supervisor]
service = "anthropic"
model = "claude-opus-4-7"
# Absent → Models.Supervisor defaults to Models.Default after merge.
# Retry fields same as [model]; each model tunes independently.

[profiles.reasoning-heavy]
service = "anthropic"
model = "claude-opus-4-7"
max_tokens = 8192
max_retries = 5

[profiles.fast]
service = "anthropic"
model = "claude-haiku-4-5-20251001"

# MCP servers — transport determined by which field is set.
# Set command OR endpoint, not both.
[mcp.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
# env = { KEY = "value" }

[mcp.remote-tools]
endpoint = "https://tools.example.internal/mcp"
```

## Merge semantics

When using `NewUnion` or `Merge`:

| Field | Rule |
|---|---|
| `Name`, `Security.Level` | Right (higher-priority) source wins when non-empty |
| `Models.Default`, `Models.Supervisor` | Right source wins when `Service` is non-empty |
| `Models.Profiles` | Union; right source wins on name collision |
| `MCP.Stdio`, `MCP.HTTP` | Union; right source wins on name collision |
| `Skills` | Union with right source's paths first (shadow precedence) |
| Supervisor default | Applied after merge: if `Models.Supervisor.Service == ""`, uses `Models.Default` |
