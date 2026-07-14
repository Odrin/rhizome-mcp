package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

func TestIssueLabelAssignmentsProjectAcrossCreateUpdateGetAndArchive(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "rejected", Labels: []string{"absent"}, CreateMissingLabels: false,
	})
	assertDomainCode(t, err, domain.CodeLabelNotFound)
	created, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "labeled", Labels: []string{" Zebra ", "alpha"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, created.Issue.Labels, []string{"alpha", "Zebra"})

	read, err := service.GetIssue(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, read.Labels, []string{"alpha", "Zebra"})

	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: created.ID, ExpectedVersion: 1, CreateMissingLabels: false,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{"ALPHA", "beta"}}},
	})
	assertDomainCode(t, err, domain.CodeLabelNotFound)

	updated, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: created.ID, ExpectedVersion: 1, CreateMissingLabels: true,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{"ALPHA", "beta"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Issue.Version != 2 || !equalStrings(updated.ChangedFields, []string{"labels"}) {
		t.Fatalf("label update = %#v", updated)
	}
	assertLabelNames(t, updated.Issue.Labels, []string{"alpha", "beta"})
	assertLatestEvent(t, db, created.ID, "labels_changed")
	assertIssueEventCount(t, db, created.ID, 2)

	withoutLabels, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: created.ID, ExpectedVersion: 2,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "still labeled"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, withoutLabels.Issue.Labels, []string{"alpha", "beta"})

	cleared, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: created.ID, ExpectedVersion: 3,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Issue.Version != 4 {
		t.Fatalf("clear version = %d, want 4", cleared.Issue.Version)
	}
	assertLabelNames(t, cleared.Issue.Labels, []string{})
	assertLatestEvent(t, db, created.ID, "labels_changed")
	assertIssueEventCount(t, db, created.ID, 4)

	archived, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: created.ID, ExpectedVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, archived.Issue.Labels, []string{})

	retained, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "retained", Labels: []string{"alpha"}, CreateMissingLabels: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	retainedArchive, err := service.ArchiveIssue(ctx, domain.ArchiveIssueInput{IssueID: retained.ID, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, retainedArchive.Issue.Labels, []string{"alpha"})
	retainedRead, err := service.GetIssue(ctx, retained.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, retainedRead.Labels, []string{"alpha"})
}

func TestIssueLabelPatchWithStatusUsesStatusEvent(t *testing.T) {
	service, db, _ := openIssueService(t)
	issue, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "event"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.UpdateIssue(context.Background(), domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1, CreateMissingLabels: true,
		Changes: domain.IssuePatch{
			Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusReady},
			Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{"release"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Version != 2 || !equalStrings(result.ChangedFields, []string{"labels", "status"}) {
		t.Fatalf("result = %#v", result)
	}
	assertLatestEvent(t, db, issue.ID, "status_changed")
	assertIssueEventCount(t, db, issue.ID, 2)
}

func TestGetIssueReadsVersionAndLabelsFromOneSnapshot(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	created, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "snapshot", Labels: []string{"before"}, CreateMissingLabels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	projectionRead := make(chan struct{})
	continueRead := make(chan struct{})
	restore := sqlite.SetGetIssueAfterProjectionHookForTest(repository, func() {
		close(projectionRead)
		<-continueRead
	})
	t.Cleanup(restore)
	var release sync.Once
	releaseRead := func() {
		release.Do(func() {
			close(continueRead)
		})
	}
	defer releaseRead()

	type getResult struct {
		issue domain.Issue
		err   error
	}
	results := make(chan getResult, 1)
	go func() {
		issue, err := repository.GetIssue(context.Background(), domain.IssueIdentifier{
			Kind: domain.IssueIdentifierInternalID, Value: created.ID,
		})
		results <- getResult{issue: issue, err: err}
	}()
	<-projectionRead

	updated, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: created.ID, ExpectedVersion: 1, CreateMissingLabels: true,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{"after"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Issue.Version != 2 {
		t.Fatalf("updated version = %d, want 2", updated.Issue.Version)
	}

	releaseRead()
	read := <-results
	if read.err != nil {
		t.Fatal(read.err)
	}
	if read.issue.Version != 1 {
		t.Fatalf("read version = %d, want 1", read.issue.Version)
	}
	assertLabelNames(t, read.issue.Labels, []string{"before"})
}

func TestConcurrentMissingLabelCreationConverges(t *testing.T) {
	service, db, _ := openIssueService(t)
	const requests = 2
	results := make(chan domain.Issue, requests)
	errs := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{
				Type: domain.TypeTask, Title: "concurrent", Labels: []string{"Shared"}, CreateMissingLabels: true,
			})
			if err == nil {
				results <- result.Issue
			}
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CreateIssue() error = %v", err)
		}
	}
	issues := make([]domain.Issue, 0, requests)
	for issue := range results {
		issues = append(issues, issue)
	}
	if len(issues) != requests {
		t.Fatalf("created issues = %d", len(issues))
	}
	if issues[0].Labels[0].ID != issues[1].Labels[0].ID {
		t.Fatalf("label IDs = %q and %q", issues[0].Labels[0].ID, issues[1].Labels[0].ID)
	}
	var labels, assignments int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM labels WHERE name = 'shared' COLLATE NOCASE").Scan(&labels); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_labels").Scan(&assignments)
	}); err != nil {
		t.Fatal(err)
	}
	if labels != 1 || assignments != requests {
		t.Fatalf("labels=%d assignments=%d", labels, assignments)
	}
}

func TestLabelAssignmentRollsBackLabelsAndIssueMutationOnEventFailure(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "rollback"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_labels_changed
			BEFORE INSERT ON issue_events
			WHEN NEW.event_type = 'labels_changed'
			BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: 1, CreateMissingLabels: true,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{"rollback-label"}}},
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)

	read, err := service.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Version != 1 {
		t.Fatalf("version = %d, want 1", read.Version)
	}
	assertLabelNames(t, read.Labels, []string{})
	var labels, events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM labels WHERE name = 'rollback-label'").Scan(&labels); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issue.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if labels != 0 || events != 1 {
		t.Fatalf("labels=%d events=%d", labels, events)
	}
}

func TestIssueCreationRollsBackInitialLabelsWhenEventFails(t *testing.T) {
	service, db, _ := openIssueService(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_issue_created
			BEFORE INSERT ON issue_events
			WHEN NEW.event_type = 'issue_created'
			BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "initial rollback", Labels: []string{"initial-label"}, CreateMissingLabels: true,
	})
	assertDomainCode(t, err, domain.CodeStorageConstraint)
	var issues, labels, assignments, events int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&issues); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM labels").Scan(&labels); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM issue_labels").Scan(&assignments); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events").Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if issues != 0 || labels != 0 || assignments != 0 || events != 0 {
		t.Fatalf("issues=%d labels=%d assignments=%d events=%d", issues, labels, assignments, events)
	}
}

func TestListLabelsOrderingQueryAndCursor(t *testing.T) {
	service, _, _ := openIssueService(t)
	ctx := context.Background()
	for _, name := range []string{"delta", "Bravo", "alpha", "charlie"} {
		if _, err := service.CreateIssue(ctx, domain.CreateIssueInput{
			Type: domain.TypeTask, Title: name, Labels: []string{name}, CreateMissingLabels: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ListLabels(ctx, domain.ListLabelsInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, first.Items, []string{"alpha", "Bravo"})
	if !first.HasMore || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}
	second, err := service.ListLabels(ctx, domain.ListLabelsInput{Limit: 2, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, second.Items, []string{"charlie", "delta"})
	if second.HasMore || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}
	filtered, err := service.ListLabels(ctx, domain.ListLabelsInput{Query: "b"})
	if err != nil {
		t.Fatal(err)
	}
	assertLabelNames(t, filtered.Items, []string{"Bravo"})

	_, err = service.ListLabels(ctx, domain.ListLabelsInput{Cursor: "%%%"})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
		len(domainErr.Details) != 1 || domainErr.Details[0].Code != "MALFORMED_CURSOR" {
		t.Fatalf("malformed cursor error = %#v", err)
	}
	_, err = service.ListLabels(ctx, domain.ListLabelsInput{Limit: 101})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func assertLabelNames(t *testing.T, labels []domain.Label, want []string) {
	t.Helper()
	got := make([]string, len(labels))
	for i, label := range labels {
		got[i] = label.Name
		if label.NormalizedName == "" {
			t.Fatalf("label %q has no normalized name", label.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
}

func assertLatestEvent(t *testing.T, db *sqlite.DB, issueID, want string) {
	t.Helper()
	var got string
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `
			SELECT event_type FROM issue_events WHERE issue_id = ?
			ORDER BY id DESC LIMIT 1`, issueID).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("event type = %q, want %q", got, want)
	}
}

func assertIssueEventCount(t *testing.T, db *sqlite.DB, issueID string, want int) {
	t.Helper()
	var got int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, "SELECT count(*) FROM issue_events WHERE issue_id = ?", issueID).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
}
