// Package sqlite provides the SQLite storage bootstrap and transaction boundary.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rhizome-mcp/internal/domain"

	_ "modernc.org/sqlite"
)

const (
	driverName            = "sqlite"
	defaultMaxOpenConns   = 4
	defaultMaxIdleConns   = 4
	defaultAutoCheckpoint = 1000
)

var defaultRetryDelays = []time.Duration{25 * time.Millisecond, 75 * time.Millisecond, 200 * time.Millisecond}

// Sleeper waits between complete write-transaction attempts.
type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

// SleepFunc adapts a function to Sleeper.
type SleepFunc func(context.Context, time.Duration) error

// Sleep implements Sleeper.
func (f SleepFunc) Sleep(ctx context.Context, delay time.Duration) error { return f(ctx, delay) }

// RetryPolicy controls bounded lock-contention retries. Delays are copied.
// A nil policy in Options selects the production defaults.
type RetryPolicy struct {
	Delays  []time.Duration
	Sleeper Sleeper
}

// Options contains test and production injection points for Open.
type Options struct {
	RetryPolicy *RetryPolicy
}

// DB is a configured SQLite connection pool. Its parent directory must exist
// before Open is called; Open creates neither directories nor schema objects.
type DB struct {
	pool   *sql.DB
	retry  retryPolicy
	path   string
	mu     sync.RWMutex
	closed bool
}

// Open opens path, configures every pooled connection, and verifies the
// required WAL, checkpoint, and FTS5 capabilities. The path must be explicit,
// non-empty, and have an existing parent directory.
func Open(ctx context.Context, path string, options Options) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, configurationError(errors.New("empty database path"), "database path must not be empty")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, configurationError(err, "database path is invalid")
	}
	parent := filepath.Dir(absPath)
	info, err := os.Stat(parent)
	if err != nil {
		return nil, configurationError(err, "database parent directory must exist")
	}
	if !info.IsDir() {
		return nil, configurationError(errors.New("database parent is not a directory"), "database parent directory must exist")
	}

	retry, err := newRetryPolicy(options.RetryPolicy)
	if err != nil {
		return nil, configurationError(err, "SQLite retry policy is invalid")
	}

	pool, err := sql.Open(driverName, dataSourceName(absPath))
	if err != nil {
		return nil, TranslateError(err)
	}
	pool.SetMaxOpenConns(defaultMaxOpenConns)
	pool.SetMaxIdleConns(defaultMaxIdleConns)
	pool.SetConnMaxLifetime(0)
	pool.SetConnMaxIdleTime(0)

	db := &DB{pool: pool, retry: retry, path: absPath}
	if err := db.verify(ctx); err != nil {
		closeErr := pool.Close()
		return nil, TranslateError(errors.Join(err, closeErr))
	}
	return db, nil
}

func dataSourceName(databasePath string) string {
	uriPath := filepath.ToSlash(databasePath)
	if len(uriPath) >= 2 && uriPath[1] == ':' && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}

	query := url.Values{}
	for _, pragma := range []string{
		"foreign_keys(ON)",
		"synchronous(NORMAL)",
		"busy_timeout(5000)",
		"temp_store(MEMORY)",
		"trusted_schema(OFF)",
		"wal_autocheckpoint(1000)",
	} {
		query.Add("_pragma", pragma)
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String()
}

func (db *DB) verify(ctx context.Context) error {
	if err := db.pool.PingContext(ctx); err != nil {
		return err
	}
	conn, err := db.pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return configurationError(fmt.Errorf("journal mode %q", mode), "SQLite journal mode must be WAL")
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return configurationError(fmt.Errorf("journal mode %q", mode), "SQLite journal mode must be WAL")
	}

	var checkpoint int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint = 1000").Scan(&checkpoint); err != nil {
		return err
	}
	if checkpoint != defaultAutoCheckpoint {
		return configurationError(fmt.Errorf("WAL auto-checkpoint %d", checkpoint), "SQLite WAL auto-checkpoint is invalid")
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&checkpoint); err != nil {
		return err
	}
	if checkpoint != defaultAutoCheckpoint {
		return configurationError(fmt.Errorf("WAL auto-checkpoint %d", checkpoint), "SQLite WAL auto-checkpoint is invalid")
	}

	var fts5Enabled int
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&fts5Enabled); err != nil {
		return configurationError(err, "SQLite FTS5 support is required")
	}
	if fts5Enabled != 1 {
		return configurationError(errors.New("SQLite was compiled without ENABLE_FTS5"), "SQLite FTS5 support is required")
	}
	return nil
}

// Backup creates a new database file with VACUUM INTO after a controlled WAL
// checkpoint, validating the requested output path before starting.
func (db *DB) Backup(ctx context.Context, output string) (string, error) {
	if db == nil {
		return "", configurationError(errors.New("SQLite database is not open"), "SQLite database is not open")
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed || db.pool == nil {
		return "", configurationError(errors.New("SQLite database is not open"), "SQLite database is not open")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	absOutput, err := db.validateBackupOutput(ctx, output)
	if err != nil {
		return "", err
	}
	tempOutput, err := prepareBackupTemp(absOutput)
	if err != nil {
		return "", err
	}
	defer func() {
		if tempOutput != "" {
			_ = os.Remove(tempOutput)
		}
	}()
	conn, err := db.pool.Conn(ctx)
	if err != nil {
		return "", TranslateError(err)
	}
	defer conn.Close()

	var checkpointBusy, checkpointLog, checkpointCheckpointed int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(FULL)").Scan(&checkpointBusy, &checkpointLog, &checkpointCheckpointed); err != nil {
		return "", TranslateError(err)
	}
	if checkpointBusy != 0 {
		return "", domain.WrapError(errors.New("wal checkpoint reported busy readers"), domain.CodeStorageBusy, "storage is busy; retry the operation", true)
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", tempOutput); err != nil {
		return "", TranslateError(err)
	}
	if err := os.Link(tempOutput, absOutput); err != nil {
		return "", configurationError(err, "backup output path could not be created")
	}
	if err := os.Remove(tempOutput); err != nil {
		return "", configurationError(err, "cannot finalize backup output")
	}
	tempOutput = ""
	return absOutput, nil
}

func (db *DB) validateBackupOutput(ctx context.Context, output string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if output == "" {
		return "", configurationError(errors.New("backup output path must not be empty"), "backup output path must not be empty")
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return "", configurationError(err, "backup output path is invalid")
	}
	if db.path != "" && absOutput == db.path {
		return "", configurationError(errors.New("backup output path must differ from the source database path"), "backup output path must differ from the source database path")
	}
	parent := filepath.Dir(absOutput)
	info, err := os.Stat(parent)
	if err != nil {
		return "", configurationError(err, "backup output parent directory must exist")
	}
	if !info.IsDir() {
		return "", configurationError(errors.New("backup output parent is not a directory"), "backup output parent directory must exist")
	}
	info, err = os.Lstat(absOutput)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return absOutput, nil
	case err != nil:
		return "", configurationError(err, "backup output path is invalid")
	default:
		return "", configurationError(errors.New("backup output path already exists"), "backup output path already exists")
	}
}

func prepareBackupTemp(output string) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(output), ".rhizome-backup-*")
	if err != nil {
		return "", configurationError(err, "cannot create backup output")
	}
	path := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return "", configurationError(err, "cannot create backup output")
	}
	if err := os.Remove(path); err != nil {
		return "", configurationError(err, "cannot create backup output")
	}
	return path, nil
}

// Close attempts a passive WAL checkpoint and always closes the pool. Any
// checkpoint and close failures are joined before safe storage translation.
func (db *DB) Close(ctx context.Context) error {
	if db == nil {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed || db.pool == nil {
		return nil
	}
	db.closed = true
	_, checkpointErr := db.pool.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	closeErr := db.pool.Close()
	return TranslateError(errors.Join(checkpointErr, closeErr))
}
