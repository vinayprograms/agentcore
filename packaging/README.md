# packaging

Create, verify, and install signed agent packages. A package is a `.agent` file — a zip container holding `manifest.json`, `content.tar.gz`, and an optional Ed25519 signature.

## Usage

```go
import (
    "github.com/vinayprograms/agentcore/packaging"
    "github.com/vinayprograms/agentcore/packaging/keys"
)
```

### Key generation

```go
pub, priv, _ := keys.New()
keys.Save("my-key.pem", priv)
keys.Save("my-key.pub", pub)

loadedPub, _ := keys.Public("my-key.pub")
loadedPriv, _ := keys.Private("my-key.pem")
```

### Packing

```go
// manifest.json provides metadata the Agentfile doesn't carry.
os.WriteFile("manifest.json", []byte(`{
    "version": "1.0.0",
    "description": "A code-review agent",
    "inputs": {
        "path": {"required": true, "type": "string"}
    },
    "dependencies": {
        "helper": "^1.0.0"
    }
}`), 0644)

pkg, err := packaging.Pack("./my-agent", "my-agent-1.0.0.agent", priv, packaging.Metadata{
    Author:      &packaging.Author{Name: "Vinay", Email: "v@example.com"},
    Description: "A code-review agent",
    License:     "MIT",
})

// Unsigned package: pass nil for the private key.
pkg, err = packaging.Pack("./my-agent", "my-agent-1.0.0.agent", nil, packaging.Metadata{})
```

The package name is extracted from the Agentfile's `NAME` keyword. Everything else — version, inputs, outputs, profiles, dependencies — comes from `manifest.json` or the `Metadata` argument.

### Verifying & loading

```go
loaded, _ := packaging.FromFile("my-agent-1.0.0.agent")
if err := packaging.Verify(loaded, pub); err != nil {
    log.Fatal("signature check failed:", err)
}
```

Pass `nil` as public key to skip verification.

### Installing

```go
// Default install — files extracted to ~/.agent/packages/my-agent/1.0.0/
result, err := packaging.Install("my-agent-1.0.0.agent", "", pub, packaging.Mode{})

// Preview dependencies without writing files:
result, _ := packaging.Install("my-agent-1.0.0.agent", "", pub, packaging.Mode{DryRun: true})

// Skip dependency listing:
result, _ := packaging.Install("my-agent-1.0.0.agent", "", pub, packaging.Mode{NoDeps: true})

// Install to a custom target:
result, _ := packaging.Install("my-agent-1.0.0.agent", "/opt/agents", pub, packaging.Mode{})
```

Pass `nil` for the public key to skip signature verification.

### Accessing package contents

```go
// Extract to a temp directory:
dir, _ := pkg.ExtractToTemp()
defer os.RemoveAll(dir)

// Read individual files without extracting:
agentfile, _ := pkg.GetAgentfile()
config, _ := pkg.GetConfig()       // agent.toml
policy, _ := pkg.GetPolicy()       // policy.toml
file, _ := pkg.GetFile("goals/audit.md")
```

## Package format

A `.agent` file is a zip container with `Store` (no compression):

| Entry | Content |
|---|---|
| `manifest.json` | JSON manifest (format, name, version, inputs, outputs, etc.) |
| `content.tar.gz` | Deterministic tar.gz of the agent directory |
| `signature` | 64-byte Ed25519 signature over SHA-256(manifest) ‖ SHA-256(content) |

The tar.gz is deterministic: files are sorted, timestamps are fixed to `2020-01-01T00:00:00Z`, and UID/GID are zeroed. Hidden files and directories (`.git`, `.env`, etc.) are excluded.

## Manifest Example

```go
manifest := packaging.Manifest{
    Format:      1,
    Name:        "code-review",
    Version:     "2.1.0",
    Description: "Reviews code for bugs and security issues",
    Author:      &packaging.Author{Name: "Vinay", Email: "v@example.com"},
    License:     "MIT",
    Inputs: map[string]packaging.Input{
        "path":     {Required: true, Type: "string", Description: "File or directory to review"},
        "language": {Default: "auto-detect", Type: "string"},
    },
    Outputs: map[string]packaging.Output{
        "report": {Type: "string", Description: "Review findings"},
    },
    Requires: &packaging.Requirements{
        Profiles: []string{"reasoning-heavy"},
    },
    Dependencies: map[string]string{
        "helper": "^1.0.0",
    },
}
```

Name is extracted from the Agentfile. Everything else comes from the `Manifest` struct or the `Metadata` argument.

## Constructors & functions

| Function | Returns | Purpose |
|---|---|---|
| `Pack(sourceDir, outputPath, privateKey, meta)` | `(*Package, error)` | Create a package from a directory; `nil` privateKey produces unsigned |
| `FromFile(path)` | `(*Package, error)` | Load a `.agent` file |
| `Verify(pkg, publicKey)` | `error` | Check Ed25519 signature; `nil` publicKey skips verification |
| `Install(packagePath, targetDir, publicKey, mode)` | `(*InstallResult, error)` | Verify + extract; `""` targetDir defaults to `~/.agent/packages` |

Key management is optional — use `packaging/keys` for the common path, or provide your own `ed25519.PublicKey`/`ed25519.PrivateKey` directly.

## Methods on Package

| Method | Returns | Purpose |
|---|---|---|
| `ExtractToTemp()` | `(string, error)` | Extract to temp directory |
| `GetFile(name)` | `([]byte, error)` | Read one file from content |
| `GetAgentfile()` | `([]byte, error)` | Convenience for `GetFile("Agentfile")` |
| `GetConfig()` | `([]byte, error)` | Convenience for `GetFile("agent.toml")` |
| `GetPolicy()` | `([]byte, error)` | Convenience for `GetFile("policy.toml")` |

## Security

- **Path traversal safe.** `extractContent` rejects entries with `..` components.
- **Hidden files excluded.** Dotfiles (`.env`, `.git/`) never enter the package.
- **Self-contained enforcement.** `Pack` rejects Agentfiles whose `AGENT FROM` references a `.agent` file. Cross-package relationships belong in `manifest.json` dependencies.
- **Signature covers both manifest and content.** Tampering with either invalidates the signature.
