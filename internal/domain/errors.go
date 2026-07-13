package domain

import (
	"sort"
)

const (
	// CodeInvalidArgument identifies invalid caller input.
	CodeInvalidArgument = "INVALID_ARGUMENT"
	// CodeInvalidTransition identifies a forbidden issue status transition.
	CodeInvalidTransition = "INVALID_TRANSITION"
	// CodeLimitExceeded identifies input beyond a documented bound.
	CodeLimitExceeded = "LIMIT_EXCEEDED"
	// CodeStorageBusy identifies exhausted SQLite lock-contention retries.
	CodeStorageBusy = "STORAGE_BUSY"
	// CodeStorageUnavailable identifies inaccessible or failed storage.
	CodeStorageUnavailable = "STORAGE_UNAVAILABLE"
	// CodeStorageCorrupt identifies corrupt or non-database SQLite files.
	CodeStorageCorrupt = "STORAGE_CORRUPT"
	// CodeStorageConfiguration identifies an invalid or unsupported storage setup.
	CodeStorageConfiguration = "STORAGE_CONFIGURATION"
	// CodeStorageConstraint identifies a database constraint violation.
	CodeStorageConstraint = "STORAGE_CONSTRAINT"
	// CodeStorageMigration identifies invalid migration history or schema migration failure.
	CodeStorageMigration = "STORAGE_MIGRATION"
	// CodeStorageFailure identifies another SQLite operation failure.
	CodeStorageFailure = "STORAGE_FAILURE"
)

// Detail is one stable, field-oriented domain error detail. EntityIndex is nil
// when the detail does not belong to an indexed batch entity.
type Detail struct {
	EntityIndex *int   `json:"entity_index,omitempty"`
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Message     string `json:"message,omitempty"`
}

// Error is a stable structured domain error suitable for adapter mapping.
type Error struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Details   []Detail `json:"details"`
	Retryable bool     `json:"retryable"`
	cause     error
}

// NewError constructs an Error with a defensive, deterministic detail order.
func NewError(code, message string, retryable bool, details ...Detail) *Error {
	ordered := append([]Detail(nil), details...)
	SortDetails(ordered)
	if ordered == nil {
		ordered = []Detail{}
	}
	return &Error{Code: code, Message: message, Details: ordered, Retryable: retryable}
}

// WrapError constructs an Error that unwraps to cause.
func WrapError(cause error, code, message string, retryable bool, details ...Detail) *Error {
	err := NewError(code, message, retryable, details...)
	err.cause = cause
	return err
}

// Error returns the stable human-readable message.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap returns the internal cause, when one was supplied.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is matches another domain Error by non-empty stable code.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other.Code != "" && e.Code == other.Code
}

// SortDetails orders details by entity index, field, code, and message.
func SortDetails(details []Detail) {
	sort.SliceStable(details, func(i, j int) bool {
		left, right := details[i], details[j]
		if compare := compareEntityIndex(left.EntityIndex, right.EntityIndex); compare != 0 {
			return compare < 0
		}
		if left.Field != right.Field {
			return left.Field < right.Field
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Message < right.Message
	})
}

func compareEntityIndex(left, right *int) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return -1
	case right == nil:
		return 1
	case *left < *right:
		return -1
	case *left > *right:
		return 1
	default:
		return 0
	}
}

var _ interface {
	error
	Unwrap() error
	Is(error) bool
} = (*Error)(nil)
