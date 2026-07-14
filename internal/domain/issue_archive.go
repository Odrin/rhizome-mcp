package domain

// ArchiveIssueInput is a typed request to archive one issue. IssueID accepts a
// canonical ULID or ISSUE-N. ExpectedVersion is required for optimistic
// concurrency.
type ArchiveIssueInput struct {
	IssueID         string
	ExpectedVersion int64
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
	return ArchiveIssueInput{
		IssueID:         identifier.Value,
		ExpectedVersion: input.ExpectedVersion,
	}, nil
}
