package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestAllStoredStatusTransitions(t *testing.T) {
	statuses := []domain.Status{
		domain.StatusOpen,
		domain.StatusReady,
		domain.StatusBlocked,
		domain.StatusReview,
		domain.StatusDone,
		domain.StatusCancelled,
	}
	allowed := map[[2]domain.Status]bool{
		{domain.StatusOpen, domain.StatusReady}:        true,
		{domain.StatusOpen, domain.StatusCancelled}:    true,
		{domain.StatusReady, domain.StatusBlocked}:     true,
		{domain.StatusReady, domain.StatusReview}:      true,
		{domain.StatusReady, domain.StatusDone}:        true,
		{domain.StatusReady, domain.StatusCancelled}:   true,
		{domain.StatusBlocked, domain.StatusReady}:     true,
		{domain.StatusBlocked, domain.StatusCancelled}: true,
		{domain.StatusReview, domain.StatusReady}:      true,
		{domain.StatusReview, domain.StatusBlocked}:    true,
		{domain.StatusReview, domain.StatusDone}:       true,
		{domain.StatusReview, domain.StatusCancelled}:  true,
		{domain.StatusDone, domain.StatusReady}:        true,
		{domain.StatusCancelled, domain.StatusOpen}:    true,
	}

	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]domain.Status{from, to}]
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				if got := domain.CanTransition(from, to); got != want {
					t.Fatalf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
				}
			})
		}
	}
}

func TestApplyStatusTransitionBlockedReason(t *testing.T) {
	tests := []struct {
		name       string
		from       domain.Status
		to         domain.Status
		reason     string
		wantReason string
		wantCode   string
	}{
		{name: "enter blocked", from: domain.StatusReady, to: domain.StatusBlocked, reason: "external dependency", wantReason: "external dependency"},
		{name: "enter blocked blank", from: domain.StatusReady, to: domain.StatusBlocked, reason: "  ", wantCode: domain.CodeInvalidArgument},
		{name: "leave blocked clears reason", from: domain.StatusBlocked, to: domain.StatusReady, reason: "old reason", wantReason: ""},
		{name: "non-blocked target clears supplied reason", from: domain.StatusReady, to: domain.StatusDone, reason: "stale", wantReason: ""},
		{name: "invalid transition", from: domain.StatusOpen, to: domain.StatusDone, wantCode: domain.CodeInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ApplyStatusTransition(tt.from, tt.to, tt.reason)
			if tt.wantCode != "" {
				if !errors.Is(err, &domain.Error{Code: tt.wantCode}) {
					t.Fatalf("ApplyStatusTransition() error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyStatusTransition() error = %v", err)
			}
			if got != tt.wantReason {
				t.Fatalf("ApplyStatusTransition() reason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

func TestStoredStatusRejectsInProgress(t *testing.T) {
	if _, err := domain.ParseStatus("in_progress"); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("ParseStatus(in_progress) error = %v, want INVALID_ARGUMENT", err)
	}
	if got, err := domain.ParseEffectiveStatus("in_progress"); err != nil || got != domain.EffectiveStatusInProgress {
		t.Fatalf("ParseEffectiveStatus(in_progress) = %q, %v", got, err)
	}
	if got, err := domain.EffectiveStatusFor(domain.StatusReady, true); err != nil || got != domain.EffectiveStatusInProgress {
		t.Fatalf("EffectiveStatusFor(ready, true) = %q, %v", got, err)
	}
}

func TestEnumParsing(t *testing.T) {
	tests := []struct {
		name  string
		parse func() error
	}{
		{name: "invalid type", parse: func() error { _, err := domain.ParseType("story"); return err }},
		{name: "invalid status", parse: func() error { _, err := domain.ParseStatus("IN_PROGRESS"); return err }},
		{name: "invalid effective status", parse: func() error { _, err := domain.ParseEffectiveStatus("working"); return err }},
		{name: "invalid priority", parse: func() error { _, err := domain.ParsePriority("urgent"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
				t.Fatalf("parse error = %v, want INVALID_ARGUMENT", err)
			}
		})
	}
}
