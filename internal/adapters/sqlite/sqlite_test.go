package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"

	moderncsqlite "modernc.org/sqlite"
)

func TestOpenConfiguresAndVerifiesSQLite(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, Options{})

	stats := db.pool.Stats()
	if stats.MaxOpenConnections != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, defaultMaxOpenConns)
	}
	if !reflect.DeepEqual(db.retry.delays, defaultRetryDelays) {
		t.Fatalf("default retry delays = %v, want %v", db.retry.delays, defaultRetryDelays)
	}

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, defaultMaxOpenConns)
	for range defaultMaxOpenConns {
		conn, err := db.pool.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn() error = %v", err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	for i, conn := range connections {
		assertPragmaInt(t, conn, "foreign_keys", 1, i)
		assertPragmaInt(t, conn, "synchronous", 1, i)
		assertPragmaInt(t, conn, "busy_timeout", 5000, i)
		assertPragmaInt(t, conn, "temp_store", 2, i)
		assertPragmaInt(t, conn, "trusted_schema", 0, i)
		assertPragmaInt(t, conn, "wal_autocheckpoint", defaultAutoCheckpoint, i)
	}

	var mode string
	if err := connections[0].QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	for _, schema := range []string{"main", "temp"} {
		var count int
		query := "SELECT count(*) FROM " + schema + ".sqlite_schema WHERE name LIKE '__rhizome_fts5_capability_check%'"
		if err := connections[0].QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("query %s schema artifacts: %v", schema, err)
		}
		if count != 0 {
			t.Fatalf("%s FTS capability-check artifacts = %d, want 0", schema, count)
		}
	}

	if _, err := connections[0].ExecContext(ctx, "CREATE VIRTUAL TABLE temp.test_fts USING fts5(content)"); err != nil {
		t.Fatalf("create FTS5 table: %v", err)
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("close pooled connection: %v", err)
		}
	}
	connections = nil
	stats = db.pool.Stats()
	if stats.Idle != defaultMaxIdleConns {
		t.Fatalf("idle connections = %d, want %d", stats.Idle, defaultMaxIdleConns)
	}
	if stats.MaxIdleClosed != 0 || stats.MaxIdleTimeClosed != 0 || stats.MaxLifetimeClosed != 0 {
		t.Fatalf("unexpected pool closures: %+v", stats)
	}
}

func TestOpenRequiresExistingParentAndDoesNotCreateIt(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "missing")
	_, err := Open(context.Background(), filepath.Join(parent, "tasks.db"), Options{})
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
	if _, statErr := os.Stat(parent); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent stat error = %v, want not-exist", statErr)
	}
}

func TestWriteCommitsAndRollsBack(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, noRetryOptions())
	ctx := context.Background()
	if _, err := db.pool.ExecContext(ctx, "CREATE TABLE values_test (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO values_test(value) VALUES (?)", "committed")
		return err
	}); err != nil {
		t.Fatalf("committing Write() error = %v", err)
	}

	callbackErr := errors.New("reject write")
	err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO values_test(value) VALUES (?)", "rolled back"); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("rollback Write() error = %v, want callback error", err)
	}

	var count int
	if err := db.pool.QueryRowContext(ctx, "SELECT count(*) FROM values_test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestReadUsesConfiguredConnectionAndPropagatesErrors(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, Options{})
	ctx := context.Background()

	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var foreignKeys int
		if err := query.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			return err
		}
		if foreignKeys != 1 {
			t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
		}
		return nil
	}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	want := errors.New("check failed")
	if err := db.Read(ctx, func(context.Context, Queryer) error { return want }); !errors.Is(err, want) {
		t.Fatalf("Read() error = %v, want callback error", err)
	}
	if err := db.Read(ctx, nil); err == nil {
		t.Fatal("Read(nil) error = nil")
	}
}

func TestWriteRollsBackCommitFailure(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, noRetryOptions())
	ctx := context.Background()
	statements := []string{
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED)",
	}
	for _, statement := range statements {
		if _, err := db.pool.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO children(parent_id) VALUES (99)")
		return err
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)
	var count int
	if err := db.pool.QueryRowContext(ctx, "SELECT count(*) FROM children").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows after failed commit = %d, want 0", count)
	}
}

func TestWriteRetriesBusyWithConfiguredDelaysAndMapsExhaustion(t *testing.T) {
	t.Parallel()
	delays := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	sleeper := &recordingSleeper{}
	db := openTestDB(t, Options{RetryPolicy: &RetryPolicy{Delays: delays, Sleeper: sleeper}})
	busyErr := obtainBusyError(t, filepath.Join(t.TempDir(), "busy.db"))

	attempts := 0
	err := db.Write(context.Background(), func(context.Context, Executor) error {
		attempts++
		return busyErr
	})
	assertDomainCode(t, err, domain.CodeStorageBusy)
	if attempts != len(delays)+1 {
		t.Fatalf("attempts = %d, want %d", attempts, len(delays)+1)
	}
	if got := sleeper.Delays(); !reflect.DeepEqual(got, delays) {
		t.Fatalf("retry delays = %v, want %v", got, delays)
	}
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatal("translated busy error does not preserve SQLite cause")
	}
}

func TestWriteCancellationStopsDuringRetryWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := SleepFunc(func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})
	db := openTestDB(t, Options{RetryPolicy: &RetryPolicy{Delays: []time.Duration{time.Hour}, Sleeper: sleeper}})
	busyErr := obtainBusyError(t, filepath.Join(t.TempDir(), "busy.db"))

	attempts := 0
	err := db.Write(ctx, func(context.Context, Executor) error {
		attempts++
		return busyErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write() error = %v, want context cancellation", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestWriteDoesNotRetryConstraintOrDomainError(t *testing.T) {
	t.Parallel()
	sleeper := &recordingSleeper{}
	db := openTestDB(t, Options{RetryPolicy: &RetryPolicy{Delays: defaultRetryDelays, Sleeper: sleeper}})
	ctx := context.Background()
	if _, err := db.pool.ExecContext(ctx, "CREATE TABLE unique_test (value TEXT UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.ExecContext(ctx, "INSERT INTO unique_test(value) VALUES ('same')"); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		attempts++
		_, err := tx.ExecContext(ctx, "INSERT INTO unique_test(value) VALUES ('same')")
		return err
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)
	if attempts != 1 || len(sleeper.Delays()) != 0 {
		t.Fatalf("constraint attempts = %d, delays = %v", attempts, sleeper.Delays())
	}

	busyErr := obtainBusyError(t, filepath.Join(t.TempDir(), "busy.db"))
	wantDomain := domain.WrapError(busyErr, "BUSINESS_RULE", "business rule rejected the write", false)
	attempts = 0
	err = db.Write(ctx, func(context.Context, Executor) error {
		attempts++
		return wantDomain
	})
	assertDomainCode(t, err, "BUSINESS_RULE")
	if err.Error() != "business rule rejected the write" {
		t.Fatalf("domain error message = %q, want safe message", err.Error())
	}
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatal("joined domain error does not preserve SQLite cause")
	}
	if attempts != 1 || len(sleeper.Delays()) != 0 {
		t.Fatalf("domain attempts = %d, delays = %v", attempts, sleeper.Delays())
	}
}

func TestOpenTranslatesCorruptDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "not-a-database.db")
	if err := os.WriteFile(path, []byte("this is not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatal("corrupt translation does not preserve SQLite cause")
	}
}

func TestBackupCreatesIndependentCopyFromWALData(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, noRetryOptions())
	ctx := context.Background()
	if _, err := db.pool.ExecContext(ctx, "CREATE TABLE backup_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.ExecContext(ctx, "INSERT INTO backup_test(value) VALUES (?)", "backup-data"); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "backup.db")
	outputPath, err := db.Backup(ctx, output)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if outputPath != filepath.Clean(output) {
		t.Fatalf("Backup() output path = %q, want %q", outputPath, filepath.Clean(output))
	}

	backupDB, err := Open(ctx, outputPath, Options{})
	if err != nil {
		t.Fatalf("reopen backup database: %v", err)
	}
	defer func() {
		if err := backupDB.Close(context.Background()); err != nil {
			t.Fatalf("close backup database: %v", err)
		}
	}()

	var value string
	if err := backupDB.Read(ctx, func(ctx context.Context, query Queryer) error {
		return query.QueryRowContext(ctx, "SELECT value FROM backup_test WHERE id = 1").Scan(&value)
	}); err != nil {
		t.Fatalf("read backup data: %v", err)
	}
	if value != "backup-data" {
		t.Fatalf("backup value = %q, want backup-data", value)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_test(value) VALUES (?)", "source-data")
		return err
	}); err != nil {
		t.Fatalf("source write after backup: %v", err)
	}
	var count int
	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM backup_test").Scan(&count)
	}); err != nil {
		t.Fatalf("read source count after backup: %v", err)
	}
	if count != 2 {
		t.Fatalf("source rows = %d, want 2", count)
	}
}

func TestBackupRejectsInvalidDestinationsWithoutOverwritingData(t *testing.T) {
	t.Parallel()
	db := openTestDB(t, noRetryOptions())
	ctx := context.Background()
	if _, err := db.pool.ExecContext(ctx, "CREATE TABLE backup_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.ExecContext(ctx, "INSERT INTO backup_test(value) VALUES (?)", "backup-data"); err != nil {
		t.Fatal(err)
	}

	var beforeCount int
	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM backup_test").Scan(&beforeCount)
	}); err != nil {
		t.Fatal(err)
	}

	_, err := db.Backup(ctx, db.path)
	if err == nil {
		t.Fatal("Backup() with same path unexpectedly succeeded")
	}
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
	if err := db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var count int
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM backup_test").Scan(&count); err != nil {
			return err
		}
		if count != beforeCount {
			t.Fatalf("source row count changed to %d, want %d", count, beforeCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	existingPath := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(existingPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Backup(ctx, existingPath)
	if err == nil {
		t.Fatal("Backup() with existing path unexpectedly succeeded")
	}
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
	contents, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "existing" {
		t.Fatalf("existing output contents = %q, want %q", string(contents), "existing")
	}

	missingParent := filepath.Join(t.TempDir(), "missing", "backup.db")
	_, err = db.Backup(ctx, missingParent)
	if err == nil {
		t.Fatal("Backup() with missing parent unexpectedly succeeded")
	}
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
	if _, statErr := os.Stat(filepath.Dir(missingParent)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing parent stat error = %v, want not-exist", statErr)
	}
}

func TestOpenTranslatesUnavailableDatabase(t *testing.T) {
	t.Parallel()
	_, err := Open(context.Background(), t.TempDir(), Options{})
	assertDomainCode(t, err, domain.CodeStorageUnavailable)
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatal("unavailable translation does not preserve SQLite cause")
	}
}

func TestCloseAlwaysClosesPoolWhenCheckpointContextCanceled(t *testing.T) {
	t.Parallel()
	db := openTestDBWithoutCleanup(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := db.Close(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context cancellation", err)
	}
	if err := db.pool.PingContext(context.Background()); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Fatal("pool remains usable after Close")
	}
}

func TestCloseCheckpointsAndCloses(t *testing.T) {
	t.Parallel()
	db := openTestDBWithoutCleanup(t, Options{})
	if err := db.Close(context.Background()); err != nil {
		var sqliteErr *moderncsqlite.Error
		if errors.As(err, &sqliteErr) {
			t.Fatalf("Close() error = %v (SQLite code %d: %v)", err, sqliteErr.Code(), sqliteErr)
		}
		t.Fatalf("Close() error = %v", err)
	}
	if err := db.pool.Ping(); err == nil {
		t.Fatal("pool remains usable after Close")
	}
}

func assertPragmaInt(t *testing.T, conn *sql.Conn, pragma string, want, connection int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
		t.Fatalf("connection %d PRAGMA %s: %v", connection, pragma, err)
	}
	if got != want {
		t.Fatalf("connection %d PRAGMA %s = %d, want %d", connection, pragma, got, want)
	}
}

func openTestDB(t *testing.T, options Options) *DB {
	t.Helper()
	db := openTestDBWithoutCleanup(t, options)
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return db
}

func openTestDBWithoutCleanup(t *testing.T, options Options) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "tasks.db"), options)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return db
}

func noRetryOptions() Options {
	return Options{RetryPolicy: &RetryPolicy{Delays: []time.Duration{}, Sleeper: SleepFunc(func(context.Context, time.Duration) error {
		return nil
	})}}
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}

type recordingSleeper struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *recordingSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delays = append(s.delays, delay)
	return nil
}

func (s *recordingSleeper) Delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

func obtainBusyError(t *testing.T, path string) error {
	t.Helper()
	owner, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	contenderDSN := dataSourceName(path) + "&_pragma=busy_timeout(0)"
	contender, err := sql.Open(driverName, contenderDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()

	ctx := context.Background()
	ownerConn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerConn.Close()
	if _, err := ownerConn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer ownerConn.ExecContext(context.Background(), "ROLLBACK")

	contenderConn, err := contender.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contenderConn.Close()
	if _, err := contenderConn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	_, busyErr := contenderConn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if busyErr == nil {
		_, _ = contenderConn.ExecContext(ctx, "ROLLBACK")
		t.Fatal("contending BEGIN IMMEDIATE unexpectedly succeeded")
	}
	if !isLockContention(busyErr) {
		t.Fatalf("contending error = %v, want BUSY or LOCKED", busyErr)
	}
	return busyErr
}
