package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

const (
	exportArtifactScheme    = "rhizome-export"
	exportArtifactRetention = 24 * time.Hour
	maxInlineExportBytes    = 64 * 1024
)

type exportArtifactStore struct {
	directory string
	now       func() time.Time
}

func newExportArtifactStore(directory string) (*exportArtifactStore, error) {
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "rhizome-mcp", "exports")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create managed export directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect managed export directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("managed export directory must be a real directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect managed export directory: %w", err)
	}
	return &exportArtifactStore{directory: directory, now: time.Now}, nil
}

func (store *exportArtifactStore) write(data []byte) (exportArtifactOutput, error) {
	if err := store.cleanup(); err != nil {
		return exportArtifactOutput{}, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return exportArtifactOutput{}, fmt.Errorf("generate export artifact name: %w", err)
	}
	name := "export-" + hex.EncodeToString(random) + ".json"
	path := filepath.Join(store.directory, name)
	file, err := os.CreateTemp(store.directory, ".export-*.tmp")
	if err != nil {
		return exportArtifactOutput{}, fmt.Errorf("create export artifact: %w", err)
	}
	temporaryPath := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(temporaryPath)
		return exportArtifactOutput{}, fmt.Errorf("protect export artifact: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(temporaryPath)
		return exportArtifactOutput{}, fmt.Errorf("write export artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return exportArtifactOutput{}, fmt.Errorf("close export artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return exportArtifactOutput{}, fmt.Errorf("finalize export artifact: %w", err)
	}
	digest := sha256.Sum256(data)
	value := hex.EncodeToString(digest[:])
	return exportArtifactOutput{ByteCount: len(data), SHA256: value, ArtifactURI: exportArtifactURI(value, name)}, nil
}

func (store *exportArtifactStore) read(sourceURI string) ([]byte, error) {
	digest, name, err := parseExportArtifactURI(sourceURI)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(store.directory, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, invalidSourceURI("source_uri does not identify a managed export artifact")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalidSourceURI("source_uri artifact cannot be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxLogicalProjectImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read export artifact: %w", err)
	}
	if len(data) > domain.MaxLogicalProjectImportBytes {
		return nil, domain.NewError(domain.CodeLimitExceeded, "document exceeds the maximum size of 1048576 bytes", false, domain.Detail{Field: "source_uri", Code: "MAX_BYTES"})
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != digest {
		return nil, invalidSourceURI("source_uri artifact digest does not match its contents")
	}
	return data, nil
}

func (store *exportArtifactStore) cleanup() error {
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		return fmt.Errorf("read managed export directory: %w", err)
	}
	cutoff := store.now().Add(-exportArtifactRetention)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "export-") || !strings.HasSuffix(entry.Name(), ".json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(store.directory, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clean expired export artifact: %w", err)
		}
	}
	return nil
}

func exportArtifactURI(digest, name string) string {
	return exportArtifactScheme + "://sha256/" + digest + "/" + name
}

func parseExportArtifactURI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != exportArtifactScheme || parsed.Host != "sha256" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", invalidSourceURI("source_uri must be a managed export artifact URI")
	}
	parts := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || len(parts[0]) != sha256.Size*2 || !isLowerHex(parts[0]) || !isExportArtifactName(parts[1]) {
		return "", "", invalidSourceURI("source_uri must be a managed export artifact URI")
	}
	return parts[0], parts[1], nil
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func isExportArtifactName(value string) bool {
	return strings.HasPrefix(value, "export-") && strings.HasSuffix(value, ".json") && len(value) == len("export-")+32+len(".json") && isLowerHex(strings.TrimSuffix(strings.TrimPrefix(value, "export-"), ".json"))
}

func invalidSourceURI(message string) error {
	return domain.NewError(domain.CodeInvalidArgument, message, false, domain.Detail{Field: "source_uri", Code: "INVALID_EXPORT_ARTIFACT"})
}
