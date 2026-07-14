package domain_test

import (
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestAttemptLeaseInputValidation(t *testing.T) {
	for _, seconds := range []int{domain.MinLeaseSeconds - 1, domain.MaxLeaseSeconds + 1} {
		seconds := seconds
		if _, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1", LeaseSeconds: &seconds}).Validate(); err == nil {
			t.Fatalf("lease %d was accepted", seconds)
		}
	}
	input, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1"}).Validate()
	if err != nil || input.LeaseSeconds == nil || *input.LeaseSeconds != domain.DefaultLeaseSeconds {
		t.Fatalf("default lease = %#v, %v", input, err)
	}
	if _, err := (domain.RenewAttemptInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: ""}).Validate(); err == nil {
		t.Fatal("empty renewal token was accepted")
	}
}

func TestSaveAttemptNoteInputValidation(t *testing.T) {
	valid := domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Kind: domain.AttemptNoteKindCheckpoint,
		Content: "checkpoint", NextSteps: []string{"resume tests"}, Important: true,
	}

	normalized, err := valid.Validate()
	if err != nil || normalized.NextSteps[0] != "resume tests" || !normalized.Important {
		t.Fatalf("valid input = %#v, %v", normalized, err)
	}
	normalized.NextSteps[0] = "changed"
	if valid.NextSteps[0] != "resume tests" {
		t.Fatal("next steps were not defensively copied")
	}

	cases := []domain.SaveAttemptNoteInput{
		{AttemptID: "bad", LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note"},
		{AttemptID: strings.ToLower(valid.AttemptID), LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note"},
		{AttemptID: valid.AttemptID, LeaseToken: "", Kind: domain.AttemptNoteKindProgress, Content: "note"},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: "other", Content: "note"},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: " \t"},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: string([]byte{0xff})},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note", NextSteps: []string{" "}},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note", NextSteps: make([]string, domain.MaxAttemptNoteNextSteps+1)},
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: strings.Repeat("x", domain.MaxAttemptNoteRunes+1)},
	}
	for _, input := range cases {
		if _, err := input.Validate(); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) &&
			!errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
			t.Fatalf("input %#v error = %v", input, err)
		}
	}
}

func TestFinishAttemptInputValidationAndKindRules(t *testing.T) {
	target := domain.StatusReview
	next := []string{"resume"}
	verification := []string{"tests passed"}
	input := domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary",
		NextSteps: next, Verification: verification, TargetIssueStatus: &target,
	}
	normalized, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	next[0], verification[0] = "changed", "changed"
	if normalized.NextSteps[0] != "resume" || normalized.Verification[0] != "tests passed" {
		t.Fatal("finish slices were not copied")
	}
	if err := domain.ValidateFinishAttemptForKind(normalized, domain.AttemptKindWork); err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateFinishAttemptForKind(normalized, domain.AttemptKindReview); err == nil {
		t.Fatal("work shape accepted for review")
	}
	for _, bad := range []domain.FinishAttemptInput{
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: "bad", ResultSummary: "summary"},
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeFailed, ResultSummary: "summary"},
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeInterrupted, ResultSummary: "summary"},
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary", NextSteps: []string{" "}},
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary", Verification: []string{" "}},
	} {
		if _, err := bad.Validate(); err == nil {
			t.Fatalf("invalid finish input accepted: %#v", bad)
		}
	}
	ack := domain.AttemptAcknowledgement{IssueVersion: 1, LatestEventID: 0}
	input.AcknowledgedChanges = &ack
	if _, err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	ack.IssueVersion = 0
	if _, err := input.Validate(); err == nil {
		t.Fatal("invalid acknowledgement accepted")
	}
}
