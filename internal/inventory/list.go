package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rhizome-mcp/internal/projectconfig"
)

// Result is the read-only project inventory for a data root.
type Result struct {
	Items []Entry `json:"items"`
}

// Entry is a single inspected project directory entry.
type Entry struct {
	ProjectID     string       `json:"project_id"`
	ID            *string      `json:"id,omitempty"`
	Name          *string      `json:"name,omitempty"`
	Origin        *string      `json:"origin,omitempty"`
	IssueCount    *int64       `json:"issue_count,omitempty"`
	SchemaVersion *int         `json:"schema_version,omitempty"`
	Size          *int64       `json:"size,omitempty"`
	Status        string       `json:"status"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Diagnostic captures a per-entry inspection issue.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// List inspects the on-disk project inventory without creating or mutating state.
func List(dataRoot string, pathInputs projectconfig.PathInputs) (Result, error) {
	if dataRoot == "" {
		resolved, err := projectconfig.ResolveDataRoot(pathInputs)
		if err != nil {
			return Result{}, err
		}
		dataRoot = resolved
	}
	projectsDir := filepath.Join(dataRoot, "projects")
	info, err := os.Stat(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("cannot inspect projects directory: %w", err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("projects path is not a directory: %s", projectsDir)
	}
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return Result{}, fmt.Errorf("cannot read projects directory: %w", err)
	}
	items := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		items = append(items, inspectEntry(projectsDir, entry.Name()))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ProjectID < items[j].ProjectID })
	return Result{Items: items}, nil
}

func inspectEntry(projectsDir, name string) Entry {
	entry := Entry{ProjectID: name, Status: "ok"}
	projectDir := filepath.Join(projectsDir, name)
	if entryInfo, err := os.Lstat(projectDir); err != nil {
		return withDiagnostic(entry, "disappeared", "project entry is missing", "path")
	} else if entryInfo.Mode()&os.ModeSymlink != 0 {
		return withDiagnostic(entry, "unsafe", "project entry is a symlink", "path")
	} else if !entryInfo.IsDir() {
		return withDiagnostic(entry, "unsafe", "project entry is not a directory", "path")
	}
	if !looksLikeProjectID(name) {
		return withDiagnostic(entry, "unsafe", "project directory name is not a canonical project ID", "project_id")
	}
	databasePath := filepath.Join(projectDir, "tasks.db")
	if info, err := os.Lstat(databasePath); err != nil {
		return withDiagnostic(entry, "disappeared", "tasks.db is missing", "tasks.db")
	} else if info.Mode()&os.ModeSymlink != 0 {
		return withDiagnostic(entry, "unsafe", "tasks.db must not be a symlink", "tasks.db")
	} else if !info.Mode().IsRegular() {
		return withDiagnostic(entry, "unsafe", "tasks.db is not a regular file", "tasks.db")
	}
	var diagnostics []Diagnostic
	item, ok := inspectDatabase(projectDir, databasePath)
	if !ok {
		entry = item
		return entry
	}
	entry.ID = item.ID
	entry.Name = item.Name
	entry.Origin = item.Origin
	entry.IssueCount = item.IssueCount
	entry.SchemaVersion = item.SchemaVersion
	entry.Size = item.Size
	entry.Status = item.Status
	entry.Diagnostics = diagnostics
	return entry
}

func inspectDatabase(projectDir, databasePath string) (Entry, bool) {
	entry := Entry{ProjectID: filepath.Base(projectDir), Status: "ok"}
	db, err := openReadOnlyDB(databasePath)
	if err != nil {
		return withDiagnostic(entry, "locked", fmt.Sprintf("cannot open database read-only: %v", err), "tasks.db"), false
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return withDiagnostic(entry, "locked", fmt.Sprintf("database is unavailable: %v", err), "tasks.db"), false
	}
	var id, name, origin sql.NullString
	var issueCount, schemaVersion sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `SELECT id, name, origin FROM projects ORDER BY id LIMIT 2`).Scan(&id, &name, &origin); err != nil {
		if strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "does not exist") {
			if err := db.QueryRowContext(context.Background(), `SELECT id, name FROM projects ORDER BY id LIMIT 2`).Scan(&id, &name); err != nil {
				return withDiagnostic(entry, "corrupt", fmt.Sprintf("project row is unreadable: %v", err), "projects"), false
			}
		} else {
			return withDiagnostic(entry, "corrupt", fmt.Sprintf("project row is unreadable: %v", err), "projects"), false
		}
	}
	if !id.Valid || id.String == "" {
		return withDiagnostic(entry, "corrupt", "stored project id is missing", "projects.id"), false
	}
	entry.ID = ptrString(id.String)
	if name.Valid {
		entry.Name = ptrString(name.String)
	}
	if origin.Valid && origin.String != "" {
		entry.Origin = ptrString(origin.String)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM issues`).Scan(&issueCount); err != nil {
		return withDiagnostic(entry, "corrupt", fmt.Sprintf("issue count is unreadable: %v", err), "issues"), false
	}
	entry.IssueCount = ptrInt64(issueCount.Int64)
	if err := db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return withDiagnostic(entry, "unsupported", fmt.Sprintf("schema version is unreadable: %v", err), "schema_migrations"), false
	}
	entry.SchemaVersion = ptrInt(int(schemaVersion.Int64))
	entry.Size = ptrInt64(sumDatabaseSizes(databasePath))
	if id.String != entry.ProjectID {
		return withDiagnostic(entry, "mismatched", "project directory name does not match stored project id", "project_id"), false
	}
	if !looksLikeProjectID(id.String) {
		return withDiagnostic(entry, "mismatched", "stored project id is not canonical", "projects.id"), false
	}
	return entry, true
}

func openReadOnlyDB(path string) (*sql.DB, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absPath)
	if len(uriPath) >= 2 && uriPath[1] == ':' && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{}
	query.Add("_pragma", "mode=ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "trusted_schema(OFF)")
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func sumDatabaseSizes(path string) int64 {
	base := filepath.Clean(path)
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := base + suffix
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

func withDiagnostic(entry Entry, code, message, field string) Entry {
	entry.Status = code
	entry.Diagnostics = append(entry.Diagnostics, Diagnostic{Code: code, Message: message, Field: field})
	return entry
}

func ptrString(value string) *string { return &value }
func ptrInt64(value int64) *int64    { return &value }
func ptrInt(value int) *int          { return &value }

func looksLikeProjectID(value string) bool {
	if value == "" {
		return false
	}
	_, err := projectconfig.ProjectDatabasePath("/tmp", value)
	return err == nil
}
