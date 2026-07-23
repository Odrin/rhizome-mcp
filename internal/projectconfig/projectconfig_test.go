package projectconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectconfig"
)

const testProjectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type generatorFunc func() (string, error)

func (f generatorFunc) New() (string, error) { return f() }

func TestDiscoverSearchesUpwardFromDirectoryAndFile(t *testing.T) {
	tests := []struct {
		name      string
		startFile bool
	}{
		{name: "directory"},
		{name: "file", startFile: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "repository")
			child := filepath.Join(root, "one", "two")
			mustMkdirAll(t, child)
			writeIdentity(t, root, validIdentityJSON())

			start := child
			if tt.startFile {
				start = filepath.Join(child, "source.go")
				if err := os.WriteFile(start, []byte("package source\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			project, err := projectconfig.Discover(start)
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			wantRoot := resolvedPath(t, root)
			if project.Root != wantRoot {
				t.Errorf("Root = %q, want %q", project.Root, wantRoot)
			}
			if project.Identity != (projectconfig.Identity{Version: 1, ProjectID: testProjectID}) {
				t.Errorf("Identity = %#v", project.Identity)
			}
		})
	}
}

func TestDiscoverStopsAtFilesystemRoot(t *testing.T) {
	_, err := projectconfig.Discover(t.TempDir())
	assertDomainCode(t, err, projectconfig.CodeProjectNotFound)
}

func TestDiscoverRejectsStrictIdentityViolations(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "not object", content: `[]`},
		{name: "unknown field", content: `{"version":1,"project_id":"` + testProjectID + `","extra":true}`},
		{name: "duplicate version", content: `{"version":1,"version":1,"project_id":"` + testProjectID + `"}`},
		{name: "duplicate project id", content: `{"version":1,"project_id":"` + testProjectID + `","project_id":"` + testProjectID + `"}`},
		{name: "trailing json", content: validIdentityJSON() + `{}`},
		{name: "trailing data", content: validIdentityJSON() + `garbage`},
		{name: "unsupported version", content: `{"version":2,"project_id":"` + testProjectID + `"}`},
		{name: "missing version", content: `{"project_id":"` + testProjectID + `"}`},
		{name: "missing project id", content: `{"version":1}`},
		{name: "empty project id", content: `{"version":1,"project_id":""}`},
		{name: "invalid project id", content: `{"version":1,"project_id":"not-a-ulid"}`},
		{name: "noncanonical project id", content: `{"version":1,"project_id":"` + strings.ToLower(testProjectID) + `"}`},
		{name: "wrong version type", content: `{"version":"1","project_id":"` + testProjectID + `"}`},
		{name: "wrong project id type", content: `{"version":1,"project_id":1}`},
		{name: "truncated", content: `{"version":1`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeIdentity(t, root, tt.content)
			_, err := projectconfig.Discover(root)
			assertDomainCode(t, err, projectconfig.CodeInvalidIdentity)
		})
	}
}

func TestDiscoverRejectsNonRegularIdentity(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "identity-target")
				if err := os.WriteFile(target, []byte(validIdentityJSON()), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, filepath.Join(root, projectconfig.IdentityFileName))
			_, err := projectconfig.Discover(root)
			assertDomainCode(t, err, projectconfig.CodeInvalidIdentity)
		})
	}
}

func TestResolveDataRoot(t *testing.T) {
	tests := []struct {
		name  string
		input projectconfig.PathInputs
		want  string
		code  string
	}{
		{
			name:  "macOS",
			input: projectconfig.PathInputs{GOOS: "darwin", HomeDir: "/Users/tester"},
			want:  "/Users/tester/Library/Application Support/rhizome-mcp",
		},
		{
			name:  "Linux XDG",
			input: projectconfig.PathInputs{GOOS: "linux", HomeDir: "/home/ignored", XDGDataHome: "/data"},
			want:  "/data/rhizome-mcp",
		},
		{
			name:  "Linux fallback",
			input: projectconfig.PathInputs{GOOS: "linux", HomeDir: "/home/tester"},
			want:  "/home/tester/.local/share/rhizome-mcp",
		},
		{
			name:  "Linux cleans redundant separators",
			input: projectconfig.PathInputs{GOOS: "linux", XDGDataHome: "/data//shared/"},
			want:  "/data/shared/rhizome-mcp",
		},
		{
			name:  "Windows",
			input: projectconfig.PathInputs{GOOS: "windows", LocalAppData: `C:\Users\tester\AppData\Local`},
			want:  `C:\Users\tester\AppData\Local\rhizome-mcp`,
		},
		{name: "macOS missing home", input: projectconfig.PathInputs{GOOS: "darwin"}, code: projectconfig.CodePathResolution},
		{name: "Linux missing home", input: projectconfig.PathInputs{GOOS: "linux"}, code: projectconfig.CodePathResolution},
		{name: "Windows missing local app data", input: projectconfig.PathInputs{GOOS: "windows"}, code: projectconfig.CodePathResolution},
		{name: "unsupported OS", input: projectconfig.PathInputs{GOOS: "plan9", HomeDir: "/home/tester"}, code: projectconfig.CodeUnsupportedOS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectconfig.ResolveDataRoot(tt.input)
			if tt.code != "" {
				assertDomainCode(t, err, tt.code)
				return
			}
			if err != nil {
				t.Fatalf("ResolveDataRoot() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveDataRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectDatabasePath(t *testing.T) {
	tests := []struct {
		name string
		root string
		want string
	}{
		{name: "POSIX", root: "/app/rhizome-mcp", want: "/app/rhizome-mcp/projects/" + testProjectID + "/tasks.db"},
		{name: "Windows", root: `C:\Data\rhizome-mcp`, want: `C:\Data\rhizome-mcp\projects\` + testProjectID + `\tasks.db`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectconfig.ProjectDatabasePath(tt.root, testProjectID)
			if err != nil {
				t.Fatalf("ProjectDatabasePath() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ProjectDatabasePath() = %q, want %q", got, tt.want)
			}
		})
	}

	_, err := projectconfig.ProjectDatabasePath("/data", strings.ToLower(testProjectID))
	assertDomainCode(t, err, projectconfig.CodeInvalidIdentity)
	_, err = projectconfig.ProjectDatabasePath("", testProjectID)
	assertDomainCode(t, err, projectconfig.CodePathResolution)
}

func TestInitializeCreatesCanonicalIdentityAndDataDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	mustMkdirAll(t, root)
	dataRoot := filepath.Join(t.TempDir(), "application-data", "rhizome-mcp")

	project, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	wantDataDir := filepath.Join(dataRoot, "projects", testProjectID)
	if project.Root != resolvedPath(t, root) || project.Identity.ProjectID != testProjectID || project.DataDir != wantDataDir {
		t.Errorf("Initialize() project = %#v", project)
	}
	if project.DatabasePath != filepath.Join(wantDataDir, "tasks.db") {
		t.Errorf("DatabasePath = %q", project.DatabasePath)
	}

	identityPath := filepath.Join(root, projectconfig.IdentityFileName)
	contents, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	wantContents := "{\n  \"version\": 1,\n  \"project_id\": \"" + testProjectID + "\"\n}\n"
	if string(contents) != wantContents {
		t.Errorf("identity contents = %q, want %q", contents, wantContents)
	}
	assertPermissions(t, identityPath, 0o600)
	assertPermissions(t, wantDataDir, 0o700)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != projectconfig.IdentityFileName {
		t.Errorf("repository entries after initialization = %v, want only identity", entries)
	}

	discovered, err := projectconfig.Discover(root)
	if err != nil {
		t.Fatalf("Discover(initialized root) error = %v", err)
	}
	if discovered.Identity != project.Identity {
		t.Errorf("discovered identity = %#v, initialized = %#v", discovered.Identity, project.Identity)
	}
}

func TestInitializeRefusesExistingIdentityDestination(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "matching file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(validIdentityJSON()), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "outside-identity")
				if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			identityPath := filepath.Join(root, projectconfig.IdentityFileName)
			tt.setup(t, identityPath)
			dataRoot := filepath.Join(t.TempDir(), "data")
			called := false
			_, err := projectconfig.Initialize(root, generatorFunc(func() (string, error) {
				called = true
				return testProjectID, nil
			}), dataRoot)
			assertDomainCode(t, err, projectconfig.CodeProjectAlreadyInitialized)
			if called {
				t.Error("generator called for an existing identity destination")
			}
			if _, statErr := os.Stat(dataRoot); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("data root stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestInitializeValidatesRepositoryRoot(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{"", filepath.Join(base, "missing"), file} {
		_, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), filepath.Join(base, "data"))
		assertDomainCode(t, err, projectconfig.CodeInitializationFailed)
	}
}

func TestInitializeGeneratorFailureCreatesNothing(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")
	generatorErr := errors.New("entropy unavailable")

	_, err := projectconfig.Initialize(root, generatorFunc(func() (string, error) {
		return "", generatorErr
	}), dataRoot)
	assertDomainCode(t, err, projectconfig.CodeInitializationFailed)
	if !errors.Is(err, generatorErr) {
		t.Errorf("Initialize() error does not wrap generator error: %v", err)
	}
	assertNotExist(t, filepath.Join(root, projectconfig.IdentityFileName))
	assertNotExist(t, dataRoot)
}

func TestInitializeRejectsInvalidGeneratedID(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")

	_, err := projectconfig.Initialize(root, fixedGenerator(strings.ToLower(testProjectID)), dataRoot)
	assertDomainCode(t, err, projectconfig.CodeInitializationFailed)
	assertNotExist(t, filepath.Join(root, projectconfig.IdentityFileName))
	assertNotExist(t, dataRoot)
}

func TestInitializePreservesPreExistingProjectData(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")
	dataDir := filepath.Join(dataRoot, "projects", testProjectID)
	mustMkdirAll(t, dataDir)
	marker := filepath.Join(dataDir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
	assertDomainCode(t, err, projectconfig.CodeInitializationFailed)
	assertNotExist(t, filepath.Join(root, projectconfig.IdentityFileName))
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "keep" {
		t.Errorf("pre-existing project data = %q, %v", contents, readErr)
	}
}

func TestInitializeFailureCleansOnlyCreatedArtifacts(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")
	projectsDir := filepath.Join(dataRoot, "projects")
	mustMkdirAll(t, projectsDir)
	marker := filepath.Join(projectsDir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(root, projectconfig.IdentityFileName)

	generator := generatorFunc(func() (string, error) {
		// Simulate another initializer winning after the initial destination check.
		if err := os.Symlink(marker, identityPath); err != nil {
			return "", err
		}
		return testProjectID, nil
	})
	_, err := projectconfig.Initialize(root, generator, dataRoot)
	assertDomainCode(t, err, projectconfig.CodeProjectAlreadyInitialized)

	assertNotExist(t, filepath.Join(projectsDir, testProjectID))
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "keep" {
		t.Errorf("pre-existing marker = %q, %v", contents, readErr)
	}
	if info, lstatErr := os.Lstat(identityPath); lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("winning identity destination changed: info=%v err=%v", info, lstatErr)
	}
}

func TestInitializeRejectsDataRootInsideRepository(t *testing.T) {
	tests := []struct {
		name              string
		preCreateDataRoot bool
	}{
		{name: "existing directory", preCreateDataRoot: true},
		{name: "nonexistent path", preCreateDataRoot: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dataRoot := filepath.Join(root, "data")
			if tt.preCreateDataRoot {
				mustMkdirAll(t, dataRoot)
			}

			called := false
			_, err := projectconfig.Initialize(root, generatorFunc(func() (string, error) {
				called = true
				return testProjectID, nil
			}), dataRoot)

			assertDomainCode(t, err, domain.CodeStorageConfiguration)
			if !strings.Contains(err.Error(), "must exist outside the repository") {
				t.Errorf("error message = %q, want mention of outside repository", err.Error())
			}
			if called {
				t.Error("generator called despite an invalid data root")
			}
			assertNotExist(t, filepath.Join(root, projectconfig.IdentityFileName))

			if tt.preCreateDataRoot {
				info, statErr := os.Stat(dataRoot)
				if statErr != nil || !info.IsDir() {
					t.Fatalf("pre-existing data root changed: stat error = %v", statErr)
				}
				entries, readErr := os.ReadDir(dataRoot)
				if readErr != nil {
					t.Fatalf("read pre-existing data root: %v", readErr)
				}
				if len(entries) != 0 {
					t.Errorf("pre-existing data root gained entries: %v", entries)
				}
			} else {
				assertNotExist(t, dataRoot)
			}

			entries, err2 := os.ReadDir(root)
			if err2 != nil {
				t.Fatalf("read repository root: %v", err2)
			}
			wantEntries := 0
			if tt.preCreateDataRoot {
				wantEntries = 1 // only the pre-existing "data" directory remains
			}
			if len(entries) != wantEntries {
				t.Errorf("repository root entries = %v, want %d entries", entries, wantEntries)
			}
		})
	}
}

func TestInitializeAlreadyExistsErrorNamesPathAndDistinguishesPartialInit(t *testing.T) {
	t.Run("no database yet", func(t *testing.T) {
		root := t.TempDir()
		dataRoot := filepath.Join(t.TempDir(), "data")
		writeIdentity(t, root, validIdentityJSON())

		_, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
		assertDomainCode(t, err, projectconfig.CodeProjectAlreadyInitialized)

		identityPath := filepath.Join(root, projectconfig.IdentityFileName)
		message := err.Error()
		if !strings.Contains(message, identityPath) {
			t.Errorf("error message = %q, want identity path %q", message, identityPath)
		}
		if !strings.Contains(message, "no project database was found") {
			t.Errorf("error message = %q, want partial-init guidance", message)
		}
		if !strings.Contains(message, "delete") {
			t.Errorf("error message = %q, want deletion guidance", message)
		}
	})

	t.Run("database exists", func(t *testing.T) {
		root := t.TempDir()
		dataRoot := filepath.Join(t.TempDir(), "data")
		writeIdentity(t, root, validIdentityJSON())
		databasePath, err := projectconfig.ProjectDatabasePath(dataRoot, testProjectID)
		if err != nil {
			t.Fatalf("resolve database path: %v", err)
		}
		mustMkdirAll(t, filepath.Dir(databasePath))
		if err := os.WriteFile(databasePath, []byte("sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err = projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
		assertDomainCode(t, err, projectconfig.CodeProjectAlreadyInitialized)

		identityPath := filepath.Join(root, projectconfig.IdentityFileName)
		message := err.Error()
		if !strings.Contains(message, identityPath) {
			t.Errorf("error message = %q, want identity path %q", message, identityPath)
		}
		if !strings.Contains(message, "already initialized") {
			t.Errorf("error message = %q, want already-initialized guidance", message)
		}
	})

	t.Run("unreadable identity", func(t *testing.T) {
		root := t.TempDir()
		dataRoot := filepath.Join(t.TempDir(), "data")
		identityPath := filepath.Join(root, projectconfig.IdentityFileName)
		if err := os.Mkdir(identityPath, 0o700); err != nil {
			t.Fatal(err)
		}

		_, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
		assertDomainCode(t, err, projectconfig.CodeProjectAlreadyInitialized)

		message := err.Error()
		if !strings.Contains(message, identityPath) {
			t.Errorf("error message = %q, want identity path %q", message, identityPath)
		}
	})
}

func TestRollbackInitializeRemovesCreatedArtifacts(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(t.TempDir(), "data")

	project, err := projectconfig.Initialize(root, fixedGenerator(testProjectID), dataRoot)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if err := projectconfig.RollbackInitialize(project); err != nil {
		t.Fatalf("RollbackInitialize() error = %v", err)
	}

	assertNotExist(t, filepath.Join(root, projectconfig.IdentityFileName))
	assertNotExist(t, project.DataDir)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read repository root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("repository root entries after rollback = %v, want none", entries)
	}

	// Rollback must tolerate being invoked again against already-removed
	// artifacts without error.
	if err := projectconfig.RollbackInitialize(project); err != nil {
		t.Fatalf("RollbackInitialize() second call error = %v", err)
	}
}

func fixedGenerator(value string) projectconfig.IDGenerator {
	return generatorFunc(func() (string, error) { return value, nil })
}

func validIdentityJSON() string {
	return `{"version":1,"project_id":"` + testProjectID + `"}`
}

func writeIdentity(t *testing.T, root, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, projectconfig.IdentityFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertDomainCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %s", want)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T, want *domain.Error: %v", err, err)
	}
	if domainErr.Code != want {
		t.Fatalf("error code = %q, want %q", domainErr.Code, want)
	}
	if domainErr.Retryable {
		t.Errorf("error unexpectedly retryable: %v", err)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%q) error = %v, want not exist", path, err)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("permissions for %q = %#o, want %#o", path, got, want)
	}
}
