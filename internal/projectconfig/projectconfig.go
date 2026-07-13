// Package projectconfig discovers and initializes repository identity and
// resolves per-project application-data paths without consulting process-global
// working-directory or environment state.
package projectconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
)

const (
	// IdentityFileName is the repository-local project identity file.
	IdentityFileName = ".agent-tracker.json"
	// CurrentIdentityVersion is the only identity format version supported.
	CurrentIdentityVersion = 1

	// CodeProjectNotFound means no identity file exists at or above the start path.
	CodeProjectNotFound = "PROJECT_NOT_FOUND"
	// CodeInvalidIdentity means an identity file is unsafe or has invalid contents.
	CodeInvalidIdentity = "INVALID_PROJECT_IDENTITY"
	// CodeProjectAlreadyInitialized means the repository has any entry at the identity destination.
	CodeProjectAlreadyInitialized = "PROJECT_ALREADY_INITIALIZED"
	// CodePathResolution means required application-path input is absent or invalid.
	CodePathResolution = "APP_DATA_PATH_ERROR"
	// CodeUnsupportedOS means application paths are undefined for the supplied OS.
	CodeUnsupportedOS = "UNSUPPORTED_OS"
	// CodeInitializationFailed means project initialization could not be completed atomically.
	CodeInitializationFailed = "PROJECT_INITIALIZATION_FAILED"
	// CodeDiscoveryFailed means the supplied start path or filesystem could not be inspected.
	CodeDiscoveryFailed = "PROJECT_DISCOVERY_FAILED"
)

// Identity is the complete on-disk .agent-tracker.json model.
type Identity struct {
	Version   int    `json:"version"`
	ProjectID string `json:"project_id"`
}

// Project identifies a repository and its external storage locations. DataDir
// and DatabasePath are empty for projects returned by Discover.
type Project struct {
	Root         string
	Identity     Identity
	DataDir      string
	DatabasePath string
}

// PathInputs contains all platform-dependent values needed to resolve an
// application-data root. Callers populate it from their environment adapter.
type PathInputs struct {
	GOOS         string
	HomeDir      string
	XDGDataHome  string
	LocalAppData string
}

// IDGenerator is the minimal project-ID generator dependency used by Initialize.
type IDGenerator interface {
	New() (string, error)
}

// Discover searches upward from start for a strict project identity. If start
// names a file, discovery begins in its containing directory.
func Discover(start string) (Project, error) {
	dir, err := discoveryStart(start)
	if err != nil {
		return Project{}, domain.WrapError(err, CodeDiscoveryFailed, "cannot inspect project discovery start", false)
	}

	for {
		identityPath := filepath.Join(dir, IdentityFileName)
		info, lstatErr := os.Lstat(identityPath)
		switch {
		case lstatErr == nil:
			if !info.Mode().IsRegular() {
				return Project{}, invalidIdentity(errors.New("identity path is not a regular file"))
			}
			identity, readErr := readIdentity(identityPath)
			if readErr != nil {
				return Project{}, readErr
			}
			return Project{Root: dir, Identity: identity}, nil
		case errors.Is(lstatErr, fs.ErrNotExist):
			// Continue at the parent.
		default:
			return Project{}, domain.WrapError(lstatErr, CodeDiscoveryFailed, "cannot inspect project identity", false)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return Project{}, domain.NewError(CodeProjectNotFound, "project identity not found", false)
		}
		dir = parent
	}
}

// ResolveDataRoot computes the platform application-data directory solely from
// supplied values. It does not read environment variables or the current user.
func ResolveDataRoot(input PathInputs) (string, error) {
	switch input.GOOS {
	case "darwin":
		if input.HomeDir == "" {
			return "", pathError("home directory is required on macOS")
		}
		return filepath.Join(input.HomeDir, "Library", "Application Support", "rhizome-mcp"), nil
	case "linux":
		if input.XDGDataHome != "" {
			return filepath.Join(input.XDGDataHome, "rhizome-mcp"), nil
		}
		if input.HomeDir == "" {
			return "", pathError("home directory is required when XDG_DATA_HOME is unset")
		}
		return filepath.Join(input.HomeDir, ".local", "share", "rhizome-mcp"), nil
	case "windows":
		if input.LocalAppData == "" {
			return "", pathError("LOCALAPPDATA is required on Windows")
		}
		return joinWindows(input.LocalAppData, "rhizome-mcp"), nil
	default:
		return "", domain.NewError(CodeUnsupportedOS, "unsupported operating system", false)
	}
}

// ProjectDatabasePath returns the tasks database path below dataRoot after
// validating that projectID is canonical.
func ProjectDatabasePath(dataRoot, projectID string) (string, error) {
	canonical, err := canonicalProjectID(projectID)
	if err != nil {
		return "", invalidIdentity(err)
	}
	if dataRoot == "" {
		return "", pathError("application data root is required")
	}
	if strings.Contains(dataRoot, `\`) {
		return joinWindows(dataRoot, "projects", canonical, "tasks.db"), nil
	}
	return filepath.Join(dataRoot, "projects", canonical, "tasks.db"), nil
}

// Initialize creates a new repository identity and project data directory.
// repositoryRoot and dataRoot are explicit; existing identity destinations are
// never overwritten, and failures remove only artifacts created by this call.
func Initialize(repositoryRoot string, generator IDGenerator, dataRoot string) (Project, error) {
	root, err := validateRepositoryRoot(repositoryRoot)
	if err != nil {
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "repository root must be an existing directory", false)
	}
	if generator == nil {
		return Project{}, domain.NewError(CodeInitializationFailed, "project ID generator is required", false)
	}
	if dataRoot == "" {
		return Project{}, pathError("application data root is required")
	}

	identityPath := filepath.Join(root, IdentityFileName)
	if _, err := os.Lstat(identityPath); err == nil {
		return Project{}, domain.NewError(CodeProjectAlreadyInitialized, "project identity already exists", false)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "cannot inspect identity destination", false)
	}

	generated, err := generator.New()
	if err != nil {
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "cannot generate project ID", false)
	}
	projectID, err := canonicalProjectID(generated)
	if err != nil {
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "generated project ID is invalid", false)
	}
	identity := Identity{Version: CurrentIdentityVersion, ProjectID: projectID}
	dataDir := filepath.Join(dataRoot, "projects", projectID)

	createdDirs, err := createDirectories(dataDir)
	if err != nil {
		cleanupDirectories(createdDirs)
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "cannot create project data directory", false)
	}
	if err := createIdentityAtomically(root, identity); err != nil {
		cleanupDirectories(createdDirs)
		if errors.Is(err, fs.ErrExist) {
			return Project{}, domain.WrapError(err, CodeProjectAlreadyInitialized, "project identity already exists", false)
		}
		return Project{}, domain.WrapError(err, CodeInitializationFailed, "cannot create project identity", false)
	}

	return Project{
		Root:         root,
		Identity:     identity,
		DataDir:      dataDir,
		DatabasePath: filepath.Join(dataDir, "tasks.db"),
	}, nil
}

func discoveryStart(start string) (string, error) {
	if start == "" {
		return "", errors.New("start path is required")
	}
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.Mode().IsRegular() {
		return filepath.Dir(resolved), nil
	}
	if !info.IsDir() {
		return "", errors.New("start path is neither a directory nor a regular file")
	}
	return resolved, nil
}

func readIdentity(path string) (Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return Identity{}, invalidIdentity(err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Identity{}, invalidIdentity(err)
	}
	if !info.Mode().IsRegular() {
		return Identity{}, invalidIdentity(errors.New("identity path is not a regular file"))
	}

	identity, err := decodeIdentity(file)
	if err != nil {
		return Identity{}, invalidIdentity(err)
	}
	return identity, nil
}

func decodeIdentity(reader io.Reader) (Identity, error) {
	decoder := json.NewDecoder(reader)
	opening, err := decoder.Token()
	if err != nil {
		return Identity{}, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return Identity{}, errors.New("identity must be a JSON object")
	}

	var identity Identity
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Identity{}, err
		}
		name, ok := token.(string)
		if !ok {
			return Identity{}, errors.New("identity field name must be a string")
		}
		if _, duplicate := seen[name]; duplicate {
			return Identity{}, fmt.Errorf("duplicate identity field %q", name)
		}
		seen[name] = struct{}{}

		switch name {
		case "version":
			if err := decoder.Decode(&identity.Version); err != nil {
				return Identity{}, fmt.Errorf("decode version: %w", err)
			}
		case "project_id":
			if err := decoder.Decode(&identity.ProjectID); err != nil {
				return Identity{}, fmt.Errorf("decode project_id: %w", err)
			}
		default:
			return Identity{}, fmt.Errorf("unknown identity field %q", name)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return Identity{}, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return Identity{}, errors.New("identity object is not closed")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return Identity{}, errors.New("trailing JSON value")
		}
		return Identity{}, fmt.Errorf("trailing data: %w", err)
	}

	if identity.Version != CurrentIdentityVersion {
		return Identity{}, fmt.Errorf("unsupported identity version %d", identity.Version)
	}
	canonical, err := canonicalProjectID(identity.ProjectID)
	if err != nil {
		return Identity{}, err
	}
	identity.ProjectID = canonical
	return identity, nil
}

func canonicalProjectID(value string) (string, error) {
	if value == "" {
		return "", errors.New("project_id is required")
	}
	parsed, err := ids.ParseStrict(value)
	if err != nil {
		return "", fmt.Errorf("invalid project_id: %w", err)
	}
	return parsed.String(), nil
}

func validateRepositoryRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("repository root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("repository root is not a directory")
	}
	return resolved, nil
}

func createIdentityAtomically(root string, identity Identity) error {
	contents, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')

	temporary, err := os.CreateTemp(root, ".agent-tracker.json.tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	// A hard-link installation is atomic and, unlike Rename on Unix, refuses
	// to replace any existing file, directory, or symlink at the destination.
	return os.Link(temporaryPath, filepath.Join(root, IdentityFileName))
}

func createDirectories(path string) ([]string, error) {
	missing := make([]string, 0, 3)
	cursor := filepath.Clean(path)
	for {
		info, err := os.Stat(cursor)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%s is not a directory", cursor)
			}
			if cursor == filepath.Clean(path) {
				return nil, errors.New("project data path already exists")
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, cursor)
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
	}

	created := make([]string, 0, len(missing))
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil {
			if errors.Is(err, fs.ErrExist) {
				if missing[i] == filepath.Clean(path) {
					return created, errors.New("project data path already exists")
				}
				info, statErr := os.Stat(missing[i])
				if statErr == nil && info.IsDir() {
					continue
				}
			}
			return created, err
		}
		created = append(created, missing[i])
	}
	return created, nil
}

func cleanupDirectories(created []string) {
	for i := len(created) - 1; i >= 0; i-- {
		_ = os.Remove(created[i])
	}
}

func joinWindows(base string, elements ...string) string {
	result := strings.TrimRight(strings.ReplaceAll(base, "/", `\`), `\`)
	for _, element := range elements {
		result += `\` + strings.Trim(element, `\/`)
	}
	return result
}

func invalidIdentity(cause error) error {
	return domain.WrapError(cause, CodeInvalidIdentity, "project identity is invalid", false)
}

func pathError(message string) error {
	return domain.NewError(CodePathResolution, message, false)
}
