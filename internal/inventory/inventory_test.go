package inventory

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"rhizome-mcp/internal/projectconfig"
)

func TestListProjectsEmptyAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	result, err := List(dataRoot, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("List() items = %#v, want none", result.Items)
	}
	if _, statErr := os.Stat(dataRoot); !os.IsNotExist(statErr) {
		t.Fatalf("List() created data root unexpectedly: stat err = %v", statErr)
	}
}

func TestListProjectsIncludesHealthyAndBadSiblings(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
		t.Fatalf("mkdir projects root: %v", err)
	}

	goodID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	goodDir := filepath.Join(projectsRoot, goodID)
	if err := os.Mkdir(goodDir, 0o700); err != nil {
		t.Fatalf("mkdir good project: %v", err)
	}
	createProjectDB(t, filepath.Join(goodDir, "tasks.db"), goodID, "demo", "/tmp/repo")

	badDir := filepath.Join(projectsRoot, "not-a-valid-id")
	if err := os.Mkdir(badDir, 0o700); err != nil {
		t.Fatalf("mkdir bad project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "tasks.db"), []byte("bad"), 0o600); err != nil {
		t.Fatalf("write bad database: %v", err)
	}

	result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("List() item count = %d, want 2", len(result.Items))
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		seen[item.ProjectID] = true
		if item.ProjectID == goodID {
			if item.Status != "ok" {
				t.Fatalf("healthy item status = %q, want %q", item.Status, "ok")
			}
			if item.IssueCount == nil || *item.IssueCount != 1 {
				t.Fatalf("healthy issue count = %#v, want 1", item.IssueCount)
			}
			if item.Origin == nil || *item.Origin != "/tmp/repo" {
				t.Fatalf("healthy origin = %#v, want %q", item.Origin, "/tmp/repo")
			}
		}
		if item.ProjectID == "not-a-valid-id" && len(item.Diagnostics) == 0 {
			t.Fatal("invalid sibling should report diagnostics")
		}
	}
	if !seen[goodID] || !seen["not-a-valid-id"] {
		t.Fatalf("unexpected projects list = %#v", result.Items)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ProjectID < result.Items[j].ProjectID })
	if result.Items[0].ProjectID != goodID || result.Items[1].ProjectID != "not-a-valid-id" {
		t.Fatalf("project ordering = %#v, want deterministic sort", result.Items)
	}
}

func TestListProjectsReportsStoredProjectIDMismatch(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	directoryID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	storedID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	projectDir := filepath.Join(projectsRoot, directoryID)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	createProjectDB(t, filepath.Join(projectDir, "tasks.db"), storedID, "mismatched", "")

	result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Status != "mismatched" {
		t.Fatalf("List() result = %#v, want one mismatched item", result.Items)
	}
	if len(result.Items[0].Diagnostics) != 1 || result.Items[0].Diagnostics[0].Code != "mismatched" {
		t.Fatalf("mismatch diagnostics = %#v", result.Items[0].Diagnostics)
	}
}

func TestListProjectsRejectsFutureSchemaVersion(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	projectID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	projectDir := filepath.Join(projectsRoot, projectID)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	createProjectDBSchemaVersion(t, filepath.Join(projectDir, "tasks.db"), projectID, "demo", "/tmp/repo", 999)

	result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List() item count = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Status != "unsupported" {
		t.Fatalf("List() status = %q, want %q", item.Status, "unsupported")
	}
	if len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "unsupported" {
		t.Fatalf("unsupported diagnostics = %#v", item.Diagnostics)
	}
}

func TestListProjectsRejectsInvalidSchemaHistory(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{name: "empty history", prepare: createProjectDBWithEmptyHistory},
		{name: "zero version", prepare: func(t *testing.T, path string) {
			createProjectDBSchemaVersion(t, path, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "demo", "/tmp/repo", 0)
		}},
		{name: "negative version", prepare: func(t *testing.T, path string) {
			createProjectDBSchemaVersion(t, path, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "demo", "/tmp/repo", -1)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			projectsRoot := filepath.Join(root, "projects")
			projectID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
			projectDir := filepath.Join(projectsRoot, projectID)
			if err := os.MkdirAll(projectDir, 0o700); err != nil {
				t.Fatalf("mkdir project: %v", err)
			}
			path := filepath.Join(projectDir, "tasks.db")
			tc.prepare(t, path)

			result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(result.Items) != 1 {
				t.Fatalf("List() item count = %d, want 1", len(result.Items))
			}
			item := result.Items[0]
			if item.Status == "ok" {
				t.Fatalf("invalid history should not be OK: %#v", item)
			}
			if len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "unsupported" {
				t.Fatalf("invalid history diagnostics = %#v", item.Diagnostics)
			}
			if item.ID != nil || item.IssueCount != nil {
				t.Fatalf("invalid history should not interpret project/issue tables: %#v", item)
			}
		})
	}
}

func TestListProjectsRejectsMultipleProjectRows(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	projectID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	projectDir := filepath.Join(projectsRoot, projectID)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	createProjectDBWithRows(t, filepath.Join(projectDir, "tasks.db"), projectID, "demo", "/tmp/repo", []projectRow{
		{id: projectID, name: "demo", origin: "/tmp/repo"},
		{id: "01ARZ3NDEKTSV4RRFFQ69G5FAW", name: "other", origin: "/tmp/repo2"},
	})

	result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List() item count = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Status != "corrupt" {
		t.Fatalf("List() status = %q, want %q", item.Status, "corrupt")
	}
	if len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "corrupt" {
		t.Fatalf("corrupt diagnostics = %#v", item.Diagnostics)
	}
}

func TestListProjectsSupportsLegacySchemaWithoutOrigin(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	projectID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	projectDir := filepath.Join(projectsRoot, projectID)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	createLegacyProjectDB(t, filepath.Join(projectDir, "tasks.db"), projectID, "demo")

	result, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("List() item count = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Status != "ok" {
		t.Fatalf("List() status = %q, want %q", item.Status, "ok")
	}
	if item.Origin != nil {
		t.Fatalf("legacy origin = %#v, want nil", item.Origin)
	}
	if item.ID == nil || *item.ID != projectID {
		t.Fatalf("legacy ID = %#v, want %q", item.ID, projectID)
	}
}

func TestOpenReadOnlyDBSeesWALVisibilityWithoutMutatingArtifacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tasks.db")
	if err := spawnCrashLeftWALWriter(path); err != nil {
		t.Fatalf("spawn crash-left WAL writer: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("writer did not leave tasks.db-wal behind: %v", err)
	}
	if _, err := os.Stat(path + "-shm"); err != nil {
		t.Fatalf("writer did not leave tasks.db-shm behind: %v", err)
	}
	before := snapshotDatabaseArtifacts(t, path)

	readOnlyDB, err := openReadOnlyDB(path)
	if err != nil {
		t.Fatalf("openReadOnlyDB() error = %v", err)
	}
	var issueCount int
	if err := readOnlyDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issues`).Scan(&issueCount); err != nil {
		t.Fatalf("query WAL-visible issue count: %v", err)
	}
	if issueCount != 1 {
		t.Fatalf("readOnlyDB issue count = %d, want 1 (WAL committed row visible)", issueCount)
	}
	if err := readOnlyDB.Close(); err != nil {
		t.Fatalf("close read-only inspector: %v", err)
	}

	after := snapshotDatabaseArtifacts(t, path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only inspection modified database artifacts: before=%v after=%v", databaseArtifactSummary(before), databaseArtifactSummary(after))
	}
}

func TestListProjectsRejectsRootReadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny directory reads on Windows")
	}
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
		t.Fatalf("mkdir projects root: %v", err)
	}
	if err := os.Chmod(projectsRoot, 0); err != nil {
		t.Fatalf("chmod projects root: %v", err)
	}
	defer os.Chmod(projectsRoot, 0o700)

	if _, err := os.ReadDir(projectsRoot); err == nil {
		t.Skip("host can read directories despite mode 000")
	} else if !os.IsPermission(err) {
		t.Fatalf("probe unreadable projects directory: %v", err)
	}

	_, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err == nil {
		t.Fatal("List() expected error when projects dir is unreadable")
	}
}

func TestListProjectsRejectsNonDirectoryRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "projects"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create projects file: %v", err)
	}
	_, err := List(root, projectconfig.PathInputs{})
	if err == nil || !strings.Contains(err.Error(), "projects path is not a directory") {
		t.Fatalf("List() error = %v, want non-directory projects path error", err)
	}
}

func createProjectDBWithEmptyHistory(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, origin TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL) STRICT;`); err != nil {
		t.Fatalf("create projects table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at) VALUES (?, ?, ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "demo", "/tmp/repo"); err != nil {
		t.Fatalf("insert project row: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);`); err != nil {
		t.Fatalf("create issues table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES (?, 'demo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);`, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil {
		t.Fatalf("insert issue row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database missing after creation: %v", err)
	}
}

func createProjectDB(t *testing.T, path, projectID, projectName, origin string) {
	t.Helper()
	createProjectDBSchemaVersion(t, path, projectID, projectName, origin, 15)
}

func createProjectDBSchemaVersion(t *testing.T, path, projectID, projectName, origin string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, 'project_origin', 'deadbeef', '2026-01-01T00:00:00Z');`, version); err != nil {
		t.Fatalf("insert schema version: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, origin TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL) STRICT;`); err != nil {
		t.Fatalf("create projects table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at) VALUES (?, ?, ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`, projectID, projectName, origin); err != nil {
		t.Fatalf("insert project row: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);`); err != nil {
		t.Fatalf("create issues table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES (?, 'demo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);`, projectID); err != nil {
		t.Fatalf("insert issue row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database missing after creation: %v", err)
	}
	if strings.Contains(path, "not-a-valid-id") {
		fmt.Println("debug")
	}
}

func createProjectDBWithRows(t *testing.T, path, projectID, projectName, origin string, rows []projectRow) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (15, 'project_origin', 'deadbeef', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("insert schema version: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, origin TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL) STRICT;`); err != nil {
		t.Fatalf("create projects table: %v", err)
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at) VALUES (?, ?, ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`, row.id, row.name, row.origin); err != nil {
			t.Fatalf("insert extra project row: %v", err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);`); err != nil {
		t.Fatalf("create issues table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES (?, 'demo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);`, projectID); err != nil {
		t.Fatalf("insert issue row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
}

type projectRow struct {
	id     string
	name   string
	origin string
}

func createLegacyProjectDB(t *testing.T, path, projectID, projectName string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (14, 'search_index_gates', 'deadbeef', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("insert schema version: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL) STRICT;`); err != nil {
		t.Fatalf("create projects table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id, name, next_issue_number, created_at, updated_at) VALUES (?, ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`, projectID, projectName); err != nil {
		t.Fatalf("insert project row: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);`); err != nil {
		t.Fatalf("create issues table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES (?, 'demo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);`, projectID); err != nil {
		t.Fatalf("insert issue row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
}

func spawnCrashLeftWALWriter(path string) error {
	cmd := exec.Command(os.Args[0], "-test.run=TestCrashWriterLeavesWALArtifacts")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "RHIZOME_TEST_DB_PATH="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("writer subprocess failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func TestCrashWriterLeavesWALArtifacts(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	path := os.Getenv("RHIZOME_TEST_DB_PATH")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		panic(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (15, 'project_origin', 'deadbeef', '2026-01-01T00:00:00Z');
		CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, origin TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', 'demo', '/tmp/repo', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);
		INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FB0', 'wal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);
	`); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func snapshotDatabaseArtifacts(t *testing.T, path string) map[string]databaseArtifact {
	t.Helper()
	artifacts := map[string]databaseArtifact{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				artifacts[candidate] = databaseArtifact{Exists: false}
				continue
			}
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if !info.Mode().IsRegular() {
			artifacts[candidate] = databaseArtifact{Exists: false}
			continue
		}
		bytes, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatalf("read %s: %v", candidate, err)
		}
		artifacts[candidate] = databaseArtifact{Exists: true, Bytes: append([]byte(nil), bytes...)}
	}
	return artifacts
}

type databaseArtifact struct {
	Exists bool
	Bytes  []byte
}

func databaseArtifactSummary(artifacts map[string]databaseArtifact) map[string]string {
	summary := make(map[string]string, len(artifacts))
	for path, artifact := range artifacts {
		if !artifact.Exists {
			summary[path] = "absent"
			continue
		}
		summary[path] = fmt.Sprintf("present:%x", sha256.Sum256(artifact.Bytes))
	}
	return summary
}
