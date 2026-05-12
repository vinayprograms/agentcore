// Coverage notes (maintainer-facing; this file is a *_test.go so the comment
// does not appear in godoc / pkg.go.dev).
//
// agentcore's policy targets 100% statement coverage. packaging currently
// lands at ~90% because the remaining branches are filesystem and writer
// error paths that cannot be exercised in unit tests without either
// (a) injecting an fs.FS abstraction throughout — a large refactor for
// marginal coverage — or (b) tests that depend on environment-specific
// permission setups that are fragile across CI runners.
//
// Categories of intentionally-uncovered branches:
//
//  1. filepath.Walk callback receiving an error mid-walk
//     (createContentArchive). Race-window — requires concurrent FS mutation.
//
//  2. os.Stat / os.ReadFile on a file filepath.Walk just visited
//     (createContentArchive). Same race-window problem.
//
//  3. tar.WriteHeader / Writer.Write / Writer.Close errors and the matching
//     gzip.Writer.Close error (createContentArchive). Requires injecting a
//     broken writer; would need an internal writer interface.
//
//  4. os.MkdirAll / os.Create / io.Copy errors during extractContent on the
//     target filesystem. Requires a read-only or full filesystem.
//
//  5. os.Create on writePackage's output path when the destination directory
//     is unwritable. Partial coverage exists (nonexistent-directory case);
//     the read-only-directory case is not exercised.
//
//  6. zip.Writer.CreateHeader / Write errors (writeZipFileStored). Same
//     trade-off as (3).
//
//  7. (*zip.File).Open error inside readZipFile. Triggered by a corrupt zip
//     entry; difficult to craft deterministically.
//
//  8. json.MarshalIndent failure on *Manifest (Pack, Verify). Practically
//     unreachable — Manifest contains no cyclic types or unsupported values.
//
//  9. os.MkdirTemp failure (ExtractToTemp). Requires the system temp
//     directory unavailable or out of space.
//
// 10. os.MkdirAll on the install target (Install). Read-only target dir.
//
// If we introduce an fs.FS-based abstraction for these filesystem operations,
// the branches become testable via in-memory mocks and this list should be
// closed out. Until then: accept the gap, keep this list current, and review
// on each material change to the package.
package packaging

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func generateKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestPackAndFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	agentfile := []byte(`NAME test-agent
GOAL process "Process the input at $path"
RUN main USING process`)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), agentfile, 0644)

	manifestJSON := `{
		"version": "1.0.0",
		"inputs": {
			"path": {"required": true, "type": "string"},
			"format": {"default": "json", "type": "string"}
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	pubKey, privKey := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "test-agent-1.0.0.agent")
	pkg, err := Pack(agentDir, outputPath, privKey, Metadata{
		Author: &Author{
			Name:  "Test Author",
			Email: "test@example.com",
		},
		Description: "A test agent",
		License:     "MIT",
	})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	if pkg.Manifest.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %q", pkg.Manifest.Name)
	}
	if pkg.Manifest.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", pkg.Manifest.Version)
	}
	if pkg.Manifest.Format != formatVersion {
		t.Errorf("expected format %d, got %d", formatVersion, pkg.Manifest.Format)
	}
	if len(pkg.Manifest.Inputs) != 2 {
		t.Errorf("expected 2 inputs, got %d", len(pkg.Manifest.Inputs))
	}
	if !pkg.Manifest.Inputs["path"].Required {
		t.Error("expected 'path' input to be required")
	}
	if pkg.Manifest.Inputs["format"].Default != "json" {
		t.Errorf("expected 'format' default 'json', got %q", pkg.Manifest.Inputs["format"].Default)
	}
	if pkg.Manifest.Author == nil {
		t.Fatal("expected author to be set")
	}
	if pkg.Manifest.Author.Name != "Test Author" {
		t.Errorf("expected author name, got %q", pkg.Manifest.Author.Name)
	}
	if pkg.Manifest.Description != "A test agent" {
		t.Errorf("expected description, got %q", pkg.Manifest.Description)
	}
	if pkg.Manifest.License != "MIT" {
		t.Errorf("expected license MIT, got %q", pkg.Manifest.License)
	}
	if pkg.Manifest.CreatedAt == "" {
		t.Error("expected created_at to be set")
	}
	if pkg.Manifest.Author.KeyFingerprint == "" {
		t.Error("expected key fingerprint to be set")
	}

	if pkg.Signature == nil {
		t.Fatal("expected signature to be set")
	}
	if len(pkg.Signature) != 64 {
		t.Errorf("expected 64-byte signature, got %d", len(pkg.Signature))
	}

	loaded, err := FromFile(outputPath)
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	if loaded.Manifest.Name != pkg.Manifest.Name {
		t.Error("loaded manifest doesn't match")
	}
	if len(loaded.Content) == 0 {
		t.Error("loaded content is empty")
	}

	if err := Verify(loaded, pubKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestPackUnsigned(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	agentfile := []byte(`NAME unsigned-test
GOAL test "Do the thing"
RUN main USING test`)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), agentfile, 0644)

	outputPath := filepath.Join(tmpDir, "unsigned.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	if pkg.Signature != nil {
		t.Error("expected no signature for unsigned package")
	}

	if err := Verify(pkg, nil); err != nil {
		t.Errorf("Verify with nil key: %v", err)
	}

	if err := Verify(pkg, make(ed25519.PublicKey, 32)); err == nil {
		t.Error("expected error when verifying unsigned package with key")
	}
}

func TestVerifyTamperedContent(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME tamper-test
GOAL test "Test"
RUN main USING test`), 0644)

	pubKey, privKey := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "tamper.agent")
	_, err := Pack(agentDir, outputPath, privKey, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	pkg, _ := FromFile(outputPath)
	pkg.Content = []byte("tampered content")

	if err := Verify(pkg, pubKey); err == nil {
		t.Error("expected verification to fail for tampered content")
	}
}

func TestVerifyTamperedManifest(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME manifest-tamper
GOAL test "Test"
RUN main USING test`), 0644)

	pubKey, privKey := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "manifest-tamper.agent")
	_, err := Pack(agentDir, outputPath, privKey, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	pkg, _ := FromFile(outputPath)
	pkg.Manifest.Version = "tampered"

	if err := Verify(pkg, pubKey); err == nil {
		t.Error("expected verification to fail for tampered manifest")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME key-test
GOAL test "Test"
RUN main USING test`), 0644)

	_, privKey := generateKeyPair(t)
	wrongPubKey, _ := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "key-test.agent")
	Pack(agentDir, outputPath, privKey, Metadata{})

	pkg, _ := FromFile(outputPath)

	if err := Verify(pkg, wrongPubKey); err == nil {
		t.Error("expected verification to fail with wrong key")
	}
}

func TestInstall(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME install-test
GOAL test "Test"
RUN main USING test`), 0644)
	manifestJSON := `{"name":"install-test","version":"2.0.0"}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)
	os.WriteFile(filepath.Join(agentDir, "README.md"), []byte("# Test"), 0644)
	os.MkdirAll(filepath.Join(agentDir, "agents"), 0755)
	os.WriteFile(filepath.Join(agentDir, "agents/critic.md"), []byte("You are a critic."), 0644)

	outputPath := filepath.Join(tmpDir, "install-test.agent")
	Pack(agentDir, outputPath, nil, Metadata{})

	installDir := filepath.Join(tmpDir, "installed")
	result, err := Install(outputPath, installDir, nil, Mode{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(result.Installed) != 1 || result.Installed[0] != "install-test" {
		t.Errorf("unexpected installed: %v", result.Installed)
	}

	expectedPath := filepath.Join(installDir, "install-test", "2.0.0", "Agentfile")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Agentfile not found at %s", expectedPath)
	}

	readmePath := filepath.Join(installDir, "install-test", "2.0.0", "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Errorf("README.md not found at %s", readmePath)
	}

	agentPath := filepath.Join(installDir, "install-test", "2.0.0", "agents", "critic.md")
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		t.Errorf("agents/critic.md not found at %s", agentPath)
	}
}

func TestInstallDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME dryrun-test
GOAL test "Test"
RUN main USING test`), 0644)

	manifestJSON := `{
		"name": "dryrun-test",
		"version": "1.0.0",
		"dependencies": {
			"dep-a": "^1.0.0",
			"dep-b": ">=2.0.0"
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	outputPath := filepath.Join(tmpDir, "dryrun-test.agent")
	Pack(agentDir, outputPath, nil, Metadata{})

	installDir := filepath.Join(tmpDir, "should-not-exist")
	result, err := Install(outputPath, installDir, nil, Mode{DryRun: true})
	if err != nil {
		t.Fatalf("Install dry run: %v", err)
	}

	if len(result.Dependencies) != 2 {
		t.Errorf("expected 2 dependencies, got %d", len(result.Dependencies))
	}

	if _, err := os.Stat(installDir); !os.IsNotExist(err) {
		t.Error("dry run should not create directory")
	}
}

func TestInstallNoDeps(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME nodeps-test
GOAL test "Test"
RUN main USING test`), 0644)

	manifestJSON := `{
		"name": "nodeps-test",
		"version": "1.0.0",
		"dependencies": {
			"dep-a": "^1.0.0"
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	outputPath := filepath.Join(tmpDir, "nodeps.agent")
	Pack(agentDir, outputPath, nil, Metadata{})

	result, err := Install(outputPath, filepath.Join(tmpDir, "installed"), nil, Mode{NoDeps: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(result.Dependencies) != 0 {
		t.Errorf("expected 0 dependencies with NoDeps, got %d", len(result.Dependencies))
	}
}

func TestInstallVerificationFailure(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME fail-verify
GOAL test "Test"
RUN main USING test`), 0644)

	_, privKey := generateKeyPair(t)
	wrongPubKey, _ := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "fail-verify.agent")
	Pack(agentDir, outputPath, privKey, Metadata{})

	_, err := Install(outputPath, filepath.Join(tmpDir, "installed"), wrongPubKey, Mode{})
	if err == nil {
		t.Error("expected installation failure on verification failure")
	}
}

func TestDeterministicPacking(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME deterministic-test
GOAL test "Test"
RUN main USING test`), 0644)

	pkg1, _ := Pack(agentDir, "", nil, Metadata{})
	pkg2, _ := Pack(agentDir, "", nil, Metadata{})

	if string(pkg1.Content) != string(pkg2.Content) {
		t.Error("packing is not deterministic - content differs")
	}
}

func TestExtractRequiresProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)
	os.MkdirAll(filepath.Join(agentDir, "agents"), 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME profile-test
GOAL review "Review"
RUN main USING review`), 0644)

	manifestJSON := `{
		"name": "profile-test",
		"version": "1.0.0",
		"requires": {
			"profiles": ["reasoning-heavy", "fast"]
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	outputPath := filepath.Join(tmpDir, "profile-test.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	if pkg.Manifest.Requires == nil {
		t.Fatal("expected Requires to be set")
	}
	if len(pkg.Manifest.Requires.Profiles) != 2 {
		t.Errorf("expected 2 profiles, got %d", len(pkg.Manifest.Requires.Profiles))
	}
}

func TestValidateAgentReferencesValid(t *testing.T) {
	tmpDir := t.TempDir()
	agentfile := []byte(`NAME test
AGENT critic FROM agents/critic.md
AGENT helper FROM skills/helper/SKILL.md
GOAL main "Test"
RUN main USING main
`)
	path := filepath.Join(tmpDir, "Agentfile")
	os.WriteFile(path, agentfile, 0644)

	if err := validateAgentReferences(path); err != nil {
		t.Errorf("expected no error for valid references, got: %v", err)
	}
}

func TestValidateAgentReferencesRejectsAgentPackage(t *testing.T) {
	tmpDir := t.TempDir()
	agentfile := []byte(`NAME test
AGENT helper FROM other-agent.agent
GOAL main "Test"
RUN main USING main
`)
	path := filepath.Join(tmpDir, "Agentfile")
	os.WriteFile(path, agentfile, 0644)

	err := validateAgentReferences(path)
	if err == nil {
		t.Error("expected error for .agent reference")
	}
	if !strings.Contains(err.Error(), ".agent packages") {
		t.Errorf("expected error about .agent packages, got: %v", err)
	}
}

func TestValidateAgentReferencesRejectsPathWithAgentExtension(t *testing.T) {
	tmpDir := t.TempDir()
	agentfile := []byte(`NAME test
AGENT coder FROM packages/coder-go.agent REQUIRES "fast"
GOAL main "Test"
RUN main USING main
`)
	path := filepath.Join(tmpDir, "Agentfile")
	os.WriteFile(path, agentfile, 0644)

	err := validateAgentReferences(path)
	if err == nil {
		t.Error("expected error for .agent path reference")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected error on line 2, got: %v", err)
	}
}

func TestValidateAgentReferencesAllowsQuotedPaths(t *testing.T) {
	tmpDir := t.TempDir()
	agentfile := []byte(`NAME test
AGENT critic FROM "agents/my critic.md"
GOAL main "Test"
RUN main USING main
`)
	path := filepath.Join(tmpDir, "Agentfile")
	os.WriteFile(path, agentfile, 0644)

	if err := validateAgentReferences(path); err != nil {
		t.Errorf("expected no error for quoted .md path, got: %v", err)
	}
}

func TestExtractToTemp(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME extract-test
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "extract.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	extracted, err := pkg.ExtractToTemp()
	if err != nil {
		t.Fatalf("ExtractToTemp: %v", err)
	}
	defer os.RemoveAll(extracted)

	agentfilePath := filepath.Join(extracted, "Agentfile")
	if _, err := os.Stat(agentfilePath); os.IsNotExist(err) {
		t.Error("Agentfile not found in extracted temp dir")
	}
}

func TestGetFile(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME getfile-test
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "getfile.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	data, err := pkg.GetAgentfile()
	if err != nil {
		t.Fatalf("GetAgentfile: %v", err)
	}
	if !strings.Contains(string(data), "getfile-test") {
		t.Errorf("Agentfile content doesn't contain expected name: %s", string(data))
	}

	_, err = pkg.GetConfig()
	if err == nil {
		t.Error("expected error for missing agent.toml")
	}

	_, err = pkg.GetPolicy()
	if err == nil {
		t.Error("expected error for missing policy.toml")
	}

	_, err = pkg.GetFile("nonexistent.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestManifestJSONMerge(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME merge-test
GOAL test "Test $topic"
RUN main USING test`), 0644)

	manifestJSON := `{
		"name": "",
		"version": "3.0.0",
		"description": "A merged manifest",
		"outputs": {
			"report": {"type": "string", "description": "The report"}
		},
		"inputs": {
			"topic": {"required": true, "type": "string"}
		},
		"dependencies": {
			"helper": "^1.0.0"
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	outputPath := filepath.Join(tmpDir, "merge.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	if pkg.Manifest.Name != "merge-test" {
		t.Errorf("expected name from Agentfile, got %q", pkg.Manifest.Name)
	}
	if pkg.Manifest.Version != "3.0.0" {
		t.Errorf("expected version from Agentfile, got %q", pkg.Manifest.Version)
	}
	if pkg.Manifest.Description != "A merged manifest" {
		t.Errorf("expected description from manifest.json, got %q", pkg.Manifest.Description)
	}
	if len(pkg.Manifest.Inputs) != 1 {
		t.Errorf("expected 1 input from Agentfile, got %d", len(pkg.Manifest.Inputs))
	}
	if len(pkg.Manifest.Outputs) != 1 {
		t.Errorf("expected 1 output from manifest.json, got %d", len(pkg.Manifest.Outputs))
	}
	if len(pkg.Manifest.Dependencies) != 1 {
		t.Errorf("expected 1 dependency from manifest.json, got %d", len(pkg.Manifest.Dependencies))
	}
}

func TestPackMissingAgentfile(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "no-agentfile")
	os.MkdirAll(agentDir, 0755)

	_, err := Pack(agentDir, "", nil, Metadata{})
	if err == nil {
		t.Error("expected error for missing Agentfile")
	}
}

func TestPackMissingName(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`GOAL test "Test"
RUN main USING test`), 0644)

	_, err := Pack(agentDir, "", nil, Metadata{})
	if err == nil {
		t.Error("expected error for Agentfile missing NAME")
	}
}

func TestFromFileMissingFile(t *testing.T) {
	_, err := FromFile("/nonexistent/path.agent")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestFromFileInvalidZip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.agent")
	os.WriteFile(path, []byte("not a zip"), 0644)

	_, err := FromFile(path)
	if err == nil {
		t.Error("expected error for invalid zip")
	}
}

func TestSignatureRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME sig-test
GOAL test "Test"
RUN main USING test`), 0644)

	pubKey, privKey := generateKeyPair(t)

	outputPath := filepath.Join(tmpDir, "sig.agent")
	pkg, err := Pack(agentDir, outputPath, privKey, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	manifestJSON, _ := json.MarshalIndent(pkg.Manifest, "", "  ")
	manifestHash := sha256.Sum256(manifestJSON)
	contentHash := sha256.Sum256(pkg.Content)
	toVerify := append(manifestHash[:], contentHash[:]...)

	if !ed25519.Verify(pubKey, toVerify, pkg.Signature) {
		t.Error("raw signature verification failed")
	}
}

func TestPackageWithDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(filepath.Join(agentDir, "agents"), 0755)
	os.MkdirAll(filepath.Join(agentDir, "goals"), 0755)
	os.MkdirAll(filepath.Join(agentDir, "skills", "my-skill"), 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME dirs-test
GOAL test "Test"
RUN main USING test`), 0644)
	os.WriteFile(filepath.Join(agentDir, "agents", "critic.md"), []byte("Critic"), 0644)
	os.WriteFile(filepath.Join(agentDir, "goals", "analyze.md"), []byte("Analyze"), 0644)
	os.WriteFile(filepath.Join(agentDir, "skills", "my-skill", "SKILL.md"), []byte("A skill"), 0644)

	outputPath := filepath.Join(tmpDir, "dirs.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	critic, _ := pkg.GetFile("agents/critic.md")
	if string(critic) != "Critic" {
		t.Errorf("expected 'Critic', got %q", string(critic))
	}

	skill, _ := pkg.GetFile("skills/my-skill/SKILL.md")
	if string(skill) != "A skill" {
		t.Errorf("expected 'A skill', got %q", string(skill))
	}
}

func TestPackageSkipsHiddenFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME hidden-test
GOAL test "Test"
RUN main USING test`), 0644)
	os.WriteFile(filepath.Join(agentDir, ".hidden"), []byte("secret"), 0644)
	os.MkdirAll(filepath.Join(agentDir, ".git"), 0755)
	os.WriteFile(filepath.Join(agentDir, ".git", "config"), []byte("git"), 0644)

	outputPath := filepath.Join(tmpDir, "hidden.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	_, err = pkg.GetFile(".hidden")
	if err == nil {
		t.Error("expected hidden file to be excluded")
	}

	_, err = pkg.GetFile(".git/config")
	if err == nil {
		t.Error("expected .git directory to be excluded")
	}
}

func TestPackRejectsAgentReferences(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME ref-test
AGENT helper FROM other.agent
GOAL main "Test"
RUN main USING main
`), 0644)

	_, err := Pack(agentDir, "", nil, Metadata{})
	if err == nil {
		t.Error("expected error for .agent reference")
	}
	if !strings.Contains(err.Error(), ".agent packages") {
		t.Errorf("expected error about .agent packages, got: %v", err)
	}
}

func TestFromFileManifestInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad-manifest.agent")
	createZipWithManifest(t, path, "not json")

	_, err := FromFile(path)
	if err == nil {
		t.Error("expected error for invalid manifest JSON")
	}
}

func TestFromFileMissingManifest(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "no-manifest.agent")
	createZipWithoutManifest(t, path)

	_, err := FromFile(path)
	if err == nil {
		t.Error("expected error for missing manifest")
	}
	if !strings.Contains(err.Error(), "missing manifest.json") {
		t.Errorf("expected 'missing manifest.json' error, got: %v", err)
	}
}

func TestFromFileMissingContent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "no-content.agent")
	createZipWithoutContent(t, path)

	_, err := FromFile(path)
	if err == nil {
		t.Error("expected error for missing content")
	}
	if !strings.Contains(err.Error(), "missing content.tar.gz") {
		t.Errorf("expected 'missing content.tar.gz' error, got: %v", err)
	}
}

func TestExtractToTempFailure(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME extract-fail
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "extract-fail.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	pkg.Content = []byte("not a gzip archive")

	_, err = pkg.ExtractToTemp()
	if err == nil {
		t.Error("expected error for corrupt content")
	}
}

func TestManifestJSONInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME invalid-manifest-test
GOAL test "Test"
RUN main USING test`), 0644)
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(`{invalid json`), 0644)

	_, err := Pack(agentDir, "", nil, Metadata{})
	if err == nil {
		t.Error("expected error for invalid manifest.json")
	}
}

func TestManifestJSONMergesProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME merge-profiles-test
GOAL review "Review"
RUN main USING review`), 0644)

	manifestJSON := `{
		"name": "merge-profiles-test",
		"version": "1.0.0",
		"requires": {
			"profiles": ["fast", "reasoning-heavy"]
		}
	}`
	os.WriteFile(filepath.Join(agentDir, "manifest.json"), []byte(manifestJSON), 0644)

	outputPath := filepath.Join(tmpDir, "merge-profiles.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	if pkg.Manifest.Requires == nil {
		t.Fatal("expected Requires to be set")
	}
	if len(pkg.Manifest.Requires.Profiles) != 2 {
		t.Errorf("expected 2 profiles, got %d: %v", len(pkg.Manifest.Requires.Profiles), pkg.Manifest.Requires.Profiles)
	}
}

func TestInstallToDefaultDir(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME default-dir-test
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "default-dir.agent")
	Pack(agentDir, outputPath, nil, Metadata{})

	homeBackup := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", homeBackup)

	result, err := Install(outputPath, "", nil, Mode{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if result.InstallPath == "" {
		t.Error("expected InstallPath to be set")
	}
}

func TestGetFileWithLeadingDotSlash(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME dotslash-test
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "dotslash.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	data, err := pkg.GetFile("./Agentfile")
	if err != nil {
		t.Errorf("GetFile with ./ prefix: %v", err)
	}
	if !strings.Contains(string(data), "dotslash-test") {
		t.Error("expected content match for ./Agentfile lookup")
	}
}

func TestGetFileWithPrefixDotSlash(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "test-agent")
	os.MkdirAll(agentDir, 0755)

	os.WriteFile(filepath.Join(agentDir, "Agentfile"),
		[]byte(`NAME prefix-test
GOAL test "Test"
RUN main USING test`), 0644)

	outputPath := filepath.Join(tmpDir, "prefix.agent")
	pkg, err := Pack(agentDir, outputPath, nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	data, err := pkg.GetFile("Agentfile")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !strings.Contains(string(data), "prefix-test") {
		t.Error("expected content match")
	}
}

// ----- zip helpers for edge-case tests ---------------------------------------

func createZipWithManifest(t *testing.T, path, manifestData string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	hdr := &zip.FileHeader{
		Name:   manifestFile,
		Method: zip.Store,
	}
	hdr.Modified = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(manifestData))
	zw.Close()
}

func createZipWithoutManifest(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	zw.Close()
}

func createZipWithoutContent(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	manifestJSON := `{"name":"test","version":"1.0.0"}`
	hdr := &zip.FileHeader{
		Name:   manifestFile,
		Method: zip.Store,
	}
	hdr.Modified = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	w, _ := zw.CreateHeader(hdr)
	w.Write([]byte(manifestJSON))
	zw.Close()
}

func TestPackageExtractPathTraversalSafe(t *testing.T) {
	tmpDir := t.TempDir()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "../escape.txt",
		Size:     5,
		Mode:     0644,
		ModTime:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Typeflag: tar.TypeReg,
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("hello"))
	tw.Close()
	gw.Close()

	extractDir := filepath.Join(tmpDir, "target")
	os.MkdirAll(extractDir, 0755)

	err := extractContent(buf.Bytes(), extractDir)
	if err == nil {
		t.Error("expected path traversal to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid path") {
		t.Errorf("expected 'invalid path' error, got: %v", err)
	}
}

func TestPackOutputPathUnwritable(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), []byte("NAME test\n"), 0644)

	// Output path points into a directory that doesn't exist — writePackage will fail.
	bogusOutput := filepath.Join(tmpDir, "no-such-dir", "out.agent")
	_, err := Pack(agentDir, bogusOutput, nil, Metadata{})
	if err == nil {
		t.Fatal("expected error writing to nonexistent directory")
	}
	if !strings.Contains(err.Error(), "failed to write package") {
		t.Errorf("expected write-package error, got: %v", err)
	}
}

func TestInstallBadPackagePath(t *testing.T) {
	_, err := Install("/nonexistent/path/foo.agent", "", nil, Mode{})
	if err == nil {
		t.Fatal("expected error opening nonexistent package")
	}
}

func TestInstallHomeUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), []byte("NAME test\n"), 0644)

	pkgPath := filepath.Join(tmpDir, "test.agent")
	if _, err := Pack(agentDir, pkgPath, nil, Metadata{}); err != nil {
		t.Fatal(err)
	}

	// Clear env vars that os.UserHomeDir consults so it returns an error.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("home", "")

	_, err := Install(pkgPath, "", nil, Mode{})
	if err == nil {
		t.Fatal("expected error when home directory is unavailable")
	}
	if !strings.Contains(err.Error(), "home directory unavailable") {
		t.Errorf("expected home-unavailable error, got: %v", err)
	}
}

func TestInstallExtractFailure(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), []byte("NAME test\n"), 0644)

	pkgPath := filepath.Join(tmpDir, "test.agent")
	pkg, err := Pack(agentDir, pkgPath, nil, Metadata{})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the content so extractContent fails.
	pkg.Content = []byte("not gzip")

	// Re-pack the corrupted in-memory package to disk by writing the zip manually,
	// so FromFile loads it back and Install fails at extractContent.
	corruptPath := filepath.Join(tmpDir, "corrupt.agent")
	manifestJSON, _ := json.MarshalIndent(pkg.Manifest, "", "  ")
	if err := writePackage(corruptPath, manifestJSON, pkg); err != nil {
		t.Fatal(err)
	}

	_, err = Install(corruptPath, filepath.Join(tmpDir, "out"), nil, Mode{})
	if err == nil {
		t.Fatal("expected error extracting corrupt content")
	}
	if !strings.Contains(err.Error(), "failed to extract content") {
		t.Errorf("expected extract error, got: %v", err)
	}
}

func TestGetFileCorruptContent(t *testing.T) {
	pkg := &Package{
		Manifest: &Manifest{Name: "x"},
		Content:  []byte("not gzip"),
	}
	_, err := pkg.GetFile("anything")
	if err == nil {
		t.Fatal("expected error reading corrupt content")
	}
}

func TestGetFileCorruptTar(t *testing.T) {
	// Valid gzip wrapping garbage tar.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("not a tar archive"))
	gw.Close()

	pkg := &Package{
		Manifest: &Manifest{Name: "x"},
		Content:  buf.Bytes(),
	}
	_, err := pkg.GetFile("anything")
	if err == nil {
		t.Fatal("expected error reading malformed tar")
	}
}

func TestExtractContentCorruptTar(t *testing.T) {
	tmpDir := t.TempDir()
	// Valid gzip, malformed tar inside.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("not a tar archive"))
	gw.Close()

	err := extractContent(buf.Bytes(), tmpDir)
	if err == nil {
		t.Fatal("expected error extracting malformed tar")
	}
}

func TestPackNameAfterCommentsAndStrayLine(t *testing.T) {
	// Exercises the comment/blank-line skip and short-line skip in
	// extractNameFromAgentfile (the "scan until NAME" loop branches).
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "Agentfile"), []byte(`# leading comment
# another comment

LONELY
NAME myagent
`), 0644)

	pkg, err := Pack(agentDir, "", nil, Metadata{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if pkg.Manifest.Name != "myagent" {
		t.Errorf("expected name 'myagent', got %q", pkg.Manifest.Name)
	}
}

func TestValidateAgentReferencesUnreadable(t *testing.T) {
	// Direct call with a path that doesn't exist — covers the os.ReadFile
	// error branch which Pack itself cannot reach (Pack stat-checks first).
	err := validateAgentReferences("/nonexistent/Agentfile")
	if err == nil {
		t.Fatal("expected read error for nonexistent Agentfile")
	}
	if !strings.Contains(err.Error(), "failed to read") {
		t.Errorf("expected read-error message, got: %v", err)
	}
}
