package domain_test

import (
	"encoding/json"
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
	title := "artifact"
	metadata := json.RawMessage(`{"kind":"source"}`)
	valid := domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Kind: domain.AttemptNoteKindCheckpoint,
		Content: "checkpoint", NextSteps: []string{"resume tests"}, Important: true,
		Artifacts: []domain.ArtifactInput{{
			Type: domain.ArtifactTypeFile, URI: "internal/application/attempt_service.go", Title: &title, Metadata: metadata,
		}},
	}

	normalized, err := valid.Validate()
	if err != nil || normalized.NextSteps[0] != "resume tests" || !normalized.Important ||
		len(normalized.Artifacts) != 1 || string(normalized.Artifacts[0].Metadata) != `{"kind":"source"}` {
		t.Fatalf("valid input = %#v, %v", normalized, err)
	}
	normalized.NextSteps[0] = "changed"
	if valid.NextSteps[0] != "resume tests" {
		t.Fatal("next steps were not defensively copied")
	}
	title = "changed"
	metadata[2] = 'x'
	if *normalized.Artifacts[0].Title != "artifact" || string(normalized.Artifacts[0].Metadata) != `{"kind":"source"}` {
		t.Fatal("artifacts were not defensively copied")
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
		{AttemptID: valid.AttemptID, LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note",
			Artifacts: make([]domain.ArtifactInput, domain.MaxArtifactsPerAttemptMutation+1)},
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
	title := "result"
	metadata := json.RawMessage(`{"kind":"result"}`)
	input := domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary",
		NextSteps: next, Verification: verification, TargetIssueStatus: &target,
		Artifacts: []domain.ArtifactInput{{
			Type: domain.ArtifactTypeFile, URI: "build/result.txt", Title: &title, Metadata: metadata,
		}},
	}

	normalized, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	next[0], verification[0] = "changed", "changed"
	if normalized.NextSteps[0] != "resume" || normalized.Verification[0] != "tests passed" {
		t.Fatal("finish slices were not copied")
	}
	title = "changed"
	metadata[2] = 'x'
	if len(normalized.Artifacts) != 1 || *normalized.Artifacts[0].Title != "result" ||
		string(normalized.Artifacts[0].Metadata) != `{"kind":"result"}` {
		t.Fatalf("finish artifacts were not defensively copied: %#v", normalized.Artifacts)
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
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary",
			Artifacts: []domain.ArtifactInput{{Type: domain.ArtifactTypeFile, URI: "../outside"}}},
		{AttemptID: input.AttemptID, LeaseToken: "token", Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary",
			Artifacts: make([]domain.ArtifactInput, domain.MaxArtifactsPerAttemptMutation+1)},
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

func TestAttemptInputsNormalizeOptionalSessionIDs(t *testing.T) {
	sessionID := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	claim, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1", SessionID: &sessionID}).Validate()
	if err != nil || claim.SessionID == nil || *claim.SessionID != sessionID || claim.SessionID == &sessionID {
		t.Fatalf("claim session = %#v, %v", claim.SessionID, err)
	}
	renew, err := (domain.RenewAttemptInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", SessionID: &sessionID}).Validate()
	if err != nil || renew.SessionID == nil || *renew.SessionID != sessionID {
		t.Fatalf("renew session = %#v, %v", renew.SessionID, err)
	}
	note, err := (domain.SaveAttemptNoteInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Kind: domain.AttemptNoteKindCheckpoint, Content: "checkpoint", SessionID: &sessionID}).Validate()
	if err != nil || note.SessionID == nil || *note.SessionID != sessionID {
		t.Fatalf("note session = %#v, %v", note.SessionID, err)
	}
	finish, err := (domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Outcome: domain.AttemptOutcomeFailed,
		ResultSummary: "failed", FailureReasonCode: optionalFailurePointer(domain.FailureReasonOther), SessionID: &sessionID,
	}).Validate()
	if err != nil || finish.SessionID == nil || *finish.SessionID != sessionID {
		t.Fatalf("finish session = %#v, %v", finish.SessionID, err)
	}
	sessionID = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	if *claim.SessionID != "01BX5ZZKBKACTAV9WEVGEMMVRZ" || *renew.SessionID != "01BX5ZZKBKACTAV9WEVGEMMVRZ" ||
		*note.SessionID != "01BX5ZZKBKACTAV9WEVGEMMVRZ" || *finish.SessionID != "01BX5ZZKBKACTAV9WEVGEMMVRZ" {
		t.Fatal("session IDs were not defensively copied")
	}
	for _, input := range []struct {
		name string
		err  error
	}{
		{"claim", func() error {
			_, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1", SessionID: optionalStringPointer("bad")}).Validate()
			return err
		}()},
		{"renew", func() error {
			_, err := (domain.RenewAttemptInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", SessionID: optionalStringPointer("bad")}).Validate()
			return err
		}()},
		{"note", func() error {
			_, err := (domain.SaveAttemptNoteInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note", SessionID: optionalStringPointer("bad")}).Validate()
			return err
		}()},
		{"finish", func() error {
			_, err := (domain.FinishAttemptInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", Outcome: domain.AttemptOutcomeFailed, ResultSummary: "failed", FailureReasonCode: optionalFailurePointer(domain.FailureReasonOther), SessionID: optionalStringPointer("bad")}).Validate()
			return err
		}()},
	} {
		if !errors.Is(input.err, &domain.Error{Code: domain.CodeInvalidArgument}) {
			t.Fatalf("%s session error = %v", input.name, input.err)
		}
	}
}

func optionalStringPointer(value string) *string                                      { return &value }
func optionalFailurePointer(value domain.FailureReasonCode) *domain.FailureReasonCode { return &value }
