package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportArtifactStoreRoundTripAndPermissions(t *testing.T) {
	store, err := newExportArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.write([]byte(`{"format":"rhizome-logical-project"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifact.ArtifactURI, "rhizome-export://sha256/") || artifact.ByteCount == 0 || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact = %#v", artifact)
	}
	data, err := store.read(artifact.ArtifactURI)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"format":"rhizome-logical-project"}` {
		t.Fatalf("artifact content = %q", data)
	}
	_, name, err := parseExportArtifactURI(artifact.ArtifactURI)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestExportArtifactStoreRejectsInvalidURIsAndDigestMismatch(t *testing.T) {
	store, err := newExportArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.write([]byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	_, name, err := parseExportArtifactURI(artifact.ArtifactURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, name), []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sourceURI := range []string{"file:///etc/passwd", "rhizome-export://sha256/../../etc/passwd", artifact.ArtifactURI} {
		if _, err := store.read(sourceURI); err == nil {
			t.Fatalf("read(%q) succeeded", sourceURI)
		}
	}
}

func TestExportArtifactStoreRemovesExpiredRegularArtifacts(t *testing.T) {
	store, err := newExportArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC) }
	expired := filepath.Join(store.directory, "export-0123456789abcdef0123456789abcdef.json")
	if err := os.WriteFile(expired, []byte("expired"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := store.now().Add(-exportArtifactRetention - time.Second)
	if err := os.Chtimes(expired, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write([]byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired artifact still exists: %v", err)
	}
}

func TestNewExportArtifactStoreRejectsSymlinkDirectory(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "exports")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newExportArtifactStore(link); err == nil {
		t.Fatal("newExportArtifactStore accepted a symlinked directory")
	}
}
