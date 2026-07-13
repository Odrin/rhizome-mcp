package sqlite

import (
	"context"
	"errors"

	"rhizome-mcp/internal/domain"

	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// TranslateError converts SQLite failures to stable domain errors while
// retaining the original cause for errors.Is and errors.As. Domain and context
// errors pass through unchanged.
func TranslateError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		if err == domainErr {
			return err
		}
		return domain.WrapError(err, domainErr.Code, domainErr.Message, domainErr.Retryable, domainErr.Details...)
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}

	code, ok := sqliteCode(err)
	if !ok {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return err
	}
	switch code & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return domain.WrapError(err, domain.CodeStorageBusy, "storage is busy; retry the operation", true)
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return domain.WrapError(err, domain.CodeStorageCorrupt, "storage is corrupt or has an invalid format", false)
	case sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_IOERR, sqlite3.SQLITE_FULL, sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM:
		return domain.WrapError(err, domain.CodeStorageUnavailable, "storage is unavailable", false)
	case sqlite3.SQLITE_CONSTRAINT:
		return domain.WrapError(err, domain.CodeStorageConstraint, "storage constraint rejected the operation", false)
	default:
		return domain.WrapError(err, domain.CodeStorageFailure, "storage operation failed", false)
	}
}

func configurationError(cause error, message string) error {
	return domain.WrapError(cause, domain.CodeStorageConfiguration, message, false)
}

func sqliteCode(err error) (int, bool) {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return 0, false
	}
	return sqliteErr.Code(), true
}

func isLockContention(err error) bool {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return false
	}
	code, ok := sqliteCode(err)
	if !ok {
		return false
	}
	code &= 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}
