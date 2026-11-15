package application_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type attributionPlanningRepository struct {
	lookupCalls  int
	lookupHash   []byte
	replayResult *ports.ApplyIssuePlanResult
	applyCalls   int
	lastCommand  ports.ApplyIssuePlanCommand
}

func (r *attributionPlanningRepository) ValidateIssuePlan(context.Context, domain.IssuePlan) ([]domain.Detail, error) {
	return nil, nil
}

func (r *attributionPlanningRepository) LookupAppliedIssuePlan(_ context.Context, _ string, hash []byte) (ports.ApplyIssuePlanResult, bool, error) {
	r.lookupCalls++
	r.lookupHash = append([]byte(nil), hash...)
	if r.replayResult != nil {
		return *r.replayResult, true, nil
	}
	return ports.ApplyIssuePlanResult{}, false, nil
}

func (r *attributionPlanningRepository) ApplyIssuePlan(_ context.Context, command ports.ApplyIssuePlanCommand) (ports.ApplyIssuePlanResult, error) {
	r.applyCalls++
	r.lastCommand = command
	result := ports.ApplyIssuePlanResult{}
	if len(command.DecisionIDs) > 0 {
		result.CreatedDecisions = []ports.Decision{{
			ID:                 command.DecisionIDs[0],
			Title:              command.Plan.Decisions[0].Title,
			Summary:            command.Plan.Decisions[0].Summary,
			Content:            command.Plan.Decisions[0].Content,
			Status:             string(domain.DecisionStatusActive),
			CreatedBySessionID: command.SessionID,
			CreatedAt:          command.OccurredAt,
		}}
	}
	return result, nil
}

type attributionReviewRepository struct {
	createCommand  ports.CreateReviewRequestCommand
	cancelCommand  ports.ReviewMutationCommand
	replaceCommand ports.ReplaceReviewRequestCommand
	replaceCalls   int
	replaceReplay  *ports.ReplaceReviewRequestResult
}

func (r *attributionReviewRepository) CreateReviewRequest(_ context.Context, command ports.CreateReviewRequestCommand) (ports.CreateReviewRequestResult, error) {
	r.createCommand = command
	return ports.CreateReviewRequestResult{Request: domain.ReviewRequest{ID: command.RequestID, IssueID: command.IssueID, Status: domain.ReviewRequestStatusOpen, Version: 1}}, nil
}

func (r *attributionReviewRepository) GetReviewRequest(context.Context, string) (ports.GetReviewRequestResult, error) {
	return ports.GetReviewRequestResult{}, nil
}

func (r *attributionReviewRepository) ListReviewRequests(context.Context, ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	return ports.ListReviewRequestsResult{}, nil
}

func (r *attributionReviewRepository) CancelReviewRequest(_ context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	r.cancelCommand = command
	return ports.ReviewMutationResult{Request: domain.ReviewRequest{ID: command.RequestID, Status: domain.ReviewRequestStatusCancelled, Version: command.ExpectedVersion + 1}}, nil
}

func (r *attributionReviewRepository) ClaimReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (r *attributionReviewRepository) ResolveReviewRequest(context.Context, ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	return ports.ResolveReviewRequestResult{}, nil
}

func (r *attributionReviewRepository) LookupReplaceReviewRequest(_ context.Context, _ string, _ []byte) (ports.ReplaceReviewRequestResult, bool, error) {
	if r.replaceReplay != nil {
		return *r.replaceReplay, true, nil
	}
	return ports.ReplaceReviewRequestResult{}, false, nil
}

func (r *attributionReviewRepository) ReplaceReviewRequest(_ context.Context, command ports.ReplaceReviewRequestCommand) (ports.ReplaceReviewRequestResult, error) {
	r.replaceCalls++
	r.replaceCommand = command
	return ports.ReplaceReviewRequestResult{
		Predecessor: domain.ReviewRequest{ID: command.PredecessorRequestID, Status: domain.ReviewRequestStatusSuperseded, Version: command.PredecessorExpectedVersion + 1},
		Successor:   domain.ReviewRequest{ID: command.SuccessorID, Status: domain.ReviewRequestStatusOpen, Version: 1},
	}, nil
}

func TestApplicationIssueAndRelationSessionForwarding(t *testing.T) {
	const (
		issueID   = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		targetID  = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		sessionID = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	issueRepo := &recordingIssueRepository{issue: domain.Issue{ID: issueID, DisplayID: "ISSUE-1", Version: 2}}
	issueService, err := application.NewIssueService(issueRepo, clock.NewFakeClock(now), fixedIDGenerator{id: issueID})
	if err != nil {
		t.Fatal(err)
	}
	sid := sessionID
	if _, err := issueService.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "T", SessionID: &sid}); err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if issueRepo.command.Input.SessionID == nil || *issueRepo.command.Input.SessionID != sessionID {
		t.Fatalf("CreateIssueCommand.Input.SessionID = %#v, want %q", issueRepo.command.Input.SessionID, sessionID)
	}
	if _, err := issueService.UpdateIssue(context.Background(), domain.UpdateIssueInput{
		IssueID: issueID, ExpectedVersion: 2,
		Changes:   domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "T2"}},
		SessionID: &sid,
	}); err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if issueRepo.updateCommand.SessionID == nil || *issueRepo.updateCommand.SessionID != sessionID {
		t.Fatalf("UpdateIssueCommand.SessionID = %#v, want %q", issueRepo.updateCommand.SessionID, sessionID)
	}
	if _, err := issueService.ArchiveIssue(context.Background(), domain.ArchiveIssueInput{
		IssueID: issueID, ExpectedVersion: 2, SessionID: &sid,
	}); err != nil {
		t.Fatalf("ArchiveIssue() error = %v", err)
	}
	if issueRepo.archiveCommand.SessionID == nil || *issueRepo.archiveCommand.SessionID != sessionID {
		t.Fatalf("ArchiveIssueCommand.SessionID = %#v, want %q", issueRepo.archiveCommand.SessionID, sessionID)
	}

	relRepo := &recordingRelationRepository{}
	relService, err := application.NewRelationService(relRepo, clock.NewFakeClock(now), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAX"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relService.ManageIssueRelation(context.Background(), domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: issueID, TargetIssueID: targetID,
		RelationType: domain.RelationTypeBlocks, SessionID: &sid,
	}); err != nil {
		t.Fatalf("ManageIssueRelation() error = %v", err)
	}
	if relRepo.command.SessionID == nil || *relRepo.command.SessionID != sessionID {
		t.Fatalf("ManageIssueRelationCommand.SessionID = %#v, want %q", relRepo.command.SessionID, sessionID)
	}
}

func TestPlanningServiceSessionAttributionAndReplay(t *testing.T) {
	const (
		genID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	repo := &attributionPlanningRepository{}
	service, err := application.NewPlanningService(repo, clock.NewFakeClock(now), fixedIDGenerator{id: genID})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.IssuePlan{
		Issues:    []domain.PlannedIssue{{Ref: "a", Type: domain.TypeTask, Title: "Task A"}},
		Decisions: []domain.PlannedDecision{{Title: "Decision A", Summary: "Summary", Content: "Content"}},
	}

	// 3-arg compatibility leaves SessionID nil.
	if _, err := service.ApplyIssuePlan(context.Background(), plan, "key-3arg"); err != nil {
		t.Fatalf("3-arg ApplyIssuePlan() error = %v", err)
	}
	if repo.lastCommand.SessionID != nil {
		t.Fatalf("3-arg ApplyIssuePlanCommand.SessionID = %#v, want nil", repo.lastCommand.SessionID)
	}
	hashNoSession := append([]byte(nil), repo.lookupHash...)

	// Invalid ULID is rejected before lookup.
	bad := "not-a-ulid"
	prevLookups := repo.lookupCalls
	_, err = service.ApplyIssuePlan(context.Background(), plan, "key-bad", &bad)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("invalid session ApplyIssuePlan() err = %v, want INVALID_ARGUMENT", err)
	}
	if repo.lookupCalls != prevLookups {
		t.Fatalf("lookupCalls = %d, want %d (invalid session should fail before lookup)", repo.lookupCalls, prevLookups)
	}

	// Multiple variadic session IDs are rejected cleanly.
	sa, sb := sessionA, sessionB
	if _, err := service.ApplyIssuePlan(context.Background(), plan, "key-multi", &sa, &sb); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("multi session ApplyIssuePlan() err = %v, want INVALID_ARGUMENT", err)
	}

	// 4-arg forwards copied ULID and preserves decision attribution while keeping RequestHash session-independent.
	mutable := sessionA
	res, err := service.ApplyIssuePlan(context.Background(), plan, "key-session-a", &mutable)
	if err != nil {
		t.Fatalf("4-arg ApplyIssuePlan() error = %v", err)
	}
	mutable = sessionB
	if repo.lastCommand.SessionID == nil || *repo.lastCommand.SessionID != sessionA {
		t.Fatalf("ApplyIssuePlanCommand.SessionID = %#v, want %q", repo.lastCommand.SessionID, sessionA)
	}
	if len(res.CreatedDecisions) != 1 || res.CreatedDecisions[0].CreatedBySessionID == nil || *res.CreatedDecisions[0].CreatedBySessionID != sessionA {
		t.Fatalf("CreatedDecisions = %#v, want CreatedBySessionID %q", res.CreatedDecisions, sessionA)
	}
	if !bytes.Equal(hashNoSession, repo.lastCommand.RequestHash) {
		t.Fatalf("RequestHash changed when sessionID was provided")
	}

	// Different session produces identical hash and hits replay without re-applying.
	orig := sessionA
	repo.replayResult = &ports.ApplyIssuePlanResult{
		CreatedDecisions: []ports.Decision{{ID: genID, Title: "Decision A", Status: string(domain.DecisionStatusActive), CreatedBySessionID: &orig, CreatedAt: now}},
		LatestEventID:    7,
	}
	replay, err := service.ApplyIssuePlan(context.Background(), plan, "key-session-a", &sb)
	if err != nil {
		t.Fatalf("replay ApplyIssuePlan() error = %v", err)
	}
	if !bytes.Equal(hashNoSession, repo.lookupHash) {
		t.Fatalf("replay lookupHash changed with sessionB")
	}
	if repo.applyCalls != 2 || replay.CreatedDecisions[0].CreatedBySessionID == nil || *replay.CreatedDecisions[0].CreatedBySessionID != sessionA {
		t.Fatalf("replay unexpectedly mutated or lost attribution: applyCalls=%d decisions=%#v", repo.applyCalls, replay.CreatedDecisions)
	}
}

func TestReviewServiceSessionAttributionAndReplay(t *testing.T) {
	const (
		genID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		issueID  = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	reviewRepo := &attributionReviewRepository{}
	issueRepo := &recordingIssueRepository{issue: domain.Issue{ID: issueID}}
	service, err := application.NewReviewService(reviewRepo, issueRepo, clock.NewFakeClock(now), fixedIDGenerator{id: genID})
	if err != nil {
		t.Fatal(err)
	}

	bad := "bad-ulid"
	var domainErr *domain.Error
	if _, err := service.CreateReviewRequest(context.Background(), application.CreateReviewRequestInput{
		IssueID: issueID, TargetIssueVersion: 1, TargetEventID: 1, SessionID: &bad,
	}); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("CreateReviewRequest bad session = %v, want INVALID_ARGUMENT", err)
	}
	if _, err := service.CancelReviewRequest(context.Background(), application.ReviewMutationInput{
		RequestID: genID, ExpectedVersion: 1, SessionID: &bad,
	}); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("CancelReviewRequest bad session = %v, want INVALID_ARGUMENT", err)
	}
	if _, err := service.ReplaceReviewRequest(context.Background(), application.ReplaceReviewRequestInput{
		PredecessorRequestID: genID, PredecessorExpectedVersion: 1, TargetIssueVersion: 2, TargetEventID: 2,
		IdempotencyKey: "rep-bad", SessionID: &bad,
	}); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("ReplaceReviewRequest bad session = %v, want INVALID_ARGUMENT", err)
	}

	sa, sb := sessionA, sessionB
	if _, err := service.CreateReviewRequest(context.Background(), application.CreateReviewRequestInput{
		IssueID: issueID, TargetIssueVersion: 1, TargetEventID: 1, SessionID: &sa,
	}); err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}
	if reviewRepo.createCommand.SessionID == nil || *reviewRepo.createCommand.SessionID != sessionA {
		t.Fatalf("createCommand.SessionID = %#v, want %q", reviewRepo.createCommand.SessionID, sessionA)
	}

	if _, err := service.CancelReviewRequest(context.Background(), application.ReviewMutationInput{
		RequestID: genID, ExpectedVersion: 1, SessionID: &sb,
	}); err != nil {
		t.Fatalf("CancelReviewRequest() error = %v", err)
	}
	if reviewRepo.cancelCommand.SessionID == nil || *reviewRepo.cancelCommand.SessionID != sessionB {
		t.Fatalf("cancelCommand.SessionID = %#v, want %q", reviewRepo.cancelCommand.SessionID, sessionB)
	}

	if _, err := service.ReplaceReviewRequest(context.Background(), application.ReplaceReviewRequestInput{
		PredecessorRequestID: genID, PredecessorExpectedVersion: 1, TargetIssueVersion: 2, TargetEventID: 2,
		IdempotencyKey: "rep-1", SessionID: &sa,
	}); err != nil {
		t.Fatalf("ReplaceReviewRequest(sa) error = %v", err)
	}
	hashA := append([]byte(nil), reviewRepo.replaceCommand.RequestHash...)
	if reviewRepo.replaceCommand.SessionID == nil || *reviewRepo.replaceCommand.SessionID != sessionA {
		t.Fatalf("replaceCommand.SessionID = %#v, want %q", reviewRepo.replaceCommand.SessionID, sessionA)
	}

	if _, err := service.ReplaceReviewRequest(context.Background(), application.ReplaceReviewRequestInput{
		PredecessorRequestID: genID, PredecessorExpectedVersion: 1, TargetIssueVersion: 2, TargetEventID: 2,
		IdempotencyKey: "rep-2", SessionID: &sb,
	}); err != nil {
		t.Fatalf("ReplaceReviewRequest(sb) error = %v", err)
	}
	if !bytes.Equal(hashA, reviewRepo.replaceCommand.RequestHash) {
		t.Fatalf("ReplaceReviewRequest RequestHash differs by SessionID")
	}

	// Idempotency replay skips ReplaceReviewRequest regardless of sessionID.
	prevReplaceCalls := reviewRepo.replaceCalls
	reviewRepo.replaceReplay = &ports.ReplaceReviewRequestResult{LatestEventID: 99}
	replay, err := service.ReplaceReviewRequest(context.Background(), application.ReplaceReviewRequestInput{
		PredecessorRequestID: genID, PredecessorExpectedVersion: 1, TargetIssueVersion: 2, TargetEventID: 2,
		IdempotencyKey: "rep-1", SessionID: &sb,
	})
	if err != nil {
		t.Fatalf("ReplaceReviewRequest replay error = %v", err)
	}
	if reviewRepo.replaceCalls != prevReplaceCalls || replay.LatestEventID != 99 {
		t.Fatalf("replaceCalls=%d replay=%#v, want replay hit", reviewRepo.replaceCalls, replay)
	}
}
