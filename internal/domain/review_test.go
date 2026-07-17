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
