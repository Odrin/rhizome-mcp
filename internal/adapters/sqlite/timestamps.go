package sqlite

import (
	"errors"
	"time"
)

// storageTimestampLayout is the fixed-width canonical form every timestamp
// column in this schema is stored in: always UTC, always exactly 9
// fractional digits.
//
// time.RFC3339Nano ("2006-01-02T15:04:05.999999999Z07:00") trims trailing
// zero fractional digits, so it produces variable-width strings: a
// whole-second value formats as "...05Z" (20 chars) while a value with a
// nonzero fraction formats as "...05.5Z" (22 chars) or "...05.123456789Z"
// (30 chars). SQLite compares TEXT with memcmp, and this schema's lease and
// ordering predicates (lease_expires_at <= ?, lease_expires_at > ?,
// occurred_at/created_at ordering and keyset cursors) compare these strings
// directly in SQL. Byte-for-byte, '.' (0x2E) sorts before every digit
// (0x30-0x39), so at the position right after the seconds field, a
// whole-second value's 'Z' terminator is being compared against a
// fractional value's '.' -- and 'Z' (0x5A) sorts AFTER any digit, so
// "...05Z" > "...05.1Z" under memcmp even though 05.0 is chronologically
// EARLIER than 05.1. Fixing every value to the same 9-digit width removes
// the ambiguity: with equal width, memcmp order and chronological order
// agree.
const storageTimestampLayout = "2006-01-02T15:04:05.000000000Z"

// FormatStorageTime renders t in the fixed-width canonical form. Every
// timestamp write in this package must go through this function (or a
// helper that delegates to it) instead of formatting with
// time.RFC3339Nano directly.
func FormatStorageTime(t time.Time) string {
	return t.UTC().Format(storageTimestampLayout)
}

// formatStorageTime is the unexported alias for backwards compatibility
// with internal package code.
func formatStorageTime(t time.Time) string {
	return FormatStorageTime(t)
}

// parseStorageTime parses a stored timestamp. It accepts both the new
// fixed-width form and every width time.RFC3339Nano can produce (values
// written before this migration, and interchange imports authored
// elsewhere).
//
// This must parse against time.RFC3339Nano, not storageTimestampLayout:
// Go's time.Parse leniency on fractional-second width is a property of the
// reference layout's OWN fractional marker, not universal. A reference
// layout using zeros (".000000000", storageTimestampLayout's form) demands
// the input contain exactly that many digits -- "...05Z" fails to parse
// against it with "cannot parse \"Z\" as \".000000000\"". A reference layout
// using nines (".999999999", RFC3339Nano's form) is the one that accepts
// any digit count from 0 to 9, including none at all. So parsing must use
// the lenient nines-style layout; only formatting uses the fixed
// zeros-style one.
//
// A non-UTC offset is rejected explicitly after parsing (RFC3339Nano's
// "Z07:00" accepts any offset) -- every timestamp in this schema is stored
// as UTC.
func parseStorageTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, errors.New("storage timestamp must be UTC")
	}
	return parsed.UTC(), nil
}
