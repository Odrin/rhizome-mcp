// Package runtime composes the Phase 1 project identity, external SQLite
// storage, migrations, and lifecycle health verification.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/projectconfig"
)

const (
	// CodeProjectOpen identifies a project lifecycle composition failure not
	// already represented by a more specific domain error.
	CodeProjectOpen = "PROJECT_OPEN_FAILED"
	// CodeProjectMismatch identifies an external database belonging to a
	// different project identity or containing multiple project rows.
	CodeProjectMismatch = "PROJECT_DATABASE_MISMATCH"
	// CodeHealthCheck identifies one or more failed Phase 1 health checks.
	CodeHealthCheck = "PROJECT_HEALTH_FAILED"
	// CodeProjectBackup identifies a failed online backup validation path.
	CodeProjectBackup = "PROJECT_BACKUP_FAILED"
)

// Options supplies all environment-dependent values needed by OpenProject.
// DataRoot, when non-empty, takes precedence over PathInputs. Callers must
// provide a Clock; a later process composition root may choose RealClock and
// populate PathInputs from process state.
type Options struct {
	StartingPath string
	DataRoot     string
	PathInputs   projectconfig.PathInputs
	Clock        clock.Clock
	SQLite       sqlite.Options
}

// Project is one opened project database. It owns Database until Close and is
// safe to close more than once. Database is exposed for later application
// composition; callers must not close it independently.
type Project struct {
	Root          string
	ProjectID     string
	DatabasePath  string
	SchemaVersion int
	Database      *sqlite.DB

	mu       sync.RWMutex
	closed   bool
	closeErr error
	SQLite   sqlite.Options
	clock    clock.Clock
}

// OpenProject discovers strict repository identity, resolves external storage,
// opens and migrates SQLite, and atomically verifies or seeds the project row.
func OpenProject(ctx context.Context, options Options) (_ *Project, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Clock == nil {
		return nil, domain.NewError(CodeProjectOpen, "project clock is required", false)
	}

	discovered, err := projectconfig.Discover(options.StartingPath)
	if err != nil {
		return nil, err
	}
	dataRoot := options.DataRoot
	if dataRoot == "" {
		dataRoot, err = projectconfig.ResolveDataRoot(options.PathInputs)
		if err != nil {
			return nil, err
		}
	}
	dataRoot, err = validateExternalDataRoot(discovered.Root, dataRoot)
	if err != nil {
		return nil, lifecycleError(err, domain.CodeStorageConfiguration, "application data root must exist outside the repository")
	}
	databasePath, err := projectconfig.ProjectDatabasePath(dataRoot, discovered.Identity.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := validateDatabaseDestination(databasePath); err != nil {
		return nil, lifecycleError(err, domain.CodeStorageConfiguration, "project database destination is invalid")
	}

	db, err := sqlite.Open(ctx, databasePath, options.SQLite)
	if err != nil {
		return nil, lifecycleError(err, CodeProjectOpen, "cannot open project database")
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		closeErr := db.Close(closeCtx)
		if err == nil && closeErr != nil {
			err = lifecycleError(closeErr, CodeProjectOpen, "cannot close project database after startup failure")
		}
	}()

	migrationResult, err := migrations.Migrate(ctx, db, options.Clock)
	if err != nil {
		return nil, err
	}
	if err := ensureProjectRow(ctx, db, discovered.Identity.ProjectID, options.Clock.Now()); err != nil {
		return nil, err
	}

	keep = true
	return &Project{
		Root:          discovered.Root,
		ProjectID:     discovered.Identity.ProjectID,
		DatabasePath:  databasePath,
		SchemaVersion: migrationResult.Version,
		Database:      db,
		SQLite:        options.SQLite,
		clock:         options.Clock,
	}, nil
}

func validateExternalDataRoot(projectRoot, dataRoot string) (string, error) {
	if dataRoot == "" {
		return "", errors.New("application data root is required")
	}
	absolute, err := filepath.Abs(dataRoot)
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
		return "", errors.New("application data root is not a directory")
	}
	relative, err := filepath.Rel(projectRoot, resolved)
	if err != nil {
		return "", err
	}
	if relative == "." || (relative != ".." && !filepath.IsAbs(relative) && !startsWithParent(relative)) {
		return "", errors.New("application data root is inside the repository")
	}
	return resolved, nil
}

func startsWithParent(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}

func validateDatabaseDestination(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("project data path is not a directory")
	}
	info, err = os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return errors.New("database path is not a regular file")
	default:
		return nil
	}
}

func ensureProjectRow(ctx context.Context, db *sqlite.DB, projectID string, now time.Time) error {
	return db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM projects ORDER BY id")
		if err != nil {
			return lifecycleError(err, CodeProjectOpen, "cannot verify project database identity")
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return lifecycleError(err, CodeProjectOpen, "cannot verify project database identity")
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return lifecycleError(err, CodeProjectOpen, "cannot verify project database identity")
		}
		if len(ids) == 0 {
			timestamp := now.UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects(
				id, next_issue_number, created_at, updated_at
			) VALUES (?, 1, ?, ?)`, projectID, timestamp, timestamp); err != nil {
				return lifecycleError(err, CodeProjectOpen, "cannot initialize project database identity")
			}
			return nil
		}
		if len(ids) != 1 || ids[0] != projectID {
			return domain.NewError(CodeProjectMismatch, "project database identity does not match repository identity", false,
				domain.Detail{Field: "projects", Code: CodeProjectMismatch, Message: fmt.Sprintf("expected one row for project %s", projectID)})
		}
		return nil
	})
}

func lifecycleError(err error, fallbackCode, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return err
	}
	return domain.WrapError(err, fallbackCode, message, false)
}

// BackupReport summarizes a validated backup database artifact.
type BackupReport struct {
	OutputPath    string
	SchemaVersion int
}

// Backup creates an online backup of the opened project database, validates the
// output database, and returns the validated backup metadata.
func (project *Project) Backup(ctx context.Context, output string) (BackupReport, error) {
	if project == nil {
		return BackupReport{}, domain.NewError(CodeProjectBackup, "project backup failed", false,
			domain.Detail{Field: "project", Code: CodeProjectBackup, Message: "project is not open"})
	}
	project.mu.RLock()
	defer project.mu.RUnlock()
	if project.closed || project.Database == nil {
		return BackupReport{}, domain.NewError(CodeProjectBackup, "project backup failed", false,
			domain.Detail{Field: "project", Code: CodeProjectBackup, Message: "project is closed"})
	}

	outputPath, err := project.Database.Backup(ctx, output)
	if err != nil {
		return BackupReport{}, wrapProjectBackupError(err)
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return BackupReport{}, wrapProjectBackupError(err)
	}

	validationDB, err := sqlite.Open(ctx, outputPath, project.SQLite)
	if err != nil {
		cleanupErr := cleanupBackupOutput(outputPath, outputInfo)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return BackupReport{}, wrapProjectBackupError(err)
	}

	validationProject := &Project{
		Root:          project.Root,
		ProjectID:     project.ProjectID,
		DatabasePath:  outputPath,
		SchemaVersion: project.SchemaVersion,
		Database:      validationDB,
		SQLite:        project.SQLite,
	}

	healthReport, err := validationProject.Health(ctx)
	if err != nil {
		closeErr := validationProject.closeValidationDB(ctx)
		cleanupErr := cleanupBackupOutput(outputPath, outputInfo)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return BackupReport{}, wrapProjectBackupError(err)
	}
	if !healthReport.Healthy() {
		closeErr := validationProject.closeValidationDB(ctx)
		cleanupErr := cleanupBackupOutput(outputPath, outputInfo)
		if cleanupErr != nil {
			err = errors.Join(errors.New("backup validation failed"), cleanupErr)
		} else {
			err = errors.New("backup validation failed")
		}
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return BackupReport{}, wrapProjectBackupError(err)
	}

	closeErr := validationProject.closeValidationDB(ctx)
	if closeErr != nil {
		cleanupErr := cleanupBackupOutput(outputPath, outputInfo)
		if cleanupErr != nil {
			closeErr = errors.Join(closeErr, cleanupErr)
		}
		return BackupReport{}, wrapProjectBackupError(closeErr)
	}
	return BackupReport{OutputPath: outputPath, SchemaVersion: healthReport.CurrentSchemaVersion}, nil
}

func (project *Project) closeValidationDB(ctx context.Context) error {
	if project == nil || project.Database == nil {
		return nil
	}
	return project.Database.Close(ctx)
}

func wrapProjectBackupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return domain.WrapError(err, CodeProjectBackup, "project backup failed", false)
}

func cleanupBackupOutput(path string, expected os.FileInfo) error {
	if path == "" || expected == nil {
		return nil
	}
	actual, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(actual, expected) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Close performs the configured passive checkpoint and closes owned storage.
// Repeated calls return the result of the first close.
func (project *Project) Close(ctx context.Context) error {
	if project == nil {
		return nil
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	if project.closed {
		return project.closeErr
	}
	project.closed = true
	if project.Database != nil {
		project.closeErr = project.Database.Close(ctx)
	}
	return project.closeErr
}
