package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

// seedAttemptGateSnapshot writes a minimal attempt_gate_snapshots row
// directly, since inserting one is an unexported package-internal helper
// (ISSUE-170) meant to be called from inside a live claim transaction, not
// from an external test.
func seedAttemptGateSnapshot(t *testing.T, fixture *attemptTestFixture, attemptID string, requirements []domain.PolicyRequirement) {
	t.Helper()
	snapshot, err := domain.NewGateSnapshot(requirements, []domain.SourcePolicyRef{{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Version: 1}}, 1, fixture.clock.Now())
	if err != nil {
		t.Fatalf("NewGateSnapshot() error = %v", err)
	}
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		requirementsJSON := `[`
		for index, requirement := range requirements {
			if index > 0 {
				requirementsJSON += ","
			}
			requirementsJSON += `{"policy_id":"` + requirement.PolicyID + `","key":"` + requirement.Key + `","kind":"` + string(requirement.Kind) + `","evidence_key":"` + requirement.EvidenceKey + `"`
			if requirement.AllowNotApplicable {
				requirementsJSON += `,"allow_not_applicable":true`
			}
			requirementsJSON += `}`
		}
		requirementsJSON += `]`
		_, err := tx.ExecContext(ctx, `INSERT INTO attempt_gate_snapshots(
			attempt_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at
		) VALUES (?, ?, '[{"policy_id":"01ARZ3NDEKTSV4RRFFQ69G5FE0","version":1}]', ?, ?, ?)`,
			attemptID, requirementsJSON, snapshot.Fingerprint, snapshot.IssueVersion, sqlite.FormatStorageTime(fixture.clock.Now()))
		return err
	}); err != nil {
		t.Fatalf("seed attempt gate snapshot: %v", err)
	}
}

func seedGateEvidenceArtifact(t *testing.T, fixture *attemptTestFixture, id, issueID, attemptID string) {
	t.Helper()
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, created_at)
			VALUES (?, ?, ?, 'file', 'path/to/file', ?)`, id, issueID, attemptID, sqlite.FormatStorageTime(fixture.clock.Now()))
		return err
	}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

func claimReadyIssueForEvidence(t *testing.T, fixture *attemptTestFixture, title string) (issueID string, claimed application.ClaimIssueResult) {
	t.Helper()
	issue := createAttemptIssue(t, fixture, title, domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.Issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	return issue.Issue.ID, claim
}

func TestSubmitGateEvidenceValidatesAgainstSnapshotAndUpserts(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-upsert")
	defer fixture.close()
	issueID, claimed := claimReadyIssueForEvidence(t, fixture, "evidence issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	artifactID := "01ARZ3NDEKTSV4RRFFQ69G5FE1"
	seedGateEvidenceArtifact(t, fixture, artifactID, issueID, claimed.Attempt.ID)

	first, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "implemented the feature", ArtifactIDs: []string{artifactID},
	})
	if err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}
	if first.Evidence.Version != 1 || first.Evidence.IssueID != issueID || len(first.Evidence.ArtifactIDs) != 1 {
		t.Fatalf("first submission = %#v, want version 1 scoped to the issue with the artifact", first.Evidence)
	}

	fixture.clock.Advance(time.Minute)
	second, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "implemented the feature, revised", ArtifactIDs: []string{artifactID},
	})
	if err != nil {
		t.Fatalf("SubmitGateEvidence() replace error = %v", err)
	}
	if second.Evidence.ID != first.Evidence.ID || second.Evidence.Version != 2 || second.Evidence.Summary != "implemented the feature, revised" {
		t.Fatalf("second submission = %#v, want an in-place version-2 upsert of the same record", second.Evidence)
	}

	list, err := fixture.attempts.ListAttemptEvidence(fixture.ctx, claimed.Attempt.ID)
	if err != nil {
		t.Fatalf("ListAttemptEvidence() error = %v", err)
	}
	if len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("ListAttemptEvidence() = %#v, want exactly one current (version 2) record", list)
	}
}

func TestSubmitGateEvidenceRejectsUnknownKey(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-unknown-key")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "no snapshot issue")
	// No snapshot seeded at all: compatibility mode (docs/02 §17.10) treats
	// this as an empty requirement set, so every key is unknown.
	_, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "nonexistent",
		Result: domain.EvidenceResultSatisfied, Summary: "summary",
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestSubmitGateEvidenceForbidsNotApplicableUnlessAllowed(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-not-applicable")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "not applicable issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "strict", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "strict_key"},
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "lenient", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "lenient_key", AllowNotApplicable: true},
	})

	_, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "strict_key",
		Result: domain.EvidenceResultNotApplicable, Summary: "n/a",
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)

	allowed, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "lenient_key",
		Result: domain.EvidenceResultNotApplicable, Summary: "n/a",
	})
	if err != nil {
		t.Fatalf("SubmitGateEvidence() on the lenient key error = %v", err)
	}
	if allowed.Evidence.Result != domain.EvidenceResultNotApplicable {
		t.Fatalf("Result = %q, want not_applicable", allowed.Evidence.Result)
	}
}

func TestSubmitGateEvidenceRejectsCrossIssueArtifact(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-cross-issue")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "issue A")
	otherIssue := createAttemptIssue(t, fixture, "issue B", domain.StatusReady)
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	foreignArtifactID := "01ARZ3NDEKTSV4RRFFQ69G5FE2"
	seedGateEvidenceArtifact(t, fixture, foreignArtifactID, otherIssue.Issue.ID, claimed.Attempt.ID)

	_, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "summary", ArtifactIDs: []string{foreignArtifactID},
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestSubmitGateEvidenceRejectsWrongToken(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-wrong-token")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "wrong token issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	_, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: "wrong-token-wrong-token-wrong-token", Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "summary",
	})
	assertDomainCode(t, err, domain.CodeInvalidLeaseToken)
}

func TestSubmitGateEvidenceRejectsFinishedAttempt(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-finished")
	defer fixture.close()
	issueID, claimed := claimReadyIssueForEvidence(t, fixture, "finished issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	_, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPtrForEvidence(domain.StatusReady),
	})
	if err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	_ = issueID

	_, err = fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "too late",
	})
	assertDomainCode(t, err, domain.CodeAttemptNotActive)
}

func TestSubmitGateEvidenceRejectsReviewAttempt(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-review-kind")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "review kind issue", domain.StatusReady)
	claimed, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.Issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPtrForEvidence(domain.StatusReview),
	}); err != nil {
		t.Fatalf("FinishAttempt() to review error = %v", err)
	}

	reviewClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.Issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue() on a review-status issue error = %v", err)
	}
	if reviewClaim.Attempt.Kind != domain.AttemptKindReview {
		t.Fatalf("claimed attempt kind = %q, want review", reviewClaim.Attempt.Kind)
	}
	seedAttemptGateSnapshot(t, fixture, reviewClaim.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	_, err = fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: reviewClaim.Attempt.ID, LeaseToken: reviewClaim.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "review attempts cannot submit evidence",
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestSubmitGateEvidenceIdempotentReplayAndConflict(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-idempotency")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "idempotent issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	key := "evidence-retry"
	input := domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "summary", IdempotencyKey: &key,
	}
	first, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, input)
	if err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}
	replay, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, input)
	if err != nil {
		t.Fatalf("SubmitGateEvidence() replay error = %v", err)
	}
	if replay.Evidence.Version != first.Evidence.Version {
		t.Fatalf("replay version = %d, want the original %d (no second increment)", replay.Evidence.Version, first.Evidence.Version)
	}

	conflict := input
	conflict.Summary = "a different summary"
	_, err = fixture.attempts.SubmitGateEvidence(fixture.ctx, conflict)
	assertDomainCode(t, err, domain.CodeIdempotencyConflict)
}

func TestSubmitGateEvidenceConcurrentUpsertsAreRaceSafe(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-race")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "race issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for index := 0; index < 5; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
				AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
				Result: domain.EvidenceResultSatisfied, Summary: "concurrent submission",
			})
			errs[index] = err
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent submission %d error = %v", index, err)
		}
	}
	list, err := fixture.attempts.ListAttemptEvidence(fixture.ctx, claimed.Attempt.ID)
	if err != nil {
		t.Fatalf("ListAttemptEvidence() error = %v", err)
	}
	if len(list) != 1 || list[0].Version != 5 {
		t.Fatalf("list = %#v, want exactly one record that absorbed all 5 upserts without corruption", list)
	}
}

func TestGateEvidenceIsImmutableAndNeverDeletedOnceAttemptIsInactive(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-immutable")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "immutable issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	submitted, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "summary",
	})
	if err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, domain.FinishAttemptInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Outcome: domain.AttemptOutcomeCompleted,
		ResultSummary: "done", TargetIssueStatus: statusPtrForEvidence(domain.StatusReady),
	}); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}

	// DELETE is unconditionally rejected; UPDATE is rejected once the
	// owning attempt is no longer active (docs/02: "Records become
	// immutable when the attempt ceases to be active").
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE gate_evidence SET summary = 'tampered' WHERE id = ?`, submitted.Evidence.ID)
		return err
	}); err == nil {
		t.Fatal("UPDATE on gate_evidence after its attempt finished succeeded, want the immutability trigger to reject it")
	}
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM gate_evidence WHERE id = ?`, submitted.Evidence.ID)
		return err
	}); err == nil {
		t.Fatal("DELETE on gate_evidence succeeded, want it to be rejected unconditionally")
	}

	// Still visible in history/list after the attempt ends.
	list, err := fixture.attempts.ListAttemptEvidence(fixture.ctx, claimed.Attempt.ID)
	if err != nil {
		t.Fatalf("ListAttemptEvidence() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != submitted.Evidence.ID {
		t.Fatalf("list = %#v, want the evidence record to remain visible after its attempt ended", list)
	}
}

func TestAttemptEvidenceReturnsStorageCorruptForInvalidRows(t *testing.T) {
	fixture := newAttemptTestFixture(t, "gate-evidence-corrupt")
	defer fixture.close()
	_, claimed := claimReadyIssueForEvidence(t, fixture, "corrupt issue")
	seedAttemptGateSnapshot(t, fixture, claimed.Attempt.ID, []domain.PolicyRequirement{
		{PolicyID: "01ARZ3NDEKTSV4RRFFQ69G5FE0", Key: "impl", Kind: domain.RequirementKindAttemptEvidence, EvidenceKey: "implementation"},
	})
	if _, err := fixture.attempts.SubmitGateEvidence(fixture.ctx, domain.SubmitGateEvidenceInput{
		AttemptID: claimed.Attempt.ID, LeaseToken: claimed.LeaseToken, Key: "implementation",
		Result: domain.EvidenceResultSatisfied, Summary: "summary",
	}); err != nil {
		t.Fatalf("SubmitGateEvidence() error = %v", err)
	}
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE gate_evidence SET artifact_ids_json = '"not-an-array"' WHERE attempt_id = ?`, claimed.Attempt.ID)
		return err
	}); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	_, err := fixture.attempts.ListAttemptEvidence(fixture.ctx, claimed.Attempt.ID)
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func statusPtrForEvidence(status domain.Status) *domain.Status {
	return &status
}
