package domain_test

import (
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

const decisionSessionID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

func TestRecordDecisionValidateNormalizesDefaultsAndCopies(t *testing.T) {
	issue := "issue-42"
	session := decisionSessionID
	supersedes := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	input := domain.RecordDecisionInput{
		IssueID: &issue, Title: "  Choice  ", Summary: "  Short  ",
		Content: "\n  **exact**  \n", SupersedesID: &supersedes, SessionID: &session,
	}
	normalized, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.IssueID == input.IssueID || normalized.IssueID == nil || *normalized.IssueID != "ISSUE-42" ||
		normalized.Status != domain.DecisionStatusActive || normalized.Content != input.Content ||
		normalized.SessionID == input.SessionID || normalized.SupersedesID == input.SupersedesID {
		t.Fatalf("normalized = %#v", normalized)
	}
	issue, session, supersedes = "changed", "changed", "changed"
	if *normalized.IssueID != "ISSUE-42" || *normalized.SessionID != decisionSessionID ||
		*normalized.SupersedesID != "01ARZ3NDEKTSV4RRFFQ69G5FAX" {
		t.Fatalf("normalized pointers share caller storage: %#v", normalized)
	}
}

func TestRecordDecisionValidateProjectAndStatuses(t *testing.T) {
	for _, status := range []domain.DecisionStatus{
		domain.DecisionStatusActive, domain.DecisionStatusSuperseded, domain.DecisionStatusRejected,
	} {
		normalized, err := (domain.RecordDecisionInput{Title: "Title", Summary: "Summary", Status: status}).Validate()
		if err != nil || normalized.Status != status || normalized.IssueID != nil {
			t.Fatalf("status %q normalized=%#v error=%v", status, normalized, err)
		}
	}
}

func TestRecordDecisionValidateRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name  string
		input domain.RecordDecisionInput
		code  string
	}{
		{"issue", domain.RecordDecisionInput{IssueID: stringPtr("bad"), Title: "t", Summary: "s"}, domain.CodeInvalidArgument},
		{"title", domain.RecordDecisionInput{Title: " ", Summary: "s"}, domain.CodeInvalidArgument},
		{"summary", domain.RecordDecisionInput{Title: "t", Summary: "\t"}, domain.CodeInvalidArgument},
		{"content", domain.RecordDecisionInput{Title: "t", Summary: "s", Content: "bad\x00"}, domain.CodeInvalidArgument},
		{"session", domain.RecordDecisionInput{Title: "t", Summary: "s", SessionID: stringPtr("bad")}, domain.CodeInvalidArgument},
		{"supersedes", domain.RecordDecisionInput{Title: "t", Summary: "s", SupersedesID: stringPtr("bad")}, domain.CodeInvalidArgument},
		{"status", domain.RecordDecisionInput{Title: "t", Summary: "s", Status: "other"}, domain.CodeInvalidArgument},
		{"shape", domain.RecordDecisionInput{Title: "t", Summary: "s", Status: domain.DecisionStatusRejected, SupersedesID: stringPtr("01ARZ3NDEKTSV4RRFFQ69G5FAX")}, domain.CodeInvalidArgument},
		{"content limit", domain.RecordDecisionInput{Title: "t", Summary: "s", Content: strings.Repeat("x", domain.MaxDecisionContentRunes+1)}, domain.CodeLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.input.Validate(); !errors.Is(err, &domain.Error{Code: test.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestCloneDecisionCopiesPointers(t *testing.T) {
	issue, supersedes, session := "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAX", decisionSessionID
	decision := domain.Decision{IssueID: &issue, SupersedesID: &supersedes, CreatedBySessionID: &session}
	clone := domain.CloneDecision(decision)
	if clone.IssueID == decision.IssueID || clone.SupersedesID == decision.SupersedesID || clone.CreatedBySessionID == decision.CreatedBySessionID {
		t.Fatal("CloneDecision shared pointer storage")
	}
}

func stringPtr(value string) *string { return &value }
