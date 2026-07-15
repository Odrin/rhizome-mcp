package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"

	_ "modernc.org/sqlite"
)

func TestWorkContextRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := sqlite.NewWorkContextRepository(nil)
	assertDomainCode(t, err, domain.CodeStorageConfiguration)
}

func TestWorkContextRepositoryUsesCanonicalAndDisplayIssueIDs(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	canonicalResult, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if canonicalResult.Issue.ID != target.ID {
		t.Fatalf("issue id = %q, want %q", canonicalResult.Issue.ID, target.ID)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: fmt.Sprintf("ISSUE-%d", target.SequenceNo)}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.Issue.ID != target.ID {
		t.Fatalf("issue id = %q, want %q", result.Issue.ID, target.ID)
	}
	if result.Issue.DisplayID != fmt.Sprintf("ISSUE-%d", target.SequenceNo) {
		t.Fatalf("display id = %q, want %q", result.Issue.DisplayID, fmt.Sprintf("ISSUE-%d", target.SequenceNo))
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: "ISSUE-999"}, Now: now})
	assertDomainCode(t, err, domain.CodeIssueNotFound)
}

func TestWorkContextRepositoryCompactDefaultProjection(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	description := "target description"
	acceptance := "target acceptance"
	if err := seedIssueWithContent(t, db, target.ID, target.SequenceNo, domain.StatusReady, target.Title, &description, &acceptance, now); err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", 2, domain.StatusReady, "blocker", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedBlocksRelation(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := seedActiveAttempt(t, db, target.ID, now.Add(1*time.Minute), now.Add(2*time.Minute), now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.Issue.Description == nil || *result.Issue.Description != description {
		t.Fatalf("issue description = %#v, want %q", result.Issue.Description, description)
	}
	if result.Issue.AcceptanceCriteria == nil || *result.Issue.AcceptanceCriteria != acceptance {
		t.Fatalf("issue acceptance criteria = %#v, want %q", result.Issue.AcceptanceCriteria, acceptance)
	}
	if result.Issue.EffectiveStatus != domain.EffectiveStatusInProgress {
		t.Fatalf("effective status = %q, want %q", result.Issue.EffectiveStatus, domain.EffectiveStatusInProgress)
	}
	if result.Issue.UnresolvedBlockerCount != 1 {
		t.Fatalf("unresolved blocker count = %d, want 1", result.Issue.UnresolvedBlockerCount)
	}
	if !result.Issue.IsBlocked {
		t.Fatal("issue should be blocked")
	}
	if result.Blockers == nil || len(result.Blockers) != 1 {
		t.Fatalf("len(blockers) = %d, want 1", len(result.Blockers))
	}
	if result.Decisions == nil || len(result.Decisions) != 0 {
		t.Fatalf("len(decisions) = %d, want 0", len(result.Decisions))
	}
	if result.Warnings == nil || len(result.Warnings) != 0 {
		t.Fatalf("len(warnings) = %d, want 0", len(result.Warnings))
	}
	if result.ParentEpic != nil {
		t.Fatalf("parent epic = %#v, want nil", result.ParentEpic)
	}
	if result.ProjectInstructions != nil {
		t.Fatalf("project instructions = %#v, want nil", result.ProjectInstructions)
	}
	if result.PreviousAttempt != nil {
		t.Fatalf("previous attempt = %#v, want nil", result.PreviousAttempt)
	}
	if result.Checkpoint != nil {
		t.Fatalf("checkpoint = %#v, want nil", result.Checkpoint)
	}
	if result.Relations == nil || len(result.Relations) != 0 {
		t.Fatalf("len(relations) = %d, want 0", len(result.Relations))
	}
	if result.RecentComments == nil || len(result.RecentComments) != 0 {
		t.Fatalf("len(recent comments) = %d, want 0", len(result.RecentComments))
	}
	if result.RecentAttemptNotes == nil || len(result.RecentAttemptNotes) != 0 {
		t.Fatalf("len(recent attempt notes) = %d, want 0", len(result.RecentAttemptNotes))
	}
	if result.TruncatedSections == nil || len(result.TruncatedSections) != 0 {
		t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
	}
}

func TestWorkContextRepositoryLoadsRelationsWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	generator := newTestULIDGenerator(t, now)
	nextID := func() string {
		id, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	relatedIssueAID := nextID()
	relatedIssueBID := nextID()
	relatedIssueCID := nextID()
	parentEpicID := nextID()
	for _, issue := range []struct {
		id         string
		sequenceNo int64
		title      string
	}{
		{id: relatedIssueAID, sequenceNo: 2, title: "alpha"},
		{id: relatedIssueBID, sequenceNo: 3, title: "beta"},
		{id: relatedIssueCID, sequenceNo: 4, title: "gamma"},
		{id: parentEpicID, sequenceNo: 5, title: "parent epic"},
	} {
		if err := seedIssue(t, db, issue.id, issue.sequenceNo, domain.StatusReady, issue.title, nil, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET parent_id = ? WHERE id = ?`, parentEpicID, target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueAID, domain.RelationTypeBlocks, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, relatedIssueBID, target.ID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueCID, domain.RelationTypeDuplicates, nextID(), now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("len(relations) = %d, want 0", len(result.Relations))
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelations}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Relations) != 3 {
		t.Fatalf("len(relations) = %d, want 3", len(result.Relations))
	}
	wantTypes := []domain.RelationType{domain.RelationTypeBlocks, domain.RelationTypeDuplicates, domain.RelationTypeRelatedTo}
	gotTypes := make([]domain.RelationType, len(result.Relations))
	for index, relation := range result.Relations {
		gotTypes[index] = relation.Type
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("relation types = %v, want %v", gotTypes, wantTypes)
	}
	if result.Relations[0].SourceIssueID != target.ID || result.Relations[0].TargetIssueID != relatedIssueAID {
		t.Fatalf("first relation endpoints = %q -> %q, want %q -> %q", result.Relations[0].SourceIssueID, result.Relations[0].TargetIssueID, target.ID, relatedIssueAID)
	}
	if result.Relations[1].SourceIssueID != target.ID || result.Relations[1].TargetIssueID != relatedIssueCID {
		t.Fatalf("second relation endpoints = %q -> %q, want %q -> %q", result.Relations[1].SourceIssueID, result.Relations[1].TargetIssueID, target.ID, relatedIssueCID)
	}
	wantSourceID, wantTargetID := relatedIssueBID, target.ID
	if wantSourceID > wantTargetID {
		wantSourceID, wantTargetID = wantTargetID, wantSourceID
	}
	if result.Relations[2].SourceIssueID != wantSourceID || result.Relations[2].TargetIssueID != wantTargetID {
		t.Fatalf("third relation endpoints = %q -> %q, want %q -> %q", result.Relations[2].SourceIssueID, result.Relations[2].TargetIssueID, wantSourceID, wantTargetID)
	}
	if result.Relations[0].CreatedAt.IsZero() || result.Relations[1].CreatedAt.IsZero() || result.Relations[2].CreatedAt.IsZero() {
		t.Fatal("relations should preserve created_at timestamps")
	}
	if result.ParentEpic != nil {
		t.Fatalf("parent epic = %#v, want nil", result.ParentEpic)
	}
}

func TestWorkContextRepositoryReturnsCorruptOnMalformedRelation(t *testing.T) {
	db, dbPath, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator := newTestULIDGenerator(t, now)
	relatedIssueID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, relatedIssueID, 2, domain.StatusReady, "related", nil, nil, now); err != nil {
		t.Fatal(err)
	}

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	if _, err := rawDB.ExecContext(context.Background(), `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FB1", "not-a-ulid", target.ID, domain.RelationTypeBlocks, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelations}}, Now: now})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestWorkContextRepositoryLoadsRelatedIssueSummariesWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	generator := newTestULIDGenerator(t, now)
	nextID := func() string {
		id, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	relatedIssueAID := nextID()
	relatedIssueBID := nextID()
	relatedIssueCID := nextID()
	relatedIssueDID := nextID()
	parentEpicID := nextID()
	for _, issue := range []struct {
		id         string
		sequenceNo int64
		title      string
	}{
		{id: relatedIssueAID, sequenceNo: 3, title: "alpha"},
		{id: relatedIssueBID, sequenceNo: 2, title: "beta"},
		{id: relatedIssueCID, sequenceNo: 4, title: "gamma"},
		{id: relatedIssueDID, sequenceNo: 5, title: "delta"},
		{id: parentEpicID, sequenceNo: 6, title: "parent epic"},
	} {
		if err := seedIssue(t, db, issue.id, issue.sequenceNo, domain.StatusReady, issue.title, nil, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := seedActiveAttempt(t, db, relatedIssueBID, now.Add(2*time.Minute), now.Add(1*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := seedArchivedIssue(t, db, relatedIssueDID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET parent_id = ? WHERE id = ?`, parentEpicID, target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, relatedIssueAID, target.ID, domain.RelationTypeBlocks, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueBID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueCID, domain.RelationTypeDuplicates, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueDID, domain.RelationTypeBlocks, nextID(), now); err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueAID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.RelatedIssueSummaries) != 0 {
		t.Fatalf("len(related issue summaries) = %d, want 0", len(result.RelatedIssueSummaries))
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.RelatedIssueSummaries) != 4 {
		t.Fatalf("len(related issue summaries) = %d, want 4", len(result.RelatedIssueSummaries))
	}
	wantIDs := []string{relatedIssueBID, relatedIssueAID, relatedIssueCID, relatedIssueDID}
	gotIDs := make([]string, len(result.RelatedIssueSummaries))
	for index, summary := range result.RelatedIssueSummaries {
		gotIDs[index] = summary.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("related summary ids = %v, want %v", gotIDs, wantIDs)
	}
	if result.RelatedIssueSummaries[0].EffectiveStatus != domain.EffectiveStatusInProgress {
		t.Fatalf("effective status = %q, want %q", result.RelatedIssueSummaries[0].EffectiveStatus, domain.EffectiveStatusInProgress)
	}
	for _, summary := range result.RelatedIssueSummaries {
		if summary.ID == target.ID || summary.ID == parentEpicID {
			t.Fatalf("summary should not include target or parent epic: %#v", summary)
		}
	}
}

func TestWorkContextRepositoryBoundsRelatedIssueSummaries(t *testing.T) {
	generator := newTestULIDGenerator(t, time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.UTC))
	nextID := func() string {
		id, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("custom limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 2; index++ {
			issueID := nextID()
			if err := seedIssue(t, db, issueID, int64(index+2), domain.StatusReady, fmt.Sprintf("issue-%d", index), nil, nil, now); err != nil {
				t.Fatal(err)
			}
			if err := seedRelation(t, db, target.ID, issueID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: 1}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.RelatedIssueSummaries) != 1 {
			t.Fatalf("len(related issue summaries) = %d, want 1", len(result.RelatedIssueSummaries))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries})
		}
	})

	t.Run("default limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 21; index++ {
			issueID := nextID()
			if err := seedIssue(t, db, issueID, int64(index+2), domain.StatusReady, fmt.Sprintf("issue-%d", index), nil, nil, now); err != nil {
				t.Fatal(err)
			}
			if err := seedRelation(t, db, target.ID, issueID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.RelatedIssueSummaries) != 20 {
			t.Fatalf("len(related issue summaries) = %d, want 20", len(result.RelatedIssueSummaries))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries})
		}
	})

	t.Run("at limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 20; index++ {
			issueID := nextID()
			if err := seedIssue(t, db, issueID, int64(index+2), domain.StatusReady, fmt.Sprintf("issue-%d", index), nil, nil, now); err != nil {
				t.Fatal(err)
			}
			if err := seedRelation(t, db, target.ID, issueID, domain.RelationTypeRelatedTo, nextID(), now); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: 20}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.RelatedIssueSummaries) != 20 {
			t.Fatalf("len(related issue summaries) = %d, want 20", len(result.RelatedIssueSummaries))
		}
		if result.Truncated {
			t.Fatal("truncated should be false")
		}
		if len(result.TruncatedSections) != 0 {
			t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
		}
	})
}

func TestWorkContextRepositoryLoadsRecentCommentsAndAttemptNotesWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	otherIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	if err := seedIssue(t, db, otherIssueID, 2, domain.StatusReady, "other issue", nil, nil, now); err != nil {
		t.Fatal(err)
	}

	commentIDs := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FB1",
		"01ARZ3NDEKTSV4RRFFQ69G5FB2",
		"01ARZ3NDEKTSV4RRFFQ69G5FB3",
	}
	if err := seedComment(t, db, commentIDs[2], target.ID, "older comment", now.Add(1*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedComment(t, db, commentIDs[1], target.ID, "second tied comment", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedComment(t, db, commentIDs[0], target.ID, "first tied comment", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedComment(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FB4", otherIssueID, "other issue comment", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	statuses := []domain.AttemptStatus{
		domain.AttemptStatusActive,
		domain.AttemptStatusCompleted,
		domain.AttemptStatusFailed,
		domain.AttemptStatusInterrupted,
		domain.AttemptStatusExpired,
		domain.AttemptStatusCancelled,
	}
	attemptIDs := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FC1",
		"01ARZ3NDEKTSV4RRFFQ69G5FC2",
		"01ARZ3NDEKTSV4RRFFQ69G5FC3",
		"01ARZ3NDEKTSV4RRFFQ69G5FC4",
		"01ARZ3NDEKTSV4RRFFQ69G5FC5",
		"01ARZ3NDEKTSV4RRFFQ69G5FC6",
	}
	noteIDs := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FD1",
		"01ARZ3NDEKTSV4RRFFQ69G5FD2",
		"01ARZ3NDEKTSV4RRFFQ69G5FD3",
		"01ARZ3NDEKTSV4RRFFQ69G5FD4",
		"01ARZ3NDEKTSV4RRFFQ69G5FD5",
		"01ARZ3NDEKTSV4RRFFQ69G5FD6",
	}
	for index, status := range statuses {
		var finishedAt *time.Time
		if status != domain.AttemptStatusActive {
			finishedAt = timePtr(now.Add(time.Duration(index+1) * time.Minute))
		}
		if err := seedAttempt(t, db, attemptIDs[index], target.ID, status, now.Add(time.Hour), now, finishedAt, now); err != nil {
			t.Fatal(err)
		}
		createdAt := now.Add(time.Duration(6-index) * time.Minute)
		if index == 1 {
			createdAt = now.Add(6 * time.Minute)
		}
		if err := seedNote(t, db, noteIDs[index], attemptIDs[index], domain.AttemptNoteKindProgress, fmt.Sprintf("note-%d", index), createdAt); err != nil {
			t.Fatal(err)
		}
	}
	otherAttemptID := "01ARZ3NDEKTSV4RRFFQ69G5FC7"
	if err := seedAttempt(t, db, otherAttemptID, otherIssueID, domain.AttemptStatusCompleted, now.Add(time.Hour), now, timePtr(now.Add(time.Minute)), now); err != nil {
		t.Fatal(err)
	}
	if err := seedNote(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FD7", otherAttemptID, domain.AttemptNoteKindWarning, "other issue note", now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.RecentComments) != 0 || len(result.RecentAttemptNotes) != 0 {
		t.Fatalf("default recent sections = %d comments, %d notes; want empty", len(result.RecentComments), len(result.RecentAttemptNotes))
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{
		IssueID: target.ID,
		Include: []domain.WorkContextInclude{domain.WorkContextIncludeRecentComments, domain.WorkContextIncludeRecentAttemptNotes},
	}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	gotCommentIDs := make([]string, len(result.RecentComments))
	for index, comment := range result.RecentComments {
		gotCommentIDs[index] = comment.ID
	}
	if !reflect.DeepEqual(gotCommentIDs, commentIDs) {
		t.Fatalf("recent comment ids = %v, want %v", gotCommentIDs, commentIDs)
	}
	if result.RecentComments[0].Content != "first tied comment" {
		t.Fatalf("first comment content = %q, want %q", result.RecentComments[0].Content, "first tied comment")
	}
	gotNoteIDs := make([]string, len(result.RecentAttemptNotes))
	for index, note := range result.RecentAttemptNotes {
		gotNoteIDs[index] = note.ID
	}
	if !reflect.DeepEqual(gotNoteIDs, noteIDs) {
		t.Fatalf("recent attempt note ids = %v, want %v", gotNoteIDs, noteIDs)
	}
	for index, note := range result.RecentAttemptNotes {
		if note.AttemptID != attemptIDs[index] {
			t.Fatalf("note %d attempt id = %q, want %q", index, note.AttemptID, attemptIDs[index])
		}
	}
}

func TestWorkContextRepositoryBoundsRecentCommentsAndAttemptNotes(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	if err := seedAttempt(t, db, attemptID, target.ID, domain.AttemptStatusCompleted, now.Add(time.Hour), now, timePtr(now.Add(time.Minute)), now); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		suffix := index + 1
		if err := seedComment(t, db, fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5FB%d", suffix), target.ID, fmt.Sprintf("comment-%d", index), now.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := seedNote(t, db, fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5FC%d", suffix), attemptID, domain.AttemptNoteKindFinding, fmt.Sprintf("note-%d", index), now.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{
		IssueID: target.ID,
		Include: []domain.WorkContextInclude{
			domain.WorkContextIncludeRecentAttemptNotes,
			domain.WorkContextIncludeRecentComments,
		},
		Limits: map[domain.WorkContextInclude]int{
			domain.WorkContextIncludeRecentAttemptNotes: 1,
			domain.WorkContextIncludeRecentComments:     2,
		},
	}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.RecentAttemptNotes) != 1 || result.RecentAttemptNotes[0].Content != "note-2" {
		t.Fatalf("recent attempt notes = %#v, want newest note only", result.RecentAttemptNotes)
	}
	if len(result.RecentComments) != 2 || result.RecentComments[0].Content != "comment-2" || result.RecentComments[1].Content != "comment-1" {
		t.Fatalf("recent comments = %#v, want two newest comments", result.RecentComments)
	}
	if !result.Truncated {
		t.Fatal("truncated should be true")
	}
	wantSections := []domain.WorkContextInclude{
		domain.WorkContextIncludeRecentAttemptNotes,
		domain.WorkContextIncludeRecentComments,
	}
	if !reflect.DeepEqual(result.TruncatedSections, wantSections) {
		t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, wantSections)
	}
}

func TestWorkContextRepositoryReturnsCorruptOnMalformedRecentComment(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedComment(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FB1", target.ID, "   ", now); err != nil {
		t.Fatal(err)
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{
		IssueID: target.ID,
		Include: []domain.WorkContextInclude{domain.WorkContextIncludeRecentComments},
	}, Now: now})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestWorkContextRepositoryReturnsCorruptOnMalformedRecentAttemptNote(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	if err := seedAttempt(t, db, attemptID, target.ID, domain.AttemptStatusCompleted, now.Add(time.Hour), now, timePtr(now.Add(time.Minute)), now); err != nil {
		t.Fatal(err)
	}
	if err := seedNote(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FB1", attemptID, domain.AttemptNoteKindProgress, "   ", now); err != nil {
		t.Fatal(err)
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{
		IssueID: target.ID,
		Include: []domain.WorkContextInclude{domain.WorkContextIncludeRecentAttemptNotes},
	}, Now: now})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestWorkContextRepositoryLoadsRelationsAndRelatedSummariesTogether(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	generator := newTestULIDGenerator(t, now)
	relatedIssueID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, relatedIssueID, 2, domain.StatusReady, "related", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	relationID, err := generator.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRelation(t, db, target.ID, relatedIssueID, domain.RelationTypeRelatedTo, relationID, now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelations, domain.WorkContextIncludeRelatedIssueSummaries}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Relations) != 1 {
		t.Fatalf("len(relations) = %d, want 1", len(result.Relations))
	}
	if len(result.RelatedIssueSummaries) != 1 {
		t.Fatalf("len(related issue summaries) = %d, want 1", len(result.RelatedIssueSummaries))
	}
	if result.ParentEpic != nil {
		t.Fatalf("parent epic = %#v, want nil", result.ParentEpic)
	}
	if result.ProjectInstructions != nil {
		t.Fatalf("project instructions = %#v, want nil", result.ProjectInstructions)
	}
}

func TestWorkContextRepositoryLoadsParentEpicWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	parentID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	parentDescription := "parent description"
	parentAcceptance := "parent acceptance criteria"
	if err := seedIssueWithType(t, db, parentID, 2, domain.TypeEpic, domain.StatusReady, "parent epic", &parentDescription, &parentAcceptance, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET parent_id = ? WHERE id = ?`, parentID, target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedActiveAttempt(t, db, parentID, now.Add(1*time.Minute), now.Add(2*time.Minute), now); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ParentEpic != nil {
		t.Fatalf("parent epic = %#v, want nil", result.ParentEpic)
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ParentEpic == nil {
		t.Fatal("parent epic was nil")
	}
	if result.ParentEpic.ID != parentID {
		t.Fatalf("parent epic id = %q, want %q", result.ParentEpic.ID, parentID)
	}
	if result.ParentEpic.Description == nil || *result.ParentEpic.Description != parentDescription {
		t.Fatalf("parent description = %#v, want %q", result.ParentEpic.Description, parentDescription)
	}
	if result.ParentEpic.AcceptanceCriteria == nil || *result.ParentEpic.AcceptanceCriteria != parentAcceptance {
		t.Fatalf("parent acceptance criteria = %#v, want %q", result.ParentEpic.AcceptanceCriteria, parentAcceptance)
	}
	if result.ParentEpic.EffectiveStatus != domain.EffectiveStatusInProgress {
		t.Fatalf("parent effective status = %q, want %q", result.ParentEpic.EffectiveStatus, domain.EffectiveStatusInProgress)
	}

	*result.ParentEpic.Description = "mutated parent description"
	*result.ParentEpic.AcceptanceCriteria = "mutated parent acceptance"
	second, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}}, Now: now})
	if err != nil {
		t.Fatalf("second GetWorkContext() error = %v", err)
	}
	if second.ParentEpic == nil || second.ParentEpic.Description == nil || *second.ParentEpic.Description != parentDescription {
		t.Fatalf("mutated parent description should not leak: %#v", second.ParentEpic)
	}
	if second.ParentEpic == nil || second.ParentEpic.AcceptanceCriteria == nil || *second.ParentEpic.AcceptanceCriteria != parentAcceptance {
		t.Fatalf("mutated parent acceptance should not leak: %#v", second.ParentEpic)
	}
}

func TestWorkContextRepositoryParentEpicIncludeWithNoParentReturnsNil(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ParentEpic != nil {
		t.Fatalf("parent epic = %#v, want nil", result.ParentEpic)
	}
}

func TestWorkContextRepositoryRejectsNonEpicParent(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	nonEpicParentID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	if err := seedIssueWithType(t, db, nonEpicParentID, 2, domain.TypeTask, domain.StatusReady, "non epic", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET parent_id = ? WHERE id = ?`, nonEpicParentID, target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}}, Now: now})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestWorkContextRepositoryLoadsProjectInstructionsWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	instructions := "follow the project instructions"
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET instructions = ? WHERE id = ?`, instructions, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ProjectInstructions != nil {
		t.Fatalf("project instructions = %#v, want nil", result.ProjectInstructions)
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeProjectInstructions}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ProjectInstructions == nil || *result.ProjectInstructions != instructions {
		t.Fatalf("project instructions = %#v, want %q", result.ProjectInstructions, instructions)
	}

	*result.ProjectInstructions = "mutated instructions"
	second, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeProjectInstructions}}, Now: now})
	if err != nil {
		t.Fatalf("second GetWorkContext() error = %v", err)
	}
	if second.ProjectInstructions == nil || *second.ProjectInstructions != instructions {
		t.Fatalf("mutated project instructions should not leak: %#v", second.ProjectInstructions)
	}
}

func TestWorkContextRepositoryProjectInstructionsIncludeWithNullInstructions(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET instructions = NULL WHERE id = ?`, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeProjectInstructions}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ProjectInstructions != nil {
		t.Fatalf("project instructions = %#v, want nil", result.ProjectInstructions)
	}
}

func TestWorkContextRepositoryLoadsParentEpicAndProjectInstructionsTogether(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	parentID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	parentDescription := "parent description"
	parentAcceptance := "parent acceptance criteria"
	if err := seedIssueWithType(t, db, parentID, 2, domain.TypeEpic, domain.StatusReady, "parent epic", &parentDescription, &parentAcceptance, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET parent_id = ? WHERE id = ?`, parentID, target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE projects SET instructions = ? WHERE id = ?`, "project instructions", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic, domain.WorkContextIncludeProjectInstructions}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.ParentEpic == nil {
		t.Fatal("parent epic was nil")
	}
	if result.ProjectInstructions == nil || *result.ProjectInstructions != "project instructions" {
		t.Fatalf("project instructions = %#v, want %q", result.ProjectInstructions, "project instructions")
	}
}

func TestWorkContextRepositoryFiltersBlockersAndOrdersThem(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	blockerIDs := []string{"01ARZ3NDEKTSV4RRFFQ69G5FA1", "01ARZ3NDEKTSV4RRFFQ69G5FA2", "01ARZ3NDEKTSV4RRFFQ69G5FA3", "01ARZ3NDEKTSV4RRFFQ69G5FA4"}
	for index, id := range blockerIDs {
		sequenceNo := int64(index + 2)
		status := domain.StatusReady
		if id == "01ARZ3NDEKTSV4RRFFQ69G5FA3" {
			status = domain.StatusBlocked
		}
		if err := seedIssue(t, db, id, sequenceNo, status, "blocker", nil, nil, now); err != nil {
			t.Fatal(err)
		}
		if status == domain.StatusBlocked {
			if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
				_, err := tx.ExecContext(ctx, `UPDATE issues SET blocked_reason = ? WHERE id = ?`, "blocked reason", id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA5", 8, domain.StatusReady, "archived", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA6", 9, domain.StatusDone, "done", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA7", 10, domain.StatusCancelled, "cancelled", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedArchivedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA5", now); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FA1", "01ARZ3NDEKTSV4RRFFQ69G5FA2", "01ARZ3NDEKTSV4RRFFQ69G5FA3", "01ARZ3NDEKTSV4RRFFQ69G5FA4", "01ARZ3NDEKTSV4RRFFQ69G5FA5", "01ARZ3NDEKTSV4RRFFQ69G5FA6", "01ARZ3NDEKTSV4RRFFQ69G5FA7"} {
		if err := seedBlocksRelation(t, db, sourceID, target.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Blockers) != 4 {
		t.Fatalf("len(blockers) = %d, want 4", len(result.Blockers))
	}
	wantIDs := []string{"01ARZ3NDEKTSV4RRFFQ69G5FA1", "01ARZ3NDEKTSV4RRFFQ69G5FA2", "01ARZ3NDEKTSV4RRFFQ69G5FA3", "01ARZ3NDEKTSV4RRFFQ69G5FA4"}
	if got := blockerIDsFrom(result.Blockers); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("blocker ids = %v, want %v", got, wantIDs)
	}
	if result.Issue.UnresolvedBlockerCount != 4 {
		t.Fatalf("unresolved blocker count = %d, want 4", result.Issue.UnresolvedBlockerCount)
	}
	if !result.Issue.IsBlocked {
		t.Fatal("issue should be blocked")
	}
	if result.Blockers[2].IsBlocked != true {
		t.Fatal("blocked blocker should remain blocked")
	}

	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET status = ?, blocked_reason = ? WHERE id = ?`, domain.StatusBlocked, "manual blocked reason", target.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if !result.Issue.IsBlocked {
		t.Fatal("issue should remain blocked")
	}
	if result.Issue.UnresolvedBlockerCount != 4 {
		t.Fatalf("unresolved blocker count = %d, want 4", result.Issue.UnresolvedBlockerCount)
	}
}

func TestWorkContextRepositoryLoadsActiveIssueScopedDecisionSummaries(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA6", 2, domain.StatusReady, "other issue", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA1", "active one", "summary one", "content one", domain.DecisionStatusActive, now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA2", "active two", "summary two", "content two", domain.DecisionStatusActive, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA3", "superseded", "summary three", "content three", domain.DecisionStatusSuperseded, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA4", "rejected", "summary four", "content four", domain.DecisionStatusRejected, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, nil, "01ARZ3NDEKTSV4RRFFQ69G5FA5", "global", "summary five", "content five", domain.DecisionStatusActive, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr("01ARZ3NDEKTSV4RRFFQ69G5FA6"), "01ARZ3NDEKTSV4RRFFQ69G5FA6", "other issue", "summary six", "content six", domain.DecisionStatusActive, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Decisions) != 2 {
		t.Fatalf("len(decisions) = %d, want 2", len(result.Decisions))
	}
	if got := result.Decisions[0].ID; got != "01ARZ3NDEKTSV4RRFFQ69G5FA2" {
		t.Fatalf("first decision id = %q, want %q", got, "01ARZ3NDEKTSV4RRFFQ69G5FA2")
	}
	if result.Decisions[0].Title != "active two" {
		t.Fatalf("decision title = %q, want %q", result.Decisions[0].Title, "active two")
	}
	if result.Decisions[0].Summary != "summary two" {
		t.Fatalf("decision summary = %q, want %q", result.Decisions[0].Summary, "summary two")
	}
}

func TestWorkContextRepositoryLoadsArtifactsWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	otherIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FA6"
	if err := seedIssue(t, db, otherIssueID, 2, domain.StatusReady, "other issue", nil, nil, now); err != nil {
		t.Fatal(err)
	}

	attemptID := "01ARZ3NDEKTSV4RRFFQ69G5FA7"
	if err := seedAttempt(t, db, attemptID, target.ID, domain.AttemptStatusActive, now.Add(10*time.Minute), now.Add(1*time.Second), nil, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	artifactAID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	artifactBID := "01ARZ3NDEKTSV4RRFFQ69G5FA2"
	artifactCID := "01ARZ3NDEKTSV4RRFFQ69G5FA3"
	artifactDID := "01ARZ3NDEKTSV4RRFFQ69G5FA4"

	createdAtA := now.Add(4 * time.Second)
	createdAtB := now.Add(4 * time.Second)
	createdAtC := now.Add(2 * time.Second)
	createdAtD := now.Add(5 * time.Second)

	titleA := "alpha title"
	titleC := "gamma title"
	metadataA := json.RawMessage(`{"name":"alpha"}`)
	metadataC := json.RawMessage(`{"name":"gamma"}`)
	if err := seedArtifact(t, db, artifactAID, target.ID, &attemptID, domain.ArtifactTypeFile, "notes/alpha.txt", &titleA, metadataA, createdAtA); err != nil {
		t.Fatal(err)
	}
	if err := seedArtifact(t, db, artifactBID, target.ID, nil, domain.ArtifactTypeFile, "notes/beta.txt", nil, nil, createdAtB); err != nil {
		t.Fatal(err)
	}
	if err := seedArtifact(t, db, artifactCID, target.ID, nil, domain.ArtifactTypeFile, "notes/gamma.txt", &titleC, metadataC, createdAtC); err != nil {
		t.Fatal(err)
	}
	if err := seedArtifact(t, db, artifactDID, otherIssueID, nil, domain.ArtifactTypeFile, "notes/other.txt", nil, nil, createdAtD); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Artifacts) != 0 {
		t.Fatalf("len(artifacts) = %d, want 0", len(result.Artifacts))
	}
	if result.Truncated {
		t.Fatal("default context should not be truncated")
	}
	if len(result.TruncatedSections) != 0 {
		t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Artifacts) != 3 {
		t.Fatalf("len(artifacts) = %d, want 3", len(result.Artifacts))
	}
	gotIDs := make([]string, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		gotIDs[index] = artifact.ID
	}
	wantIDs := []string{artifactAID, artifactBID, artifactCID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("artifact ids = %v, want %v", gotIDs, wantIDs)
	}
	if result.Artifacts[0].AttemptID == nil || *result.Artifacts[0].AttemptID != attemptID {
		t.Fatalf("first artifact attempt id = %#v, want %q", result.Artifacts[0].AttemptID, attemptID)
	}
	if result.Artifacts[1].AttemptID != nil {
		t.Fatalf("second artifact attempt id = %#v, want nil", result.Artifacts[1].AttemptID)
	}
	if result.Artifacts[0].Title == nil || *result.Artifacts[0].Title != titleA {
		t.Fatalf("first artifact title = %#v, want %q", result.Artifacts[0].Title, titleA)
	}
	if !reflect.DeepEqual(result.Artifacts[0].Metadata, metadataA) {
		t.Fatalf("first artifact metadata = %s, want %s", result.Artifacts[0].Metadata, metadataA)
	}
	if result.Artifacts[1].Title != nil || result.Artifacts[1].Metadata != nil {
		t.Fatalf("second artifact should omit optional title and metadata: %#v %#v", result.Artifacts[1].Title, result.Artifacts[1].Metadata)
	}
	if result.Artifacts[2].Title == nil || *result.Artifacts[2].Title != titleC {
		t.Fatalf("third artifact title = %#v, want %q", result.Artifacts[2].Title, titleC)
	}
	if !reflect.DeepEqual(result.Artifacts[2].Metadata, metadataC) {
		t.Fatalf("third artifact metadata = %s, want %s", result.Artifacts[2].Metadata, metadataC)
	}
	if result.Truncated {
		t.Fatal("artifacts should not be truncated for this request")
	}
	if len(result.TruncatedSections) != 0 {
		t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
	}
}

func TestWorkContextRepositoryBoundsArtifacts(t *testing.T) {
	t.Run("custom limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := seedArtifact(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, nil, domain.ArtifactTypeFile, "notes/one.txt", nil, nil, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := seedArtifact(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA2", target.ID, nil, domain.ArtifactTypeFile, "notes/two.txt", nil, nil, now.Add(1*time.Second)); err != nil {
			t.Fatal(err)
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeArtifacts: 1}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.Artifacts) != 1 {
			t.Fatalf("len(artifacts) = %d, want 1", len(result.Artifacts))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts})
		}
	})

	t.Run("default limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		generator := newTestULIDGenerator(t, now)
		nextID := func() string {
			id, err := generator.New()
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		for index := 0; index < domain.DefaultWorkContextArtifactLimit+1; index++ {
			createdAt := now.Add(time.Duration(index+1) * time.Second)
			if err := seedArtifact(t, db, nextID(), target.ID, nil, domain.ArtifactTypeFile, fmt.Sprintf("notes/%d.txt", index), nil, nil, createdAt); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.Artifacts) != domain.DefaultWorkContextArtifactLimit {
			t.Fatalf("len(artifacts) = %d, want %d", len(result.Artifacts), domain.DefaultWorkContextArtifactLimit)
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeArtifacts})
		}
	})
}

func TestWorkContextRepositoryLoadsAttemptHistoryWhenRequested(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	otherIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FA6"
	if err := seedIssue(t, db, otherIssueID, 2, domain.StatusReady, "other issue", nil, nil, now); err != nil {
		t.Fatal(err)
	}

	generator := newTestULIDGenerator(t, now)
	nextID := func() string {
		id, err := generator.New()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	activeID := nextID()
	completedID := nextID()
	failedID := nextID()
	interruptedID := nextID()
	expiredID := nextID()
	cancelledID := nextID()
	otherAttemptID := nextID()

	activeStartedAt := now.Add(6 * time.Second)
	completedStartedAt := now.Add(5 * time.Second)
	failedStartedAt := now.Add(4 * time.Second)
	interruptedStartedAt := now.Add(3 * time.Second)
	expiredStartedAt := now.Add(2 * time.Second)
	cancelledStartedAt := now.Add(2 * time.Second)

	if err := seedAttempt(t, db, activeID, target.ID, domain.AttemptStatusActive, activeStartedAt.Add(10*time.Minute), activeStartedAt, nil, now.Add(1*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, completedID, target.ID, domain.AttemptStatusCompleted, completedStartedAt.Add(10*time.Minute), completedStartedAt, timePtr(completedStartedAt.Add(1*time.Second)), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, failedID, target.ID, domain.AttemptStatusFailed, failedStartedAt.Add(10*time.Minute), failedStartedAt, timePtr(failedStartedAt.Add(1*time.Second)), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, interruptedID, target.ID, domain.AttemptStatusInterrupted, interruptedStartedAt.Add(10*time.Minute), interruptedStartedAt, timePtr(interruptedStartedAt.Add(1*time.Second)), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, expiredID, target.ID, domain.AttemptStatusExpired, expiredStartedAt.Add(10*time.Minute), expiredStartedAt, timePtr(expiredStartedAt.Add(1*time.Second)), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, cancelledID, target.ID, domain.AttemptStatusCancelled, cancelledStartedAt.Add(10*time.Minute), cancelledStartedAt, timePtr(cancelledStartedAt.Add(1*time.Second)), now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, otherAttemptID, otherIssueID, domain.AttemptStatusCompleted, now.Add(20*time.Minute), now.Add(20*time.Second), timePtr(now.Add(20*time.Second)), now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}

	agentLabel := "agent"
	resultSummary := "completed result"
	reasonDetails := "details"
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE work_attempts SET agent_label = ?, result_summary = ?, next_steps_json = ?, verification_json = ?, reason_details = ? WHERE id = ?`, agentLabel, resultSummary, `["step one"]`, `["verify one"]`, reasonDetails, completedID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.AttemptHistory == nil || len(result.AttemptHistory) != 0 {
		t.Fatalf("len(attempt history) = %d, want 0", len(result.AttemptHistory))
	}
	if result.Truncated {
		t.Fatal("default context should not be truncated")
	}
	if len(result.TruncatedSections) != 0 {
		t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
	}

	result, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory}}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.AttemptHistory) != 6 {
		t.Fatalf("len(attempt history) = %d, want 6", len(result.AttemptHistory))
	}
	gotStatuses := make([]domain.AttemptStatus, len(result.AttemptHistory))
	for index, attempt := range result.AttemptHistory {
		gotStatuses[index] = attempt.Status
	}
	wantStatuses := []domain.AttemptStatus{domain.AttemptStatusActive, domain.AttemptStatusCompleted, domain.AttemptStatusFailed, domain.AttemptStatusInterrupted, domain.AttemptStatusExpired, domain.AttemptStatusCancelled}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("attempt statuses = %v, want %v", gotStatuses, wantStatuses)
	}
	if result.AttemptHistory[4].ID >= result.AttemptHistory[5].ID {
		t.Fatalf("tied attempts should be ordered by id asc: %q >= %q", result.AttemptHistory[4].ID, result.AttemptHistory[5].ID)
	}
	completedAttempt := result.AttemptHistory[1]
	if completedAttempt.ID != completedID {
		t.Fatalf("completed attempt id = %q, want %q", completedAttempt.ID, completedID)
	}
	wantCompleted := domain.WorkAttempt{
		ID:                    completedID,
		IssueID:               target.ID,
		AgentLabel:            &agentLabel,
		Kind:                  domain.AttemptKindWork,
		Status:                domain.AttemptStatusCompleted,
		IssueVersionAtStart:   1,
		ContextEventIDAtStart: 0,
		LeaseExpiresAt:        completedStartedAt.Add(10 * time.Minute),
		StartedAt:             completedStartedAt,
		LastHeartbeatAt:       now.Add(2 * time.Minute),
		FinishedAt:            timePtr(completedStartedAt.Add(1 * time.Second)),
		ResultSummary:         &resultSummary,
		NextSteps:             []string{"step one"},
		Verification:          []string{"verify one"},
		ReasonDetails:         &reasonDetails,
	}
	if !reflect.DeepEqual(completedAttempt, wantCompleted) {
		t.Fatalf("completed attempt = %#v, want %#v", completedAttempt, wantCompleted)
	}
	if completedAttempt.FailureReasonCode != nil || completedAttempt.InterruptionReasonCode != nil {
		t.Fatalf("completed attempt should not include failure or interruption metadata: %#v, %#v", completedAttempt.FailureReasonCode, completedAttempt.InterruptionReasonCode)
	}
	if result.Truncated {
		t.Fatal("attempt history should not be truncated for this request")
	}
	if len(result.TruncatedSections) != 0 {
		t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
	}
}

func TestWorkContextRepositoryBoundsAttemptHistory(t *testing.T) {
	t.Run("custom limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		generator := newTestULIDGenerator(t, now)
		nextID := func() string {
			id, err := generator.New()
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		for index := 0; index < 2; index++ {
			startedAt := now.Add(time.Duration(index+1) * time.Second)
			if err := seedAttempt(t, db, nextID(), target.ID, domain.AttemptStatusCompleted, startedAt.Add(10*time.Minute), startedAt, timePtr(startedAt.Add(1*time.Second)), now.Add(time.Duration(index+1)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeAttemptHistory: 1}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.AttemptHistory) != 1 {
			t.Fatalf("len(attempt history) = %d, want 1", len(result.AttemptHistory))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory})
		}
	})

	t.Run("default limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		generator := newTestULIDGenerator(t, now)
		nextID := func() string {
			id, err := generator.New()
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		for index := 0; index < domain.DefaultWorkContextAttemptHistoryLimit+1; index++ {
			startedAt := now.Add(time.Duration(index+1) * time.Second)
			if err := seedAttempt(t, db, nextID(), target.ID, domain.AttemptStatusCompleted, startedAt.Add(10*time.Minute), startedAt, timePtr(startedAt.Add(1*time.Second)), now.Add(time.Duration(index+1)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.AttemptHistory) != domain.DefaultWorkContextAttemptHistoryLimit {
			t.Fatalf("len(attempt history) = %d, want %d", len(result.AttemptHistory), domain.DefaultWorkContextAttemptHistoryLimit)
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !reflect.DeepEqual(result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory}) {
			t.Fatalf("truncated sections = %#v, want %#v", result.TruncatedSections, []domain.WorkContextInclude{domain.WorkContextIncludeAttemptHistory})
		}
	})
}

func TestWorkContextRepositorySelectsPreviousAttemptAndCheckpoint(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, domain.AttemptStatusActive, now.Add(10*time.Minute), now.Add(20*time.Minute), nil, now.Add(1*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA2", target.ID, domain.AttemptStatusCancelled, now.Add(15*time.Minute), now.Add(25*time.Minute), timePtr(now.Add(2*time.Minute)), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA3", target.ID, domain.AttemptStatusCompleted, now.Add(20*time.Minute), now.Add(30*time.Minute), timePtr(now.Add(3*time.Minute)), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA4", target.ID, domain.AttemptStatusFailed, now.Add(25*time.Minute), now.Add(35*time.Minute), timePtr(now.Add(4*time.Minute)), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA5", target.ID, domain.AttemptStatusCompleted, now.Add(30*time.Minute), now.Add(40*time.Minute), timePtr(now.Add(4*time.Minute)), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedAttemptWithResult(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA4", "result summary", []string{"step one"}, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedNote(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", "01ARZ3NDEKTSV4RRFFQ69G5FA5", domain.AttemptNoteKindProgress, "old note", now.Add(1*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedNote(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA2", "01ARZ3NDEKTSV4RRFFQ69G5FA4", domain.AttemptNoteKindCheckpoint, "checkpoint one", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := seedNote(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA3", "01ARZ3NDEKTSV4RRFFQ69G5FA4", domain.AttemptNoteKindCheckpoint, "checkpoint two", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now.Add(6 * time.Hour)})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if result.PreviousAttempt == nil {
		t.Fatal("previous attempt was nil")
	}
	if result.PreviousAttempt.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA4" {
		t.Fatalf("previous attempt id = %q, want %q", result.PreviousAttempt.ID, "01ARZ3NDEKTSV4RRFFQ69G5FA4")
	}
	if result.PreviousAttempt.ResultSummary == nil || *result.PreviousAttempt.ResultSummary != "result summary" {
		t.Fatalf("result summary = %#v, want %q", result.PreviousAttempt.ResultSummary, "result summary")
	}
	if !reflect.DeepEqual(result.PreviousAttempt.NextSteps, []string{"step one"}) {
		t.Fatalf("next steps = %#v, want %#v", result.PreviousAttempt.NextSteps, []string{"step one"})
	}
	if result.Checkpoint == nil {
		t.Fatal("checkpoint was nil")
	}
	if result.Checkpoint.ID != "01ARZ3NDEKTSV4RRFFQ69G5FA3" {
		t.Fatalf("checkpoint id = %q, want %q", result.Checkpoint.ID, "01ARZ3NDEKTSV4RRFFQ69G5FA3")
	}
}

func TestWorkContextRepositoryEmitsRepeatedFailureWarning(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("warns for three consecutive failures", func(t *testing.T) {
		if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, domain.AttemptStatusFailed, now.Add(1*time.Minute), now.Add(2*time.Minute), timePtr(now.Add(3*time.Minute)), now.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA2", target.ID, domain.AttemptStatusExpired, now.Add(2*time.Minute), now.Add(3*time.Minute), timePtr(now.Add(4*time.Minute)), now.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA3", target.ID, domain.AttemptStatusFailed, now.Add(3*time.Minute), now.Add(4*time.Minute), timePtr(now.Add(5*time.Minute)), now.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now.Add(6 * time.Hour)})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if !reflect.DeepEqual(result.Warnings, []string{"REPEATED_ATTEMPT_FAILURES"}) {
			t.Fatalf("warnings = %#v, want %#v", result.Warnings, []string{"REPEATED_ATTEMPT_FAILURES"})
		}
	})

	t.Run("does not warn when a newer terminal attempt breaks the run", func(t *testing.T) {
		if err := seedAttempt(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA4", target.ID, domain.AttemptStatusCompleted, now.Add(4*time.Minute), now.Add(5*time.Minute), timePtr(now.Add(6*time.Minute)), now.Add(6*time.Minute)); err != nil {
			t.Fatal(err)
		}
		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now.Add(7 * time.Hour)})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.Warnings) != 0 {
			t.Fatalf("warnings = %#v, want empty", result.Warnings)
		}
	})
}

func TestWorkContextRepositoryLoadsChangesSincePreviousAttemptWhenRequested(t *testing.T) {
	t.Run("omits section by default", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 1, target.ID, "issue_updated", nil, nil, `{"changed_fields":["title"]}`, now); err != nil {
			t.Fatal(err)
		}

		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.ChangesSincePreviousAttempt) != 0 {
			t.Fatalf("len(changes) = %d, want 0", len(result.ChangesSincePreviousAttempt))
		}
		if result.Truncated {
			t.Fatal("truncated should be false")
		}
		if len(result.TruncatedSections) != 0 {
			t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
		}
	})

	t.Run("returns empty with no recovery-relevant attempt", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(1 * time.Hour)
		if err := seedAttemptWithBoundary(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAA", target.ID, domain.AttemptStatusCancelled, 7, now.Add(1*time.Minute), now, &finishedAt, now); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 1, target.ID, "issue_updated", nil, nil, `{"changed_fields":["title"]}`, now.Add(1*time.Second)); err != nil {
			t.Fatal(err)
		}

		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeChangesSincePreviousAttempt}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.ChangesSincePreviousAttempt) != 0 {
			t.Fatalf("len(changes) = %d, want 0", len(result.ChangesSincePreviousAttempt))
		}
		if result.Truncated {
			t.Fatal("truncated should be false")
		}
		if len(result.TruncatedSections) != 0 {
			t.Fatalf("len(truncated sections) = %d, want 0", len(result.TruncatedSections))
		}
	})

	t.Run("uses latest terminal boundary and orders events", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(2 * time.Hour)
		if err := seedAttemptWithBoundary(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAA", target.ID, domain.AttemptStatusCompleted, 2, now.Add(1*time.Minute), now, &finishedAt, now); err != nil {
			t.Fatal(err)
		}
		if err := seedAttemptWithBoundary(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAB", target.ID, domain.AttemptStatusFailed, 5, now.Add(2*time.Minute), now.Add(1*time.Minute), &finishedAt, now.Add(1*time.Minute)); err != nil {
			t.Fatal(err)
		}

		otherIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FAD"
		if err := seedIssue(t, db, otherIssueID, 2, domain.StatusReady, "other", nil, nil, now); err != nil {
			t.Fatal(err)
		}
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(id, client_name, started_at, last_seen_at) VALUES (?, 'client', ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAV", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 1, target.ID, "issue_updated", nil, nil, `{"changed_fields":["title"]}`, now); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 2, target.ID, "issue_updated", nil, nil, `{"changed_fields":["description"]}`, now.Add(1*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 3, target.ID, "issue_updated", stringPtr("01ARZ3NDEKTSV4RRFFQ69G5FAV"), stringPtr("01ARZ3NDEKTSV4RRFFQ69G5FAA"), `{"changed_fields":["priority"]}`, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 4, target.ID, "issue_updated", nil, nil, `{"changed_fields":["status"]}`, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 5, otherIssueID, "issue_updated", nil, nil, `{"changed_fields":["priority"]}`, now.Add(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := seedIssueEvent(t, db, 6, target.ID, "custom_event", nil, nil, `{"payload":"value"}`, now.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}

		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeChangesSincePreviousAttempt}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.ChangesSincePreviousAttempt) != 3 {
			t.Fatalf("len(changes) = %d, want 3", len(result.ChangesSincePreviousAttempt))
		}
		wantIDs := []int64{3, 4, 6}
		gotIDs := make([]int64, len(result.ChangesSincePreviousAttempt))
		for index, event := range result.ChangesSincePreviousAttempt {
			gotIDs[index] = event.ID
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("event ids = %v, want %v", gotIDs, wantIDs)
		}
		first := result.ChangesSincePreviousAttempt[0]
		if first.IssueID == nil || *first.IssueID != target.ID {
			t.Fatalf("first issue id = %#v, want %q", first.IssueID, target.ID)
		}
		if first.EventType != "issue_updated" {
			t.Fatalf("first event type = %q, want %q", first.EventType, "issue_updated")
		}
		if first.SessionID == nil || *first.SessionID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("first session id = %#v, want %q", first.SessionID, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		}
		if first.AttemptID == nil || *first.AttemptID != "01ARZ3NDEKTSV4RRFFQ69G5FAA" {
			t.Fatalf("first attempt id = %#v, want %q", first.AttemptID, "01ARZ3NDEKTSV4RRFFQ69G5FAA")
		}
		if string(first.Payload) != `{"changed_fields":["priority"]}` {
			t.Fatalf("first payload = %q, want %q", string(first.Payload), `{"changed_fields":["priority"]}`)
		}
		if !first.CreatedAt.Equal(now.Add(2 * time.Second)) {
			t.Fatalf("first created_at = %v, want %v", first.CreatedAt, now.Add(2*time.Second))
		}
		last := result.ChangesSincePreviousAttempt[len(result.ChangesSincePreviousAttempt)-1]
		if last.SessionID != nil || last.AttemptID != nil {
			t.Fatalf("last event should have nil session and attempt ids: %#v %#v", last.SessionID, last.AttemptID)
		}
	})
}

func TestWorkContextRepositoryBoundsChangesSincePreviousAttempt(t *testing.T) {
	t.Run("defaults to 20 and truncates", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(1 * time.Hour)
		if err := seedAttemptWithBoundary(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAA", target.ID, domain.AttemptStatusCompleted, 0, now.Add(1*time.Minute), now, &finishedAt, now); err != nil {
			t.Fatal(err)
		}
		for index := 1; index <= 21; index++ {
			if err := seedIssueEvent(t, db, int64(index), target.ID, "issue_updated", nil, nil, fmt.Sprintf(`{"id":%d}`, index), now.Add(time.Duration(index)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}

		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeChangesSincePreviousAttempt}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.ChangesSincePreviousAttempt) != 20 {
			t.Fatalf("len(changes) = %d, want 20", len(result.ChangesSincePreviousAttempt))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !includesWorkContextSection(result.TruncatedSections, domain.WorkContextIncludeChangesSincePreviousAttempt) {
			t.Fatalf("truncated sections = %v, want include", result.TruncatedSections)
		}
	})

	t.Run("uses explicit custom limit", func(t *testing.T) {
		db, _, now, target := newWorkContextTestFixture(t)
		repository, err := sqlite.NewWorkContextRepository(db)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(1 * time.Hour)
		if err := seedAttemptWithBoundary(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FAA", target.ID, domain.AttemptStatusCompleted, 0, now.Add(1*time.Minute), now, &finishedAt, now); err != nil {
			t.Fatal(err)
		}
		for index := 1; index <= 3; index++ {
			if err := seedIssueEvent(t, db, int64(index), target.ID, "issue_updated", nil, nil, fmt.Sprintf(`{"id":%d}`, index), now.Add(time.Duration(index)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}

		result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID, Include: []domain.WorkContextInclude{domain.WorkContextIncludeChangesSincePreviousAttempt}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeChangesSincePreviousAttempt: 2}}, Now: now})
		if err != nil {
			t.Fatalf("GetWorkContext() error = %v", err)
		}
		if len(result.ChangesSincePreviousAttempt) != 2 {
			t.Fatalf("len(changes) = %d, want 2", len(result.ChangesSincePreviousAttempt))
		}
		if !result.Truncated {
			t.Fatal("truncated should be true")
		}
		if !includesWorkContextSection(result.TruncatedSections, domain.WorkContextIncludeChangesSincePreviousAttempt) {
			t.Fatalf("truncated sections = %v, want include", result.TruncatedSections)
		}
	})
}

func TestWorkContextRepositoryMapsCorruption(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA1", "decision", "   ", "content", domain.DecisionStatusActive, now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}

	_, err = repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now.Add(1 * time.Minute)})
	assertDomainCode(t, err, domain.CodeStorageCorrupt)
}

func TestWorkContextRepositoryUsesCommittedSnapshotWhileWriterTransactionIsOpen(t *testing.T) {
	db, dbPath, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	conn, err := rawDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}()
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'pending', 'pending', 'pending', 'active', ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}
	if len(result.Decisions) != 0 {
		t.Fatalf("len(decisions) = %d, want 0", len(result.Decisions))
	}
}

func TestWorkContextRepositoryClonesReturnedData(t *testing.T) {
	db, _, now, target := newWorkContextTestFixture(t)
	repository, err := sqlite.NewWorkContextRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedIssue(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", 2, domain.StatusReady, "blocker", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := seedBlocksRelation(t, db, "01ARZ3NDEKTSV4RRFFQ69G5FA1", target.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := seedDecision(t, db, stringPtr(target.ID), "01ARZ3NDEKTSV4RRFFQ69G5FA2", "decision", "summary", "content", domain.DecisionStatusActive, now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("GetWorkContext() error = %v", err)
	}

	*result.Issue.Description = "mutated description"
	result.Blockers[0].Title = "mutated blocker"
	result.Decisions[0].Title = "mutated decision"
	result.Warnings = append(result.Warnings, "mutated warning")

	second, err := repository.GetWorkContext(context.Background(), ports.GetWorkContextCommand{Input: domain.GetWorkContextInput{IssueID: target.ID}, Now: now})
	if err != nil {
		t.Fatalf("second GetWorkContext() error = %v", err)
	}
	if second.Issue.Description == nil || *second.Issue.Description == "mutated description" {
		t.Fatalf("description changed unexpectedly: %#v", second.Issue.Description)
	}
	if len(second.Blockers) == 0 || second.Blockers[0].Title == "mutated blocker" {
		t.Fatalf("blocker title changed unexpectedly: %#v", second.Blockers)
	}
	if len(second.Decisions) == 0 || second.Decisions[0].Title == "mutated decision" {
		t.Fatalf("decision title changed unexpectedly: %#v", second.Decisions)
	}
	if len(second.Warnings) != 0 {
		t.Fatalf("warnings changed unexpectedly: %#v", second.Warnings)
	}
}

func newWorkContextTestFixture(t *testing.T) (*sqlite.DB, string, time.Time, domain.Issue) {
	t.Helper()
	now := time.Date(2026, 7, 14, 10, 11, 12, 123_000_000, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "work-context.db")
	db := openTestDB(t, dbPath, true)
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(context.Background(), db, fakeClock); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 2, ?, ?)`, "01ARZ3NDEKTSV4RRFFQ69G5FAV", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	target := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FA0", DisplayID: "ISSUE-1", SequenceNo: 1, Type: domain.TypeTask, Title: "target", Status: domain.StatusReady, Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}
	description := "target description"
	acceptanceCriteria := "target acceptance criteria"
	if err := seedIssue(t, db, target.ID, target.SequenceNo, target.Status, target.Title, &description, &acceptanceCriteria, now); err != nil {
		t.Fatal(err)
	}
	return db, dbPath, now, target
}

func seedIssue(t *testing.T, db *sqlite.DB, id string, sequenceNo int64, status domain.Status, title string, description, acceptanceCriteria *string, now time.Time) error {
	t.Helper()
	return seedIssueWithType(t, db, id, sequenceNo, domain.TypeTask, status, title, description, acceptanceCriteria, now)
}

func seedIssueWithType(t *testing.T, db *sqlite.DB, id string, sequenceNo int64, issueType domain.Type, status domain.Status, title string, description, acceptanceCriteria *string, now time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		var descriptionValue any
		if description != nil {
			descriptionValue = *description
		} else {
			descriptionValue = nil
		}
		var acceptanceValue any
		if acceptanceCriteria != nil {
			acceptanceValue = *acceptanceCriteria
		} else {
			acceptanceValue = nil
		}
		blockedReasonValue := any(nil)
		if status == domain.StatusBlocked {
			blockedReasonValue = "blocked reason"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, description, acceptance_criteria, status, priority, blocked_reason, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'medium', ?, 1, ?, ?) ON CONFLICT(id) DO UPDATE SET sequence_no = excluded.sequence_no, type = excluded.type, title = excluded.title, description = excluded.description, acceptance_criteria = excluded.acceptance_criteria, status = excluded.status, priority = excluded.priority, blocked_reason = excluded.blocked_reason, version = excluded.version, updated_at = excluded.updated_at`, id, sequenceNo, issueType, title, descriptionValue, acceptanceValue, status, blockedReasonValue, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("seed issue %s: %w", id, err)
		}
		return nil
	})
}

func seedIssueWithContent(t *testing.T, db *sqlite.DB, id string, sequenceNo int64, status domain.Status, title string, description, acceptanceCriteria *string, now time.Time) error {
	t.Helper()
	return seedIssue(t, db, id, sequenceNo, status, title, description, acceptanceCriteria, now)
}

func seedArchivedIssue(t *testing.T, db *sqlite.DB, id string, now time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE issues SET archived_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), id)
		return err
	})
}

func seedBlocksRelation(t *testing.T, db *sqlite.DB, sourceID, targetID string, now time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'blocks', ?)`, sourceID, sourceID, targetID, now.Format(time.RFC3339Nano))
		return err
	})
}

func seedRelation(t *testing.T, db *sqlite.DB, sourceID, targetID string, relationType domain.RelationType, id string, now time.Time) error {
	t.Helper()
	if relationType == domain.RelationTypeRelatedTo && sourceID > targetID {
		sourceID, targetID = targetID, sourceID
	}
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, ?, ?)`, id, sourceID, targetID, relationType, now.Format(time.RFC3339Nano))
		return err
	})
}

func newTestULIDGenerator(t *testing.T, now time.Time) *ids.Generator {
	t.Helper()
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	return generator
}

func seedIssueEvent(t *testing.T, db *sqlite.DB, id int64, issueID string, eventType string, sessionID, attemptID *string, payload string, createdAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		var sessionValue any
		if sessionID != nil {
			sessionValue = *sessionID
		} else {
			sessionValue = nil
		}
		var attemptValue any
		if attemptID != nil {
			attemptValue = *attemptID
		} else {
			attemptValue = nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(id, issue_id, event_type, session_id, attempt_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, issueID, eventType, sessionValue, attemptValue, payload, createdAt.Format(time.RFC3339Nano))
		return err
	})
}

func seedAttemptWithBoundary(t *testing.T, db *sqlite.DB, id, issueID string, status domain.AttemptStatus, boundary int64, leaseExpiresAt, startedAt time.Time, finishedAt *time.Time, now time.Time) error {
	t.Helper()
	var finishedValue any
	if finishedAt != nil {
		finishedValue = finishedAt.Format(time.RFC3339Nano)
	}
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		failureReasonCode := any(nil)
		if status == domain.AttemptStatusFailed {
			failureReasonCode = domain.FailureReasonImplementationError
		}
		interruptionReasonCode := any(nil)
		if status == domain.AttemptStatusInterrupted {
			interruptionReasonCode = domain.InterruptionReasonHandoff
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, failure_reason_code, interruption_reason_code) VALUES (?, ?, 'work', ?, 1, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, id, issueID, status, boundary, []byte{1, 2, 3}, leaseExpiresAt.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), finishedValue, failureReasonCode, interruptionReasonCode)
		return err
	})
}

func seedArtifact(t *testing.T, db *sqlite.DB, id, issueID string, attemptID *string, artifactType domain.ArtifactType, uri string, title *string, metadata json.RawMessage, createdAt time.Time) error {
	t.Helper()
	var attemptValue any
	if attemptID != nil {
		attemptValue = *attemptID
	} else {
		attemptValue = nil
	}
	var titleValue any
	if title != nil {
		titleValue = *title
	} else {
		titleValue = nil
	}
	var metadataValue any
	if metadata != nil {
		metadataValue = string(metadata)
	} else {
		metadataValue = nil
	}
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, issueID, attemptValue, artifactType, uri, titleValue, metadataValue, createdAt.Format(time.RFC3339Nano))
		return err
	})
}

func seedDecision(t *testing.T, db *sqlite.DB, issueID *string, id, title, summary, content string, status domain.DecisionStatus, createdAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if issueID == nil {
			_, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, NULL, ?, ?, ?, ?, ?)`, id, title, summary, content, status, createdAt.Format(time.RFC3339Nano))
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, *issueID, title, summary, content, status, createdAt.Format(time.RFC3339Nano))
		return err
	})
}

func seedComment(t *testing.T, db *sqlite.DB, id, issueID, content string, createdAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, ?, ?)`, id, issueID, content, createdAt.Format(time.RFC3339Nano))
		return err
	})
}

func seedAttempt(t *testing.T, db *sqlite.DB, id, issueID string, status domain.AttemptStatus, leaseExpiresAt, startedAt time.Time, finishedAt *time.Time, now time.Time) error {
	t.Helper()
	var finishedValue any
	if finishedAt != nil {
		finishedValue = finishedAt.Format(time.RFC3339Nano)
	}
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		failureReasonCode := any(nil)
		if status == domain.AttemptStatusFailed {
			failureReasonCode = domain.FailureReasonImplementationError
		}
		interruptionReasonCode := any(nil)
		if status == domain.AttemptStatusInterrupted {
			interruptionReasonCode = domain.InterruptionReasonHandoff
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, failure_reason_code, interruption_reason_code) VALUES (?, ?, 'work', ?, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?)`, id, issueID, status, []byte{1, 2, 3}, leaseExpiresAt.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), finishedValue, nil, failureReasonCode, interruptionReasonCode)
		return err
	})
}

func seedAttemptWithResult(t *testing.T, db *sqlite.DB, attemptID string, resultSummary string, nextSteps []string, finishedAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		var nextStepsJSON any = nil
		if len(nextSteps) > 0 {
			nextStepsJSON = `[
"step one"
]`
		}
		_, err := tx.ExecContext(ctx, `UPDATE work_attempts SET result_summary = ?, next_steps_json = ?, finished_at = ? WHERE id = ?`, resultSummary, nextStepsJSON, finishedAt.Format(time.RFC3339Nano), attemptID)
		return err
	})
}

func seedActiveAttempt(t *testing.T, db *sqlite.DB, issueID string, leaseExpiresAt, startedAt, finishedAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary) VALUES (?, ?, 'work', 'active', 1, 0, ?, ?, ?, ?, NULL, NULL)`, "01ARZ3NDEKTSV4RRFFQ69G5FA9", issueID, []byte{1, 2, 3}, leaseExpiresAt.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano))
		return err
	})
}

func seedNote(t *testing.T, db *sqlite.DB, id, attemptID string, kind domain.AttemptNoteKind, content string, createdAt time.Time) error {
	t.Helper()
	return db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(id, attempt_id, kind, content, important, created_at) VALUES (?, ?, ?, ?, 0, ?)`, id, attemptID, kind, content, createdAt.Format(time.RFC3339Nano))
		return err
	})
}

func blockerIDsFrom(blockers []domain.WorkContextIssue) []string {
	result := make([]string, len(blockers))
	for index, blocker := range blockers {
		result[index] = blocker.ID
	}
	return result
}

func includesWorkContextSection(values []domain.WorkContextInclude, wanted domain.WorkContextInclude) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func timePtr(value time.Time) *time.Time {
	return &value
}
