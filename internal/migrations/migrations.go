// Package migrations owns the embedded SQLite schema migration catalog and runner.
package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

//go:embed sql/001_initial_schema.sql
var initialSchemaSQL string

//go:embed sql/002_search_index_triggers.sql
var searchIndexTriggersSQL string

//go:embed sql/003_review_workflow.sql
var reviewWorkflowSQL string

// Tests verify this checksum against the exact embedded SQL bytes. After an
// intentional edit, regenerate it with: shasum -a 256 internal/migrations/sql/001_initial_schema.sql
const initialSchemaChecksum = "2a072c9af462f54b08026d68108b5c0f2c17e7a0eec1ff9366b9824a63ef80ef"
const searchIndexTriggersChecksum = "817957b68b07b7d393fe21718743dc64dd90aa5a0d7e8da8153ff2b35bf1a695"
const reviewWorkflowChecksum = "2377585c091c8a6603b1b5e7dfd7726e2121de08858cfbca26dd70dcad7e94e1"

var (
	migrationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$`)
	embeddedCatalog      = []migration{
		{
			version:  1,
			name:     "initial_schema",
			checksum: initialSchemaChecksum,
			sql:      initialSchemaSQL,
		},
		{
			version:  2,
			name:     "search_index_triggers",
			checksum: searchIndexTriggersChecksum,
			sql:      searchIndexTriggersSQL,
		},
		{
			version:  3,
			name:     "review_workflow",
			checksum: reviewWorkflowChecksum,
			sql:      reviewWorkflowSQL,
		},
	}
)

// Clock supplies migration timestamps. Implementations should return UTC time;
// Migrate normalizes the value to UTC before persistence.
type Clock interface {
	Now() time.Time
}

// Result summarizes the database schema after a successful migration run.
type Result struct {
	Version int
	Applied int
}

type migration struct {
	version  int
	name     string
	checksum string
	sql      string
}

type historyRow struct {
	version   int
	name      string
	checksum  string
	appliedAt string
}

type foreignKeyViolation struct {
	table  string
	rowID  sql.NullInt64
	parent string
	fkID   int
}

// CurrentVersion returns the highest migration version embedded in this binary.
func CurrentVersion() int {
	return embeddedCatalog[len(embeddedCatalog)-1].version
}

// VerifyHistory validates the persisted migration history against the embedded
// catalog without applying migrations or otherwise modifying the database. It
// returns the current persisted version after successful verification.
func VerifyHistory(ctx context.Context, query sqlite.Queryer) (int, error) {
	if query == nil {
		return 0, migrationError(errors.New("nil migration queryer"), "migration database is required")
	}
	if err := validateCatalog(embeddedCatalog); err != nil {
		return 0, err
	}
	history, err := readHistory(ctx, query)
	if err != nil {
		return 0, normalizeRunError(err)
	}
	if err := validateHistory(history, embeddedCatalog); err != nil {
		return 0, normalizeRunError(err)
	}
	if len(history) == 0 {
		return 0, nil
	}
	return history[len(history)-1].version, nil
}

// Migrate validates the embedded catalog, acquires SQLite's writer lock early,
// validates migration history, applies all pending scripts atomically, and
// checks referential integrity. It never downgrades a database.
func Migrate(ctx context.Context, db *sqlite.DB, clock Clock) (Result, error) {
	return run(ctx, db, clock, embeddedCatalog)
}

func run(ctx context.Context, db *sqlite.DB, clock Clock, catalog []migration) (Result, error) {
	if err := validateCatalog(catalog); err != nil {
		return Result{}, err
	}
	if db == nil {
		return Result{}, migrationError(errors.New("nil SQLite database"), "migration database is required")
	}
	if clock == nil {
		return Result{}, migrationError(errors.New("nil migration clock"), "migration clock is required")
	}

	result := Result{}
	err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		result = Result{}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		) STRICT`); err != nil {
			return migrationError(err, "cannot bootstrap migration history")
		}

		history, err := readHistory(ctx, tx)
		if err != nil {
			return err
		}
		if err := validateHistory(history, catalog); err != nil {
			return err
		}

		for _, item := range catalog[len(history):] {
			if _, err := tx.ExecContext(ctx, item.sql); err != nil {
				return migrationError(err, fmt.Sprintf("migration %d (%s) failed", item.version, item.name))
			}
			appliedAt := clock.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
				item.version, item.name, item.checksum, appliedAt,
			); err != nil {
				return migrationError(err, fmt.Sprintf("cannot record migration %d (%s)", item.version, item.name))
			}
			result.Applied++
		}
		result.Version = catalog[len(catalog)-1].version

		return checkForeignKeys(ctx, tx)
	})
	if err != nil {
		return Result{}, normalizeRunError(err)
	}
	return result, nil
}

func validateCatalog(catalog []migration) error {
	if len(catalog) == 0 {
		return migrationError(errors.New("empty migration catalog"), "embedded migration catalog is invalid")
	}
	seenVersions := make(map[int]struct{}, len(catalog))
	seenNames := make(map[string]struct{}, len(catalog))
	for index, item := range catalog {
		expectedVersion := index + 1
		if item.version <= 0 || item.version != expectedVersion {
			return migrationError(
				fmt.Errorf("migration at index %d has version %d, expected %d", index, item.version, expectedVersion),
				"embedded migration catalog has invalid version ordering",
			)
		}
		if _, exists := seenVersions[item.version]; exists {
			return migrationError(fmt.Errorf("duplicate migration version %d", item.version), "embedded migration catalog has duplicate versions")
		}
		seenVersions[item.version] = struct{}{}
		if !migrationNamePattern.MatchString(item.name) {
			return migrationError(fmt.Errorf("invalid migration name %q", item.name), "embedded migration catalog has invalid metadata")
		}
		if _, exists := seenNames[item.name]; exists {
			return migrationError(fmt.Errorf("duplicate migration name %q", item.name), "embedded migration catalog has duplicate names")
		}
		seenNames[item.name] = struct{}{}
		if len(item.sql) == 0 {
			return migrationError(fmt.Errorf("migration %d has empty SQL", item.version), "embedded migration catalog has invalid metadata")
		}
		if !validChecksum(item.checksum) {
			return migrationError(fmt.Errorf("migration %d has malformed checksum", item.version), "embedded migration catalog has invalid metadata")
		}
		actual := sha256.Sum256([]byte(item.sql))
		if item.checksum != hex.EncodeToString(actual[:]) {
			return migrationError(fmt.Errorf("migration %d checksum does not match SQL", item.version), "embedded migration catalog checksum is invalid")
		}
	}
	return nil
}

func readHistory(ctx context.Context, tx sqlite.Queryer) ([]historyRow, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, migrationError(err, "cannot read migration history")
	}
	defer rows.Close()

	var history []historyRow
	for rows.Next() {
		var row historyRow
		if err := rows.Scan(&row.version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			return nil, migrationError(err, "migration history is malformed")
		}
		history = append(history, row)
	}
	if err := rows.Err(); err != nil {
		return nil, migrationError(err, "cannot read migration history")
	}
	return history, nil
}

func validateHistory(history []historyRow, catalog []migration) error {
	if len(history) > len(catalog) {
		return migrationError(errors.New("database migration version is newer than this binary"), "database schema is newer than this application")
	}
	for index, row := range history {
		expectedVersion := index + 1
		appliedAt, timestampErr := time.Parse(time.RFC3339Nano, row.appliedAt)
		_, offset := appliedAt.Zone()
		if row.version != expectedVersion || !validChecksum(row.checksum) || strings.TrimSpace(row.name) == "" || timestampErr != nil || offset != 0 {
			return migrationError(fmt.Errorf("malformed migration history row at index %d", index), "migration history is malformed")
		}
		item := catalog[index]
		if row.name != item.name {
			return migrationError(fmt.Errorf("migration %d name mismatch", row.version), "migration history name does not match the embedded catalog")
		}
		if row.checksum != item.checksum {
			return migrationError(fmt.Errorf("migration %d checksum mismatch", row.version), "migration history checksum does not match the embedded catalog")
		}
	}
	return nil
}

func checkForeignKeys(ctx context.Context, tx sqlite.Executor) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return migrationError(err, "cannot validate database foreign keys")
	}
	defer rows.Close()

	var violations []foreignKeyViolation
	for rows.Next() {
		var violation foreignKeyViolation
		if err := rows.Scan(&violation.table, &violation.rowID, &violation.parent, &violation.fkID); err != nil {
			return migrationError(err, "cannot validate database foreign keys")
		}
		violations = append(violations, violation)
	}
	if err := rows.Err(); err != nil {
		return migrationError(err, "cannot validate database foreign keys")
	}
	if len(violations) == 0 {
		return nil
	}

	sort.Slice(violations, func(i, j int) bool {
		left, right := violations[i], violations[j]
		if left.table != right.table {
			return left.table < right.table
		}
		if left.rowID.Valid != right.rowID.Valid {
			return !left.rowID.Valid
		}
		if left.rowID.Int64 != right.rowID.Int64 {
			return left.rowID.Int64 < right.rowID.Int64
		}
		if left.parent != right.parent {
			return left.parent < right.parent
		}
		return left.fkID < right.fkID
	})

	details := make([]domain.Detail, 0, len(violations))
	for _, violation := range violations {
		rowID := "without-rowid"
		fieldRowID := int64(-1)
		if violation.rowID.Valid {
			rowID = fmt.Sprintf("%d", violation.rowID.Int64)
			fieldRowID = violation.rowID.Int64
		}
		details = append(details, domain.Detail{
			Field:   fmt.Sprintf("foreign_keys.%s.%020d.%s.%020d", violation.table, fieldRowID, violation.parent, violation.fkID),
			Code:    "FOREIGN_KEY_VIOLATION",
			Message: fmt.Sprintf("table=%s rowid=%s parent=%s fk_index=%d", violation.table, rowID, violation.parent, violation.fkID),
		})
	}
	return domain.NewError(domain.CodeStorageMigration, "database contains foreign key violations", false, details...)
}

func validChecksum(checksum string) bool {
	if len(checksum) != sha256.Size*2 || strings.ToLower(checksum) != checksum {
		return false
	}
	_, err := hex.DecodeString(checksum)
	return err == nil
}

func migrationError(cause error, message string) error {
	return domain.WrapError(cause, domain.CodeStorageMigration, message, false)
}

func normalizeRunError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case domain.CodeStorageMigration, domain.CodeStorageBusy:
			return err
		}
	}
	return migrationError(err, "database migration failed")
}
