package inventory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=mode=ro")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (15, 'project_origin', 'deadbeef', '2026-01-01T00:00:00Z');
		CREATE TABLE projects (
			id TEXT PRIMARY KEY CHECK (length(id) = 26),
			name TEXT,
			origin TEXT,
			next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		) STRICT;
		INSERT INTO projects(id, name, origin, next_issue_number, created_at, updated_at)
		VALUES (?, ?, ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		CREATE TABLE issues (id TEXT PRIMARY KEY CHECK (length(id)=26), title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT);
		INSERT INTO issues(id, title, created_at, updated_at, archived_at) VALUES (?, 'demo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', NULL);
	`, projectID, projectName, origin, projectID); err != nil {
		t.Fatalf("create project db: %v", err)
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
