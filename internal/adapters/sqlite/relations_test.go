package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"crypto/rand"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
)

func TestRelationRepositoryCanonicalizesPersistsEventsAndRemoves(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	first := createRelationTestIssue(t, issues, "First")
	second := createRelationTestIssue(t, issues, "Second")

	added, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: second.DisplayID, TargetIssueID: first.ID,
		RelationType: domain.RelationTypeRelatedTo,
	})
	if err != nil {
		t.Fatalf("add related_to: %v", err)
	}
	if !added.Changed || added.Relation.SourceIssueID > added.Relation.TargetIssueID ||
		len(added.AffectedIssues) != 2 || added.AffectedIssues[0].ID != added.Relation.SourceIssueID ||
		added.AffectedIssues[1].ID != added.Relation.TargetIssueID {
		t.Fatalf("add result = %#v", added)
	}

	repeated, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: first.ID, TargetIssueID: second.ID,
		RelationType: domain.RelationTypeRelatedTo,
	})
	if err != nil {
		t.Fatalf("repeat related_to add: %v", err)
	}
	if repeated.Changed || repeated.Relation != added.Relation ||
		len(repeated.AffectedIssues) != 2 ||
		repeated.AffectedIssues[0].ID != added.Relation.SourceIssueID ||
		repeated.AffectedIssues[1].ID != added.Relation.TargetIssueID ||
		!reflect.DeepEqual(repeated.AffectedIssues, added.AffectedIssues) {
		t.Fatalf("repeat add result = %#v", repeated)
	}

	removed, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionRemove, SourceIssueID: first.DisplayID, TargetIssueID: second.DisplayID,
		RelationType: domain.RelationTypeRelatedTo,
	})
	if err != nil {
		t.Fatalf("remove related_to: %v", err)
	}
	if !removed.Changed || removed.Relation.ID != added.Relation.ID || !removed.Relation.CreatedAt.Equal(now) {
		t.Fatalf("remove result = %#v", removed)
	}
	noOp, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionRemove, SourceIssueID: second.ID, TargetIssueID: first.ID,
		RelationType: domain.RelationTypeRelatedTo,
	})
	if err != nil {
		t.Fatalf("remove absent relation: %v", err)
	}
	if noOp.Changed || noOp.Relation.ID != "" || noOp.Relation.SourceIssueID != added.Relation.SourceIssueID ||
		noOp.Relation.TargetIssueID != added.Relation.TargetIssueID || len(noOp.AffectedIssues) != 2 ||
		noOp.AffectedIssues[0].ID != added.Relation.SourceIssueID ||
		noOp.AffectedIssues[1].ID != added.Relation.TargetIssueID {
		t.Fatalf("remove no-op result = %#v", noOp)
	}

	var relationsCount, addedEvents, removedEvents int
	var eventIssueIDs, payloads []string
	var versions []int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations").Scan(&relationsCount); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'relation_added'").Scan(&addedEvents); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'relation_removed'").Scan(&removedEvents); err != nil {
			return err
		}
		rows, err := query.QueryContext(ctx, `SELECT issue_id, payload FROM issue_events
			WHERE event_type IN ('relation_added', 'relation_removed') ORDER BY id ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var issueID, payload string
			if err := rows.Scan(&issueID, &payload); err != nil {
				return err
			}
			eventIssueIDs = append(eventIssueIDs, issueID)
			payloads = append(payloads, payload)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		rows, err = query.QueryContext(ctx, "SELECT version FROM issues ORDER BY id ASC")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var version int64
			if err := rows.Scan(&version); err != nil {
				return err
			}
			versions = append(versions, version)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if relationsCount != 0 || addedEvents != 2 || removedEvents != 2 {
		t.Fatalf("relation state = relations=%d added_events=%d removed_events=%d", relationsCount, addedEvents, removedEvents)
	}
	wantEventIssueIDs := []string{added.Relation.SourceIssueID, added.Relation.TargetIssueID, added.Relation.SourceIssueID, added.Relation.TargetIssueID}
	if len(eventIssueIDs) != 4 {
		t.Fatalf("relation events = %v", eventIssueIDs)
	}
	for index := range wantEventIssueIDs {
		if eventIssueIDs[index] != wantEventIssueIDs[index] || payloads[index] != payloads[index/2*2] {
			t.Fatalf("relation events = issue_ids=%v payloads=%v", eventIssueIDs, payloads)
		}
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 1 {
		t.Fatalf("relation mutation changed issue versions: %v", versions)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &payload); err != nil || payload["relation_id"] != added.Relation.ID {
		t.Fatalf("event payload = %q, error = %v", payloads[0], err)
	}
}

func TestRelationRepositoryRejectsBlocksCyclesInsideWriteTransaction(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	first := createRelationTestIssue(t, issues, "First")
	second := createRelationTestIssue(t, issues, "Second")
	third := createRelationTestIssue(t, issues, "Third")
	for _, input := range []domain.ManageIssueRelationInput{
		{Action: domain.RelationActionAdd, SourceIssueID: first.ID, TargetIssueID: second.ID, RelationType: domain.RelationTypeBlocks},
		{Action: domain.RelationActionAdd, SourceIssueID: second.ID, TargetIssueID: third.ID, RelationType: domain.RelationTypeBlocks},
	} {
		if _, err := relations.ManageIssueRelation(ctx, input); err != nil {
			t.Fatalf("add blocks edge: %v", err)
		}
	}
	_, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: third.ID, TargetIssueID: first.ID, RelationType: domain.RelationTypeBlocks,
	})
	assertDomainCode(t, err, domain.CodeBlocksCycle)

	var count, events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations WHERE type = 'blocks'").Scan(&count); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE event_type = 'relation_added'").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 || events != 4 {
		t.Fatalf("cycle rejection changed state: relations=%d events=%d", count, events)
	}
}

func TestRelationRepositoryRejectsArchivedEndpointsWithoutChanges(t *testing.T) {
	for _, test := range []struct {
		name     string
		action   domain.RelationAction
		archived string
	}{
		{name: "add source", action: domain.RelationActionAdd, archived: "source"},
		{name: "add target", action: domain.RelationActionAdd, archived: "target"},
		{name: "remove source", action: domain.RelationActionRemove, archived: "source"},
		{name: "remove target", action: domain.RelationActionRemove, archived: "target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			issues, db, now := openIssueService(t)
			relations := openRelationService(t, db, now)
			ctx := context.Background()
			source := createRelationTestIssue(t, issues, "Source")
			target := createRelationTestIssue(t, issues, "Target")
			if test.action == domain.RelationActionRemove {
				if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
					Action: domain.RelationActionAdd, SourceIssueID: source.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
				}); err != nil {
					t.Fatalf("prepare relation: %v", err)
				}
			}
			archived := source
			identifier := source.DisplayID
			if test.archived == "target" {
				archived, identifier = target, target.ID
			}
			if _, err := issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: identifier, ExpectedVersion: archived.Issue.Version}); err != nil {
				t.Fatalf("archive endpoint: %v", err)
			}

			_, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
				Action: test.action, SourceIssueID: source.ID, TargetIssueID: target.DisplayID, RelationType: domain.RelationTypeBlocks,
			})
			assertDomainCode(t, err, domain.CodeIssueArchived)

			var relationsCount, eventCount int
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations").Scan(&relationsCount); err != nil {
					return err
				}
				return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events
					WHERE event_type IN ('relation_added', 'relation_removed')`).Scan(&eventCount)
			}); err != nil {
				t.Fatal(err)
			}
			wantRelations, wantEvents := 0, 0
			if test.action == domain.RelationActionRemove {
				wantRelations, wantEvents = 1, 2
			}
			if relationsCount != wantRelations || eventCount != wantEvents {
				t.Fatalf("archived operation changed state: relations=%d events=%d", relationsCount, eventCount)
			}
		})
	}
}

func TestRelationRepositoryDerivesBlockerProjections(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	target, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Target", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	unresolved, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Unresolved", Status: domain.StatusReady})
	if err != nil {
		t.Fatal(err)
	}
	done, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Done", Status: domain.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "Cancelled", Status: domain.StatusCancelled})
	if err != nil {
		t.Fatal(err)
	}
	archived := createRelationTestIssue(t, issues, "Archived")
	for _, source := range []application.CreateIssueResult{unresolved, done, cancelled, archived} {
		if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
			Action: domain.RelationActionAdd, SourceIssueID: source.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
		}); err != nil {
			t.Fatalf("add blocker %s: %v", source.ID, err)
		}
	}
	if _, err := issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: archived.ID, ExpectedVersion: archived.Issue.Version}); err != nil {
		t.Fatal(err)
	}
	assertBlockerProjection(t, issues, target.ID, 1, true, false)
	assertProjectionFilterContains(t, issues, domain.ListIssuesInput{IsBlocked: boolPointer(true)}, target.ID)

	if _, err := issues.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: unresolved.ID, ExpectedVersion: unresolved.Issue.Version,
		Changes: domain.IssuePatch{Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusDone}},
	}); err != nil {
		t.Fatal(err)
	}
	assertBlockerProjection(t, issues, target.ID, 0, false, true)
	assertProjectionFilterContains(t, issues, domain.ListIssuesInput{IsClaimable: boolPointer(true)}, target.ID)

	if _, err := issues.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: done.ID, ExpectedVersion: done.Issue.Version,
		Changes: domain.IssuePatch{Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusReady}},
	}); err != nil {
		t.Fatal(err)
	}
	assertBlockerProjection(t, issues, target.ID, 1, true, false)
	if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionRemove, SourceIssueID: done.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
	}); err != nil {
		t.Fatal(err)
	}
	assertBlockerProjection(t, issues, target.ID, 0, false, true)
}

func TestRelationRepositoryUsesDerivedClaimabilityForOrderingAndCursor(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	target, err := issues.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Target", Status: domain.StatusReady, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := issues.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "Blocker", Status: domain.StatusReady, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: blocker.ID, TargetIssueID: target.ID, RelationType: domain.RelationTypeBlocks,
	}); err != nil {
		t.Fatal(err)
	}

	firstPage, err := issues.ListIssues(ctx, domain.ListIssuesInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != blocker.ID || !firstPage.Items[0].IsClaimable ||
		!firstPage.HasMore || firstPage.NextCursor == nil {
		t.Fatalf("first claimability page = %#v", firstPage)
	}
	secondPage, err := issues.ListIssues(ctx, domain.ListIssuesInput{Limit: 1, Cursor: *firstPage.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != target.ID || secondPage.Items[0].IsClaimable ||
		secondPage.Items[0].UnresolvedBlockerCount != 1 || secondPage.HasMore {
		t.Fatalf("second claimability page = %#v", secondPage)
	}
}

func assertBlockerProjection(t *testing.T, service *application.IssueService, issueID string, count int64, blocked, claimable bool) {
	t.Helper()
	page, err := service.ListIssues(context.Background(), domain.ListIssuesInput{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == issueID {
			if item.UnresolvedBlockerCount != count || item.IsBlocked != blocked || item.IsClaimable != claimable {
				t.Fatalf("blocker projection = %#v", item)
			}
			return
		}
	}
	t.Fatalf("target issue %s not listed", issueID)
}

func assertProjectionFilterContains(t *testing.T, service *application.IssueService, input domain.ListIssuesInput, issueID string) {
	t.Helper()
	page, err := service.ListIssues(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == issueID {
			return
		}
	}
	t.Fatalf("filtered issues do not contain %s: %#v", issueID, page.Items)
}

func TestRelationRepositoryRollsBackWhenEventAppendFails(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	first := createRelationTestIssue(t, issues, "First")
	second := createRelationTestIssue(t, issues, "Second")
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_relation_event
			BEFORE INSERT ON issue_events
			WHEN NEW.event_type = 'relation_added'
			BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err := relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: first.ID, TargetIssueID: second.ID, RelationType: domain.RelationTypeBlocks,
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)
	var count int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("relation survived failed event append: %d", count)
	}
}

func TestRelationRepositoryConcurrentReverseBlocksAllowsOnlyOne(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	first := createRelationTestIssue(t, issues, "First")
	second := createRelationTestIssue(t, issues, "Second")

	inputs := []domain.ManageIssueRelationInput{
		{Action: domain.RelationActionAdd, SourceIssueID: first.ID, TargetIssueID: second.ID, RelationType: domain.RelationTypeBlocks},
		{Action: domain.RelationActionAdd, SourceIssueID: second.ID, TargetIssueID: first.ID, RelationType: domain.RelationTypeBlocks},
	}
	start := make(chan struct{})
	errs := make(chan error, len(inputs))
	var group sync.WaitGroup
	for _, input := range inputs {
		group.Add(1)
		go func(input domain.ManageIssueRelationInput) {
			defer group.Done()
			<-start
			_, err := relations.ManageIssueRelation(context.Background(), input)
			errs <- err
		}(input)
	}
	close(start)
	group.Wait()
	close(errs)

	successes, cycles := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, &domain.Error{Code: domain.CodeBlocksCycle}) {
			cycles++
		} else {
			t.Fatalf("concurrent relation error = %v", err)
		}
	}
	if successes != 1 || cycles != 1 {
		t.Fatalf("concurrent outcomes: successes=%d cycles=%d", successes, cycles)
	}
}

func TestRelationRepositoryIdempotencyReplayAndConflict(t *testing.T) {
	issues, db, now := openIssueService(t)
	relations := openRelationService(t, db, now)
	ctx := context.Background()
	first := createRelationTestIssue(t, issues, "First")
	second := createRelationTestIssue(t, issues, "Second")
	third := createRelationTestIssue(t, issues, "Third")

	key := "relation-retry"
	input := domain.ManageIssueRelationInput{
		Action: domain.RelationActionAdd, SourceIssueID: first.ID, TargetIssueID: second.ID,
		RelationType: domain.RelationTypeBlocks, IdempotencyKey: &key,
	}
	added, err := relations.ManageIssueRelation(ctx, input)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if !added.Changed || added.Relation.ID == "" {
		t.Fatalf("first add result = %#v", added)
	}
	replayed, err := relations.ManageIssueRelation(ctx, input)
	if err != nil {
		t.Fatalf("replay add: %v", err)
	}
	if !reflect.DeepEqual(added, replayed) {
		t.Fatalf("replay mismatch: %#v != %#v", added, replayed)
	}
	changed := input
	changed.TargetIssueID = third.ID
	if _, err := relations.ManageIssueRelation(ctx, changed); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflict = %v", err)
	}
	var relationCount, records int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_relations").Scan(&relationCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records WHERE operation = 'manage_issue_relation' AND idempotency_key = ?", key).Scan(&records)
	}); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || records != 1 {
		t.Fatalf("durable state = relations %d records %d", relationCount, records)
	}
}

func openRelationService(t *testing.T, db *sqlite.DB, now time.Time) *application.RelationService {
	t.Helper()
	repository, err := sqlite.NewRelationRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewRelationService(repository, clock.NewFakeClock(now), generator)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func createRelationTestIssue(t *testing.T, service *application.IssueService, title string) application.CreateIssueResult {
	t.Helper()
	issue, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}
