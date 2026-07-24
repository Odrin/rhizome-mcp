package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestReviewRequestStatusParsingAndValidation(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  domain.ReviewRequestStatus
		code  string
	}{
		{name: "valid", value: "claimed", want: domain.ReviewRequestStatusClaimed},
		{name: "invalid", value: "in_progress", code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseReviewRequestStatus(tt.value)
			if tt.code != "" {
				if !errors.Is(err, &domain.Error{Code: tt.code}) {
					t.Fatalf("ParseReviewRequestStatus(%q) error = %v, want %s", tt.value, err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReviewRequestStatus(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseReviewRequestStatus(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestReviewEventTypeParsingAndValidation(t *testing.T) {
	got, err := domain.ParseReviewEventType("review_approved")
	if err != nil || got != domain.ReviewEventTypeApproved {
		t.Fatalf("ParseReviewEventType() = %q, %v", got, err)
	}
	if _, err := domain.ParseReviewEventType("reviewing"); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("ParseReviewEventType(invalid) error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestReviewWorkflowStatusesAndEventsParse(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  domain.ReviewRequestStatus
		code  string
	}{
		{name: "approved", value: "approved", want: domain.ReviewRequestStatusApproved},
		{name: "changes requested", value: "changes_requested", want: domain.ReviewRequestStatusChangesRequested},
		{name: "blocked", value: "blocked", want: domain.ReviewRequestStatusBlocked},
		{name: "invalid", value: "in_progress", code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseReviewRequestStatus(tt.value)
			if tt.code != "" {
				if !errors.Is(err, &domain.Error{Code: tt.code}) {
					t.Fatalf("ParseReviewRequestStatus(%q) error = %v, want %s", tt.value, err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReviewRequestStatus(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseReviewRequestStatus(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}

	eventTests := []struct {
		name  string
		value string
		want  domain.ReviewEventType
		code  string
	}{
		{name: "approved event", value: "review_approved", want: domain.ReviewEventTypeApproved},
		{name: "changes requested event", value: "review_changes_requested", want: domain.ReviewEventTypeChangesRequested},
		{name: "blocked event", value: "review_blocked", want: domain.ReviewEventTypeBlocked},
		{name: "invalid event", value: "reviewing", code: domain.CodeInvalidArgument},
	}
	for _, tt := range eventTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseReviewEventType(tt.value)
			if tt.code != "" {
				if !errors.Is(err, &domain.Error{Code: tt.code}) {
					t.Fatalf("ParseReviewEventType(%q) error = %v, want %s", tt.value, err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReviewEventType(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseReviewEventType(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestReplaceReviewRequestInputValidate(t *testing.T) {
	valid := domain.ReplaceReviewRequestInput{
		PredecessorRequestID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", PredecessorExpectedVersion: 1,
		TargetIssueVersion: 2, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"}, IdempotencyKey: "key-1",
	}
	if normalized, err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	} else if normalized.IdempotencyKey != "key-1" || len(normalized.ArtifactIDs) != 1 {
		t.Fatalf("Validate() = %+v", normalized)
	}

	tests := []struct {
		name  string
		input domain.ReplaceReviewRequestInput
		code  string
	}{
		{name: "blank predecessor id", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) { i.PredecessorRequestID = "  " }), code: domain.CodeInvalidArgument},
		{name: "zero predecessor version", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) { i.PredecessorExpectedVersion = 0 }), code: domain.CodeInvalidArgument},
		{name: "zero target issue version", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) { i.TargetIssueVersion = 0 }), code: domain.CodeInvalidArgument},
		{name: "negative target event id", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) { i.TargetEventID = -1 }), code: domain.CodeInvalidArgument},
		{name: "too many artifact ids", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) {
			i.ArtifactIDs = make([]string, domain.MaxReviewArtifactIDs+1)
		}), code: domain.CodeLimitExceeded},
		{name: "blank idempotency key", input: setField(valid, func(i *domain.ReplaceReviewRequestInput) { i.IdempotencyKey = "  " }), code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.input.Validate(); !errors.Is(err, &domain.Error{Code: tt.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, tt.code)
			}
		})
	}
}

func setField(base domain.ReplaceReviewRequestInput, mutate func(*domain.ReplaceReviewRequestInput)) domain.ReplaceReviewRequestInput {
	mutate(&base)
	return base
}

func TestCanonicalReplaceReviewRequestRequestExcludesIdempotencyKey(t *testing.T) {
	input := domain.ReplaceReviewRequestInput{
		PredecessorRequestID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", PredecessorExpectedVersion: 1,
		TargetIssueVersion: 2, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"}, IdempotencyKey: "key-1",
	}
	first, err := domain.CanonicalReplaceReviewRequestRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "different-key"
	second, err := domain.CanonicalReplaceReviewRequestRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical request changed with idempotency key: %s vs %s", first, second)
	}

	input.TargetIssueVersion = 3
	third, err := domain.CanonicalReplaceReviewRequestRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(third) {
		t.Fatalf("canonical request did not change with target_issue_version")
	}
}
