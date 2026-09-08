package domain

import (
	"encoding/json"
	"strings"
)

// ArchiveIssueInput is a typed request to archive one issue. IssueID accepts a
// canonical ULID or ISSUE-N. ExpectedVersion is required for optimistic
// concurrency.
type ArchiveIssueInput struct {
	IssueID         string
	ExpectedVersion int64
	IdempotencyKey  *string
}

// Validate checks request-local archive rules and normalizes the issue
// reference.
func (input ArchiveIssueInput) Validate() (normalized ArchiveIssueInput, err error) {
	defer func() {
		err = normalizeUpdateValidationError(err)
	}()
	if input.ExpectedVersion < 1 {
		return ArchiveIssueInput{}, validationError("expected_version", "REQUIRED", "must be at least 1")
	}
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return ArchiveIssueInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return ArchiveIssueInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return ArchiveIssueInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return ArchiveIssueInput{
		IssueID:         identifier.Value,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  idempotencyKey,
	}, nil
}

// CanonicalArchiveIssueRequest returns deterministic JSON for a normalized
// archive request. The idempotency key is intentionally excluded.
func CanonicalArchiveIssueRequest(input ArchiveIssueInput) ([]byte, error) {
	request := struct {
		IssueID         string `json:"issue_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{IssueID: input.IssueID, ExpectedVersion: input.ExpectedVersion}
	return json.Marshal(request)
}

// UnarchiveIssueInput is a typed request to reverse the archival visibility of one issue.
// IssueID accepts a canonical ULID or ISSUE-N. ExpectedVersion is required for optimistic concurrency.
type UnarchiveIssueInput struct {
	IssueID         string
	ExpectedVersion int64
	IdempotencyKey  *string
}

// Validate checks request-local unarchive rules and normalizes the issue reference.
func (input UnarchiveIssueInput) Validate() (normalized UnarchiveIssueInput, err error) {
	defer func() {
		err = normalizeUpdateValidationError(err)
	}()
	if input.ExpectedVersion < 1 {
		return UnarchiveIssueInput{}, validationError("expected_version", "REQUIRED", "must be at least 1")
	}
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return UnarchiveIssueInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return UnarchiveIssueInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return UnarchiveIssueInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return UnarchiveIssueInput{
		IssueID:         identifier.Value,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  idempotencyKey,
	}, nil
}

// CanonicalUnarchiveIssueRequest returns deterministic JSON for a normalized
// unarchive request. The idempotency key is intentionally excluded.
func CanonicalUnarchiveIssueRequest(input UnarchiveIssueInput) ([]byte, error) {
	request := struct {
		IssueID         string `json:"issue_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{IssueID: input.IssueID, ExpectedVersion: input.ExpectedVersion}
	return json.Marshal(request)
}
