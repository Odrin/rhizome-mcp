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
