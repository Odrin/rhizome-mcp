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
	"time"

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
	pool  *sql.DB
	retry retryPolicy
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

	db := &DB{pool: pool, retry: retry}
	if err := db.verify(ctx); err != nil {
		closeErr := pool.Close()
		return nil, TranslateError(errors.Join(err, closeErr))
	}
	return db, nil
}

func dataSourceName(path string) string {
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
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query.Encode()}).String()
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

// Close attempts a passive WAL checkpoint and always closes the pool. Any
// checkpoint and close failures are joined before safe storage translation.
func (db *DB) Close(ctx context.Context) error {
	if db == nil || db.pool == nil {
		return nil
	}
	_, checkpointErr := db.pool.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	closeErr := db.pool.Close()
	return TranslateError(errors.Join(checkpointErr, closeErr))
}
