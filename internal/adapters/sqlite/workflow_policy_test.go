package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

const (
	workflowPolicyTestID    = "01ARZ3NDEKTSV4RRFFQ69G5FC0"
	workflowPolicyTestID2   = "01ARZ3NDEKTSV4RRFFQ69G5FC1"
	workflowPolicyTestID3   = "01ARZ3NDEKTSV4RRFFQ69G5FC2"
	workflowPolicySessionID = "01ARZ3NDEKTSV4RRFFQ69G5FC3"
)

func openWorkflowPolicyRepository(t *testing.T) (*sqlite.WorkflowPolicyRepository, *sqlite.DB, time.Time) {
	t.Helper()
	_, db, now := openIssueService(t)
	repository, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository() error = %v", err)
	}
	return repository, db, now
}

func errorsIsVersionConflict(err error) bool {
	return errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict})
}

func validPolicyInput() domain.WorkflowPolicyInput {
	return domain.WorkflowPolicyInput{
		Selector: domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirementInput{
			{Key: "acceptance_criteria", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
	}
}

func TestNewWorkflowPolicyRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := sqlite.NewWorkflowPolicyRepository(nil)
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
}

func TestWorkflowPolicyRepositoryCreatePolicyPersistsAndAudits(t *testing.T) {
	repository, db, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	seedCommentSession(t, db, workflowPolicySessionID, now)

	policy, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID: workflowPolicyTestID, Input: validPolicyInput(), SessionID: stringPtr(workflowPolicySessionID), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	if policy.ID != workflowPolicyTestID || policy.Status != domain.PolicyStatusActive || policy.Version != 1 {
		t.Fatalf("policy = %#v, want active version 1", policy)
	}
	if len(policy.Requirements) != 1 || policy.Requirements[0].PolicyID != workflowPolicyTestID {
		t.Fatalf("requirements = %#v, want one requirement carrying the policy ID", policy.Requirements)
	}

	var eventType, sessionID string
	var priorVersion sql.NullInt64
	var newVersion int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT event_type, session_id, prior_version, new_version FROM workflow_policy_events WHERE policy_id = ?`, workflowPolicyTestID).
			Scan(&eventType, &sessionID, &priorVersion, &newVersion)
	}); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if eventType != "policy_created" || sessionID != workflowPolicySessionID || priorVersion.Valid || newVersion != 1 {
		t.Fatalf("audit event = (%q, %q, %v, %d), want policy_created/session/NULL/1", eventType, sessionID, priorVersion, newVersion)
	}
}

func TestWorkflowPolicyRepositoryCreatePolicyRejectsInvalidInput(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	invalid := domain.WorkflowPolicyInput{Requirements: []domain.PolicyRequirementInput{{Key: "k", Kind: domain.RequirementKindIssueFieldNonblank, Field: "title"}}}
	_, err := repository.CreatePolicy(context.Background(), ports.CreateWorkflowPolicyCommand{
		ID: workflowPolicyTestID, Input: invalid, CreatedAt: now,
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestWorkflowPolicyRepositoryCreatePolicyIdempotentReplayAndConflict(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	command := ports.CreateWorkflowPolicyCommand{
		ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now,
		IdempotencyKey: "create-key", RequestHash: []byte("hash-a"),
	}
	first, err := repository.CreatePolicy(ctx, command)
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	replay := command
	replay.ID = workflowPolicyTestID2 // must be ignored: replay returns the original result
	second, err := repository.CreatePolicy(ctx, replay)
	if err != nil {
		t.Fatalf("CreatePolicy() replay error = %v", err)
	}
	if second.ID != first.ID || second.Version != first.Version {
		t.Fatalf("replay result = %#v, want the original %#v", second, first)
	}

	conflict := command
	conflict.RequestHash = []byte("hash-b")
	_, err = repository.CreatePolicy(ctx, conflict)
	assertDomainCode(t, err, domain.CodeIdempotencyConflict)
}

func TestWorkflowPolicyRepositoryGetPolicyNotFound(t *testing.T) {
	repository, _, _ := openWorkflowPolicyRepository(t)
	_, err := repository.GetPolicy(context.Background(), ports.GetWorkflowPolicyCommand{PolicyID: workflowPolicyTestID})
	assertDomainCode(t, err, domain.CodePolicyNotFound)
}

func TestWorkflowPolicyRepositoryListPoliciesOrdersFiltersAndPaginates(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	for index, id := range []string{workflowPolicyTestID, workflowPolicyTestID2, workflowPolicyTestID3} {
		if _, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
			ID: id, Input: validPolicyInput(), CreatedAt: now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatalf("CreatePolicy(%d) error = %v", index, err)
		}
	}
	archived := domain.PolicyStatusArchived
	if _, err := repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{
		PolicyID: workflowPolicyTestID, ExpectedVersion: 1, ArchivedAt: now.Add(time.Duration(3) * time.Second),
	}); err != nil {
		t.Fatalf("ArchivePolicy() error = %v", err)
	}

	full, err := repository.ListPolicies(ctx, ports.ListWorkflowPoliciesCommand{Input: domain.ListWorkflowPoliciesInput{Limit: 1}})
	if err != nil {
		t.Fatalf("ListPolicies() error = %v", err)
	}
	if len(full.Items) != 1 || !full.HasMore || full.NextCursor == nil || full.Items[0].ID != workflowPolicyTestID3 {
		t.Fatalf("first page = %#v, want one item (newest first) with more", full)
	}
	nextPage, err := repository.ListPolicies(ctx, ports.ListWorkflowPoliciesCommand{Input: domain.ListWorkflowPoliciesInput{Limit: 10, Cursor: *full.NextCursor}})
	if err != nil {
		t.Fatalf("ListPolicies() next page error = %v", err)
	}
	if len(nextPage.Items) != 2 || nextPage.HasMore {
		t.Fatalf("next page = %#v, want the remaining two items", nextPage)
	}

	filtered, err := repository.ListPolicies(ctx, ports.ListWorkflowPoliciesCommand{Input: domain.ListWorkflowPoliciesInput{Status: &archived, Limit: 10}})
	if err != nil {
		t.Fatalf("ListPolicies() filtered error = %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].ID != workflowPolicyTestID {
		t.Fatalf("archived filter = %#v, want exactly the archived policy", filtered.Items)
	}
}

func TestWorkflowPolicyRepositoryUpdatePolicyOptimisticAndAudited(t *testing.T) {
	repository, db, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	seedCommentSession(t, db, workflowPolicySessionID, now)
	created, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	updatedInput := domain.WorkflowPolicyInput{
		Selector:     domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeBug}},
		Requirements: []domain.PolicyRequirementInput{{Key: "security_review", Kind: domain.RequirementKindReviewApproval, Purpose: "security"}},
	}
	updated, err := repository.UpdatePolicy(ctx, ports.UpdateWorkflowPolicyCommand{
		PolicyID: created.ID, ExpectedVersion: created.Version, Input: updatedInput, SessionID: stringPtr(workflowPolicySessionID), UpdatedAt: now.Add(time.Duration(1) * time.Second),
	})
	if err != nil {
		t.Fatalf("UpdatePolicy() error = %v", err)
	}
	if updated.Version != 2 || len(updated.Selector.IssueTypes) != 1 || updated.Selector.IssueTypes[0] != domain.TypeBug {
		t.Fatalf("updated policy = %#v, want version 2 with the new selector", updated)
	}
	if len(updated.Requirements) != 1 || updated.Requirements[0].Purpose != "security" {
		t.Fatalf("updated requirements = %#v, want the replaced requirement set", updated.Requirements)
	}

	var count int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM workflow_policy_events WHERE policy_id = ? AND event_type = 'policy_updated' AND prior_version = 1 AND new_version = 2`, created.ID).Scan(&count)
	}); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if count != 1 {
		t.Fatalf("policy_updated audit event count = %d, want 1", count)
	}

	_, err = repository.UpdatePolicy(ctx, ports.UpdateWorkflowPolicyCommand{
		PolicyID: created.ID, ExpectedVersion: created.Version, Input: updatedInput, UpdatedAt: now.Add(time.Duration(2) * time.Second),
	})
	assertDomainCode(t, err, domain.CodeVersionConflict)
}

func TestWorkflowPolicyRepositoryUpdatePolicyRejectsArchived(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	created, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	archived, err := repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{PolicyID: created.ID, ExpectedVersion: created.Version, ArchivedAt: now.Add(time.Duration(1) * time.Second)})
	if err != nil {
		t.Fatalf("ArchivePolicy() error = %v", err)
	}
	_, err = repository.UpdatePolicy(ctx, ports.UpdateWorkflowPolicyCommand{
		PolicyID: archived.ID, ExpectedVersion: archived.Version, Input: validPolicyInput(), UpdatedAt: now.Add(time.Duration(2) * time.Second),
	})
	assertDomainCode(t, err, domain.CodePolicyArchived)
}

func TestWorkflowPolicyRepositoryArchivePolicyOptimisticAndAudited(t *testing.T) {
	repository, db, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	seedCommentSession(t, db, workflowPolicySessionID, now)
	created, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	archived, err := repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{
		PolicyID: created.ID, ExpectedVersion: created.Version, SessionID: stringPtr(workflowPolicySessionID), ArchivedAt: now.Add(time.Duration(1) * time.Second),
	})
	if err != nil {
		t.Fatalf("ArchivePolicy() error = %v", err)
	}
	if archived.Status != domain.PolicyStatusArchived || archived.Version != 2 {
		t.Fatalf("archived policy = %#v, want archived version 2", archived)
	}

	var count int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM workflow_policy_events WHERE policy_id = ? AND event_type = 'policy_archived' AND prior_version = 1 AND new_version = 2`, created.ID).Scan(&count)
	}); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if count != 1 {
		t.Fatalf("policy_archived audit event count = %d, want 1", count)
	}

	// Re-archiving is rejected outright, not a silent no-op; retry safety
	// comes from idempotency_key, matching ArchiveIssue's convention.
	_, err = repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{PolicyID: created.ID, ExpectedVersion: archived.Version, ArchivedAt: now.Add(time.Duration(2) * time.Second)})
	assertDomainCode(t, err, domain.CodePolicyArchived)
}

func TestWorkflowPolicyRepositoryArchivePolicyVersionConflict(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	created, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	_, err = repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{PolicyID: created.ID, ExpectedVersion: created.Version + 1, ArchivedAt: now.Add(time.Duration(1) * time.Second)})
	assertDomainCode(t, err, domain.CodeVersionConflict)
}

func TestWorkflowPolicyRepositoryConcurrentUpdateOnlyOneSucceeds(t *testing.T) {
	repository, _, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	created, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repository.UpdatePolicy(ctx, ports.UpdateWorkflowPolicyCommand{
				PolicyID: created.ID, ExpectedVersion: created.Version, Input: validPolicyInput(), UpdatedAt: now.Add(time.Duration(index+1) * time.Second),
			})
			errs[index] = err
		}(index)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errorsIsVersionConflict(err):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes/conflicts = %d/%d, want exactly 1/1", successes, conflicts)
	}
}

func TestWorkflowPolicyRepositoryGateSnapshotsNotFound(t *testing.T) {
	repository, _, _ := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	_, err := repository.GetAttemptGateSnapshot(ctx, ports.GetAttemptGateSnapshotCommand{AttemptID: workflowPolicyTestID})
	assertDomainCode(t, err, domain.CodeGateSnapshotNotFound)
	_, err = repository.GetReviewTargetGateSnapshot(ctx, ports.GetReviewTargetGateSnapshotCommand{TargetID: workflowPolicyTestID})
	assertDomainCode(t, err, domain.CodeGateSnapshotNotFound)
}

func TestWorkflowPolicyRepositoryReturnsStorageCorruptForInvalidRows(t *testing.T) {
	repository, db, now := openWorkflowPolicyRepository(t)
	ctx := context.Background()
	if _, err := repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{ID: workflowPolicyTestID, Input: validPolicyInput(), CreatedAt: now}); err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}

	tests := []struct {
		name string
		sql  string
	}{
		// json_valid() CHECK constraints on both columns already reject
		// syntactically malformed JSON at write time, so these corrupt with
		// syntactically valid JSON of the wrong shape -- the class of
		// corruption those CHECK constraints cannot catch.
		{name: "wrong-shaped selector JSON", sql: `UPDATE workflow_policies SET selector_json = '{"issue_types": "not-an-array", "labels_all": []}' WHERE id = ?`},
		{name: "wrong-shaped requirements JSON", sql: `UPDATE workflow_policies SET requirements_json = '"not-an-array"' WHERE id = ?`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
				_, err := tx.ExecContext(ctx, test.sql, workflowPolicyTestID)
				return err
			}); err != nil {
				t.Fatalf("corrupt row: %v", err)
			}
			_, err := repository.GetPolicy(ctx, ports.GetWorkflowPolicyCommand{PolicyID: workflowPolicyTestID})
			assertDomainCode(t, err, domain.CodeStorageCorrupt)
		})
	}
}
