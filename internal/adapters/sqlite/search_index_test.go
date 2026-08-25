package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

var _ ports.SearchIndexRepository = (*sqlite.SearchIndexRepository)(nil)

func TestSearchIndexTracksSourceMutationsTransactionallyAndRebuilds(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	description := "issue searchable body"
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type:        domain.TypeTask,
		Title:       "searchable issue title",
		Description: &description,
		Status:      domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const (
		commentID  = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		decisionID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		attemptID  = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
		noteID     = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	)
	timestamp := sqlite.FormatStorageTime(now)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'searchable comment body', ?)`, commentID, issue.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(
			id, issue_id, title, summary, content, status, created_at
		) VALUES (?, ?, 'searchable decision title', 'searchable decision summary', 'searchable decision body', 'active', ?)`,
			decisionID, issue.ID, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'01', ?, ?, ?)`,
			attemptID, issue.ID, timestamp, timestamp, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(
			id, attempt_id, kind, content, important, created_at
		) VALUES (?, ?, 'progress', 'searchable note body', 0, ?)`, noteID, attemptID, timestamp)
		return err
	}); err != nil {
		t.Fatalf("insert indexed sources: %v", err)
	}

	initial := searchIndexRows(t, db)
	if got, want := initial, []searchIndexRow{
		{EntityType: "attempt_note", EntityID: noteID, IssueID: issue.ID, Title: "", Content: "searchable note body"},
		{EntityType: "comment", EntityID: commentID, IssueID: issue.ID, Title: "", Content: "searchable comment body"},
		{EntityType: "decision", EntityID: decisionID, IssueID: issue.ID, Title: "searchable decision title", Content: "searchable decision summary\nsearchable decision body"},
		{EntityType: "issue", EntityID: issue.ID, IssueID: issue.ID, Title: "searchable issue title", Content: description},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("search index rows = %#v, want %#v", got, want)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET description = 'updated searchable issue body' WHERE id = ?", issue.ID)
		return err
	}); err != nil {
		t.Fatalf("update indexed issue: %v", err)
	}
	updated := searchIndexRows(t, db)
	if updated[3].Content != "updated searchable issue body" {
		t.Fatalf("updated issue index content = %q", updated[3].Content)
	}

	rollbackCommentID := "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_at)
			VALUES (?, ?, 'rolled back source', ?)`, rollbackCommentID, issue.ID, timestamp); err != nil {
			return err
		}
		return errors.New("force rollback")
	}); err == nil {
		t.Fatal("rolling back indexed source succeeded")
	}
	for _, row := range searchIndexRows(t, db) {
		if row.EntityID == rollbackCommentID {
			t.Fatal("rolled-back source remained in search index")
		}
	}

	repository, err := sqlite.NewSearchIndexRepository(db)
	if err != nil {
		t.Fatalf("NewSearchIndexRepository() error = %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM search_index")
		return err
	}); err != nil {
		t.Fatalf("clear search index: %v", err)
	}
	if err := repository.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if got, want := searchIndexRows(t, db), updated; !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt search index rows = %#v, want %#v", got, want)
	}
}

// TestSearchIndexTracksReservationLifecycleAndRebuildAgrees covers
// ISSUE-182 AC4: a reservation is indexed on creation with its display
// value searchable, releasing it makes the release reason searchable too,
// comparison_value and normalized_json are never findable, and a full
// Rebuild reproduces exactly what the live triggers already wrote.
func TestSearchIndexTracksReservationLifecycleAndRebuildAgrees(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "reservation issue", Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const (
		attemptID     = "01ARZ3NDEKTSV4RRFFQ69G5FHA"
		reservationID = "01ARZ3NDEKTSV4RRFFQ69G5FHB"
	)
	timestamp := sqlite.FormatStorageTime(now)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'05', ?, ?, ?)`,
			attemptID, issue.ID, timestamp, timestamp, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at
		) VALUES (?, ?, ?, 'file', 'searchable reservation display', 'zqxcomparisonmarker', '{"zqxjsonmarker":true}', 'active', 1, ?)`,
			reservationID, issue.ID, attemptID, timestamp)
		return err
	}); err != nil {
		t.Fatalf("insert reservation: %v", err)
	}

	active := searchIndexRows(t, db)
	activeRow := findSearchIndexRow(active, "reservation", reservationID)
	if activeRow == nil {
		t.Fatalf("reservation row not indexed after insert: %#v", active)
	}
	if activeRow.Title != "searchable reservation display" {
		t.Fatalf("reservation title = %q, want the display value", activeRow.Title)
	}
	if activeRow.Content != "file\n" {
		t.Fatalf("reservation content while active = %q, want kind plus an empty release reason", activeRow.Content)
	}

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE resource_reservations SET status = 'released', released_at = ?, release_reason = 'completed' WHERE id = ?`,
			timestamp, reservationID)
		return err
	}); err != nil {
		t.Fatalf("release reservation: %v", err)
	}

	released := searchIndexRows(t, db)
	releasedRow := findSearchIndexRow(released, "reservation", reservationID)
	if releasedRow == nil {
		t.Fatalf("reservation row not indexed after release: %#v", released)
	}
	if releasedRow.Content != "file\ncompleted" {
		t.Fatalf("reservation content after release = %q, want the release reason appended", releasedRow.Content)
	}

	searchRepository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	foundByComparisonValue, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "zqxcomparisonmarker", Limit: 20}})
	if err != nil {
		t.Fatalf("search by comparison_value: %v", err)
	}
	if len(foundByComparisonValue.Results) != 0 {
		t.Fatalf("comparison_value was searchable: %#v", foundByComparisonValue.Results)
	}
	foundByNormalizedJSON, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "zqxjsonmarker", Limit: 20}})
	if err != nil {
		t.Fatalf("search by normalized_json: %v", err)
	}
	if len(foundByNormalizedJSON.Results) != 0 {
		t.Fatalf("normalized_json was searchable: %#v", foundByNormalizedJSON.Results)
	}
	foundByDisplayValue, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "searchable", Limit: 20}})
	if err != nil {
		t.Fatalf("search by display value: %v", err)
	}
	if !containsReservationResult(foundByDisplayValue, reservationID) {
		t.Fatalf("search by display value = %#v, want reservation %q present", foundByDisplayValue, reservationID)
	}

	indexRepository, err := sqlite.NewSearchIndexRepository(db)
	if err != nil {
		t.Fatalf("NewSearchIndexRepository() error = %v", err)
	}
	if err := indexRepository.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	rebuilt := searchIndexRows(t, db)
	if !reflect.DeepEqual(rebuilt, released) {
		t.Fatalf("rebuilt search index rows = %#v, want %#v (trigger and Rebuild must agree)", rebuilt, released)
	}
}

func findSearchIndexRow(rows []searchIndexRow, entityType, entityID string) *searchIndexRow {
	for index := range rows {
		if rows[index].EntityType == entityType && rows[index].EntityID == entityID {
			return &rows[index]
		}
	}
	return nil
}

func containsReservationResult(page domain.SearchPage, reservationID string) bool {
	for _, result := range page.Results {
		if result.EntityType == domain.SearchEntityTypeReservation && result.EntityID == reservationID {
			return true
		}
	}
	return false
}

// TestSearchIndexReviewRowFollowsIssueRename is the ISSUE-214 regression:
// the FTS review row denormalizes the parent issue title, and nothing used
// to refresh it when the issue was renamed, leaving the review indexed
// under a stale title until a full Rebuild.
func TestSearchIndexReviewRowFollowsIssueRename(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "original title", Status: domain.StatusReview,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const (
		targetID = "01ARZ3NDEKTSV4RRFFQ69G5FBV"
		reviewID = "01ARZ3NDEKTSV4RRFFQ69G5FBW"
	)
	timestamp := sqlite.FormatStorageTime(now)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(
			id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
		) VALUES (?, ?, 1, 0, '[]', 1, ?)`, targetID, issue.ID, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
			id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id,
			active_attempt_id, version, created_at, resolved_at
		) VALUES (?, ?, ?, 1, 0, '[]', 'open', NULL, NULL, 1, ?, NULL)`, reviewID, targetID, issue.ID, timestamp)
		return err
	}); err != nil {
		t.Fatalf("insert review request: %v", err)
	}

	before := searchIndexRows(t, db)
	var beforeReview *searchIndexRow
	for index := range before {
		if before[index].EntityType == "review" && before[index].EntityID == reviewID {
			beforeReview = &before[index]
		}
	}
	if beforeReview == nil || beforeReview.Title != "original title review" {
		t.Fatalf("review row before rename = %#v, want title %q", beforeReview, "original title review")
	}

	if _, err := service.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:         issue.ID,
		ExpectedVersion: 1,
		Changes:         domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "renamed title"}},
	}); err != nil {
		t.Fatalf("rename issue: %v", err)
	}

	after := searchIndexRows(t, db)
	var afterReview *searchIndexRow
	for index := range after {
		if after[index].EntityType == "review" && after[index].EntityID == reviewID {
			afterReview = &after[index]
		}
	}
	if afterReview == nil || afterReview.Title != "renamed title review" {
		t.Fatalf("review row after rename = %#v, want title %q", afterReview, "renamed title review")
	}

	searchRepository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	foundByNewTitle, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "renamed", Limit: 20}})
	if err != nil {
		t.Fatalf("search by new title: %v", err)
	}
	if !containsReviewResult(foundByNewTitle, reviewID) {
		t.Fatalf("search by new title = %#v, want review %q present", foundByNewTitle, reviewID)
	}
	foundByOldTitle, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "original", Limit: 20}})
	if err != nil {
		t.Fatalf("search by old title: %v", err)
	}
	if containsReviewResult(foundByOldTitle, reviewID) {
		t.Fatalf("search by old title = %#v, want review %q absent", foundByOldTitle, reviewID)
	}

	// Rebuild must agree with the trigger-maintained row: both derive the
	// review title from the same live issues.title, so a full rebuild after
	// the rename should reproduce exactly what the trigger already wrote,
	// not just repair a fixture the trigger got wrong (ISSUE-214 AC3's
	// join-alignment concern, exercised end-to-end rather than by
	// comparing SQL text).
	indexRepository, err := sqlite.NewSearchIndexRepository(db)
	if err != nil {
		t.Fatalf("NewSearchIndexRepository() error = %v", err)
	}
	if err := indexRepository.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	rebuilt := searchIndexRows(t, db)
	var rebuiltReview *searchIndexRow
	for index := range rebuilt {
		if rebuilt[index].EntityType == "review" && rebuilt[index].EntityID == reviewID {
			rebuiltReview = &rebuilt[index]
		}
	}
	if rebuiltReview == nil || rebuiltReview.Title != afterReview.Title {
		t.Fatalf("rebuilt review row = %#v, want title %q (trigger and Rebuild must agree)", rebuiltReview, afterReview.Title)
	}
}

func containsReviewResult(page domain.SearchPage, reviewID string) bool {
	for _, result := range page.Results {
		if result.EntityType == domain.SearchEntityTypeReview && result.EntityID == reviewID {
			return true
		}
	}
	return false
}

type searchIndexRow struct {
	EntityType string
	EntityID   string
	IssueID    string
	Title      string
	Content    string
}

func searchIndexRows(t *testing.T, db *sqlite.DB) []searchIndexRow {
	t.Helper()
	var result []searchIndexRow
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		// COALESCE: workflow policies are project-scoped and carry a NULL
		// issue_id (migration 014).
		rows, err := query.QueryContext(ctx, `SELECT entity_type, entity_id, COALESCE(issue_id, ''), title, content
			FROM search_index`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row searchIndexRow
			if err := rows.Scan(&row.EntityType, &row.EntityID, &row.IssueID, &row.Title, &row.Content); err != nil {
				return err
			}
			result = append(result, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read search index: %v", err)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EntityType != result[j].EntityType {
			return result[i].EntityType < result[j].EntityType
		}
		return result[i].EntityID < result[j].EntityID
	})
	return result
}

// TestSearchIndexTracksGateEntitiesAndRebuildAgrees covers ISSUE-175 AC4: a
// workflow policy is indexed by its declared requirement keys, evidence
// keys, purposes, and selector (the locked schema has no free-text name),
// gate evidence is indexed by key, summary, and details, updates re-index
// both, a wrong-shaped requirements blob degrades to empty index text
// instead of failing the write, and a full Rebuild reproduces exactly what
// the live triggers wrote.
func TestSearchIndexTracksGateEntitiesAndRebuildAgrees(t *testing.T) {
	service, db, now := openIssueService(t)
	ctx := context.Background()
	issue, err := service.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "gated search issue", Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	const (
		attemptID  = "01ARZ3NDEKTSV4RRFFQ69G5FJA"
		policyID   = "01ARZ3NDEKTSV4RRFFQ69G5FJB"
		evidenceID = "01ARZ3NDEKTSV4RRFFQ69G5FJC"
	)
	timestamp := sqlite.FormatStorageTime(now)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'06', ?, ?, ?)`,
			attemptID, issue.ID, timestamp, timestamp, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at)
			VALUES (?, '{"issue_types":["task"]}', '[{"key":"zqxpolicykey","kind":"review_approval","purpose":"zqxsecuritypurpose"}]', 'active', 1, ?, ?)`,
			policyID, timestamp, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO gate_evidence(id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at)
			VALUES (?, ?, ?, 'zqxevidencekey', 'satisfied', 'zqxevidencesummary text', 'zqxevidencedetails text', '[]', 1, ?, ?)`,
			evidenceID, attemptID, issue.ID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatalf("insert gate fixtures: %v", err)
	}

	rows := searchIndexRows(t, db)
	policyRow := findSearchIndexRow(rows, "workflow_policy", policyID)
	if policyRow == nil {
		t.Fatalf("workflow policy not indexed: %#v", rows)
	}
	if policyRow.Title != "zqxpolicykey" {
		t.Fatalf("policy title = %q, want the requirement key", policyRow.Title)
	}
	if !strings.Contains(policyRow.Content, "zqxsecuritypurpose") || !strings.Contains(policyRow.Content, "task") {
		t.Fatalf("policy content = %q, want the purpose and selector text", policyRow.Content)
	}
	evidenceRow := findSearchIndexRow(rows, "gate_evidence", evidenceID)
	if evidenceRow == nil {
		t.Fatalf("gate evidence not indexed: %#v", rows)
	}
	if evidenceRow.Title != "zqxevidencekey" || !strings.Contains(evidenceRow.Content, "zqxevidencesummary") || !strings.Contains(evidenceRow.Content, "zqxevidencedetails") {
		t.Fatalf("evidence row = %#v, want key title with summary and details content", evidenceRow)
	}

	searchRepository, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository() error = %v", err)
	}
	byPurpose, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{Query: "zqxsecuritypurpose", Limit: 20}})
	if err != nil {
		t.Fatalf("search by purpose: %v", err)
	}
	if len(byPurpose.Results) != 1 || byPurpose.Results[0].EntityType != domain.SearchEntityTypeWorkflowPolicy {
		t.Fatalf("search by purpose = %#v, want the policy", byPurpose.Results)
	}
	if byPurpose.Results[0].IssueID != nil {
		t.Fatalf("policy result issue id = %#v, want nil for a project-scoped entity", byPurpose.Results[0].IssueID)
	}
	bySummary, err := searchRepository.Search(ctx, ports.SearchCommand{Input: domain.SearchInput{
		Query: "zqxevidencesummary", Limit: 20, EntityTypes: []domain.SearchEntityType{domain.SearchEntityTypeGateEvidence},
	}})
	if err != nil {
		t.Fatalf("search by evidence summary: %v", err)
	}
	if len(bySummary.Results) != 1 || bySummary.Results[0].EntityID != evidenceID {
		t.Fatalf("search by evidence summary = %#v, want the evidence row", bySummary.Results)
	}

	// The evidence upsert path updates summary/details/result/version; the
	// index must follow.
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE gate_evidence SET summary = 'zqxreplacedsummary', version = 2, updated_at = ? WHERE id = ?`, timestamp, evidenceID)
		return err
	}); err != nil {
		t.Fatalf("update evidence: %v", err)
	}
	updatedRows := searchIndexRows(t, db)
	updatedEvidence := findSearchIndexRow(updatedRows, "gate_evidence", evidenceID)
	if updatedEvidence == nil || !strings.Contains(updatedEvidence.Content, "zqxreplacedsummary") {
		t.Fatalf("evidence row after update = %#v, want the replaced summary indexed", updatedEvidence)
	}

	// A wrong-shaped requirements blob (valid JSON, wrong type) must not
	// fail the write; the index degrades to empty text and GetPolicy is
	// what reports the corruption.
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE workflow_policies SET requirements_json = '"not-an-array"' WHERE id = ?`, policyID)
		return err
	}); err != nil {
		t.Fatalf("wrong-shaped requirements update failed, want tolerant indexing: %v", err)
	}
	degradedRows := searchIndexRows(t, db)
	degradedPolicy := findSearchIndexRow(degradedRows, "workflow_policy", policyID)
	if degradedPolicy == nil || degradedPolicy.Title != "" {
		t.Fatalf("policy row after shape corruption = %#v, want an empty title", degradedPolicy)
	}

	indexRepository, err := sqlite.NewSearchIndexRepository(db)
	if err != nil {
		t.Fatalf("NewSearchIndexRepository() error = %v", err)
	}
	if err := indexRepository.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	rebuilt := searchIndexRows(t, db)
	if !reflect.DeepEqual(rebuilt, degradedRows) {
		t.Fatalf("rebuilt search index rows = %#v, want %#v (trigger and Rebuild must agree)", rebuilt, degradedRows)
	}
}
