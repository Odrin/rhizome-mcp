package inventory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	writerDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	defer writerDB.Close()
	if _, err := writerDB.Exec(`
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (15, 'project_origin', 'deadbeef', '2026-01-01T00:00:00Z');
		CREATE TABLE projects (id TEXT PRIMARY KEY CHECK (length(id) = 26), name TEXT, origin TEXT, next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1), created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FAV', 'demo', '/tmp/repo', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);
	`); err != nil {
		t.Fatalf("create base database: %v", err)
	}
	modDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open modify db: %v", err)
	}
	if _, err := modDB.Exec(`
		INSERT INTO issues(id, title, created_at, updated_at, archived_at)
		VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FB0', 'wal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);
	`); err != nil {
		t.Fatalf("insert WAL row: %v", err)
	}
	if err := modDB.Close(); err != nil {
		t.Fatalf("close modify db: %v", err)
	}
	beforeNames, beforeFiles := snapshotDatabaseFiles(t, path)
	readOnlyDB, err := openReadOnlyDB(path)
	if err != nil {
		t.Fatalf("openReadOnlyDB() error = %v", err)
	}
	defer readOnlyDB.Close()
	var issueCount int
	if err := readOnlyDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issues`).Scan(&issueCount); err != nil {
		t.Fatalf("query WAL-visible issue count: %v", err)
	}
	if issueCount != 1 {
		t.Fatalf("readOnlyDB issue count = %d, want 1 (WAL committed row visible)", issueCount)
	}
	afterNames, afterFiles := snapshotDatabaseFiles(t, path)
	if !reflect.DeepEqual(beforeNames, afterNames) || !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("read-only inspection modified database artifacts: before=%v after=%v", beforeFiles, afterFiles)
	}
}

func TestListProjectsRejectsRootReadFailure(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
		t.Fatalf("mkdir projects root: %v", err)
	}
	if err := os.Chmod(projectsRoot, 0); err != nil {
		t.Fatalf("chmod projects root: %v", err)
	}
	defer os.Chmod(projectsRoot, 0o700)

	_, err := List(root, projectconfig.PathInputs{GOOS: "linux", HomeDir: root, XDGDataHome: root})
	if err == nil {
		t.Fatal("List() expected error when projects dir is unreadable")
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

func snapshotDatabaseFiles(t *testing.T, path string) ([]string, map[string]int64) {
	t.Helper()
	files := map[string]int64{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			files[candidate] = info.Size()
		}
	}
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys, files
}
