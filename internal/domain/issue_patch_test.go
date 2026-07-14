package domain_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestUpdateIssueInputValidatePreservesAbsentAndNull(t *testing.T) {
	input, err := (domain.UpdateIssueInput{
		IssueID:         "issue-7",
		ExpectedVersion: 1,
		Changes: domain.IssuePatch{
			Description: domain.OptionalString{Set: true, Value: nil},
		},
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if !input.Changes.Description.Set || input.Changes.Description.Value != nil ||
		input.Changes.AcceptanceCriteria.Set {
		t.Fatalf("patch presence = %#v", input.Changes)
	}
}

func TestUpdateIssueInputValidateRejectsEmptyAndInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		input domain.UpdateIssueInput
		code  string
	}{
		{
			name:  "missing expected version",
			input: domain.UpdateIssueInput{IssueID: "ISSUE-1", ExpectedVersion: 0, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "x"}}},
			code:  domain.CodeValidationError,
		},
		{
			name:  "empty changes",
			input: domain.UpdateIssueInput{IssueID: "ISSUE-1", ExpectedVersion: 1},
			code:  domain.CodeValidationError,
		},
		{
			name:  "blank title",
			input: domain.UpdateIssueInput{IssueID: "ISSUE-1", ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: " "}}},
			code:  domain.CodeValidationError,
		},
		{
			name:  "invalid status",
			input: domain.UpdateIssueInput{IssueID: "ISSUE-1", ExpectedVersion: 1, Changes: domain.IssuePatch{Status: domain.OptionalValue[domain.Status]{Set: true, Value: "in_progress"}}},
			code:  domain.CodeValidationError,
		},
		{
			name:  "invalid issue identifier",
			input: domain.UpdateIssueInput{IssueID: "not-an-issue", ExpectedVersion: 1, Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "x"}}},
			code:  domain.CodeValidationError,
		},
		{
			name: "invalid parent identifier",
			input: domain.UpdateIssueInput{
				IssueID: "ISSUE-1", ExpectedVersion: 1,
				Changes: domain.IssuePatch{ParentID: domain.OptionalString{Set: true, Value: stringPointer("not-an-issue")}},
			},
			code: domain.CodeValidationError,
		},
		{
			name: "title limit",
			input: domain.UpdateIssueInput{
				IssueID: "ISSUE-1", ExpectedVersion: 1,
				Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: strings.Repeat("x", domain.MaxTitleRunes+1)}},
			},
			code: domain.CodeLimitExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input.Validate()
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("Validate() error = %v, want domain error", err)
			}
			if domainErr.Code != tt.code {
				t.Fatalf("Validate() error code = %q, want %q", domainErr.Code, tt.code)
			}
			if domainErr.Retryable {
				t.Fatal("Validate() error is retryable")
			}
		})
	}
}

func TestUpdateIssueInputValidatePreservesValidationDetails(t *testing.T) {
	_, err := (domain.UpdateIssueInput{
		IssueID:         "ISSUE-1",
		ExpectedVersion: 1,
		Changes: domain.IssuePatch{
			Title: domain.OptionalValue[string]{Set: true, Value: " "},
		},
	}).Validate()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("Validate() error = %v, want domain error", err)
	}
	if len(domainErr.Details) != 1 || domainErr.Details[0].Field != "title" ||
		domainErr.Details[0].Code != "REQUIRED" {
		t.Fatalf("Validate() details = %#v", domainErr.Details)
	}
	if domainErr.Retryable {
		t.Fatal("Validate() error is retryable")
	}
}

func TestApplyIssuePatchBlockedReasonAndChangedFields(t *testing.T) {
	reason := "waiting"
	current := domain.Issue{Type: domain.TypeTask, Status: domain.StatusReady}
	next, changed, err := domain.ApplyIssuePatch(current, domain.IssuePatch{
		Status:        domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusBlocked},
		BlockedReason: domain.OptionalString{Set: true, Value: &reason},
		Priority:      domain.OptionalValue[domain.Priority]{Set: true, Value: domain.PriorityHigh},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != domain.StatusBlocked || next.BlockedReason == nil || *next.BlockedReason != reason {
		t.Fatalf("next = %#v", next)
	}
	if want := []string{"blocked_reason", "priority", "status"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	_, _, err = domain.ApplyIssuePatch(current, domain.IssuePatch{
		Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusBlocked},
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("blocked without reason error = %v", err)
	}
	_, changed, err = domain.ApplyIssuePatch(next, domain.IssuePatch{
		Status:        domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusReady},
		BlockedReason: domain.OptionalString{Set: true, Value: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"blocked_reason", "status"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
}

func TestApplyIssuePatchRejectsInvalidStatusTransition(t *testing.T) {
	current := domain.Issue{Type: domain.TypeTask, Status: domain.StatusOpen}
	next, changed, err := domain.ApplyIssuePatch(current, domain.IssuePatch{
		Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusDone},
	})
	if err == nil {
		t.Fatal("ApplyIssuePatch() error = nil, want invalid status transition")
	}
	domainErr, ok := err.(*domain.Error)
	if !ok {
		t.Fatalf("ApplyIssuePatch() error type = %T, want *domain.Error", err)
	}
	if domainErr.Code != "INVALID_STATUS_TRANSITION" {
		t.Fatalf("ApplyIssuePatch() error code = %q, want INVALID_STATUS_TRANSITION", domainErr.Code)
	}
	if domainErr.Retryable {
		t.Fatal("ApplyIssuePatch() invalid transition error is retryable")
	}
	if !reflect.DeepEqual(next, domain.Issue{}) || changed != nil {
		t.Fatalf("ApplyIssuePatch() mutation = next %#v, changed %v; want no mutation", next, changed)
	}
}
