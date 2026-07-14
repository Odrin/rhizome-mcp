package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestParseIssueIdentifier(t *testing.T) {
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	tests := []struct {
		name       string
		input      string
		kind       domain.IssueIdentifierKind
		value      string
		sequenceNo int64
	}{
		{name: "canonical ULID", input: ulid, kind: domain.IssueIdentifierInternalID, value: ulid},
		{name: "uppercase display", input: "ISSUE-42", kind: domain.IssueIdentifierDisplayID, value: "ISSUE-42", sequenceNo: 42},
		{name: "lowercase prefix", input: "issue-42", kind: domain.IssueIdentifierDisplayID, value: "ISSUE-42", sequenceNo: 42},
		{name: "mixed case prefix", input: "IsSuE-42", kind: domain.IssueIdentifierDisplayID, value: "ISSUE-42", sequenceNo: 42},
		{name: "maximum sequence", input: "ISSUE-9223372036854775807", kind: domain.IssueIdentifierDisplayID, value: "ISSUE-9223372036854775807", sequenceNo: 9223372036854775807},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseIssueIdentifier(tt.input)
			if err != nil {
				t.Fatalf("ParseIssueIdentifier() error = %v", err)
			}
			if got.Kind != tt.kind || got.Value != tt.value || got.SequenceNo != tt.sequenceNo {
				t.Fatalf("ParseIssueIdentifier() = %#v", got)
			}
		})
	}

	for _, input := range []string{
		"",
		"01arz3ndektsv4rrffq69g5fav",
		"ISSUE",
		"ISSUE-",
		"ISSUE-0",
		"ISSUE-00",
		"ISSUE-01",
		"ISSUE-9223372036854775808",
		"ISSUE-1 ",
		" ISSUE-1",
		"ISSUE+1",
		"ISSUE-1.0",
		"1",
	} {
		t.Run("invalid_"+input, func(t *testing.T) {
			_, err := domain.ParseIssueIdentifier(input)
			if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
				t.Fatalf("ParseIssueIdentifier(%q) error = %v, want INVALID_ARGUMENT", input, err)
			}
		})
	}
}
