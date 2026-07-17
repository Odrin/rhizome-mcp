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
