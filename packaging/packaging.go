// Package packaging creates, verifies, and installs signed agent packages.
//
// A package is a .agent file — a zip container holding manifest.json,
// content.tar.gz, and optionally a signature. The manifest describes the
// agent's public API; the content holds the Agentfile, policy, prompts,
// and skills.
//
//	pub, priv, _ := keys.New()
//	keys.Save("key.pem", priv)
//	keys.Save("key.pub", pub)
//
//	pkg, _ := packaging.Pack("./my-agent", "my-agent-1.0.0.agent", priv, packaging.Metadata{
//	    Author: &packaging.Author{Name: "Vinay", Email: "v@example.com"},
//	})
//
//	loaded, _ := packaging.FromFile("my-agent-1.0.0.agent")
//	packaging.Verify(loaded, pub)
//
//	packaging.Install("my-agent-1.0.0.agent", "", pub, packaging.Mode{})
package packaging

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	manifestFile  = "manifest.json"
	contentFile   = "content.tar.gz"
	signatureFile = "signature"
	formatVersion = 1
)

// Package is a loaded agent package.
type Package struct {
	Manifest  *Manifest
	Content   []byte // tar.gz of agent files
	Signature []byte // raw Ed25519 signature (64 bytes)
	Path      string
}

// Metadata carries package metadata for Pack: author identity, free-text
// description, and license. All fields are optional. Author.KeyFingerprint
// is computed by Pack when a private key is provided and need not be set.
type Metadata struct {
	Author      *Author
	Description string
	License     string
}

// Pack creates an agent package from a directory. Passing nil for privateKey
// produces an unsigned package; passing "" for outputPath skips writing to
// disk and returns only the in-memory *Package.
func Pack(sourceDir, outputPath string, privateKey ed25519.PrivateKey, meta Metadata) (*Package, error) {
	agentfilePath := filepath.Join(sourceDir, "Agentfile")
	if _, err := os.Stat(agentfilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Agentfile not found in %s", sourceDir)
	}

	if err := validateAgentReferences(agentfilePath); err != nil {
		return nil, err
	}

	manifest, err := loadOrCreateManifest(sourceDir, meta)
	if err != nil {
		return nil, err
	}

	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	if privateKey != nil && manifest.Author != nil {
		pubKey := privateKey.Public().(ed25519.PublicKey)
		fingerprint := sha256.Sum256(pubKey)
		manifest.Author.KeyFingerprint = hex.EncodeToString(fingerprint[:8])
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize manifest: %w", err)
	}

	content, err := createContentArchive(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create content archive: %w", err)
	}

	pkg := &Package{
		Manifest: manifest,
		Content:  content,
	}

	if privateKey != nil {
		manifestHash := sha256.Sum256(manifestJSON)
		contentHash := sha256.Sum256(content)

		toSign := append(manifestHash[:], contentHash[:]...)
		pkg.Signature = ed25519.Sign(privateKey, toSign)
	}

	if outputPath != "" {
		if err := writePackage(outputPath, manifestJSON, pkg); err != nil {
			return nil, fmt.Errorf("failed to write package: %w", err)
		}
		pkg.Path = outputPath
	}

	return pkg, nil
}

// FromFile loads a package from a .agent file.
func FromFile(path string) (*Package, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open package: %w", err)
	}
	defer zr.Close()

	pkg := &Package{Path: path}

	for _, f := range zr.File {
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", f.Name, err)
		}

		switch f.Name {
		case manifestFile:
			pkg.Manifest = &Manifest{}
			if err := json.Unmarshal(data, pkg.Manifest); err != nil {
				return nil, fmt.Errorf("invalid manifest: %w", err)
			}
		case contentFile:
			pkg.Content = data
		case signatureFile:
			pkg.Signature = data
		}
	}

	if pkg.Manifest == nil {
		return nil, fmt.Errorf("package missing manifest.json")
	}
	if pkg.Content == nil {
		return nil, fmt.Errorf("package missing content.tar.gz")
	}

	return pkg, nil
}

// Verify checks a package's signature. If publicKey is nil, verification
// is skipped and no error is returned.
func Verify(pkg *Package, publicKey ed25519.PublicKey) error {
	manifestJSON, err := json.MarshalIndent(pkg.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize manifest: %w", err)
	}

	if publicKey == nil {
		return nil
	}

	if pkg.Signature == nil {
		return fmt.Errorf("package is not signed")
	}

	manifestHash := sha256.Sum256(manifestJSON)
	contentHash := sha256.Sum256(pkg.Content)

	toVerify := append(manifestHash[:], contentHash[:]...)
	if !ed25519.Verify(publicKey, toVerify, pkg.Signature) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// Mode toggles install behavior.
type Mode struct {
	NoDeps bool // skip dependency listing in the result
	DryRun bool // verify and resolve but write no files
}

// InstallResult reports what was installed.
type InstallResult struct {
	Installed    []string
	Dependencies []string
	InstallPath  string
}

// Install installs a package. Passing nil for publicKey skips signature
// verification. Passing "" for targetDir defaults to ~/.agent/packages.
// When mode.DryRun is true, no files are written — only the result
// metadata is returned.
func Install(packagePath, targetDir string, publicKey ed25519.PublicKey, mode Mode) (*InstallResult, error) {
	pkg, err := FromFile(packagePath)
	if err != nil {
		return nil, err
	}

	if err := Verify(pkg, publicKey); err != nil {
		return nil, fmt.Errorf("verification failed: %w", err)
	}

	result := &InstallResult{
		Installed: []string{pkg.Manifest.Name},
	}

	if pkg.Manifest.Dependencies != nil && !mode.NoDeps {
		for dep := range pkg.Manifest.Dependencies {
			result.Dependencies = append(result.Dependencies, dep)
		}
	}

	if mode.DryRun {
		return result, nil
	}

	if targetDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("targetDir not provided and home directory unavailable: %w", err)
		}
		targetDir = filepath.Join(home, ".agent", "packages")
	}

	pkgDir := filepath.Join(targetDir, pkg.Manifest.Name, pkg.Manifest.Version)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create package directory: %w", err)
	}

	if err := extractContent(pkg.Content, pkgDir); err != nil {
		return nil, fmt.Errorf("failed to extract content: %w", err)
	}

	result.InstallPath = pkgDir
	return result, nil
}

// ExtractToTemp extracts package contents to a temporary directory.
func (p *Package) ExtractToTemp() (string, error) {
	tmpDir, err := os.MkdirTemp("", "agent-"+p.Manifest.Name+"-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := extractContent(p.Content, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to extract content: %w", err)
	}

	return tmpDir, nil
}

// GetFile reads a specific file from the package content.
func (p *Package) GetFile(name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(p.Content))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == name || "./"+hdr.Name == name || hdr.Name == "./"+name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("file not found in package: %s", name)
}

// GetAgentfile returns the Agentfile content from the package.
func (p *Package) GetAgentfile() ([]byte, error) {
	return p.GetFile("Agentfile")
}

// GetConfig returns the agent.toml config from the package.
func (p *Package) GetConfig() ([]byte, error) {
	return p.GetFile("agent.toml")
}

// GetPolicy returns the policy.toml from the package.
func (p *Package) GetPolicy() ([]byte, error) {
	return p.GetFile("policy.toml")
}

// Manifest represents the public API of an agent package.
type Manifest struct {
	Format       int               `json:"format"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description,omitempty"`
	Author       *Author           `json:"author,omitempty"`
	License      string            `json:"license,omitempty"`
	Inputs       map[string]Input  `json:"inputs,omitempty"`
	Outputs      map[string]Output `json:"outputs,omitempty"`
	Requires     *Requirements     `json:"requires,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	CreatedAt    string            `json:"created_at"`
}

// Author is the package author.
type Author struct {
	Name           string `json:"name,omitempty"`
	Email          string `json:"email,omitempty"`
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
}

// Input is a declared input parameter.
type Input struct {
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Output is a declared output.
type Output struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Requirements declares the runtime capabilities this agent needs.
type Requirements struct {
	Profiles []string `json:"profiles,omitempty"`
	Tools    []string `json:"tools,omitempty"`
}

// ----- archive helpers --------------------------------------------------------

func createContentArchive(sourceDir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	var files []string
	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		name := info.Name()
		if name == signatureFile {
			return nil
		}
		if strings.HasPrefix(name, ".") && name != "." {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if relPath != "." {
			files = append(files, relPath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)

	for _, relPath := range files {
		fullPath := filepath.Join(sourceDir, relPath)
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, err
		}
		header.Name = relPath
		header.ModTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""

		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}

		if !info.IsDir() {
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return nil, err
			}
			if _, err := tw.Write(data); err != nil {
				return nil, err
			}
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func extractContent(content []byte, targetDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, header.Name)

		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)) {
			return fmt.Errorf("invalid path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			f, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}

	return nil
}

func writePackage(path string, manifestJSON []byte, pkg *Package) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)

	if err := writeZipFileStored(zw, manifestFile, manifestJSON); err != nil {
		return err
	}

	if err := writeZipFileStored(zw, contentFile, pkg.Content); err != nil {
		return err
	}

	if pkg.Signature != nil {
		if err := writeZipFileStored(zw, signatureFile, pkg.Signature); err != nil {
			return err
		}
	}

	return zw.Close()
}

func writeZipFileStored(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Store,
	}
	header.Modified = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ----- manifest helpers -------------------------------------------------------

func loadOrCreateManifest(sourceDir string, meta Metadata) (*Manifest, error) {
	manifestPath := filepath.Join(sourceDir, manifestFile)

	var manifest *Manifest

	if data, err := os.ReadFile(manifestPath); err == nil {
		manifest = &Manifest{}
		if err := json.Unmarshal(data, manifest); err != nil {
			return nil, fmt.Errorf("invalid manifest.json: %w", err)
		}
		if manifest.Name == "" {
			if name, err := extractNameFromAgentfile(sourceDir); err == nil {
				manifest.Name = name
			}
		}
	} else {
		manifest = &Manifest{
			Inputs:  make(map[string]Input),
			Outputs: make(map[string]Output),
		}
		name, err := extractNameFromAgentfile(sourceDir)
		if err != nil {
			return nil, err
		}
		manifest.Name = name
		manifest.Version = "0.0.0"
	}

	manifest.Format = formatVersion

	if meta.Author != nil {
		manifest.Author = meta.Author
	}
	if meta.Description != "" {
		manifest.Description = meta.Description
	}
	if meta.License != "" {
		manifest.License = meta.License
	}

	return manifest, nil
}

func extractNameFromAgentfile(sourceDir string) (string, error) {
	agentfilePath := filepath.Join(sourceDir, "Agentfile")
	content, err := os.ReadFile(agentfilePath)
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		if fields[0] == "NAME" {
			return fields[1], nil
		}
	}

	return "", fmt.Errorf("Agentfile missing NAME")
}

func validateAgentReferences(agentfilePath string) error {
	content, err := os.ReadFile(agentfilePath)
	if err != nil {
		return fmt.Errorf("failed to read Agentfile: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		if fields[0] == "AGENT" {
			for i, f := range fields {
				if f == "FROM" && i+1 < len(fields) {
					path := fields[i+1]
					path = strings.Trim(path, "\"'")

					if strings.HasSuffix(path, ".agent") {
						return fmt.Errorf(
							"line %d: AGENT cannot reference .agent packages (%s). "+
								"Packages must be self-contained. Use manifest.json dependencies for inter-package relationships",
							lineNum+1, path,
						)
					}
				}
			}
		}
	}

	return nil
}
