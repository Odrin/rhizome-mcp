package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/projectconfig"
)

// Result is the read-only project inventory for a data root.
type Result struct {
	Items []Entry `json:"items"`
}

type readOnlyDatabase struct {
	*sql.DB
	cleanup func()
}

func (database *readOnlyDatabase) Close() error {
	err := database.DB.Close()
	database.cleanup()
	return err
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
	item, ok := inspectDatabase(projectDir, databasePath)
	if !ok {
		return item
	}
	entry.ID = item.ID
	entry.Name = item.Name
	entry.Origin = item.Origin
	entry.IssueCount = item.IssueCount
	entry.SchemaVersion = item.SchemaVersion
	entry.Size = item.Size
	entry.Status = item.Status
	entry.Diagnostics = item.Diagnostics
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

	schemaVersion, err := readSchemaVersion(db.DB)
	if err != nil {
		return withDiagnostic(entry, "unsupported", fmt.Sprintf("schema version is unreadable: %v", err), "schema_migrations"), false
	}
	if schemaVersion > migrations.CurrentVersion() {
		return withDiagnostic(entry, "unsupported",
			fmt.Sprintf("database schema version %d is newer than the supported version %d", schemaVersion, migrations.CurrentVersion()),
			"schema_migrations.version"), false
	}
	entry.SchemaVersion = ptrInt(schemaVersion)

	id, name, origin, err := readProjectRow(db.DB)
	if err != nil {
		return withDiagnostic(entry, "corrupt", fmt.Sprintf("project row is unreadable: %v", err), "projects"), false
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
	var issueCount int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM issues`).Scan(&issueCount); err != nil {
		return withDiagnostic(entry, "corrupt", fmt.Sprintf("issue count is unreadable: %v", err), "issues"), false
	}
	entry.IssueCount = ptrInt64(issueCount)
	entry.Size = ptrInt64(sumDatabaseSizes(databasePath))
	if id.String != entry.ProjectID {
		return withDiagnostic(entry, "mismatched", "project directory name does not match stored project id", "project_id"), false
	}
	if !looksLikeProjectID(id.String) {
		return withDiagnostic(entry, "mismatched", "stored project id is not canonical", "projects.id"), false
	}
	return entry, true
}

func readSchemaVersion(db *sql.DB) (int, error) {
	rows, err := db.QueryContext(context.Background(), `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var maxVersion sql.NullInt64
	for rows.Next() {
		var version sql.NullInt64
		if err := rows.Scan(&version); err != nil {
			return 0, err
		}
		if !version.Valid {
			return 0, errors.New("schema history contains a null version")
		}
		if version.Int64 < 1 {
			return 0, fmt.Errorf("schema history contains invalid version %d", version.Int64)
		}
		if !maxVersion.Valid || version.Int64 > maxVersion.Int64 {
			maxVersion = version
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !maxVersion.Valid {
		return 0, errors.New("schema history is empty or missing")
	}
	if maxVersion.Int64 < 1 {
		return 0, fmt.Errorf("schema history has invalid maximum version %d", maxVersion.Int64)
	}
	return int(maxVersion.Int64), nil
}

func readProjectRow(db *sql.DB) (sql.NullString, sql.NullString, sql.NullString, error) {
	queryPatterns := []struct {
		sql        string
		withOrigin bool
	}{
		{sql: `SELECT id, name, origin FROM projects ORDER BY id`, withOrigin: true},
		{sql: `SELECT id, name FROM projects ORDER BY id`, withOrigin: false},
	}
	for _, query := range queryPatterns {
		rows, err := db.QueryContext(context.Background(), query.sql)
		if err != nil {
			if query.withOrigin && (strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "does not exist")) {
				continue
			}
			return sql.NullString{}, sql.NullString{}, sql.NullString{}, err
		}
		id, name, origin, ok, scanErr := scanProjectRows(rows, query.withOrigin)
		if scanErr != nil {
			return sql.NullString{}, sql.NullString{}, sql.NullString{}, scanErr
		}
		if ok {
			return id, name, origin, nil
		}
		if query.withOrigin {
			continue
		}
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("expected exactly one project row but found 0")
	}
	return sql.NullString{}, sql.NullString{}, sql.NullString{}, fmt.Errorf("project row is unreadable")
}

func scanProjectRows(rows *sql.Rows, withOrigin bool) (sql.NullString, sql.NullString, sql.NullString, bool, error) {
	defer rows.Close()
	var id, name, origin sql.NullString
	var count int
	for rows.Next() {
		count++
		if count > 1 {
			return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, fmt.Errorf("expected exactly one project row but found %d", count)
		}
		if withOrigin {
			if err := rows.Scan(&id, &name, &origin); err != nil {
				if strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "does not exist") {
					return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, nil
				}
				return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, err
			}
			continue
		}
		if err := rows.Scan(&id, &name); err != nil {
			return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, err
		}
	}
	if err := rows.Err(); err != nil {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, err
	}
	if count == 0 {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, false, fmt.Errorf("expected exactly one project row but found 0")
	}
	return id, name, origin, true, nil
}

func openReadOnlyDB(path string) (*readOnlyDatabase, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	snapshotDirectory, err := os.MkdirTemp("", "rhizome-inventory-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(snapshotDirectory) }
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyDatabaseArtifact(absPath+suffix, snapshotDirectory); err != nil {
			if errors.Is(err, os.ErrNotExist) && suffix != "" {
				continue
			}
			cleanup()
			return nil, err
		}
	}

	uriPath := filepath.ToSlash(filepath.Join(snapshotDirectory, filepath.Base(absPath)))
	if len(uriPath) >= 2 && uriPath[1] == ':' && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "trusted_schema(OFF)")
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		cleanup()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &readOnlyDatabase{DB: db, cleanup: cleanup}, nil
}

func copyDatabaseArtifact(sourcePath, destinationDirectory string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database artifact is not a regular file: %s", sourcePath)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(filepath.Join(destinationDirectory, filepath.Base(sourcePath)), os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
