package keys

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewKeyPair(t *testing.T) {
	pub, priv, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if pub == nil || priv == nil {
		t.Fatal("New returned nil key")
	}

	msg := []byte("hello")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("generated keys don't work together")
	}
}

func TestSaveAndPrivateRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.pem")

	_, priv, _ := New()
	if err := Save(path, priv); err != nil {
		t.Fatal(err)
	}

	loaded, err := Private(path)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("roundtrip")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(loaded.Public().(ed25519.PublicKey), msg, sig) {
		t.Error("roundtripped private key doesn't match")
	}
}

func TestSaveAndPublicRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.pub")

	pub, _, _ := New()
	if err := Save(path, pub); err != nil {
		t.Fatal(err)
	}

	loaded, err := Public(path)
	if err != nil {
		t.Fatal(err)
	}

	if !pub.Equal(loaded) {
		t.Error("roundtripped public key doesn't match")
	}
}

func TestSaveRejectsUnsupportedType(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "wrong.pem")

	err := Save(path, "not a key")
	if err == nil {
		t.Error("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "unsupported key type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPrivateFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.pem")

	_, priv, _ := New()
	if err := Save(path, priv); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected mode 0600, got %04o", perm)
	}
}

func TestPublicFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.pub")

	pub, _, _ := New()
	if err := Save(path, pub); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	perm := info.Mode().Perm()
	if perm != 0644 {
		t.Errorf("expected mode 0644, got %04o", perm)
	}
}

func TestPrivateMissingFile(t *testing.T) {
	_, err := Private("/nonexistent/key.pem")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestPrivateBadPEM(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.pem")
	os.WriteFile(path, []byte("not pem"), 0644)

	_, err := Private(path)
	if err == nil {
		t.Error("expected error for bad PEM")
	}
}

func TestPrivateWrongType(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "wrong.pem")
	pub, _, _ := New()
	Save(path, pub)

	_, err := Private(path)
	if err == nil {
		t.Error("expected error when loading public key as private")
	}
}

func TestPublicMissingFile(t *testing.T) {
	_, err := Public("/nonexistent/key.pub")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestPublicBadPEM(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.pub")
	os.WriteFile(path, []byte("not pem"), 0644)

	_, err := Public(path)
	if err == nil {
		t.Error("expected error for bad PEM")
	}
}

func TestPublicWrongType(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "wrong.pub")
	_, priv, _ := New()
	Save(path, priv)

	_, err := Public(path)
	if err == nil {
		t.Error("expected error when loading private key as public")
	}
}
