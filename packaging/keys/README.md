# keys

Lightweight Ed25519 key management for agent packages — generate, save, and load. Completely optional: `packaging` works with plain `ed25519.PublicKey` and `ed25519.PrivateKey` directly. Use this package for the common path; skip it when you bring your own key management (KMS, HSM, env vars).

## Usage

```go
import "github.com/vinayprograms/agentcore/packaging/keys"

pub, priv, _ := keys.New()

keys.Save("my-key.pem", priv) // auto-detects type, sets mode 0600
keys.Save("my-key.pub", pub)  // mode 0644

loadedPub, _ := keys.Public("my-key.pub")
loadedPriv, _ := keys.Private("my-key.pem")
```

## Functions

| Function | Returns | Purpose |
|---|---|---|
| `New()` | `(PublicKey, PrivateKey, error)` | Generate new Ed25519 key pair |
| `Save(path, key)` | `error` | Write any key to PEM (auto-detects type) |
| `Private(path)` | `(PrivateKey, error)` | Read private key from PEM |
| `Public(path)` | `(PublicKey, error)` | Read public key from PEM |

`Save` accepts `ed25519.PrivateKey` or `ed25519.PublicKey` and sets the correct PEM header and file permissions automatically. Any other type returns an error.
