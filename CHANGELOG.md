# Changelog

All notable changes to agentcore will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial repository scaffolding: `README.md`, `CONTRIBUTING.md`,
  `CHANGELOG.md`, `LICENSE` (Apache 2.0), `.gitignore`.
- Phase 1 scope and package inventory captured in local design docs
  (gitignored `docs/` directory).
- `packaging` package: create, verify, load, and install signed `.agent`
  packages. Ed25519 signing over SHA-256(manifest ‖ content),
  deterministic tar.gz archives, and path-traversal-safe extraction.

No packages are published yet. See `README.md` for the planned surface.
