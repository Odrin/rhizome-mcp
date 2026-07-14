package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestArchiveIssueInputValidateNormalizesIdentifier(t *testing.T) {
	input, err := (domain.ArchiveIssueInput{
		IssueID: "issue-7", ExpectedVersion: 3,
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if input.IssueID != "ISSUE-7" || input.ExpectedVersion != 3 {
		t.Fatalf("normalized input = %#v", input)
	}
}

func TestArchiveIssueInputValidateRejectsMissingOrInvalidValues(t *testing.T) {
	tests := []domain.ArchiveIssueInput{
		{IssueID: "", ExpectedVersion: 1},
		{IssueID: "not-an-issue", ExpectedVersion: 1},
		{IssueID: "ISSUE-1", ExpectedVersion: 0},
	}
	for _, input := range tests {
		_, err := input.Validate()
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("Validate(%#v) error = %v, want domain error", input, err)
		}
		if domainErr.Code != domain.CodeValidationError {
			t.Fatalf("Validate(%#v) code = %q, want %q", input, domainErr.Code, domain.CodeValidationError)
		}
		if domainErr.Retryable {
			t.Fatalf("Validate(%#v) unexpectedly retryable", input)
		}
	}
}
