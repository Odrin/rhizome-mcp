package sqlite_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
)

func TestProjectRepositoryReturnsMetadataAndDeterministicMaximums(t *testing.T) {
	db, now := openProjectDatabase(t, "Project name", "Project instructions")
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES (4, 'later_migration', 'checksum', ?)`, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES (3, 'middle_migration', 'checksum', ?)`, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed latest event: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	service, err := application.NewProjectService(repository)
	if err != nil {
		t.Fatalf("NewProjectService() error = %v", err)
	}
	got, err := service.GetProject(ctx)
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}

	if got.ID != sqliteTestProjectID || got.Name == nil || *got.Name != "Project name" ||
		got.Instructions == nil || *got.Instructions != "Project instructions" {
		t.Fatalf("project identity/text = %#v", got)
	}
	if got.NextIssueNumber != 7 || !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("project values = %#v", got)
	}
	if got.SchemaVersion != 4 || got.LatestEventID != 2 {
		t.Fatalf("derived values = schema %d, event %d; want 4, 2", got.SchemaVersion, got.LatestEventID)
	}
}

func TestProjectRepositoryMapsNullableMetadataAndNoEventToZero(t *testing.T) {
	db, now := openProjectDatabase(t, "", "")
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	got, err := repository.GetProject(context.Background())
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if got.Name != nil || got.Instructions != nil {
		t.Fatalf("nullable values = name %#v, instructions %#v; want nil", got.Name, got.Instructions)
	}
	if got.LatestEventID != 0 {
		t.Fatalf("latest event ID = %d, want 0", got.LatestEventID)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %v, %v; want %v", got.CreatedAt, got.UpdatedAt, now)
	}
}

func TestProjectRepositoryExportsLogicalProjectSnapshotDeterministically(t *testing.T) {
	db, now := openProjectDatabase(t, "name", "instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueID, err := generator.New()
	if err != nil {
		t.Fatalf("issue ID generation: %v", err)
	}
	archivedIssueID, err := generator.New()
	if err != nil {
		t.Fatalf("archived issue ID generation: %v", err)
	}
	relatedIssueID, err := generator.New()
	if err != nil {
		t.Fatalf("related issue ID generation: %v", err)
	}
	labelID, err := generator.New()
	if err != nil {
		t.Fatalf("label ID generation: %v", err)
	}
	attemptID, err := generator.New()
	if err != nil {
		t.Fatalf("attempt ID generation: %v", err)
	}
	attemptNoteID, err := generator.New()
	if err != nil {
		t.Fatalf("attempt note ID generation: %v", err)
	}
	artifactID, err := generator.New()
	if err != nil {
		t.Fatalf("artifact ID generation: %v", err)
	}
	commentID, err := generator.New()
	if err != nil {
		t.Fatalf("comment ID generation: %v", err)
	}
	decisionID, err := generator.New()
	if err != nil {
		t.Fatalf("decision ID generation: %v", err)
	}
	relationID, err := generator.New()
	if err != nil {
		t.Fatalf("relation ID generation: %v", err)
	}
	if err = db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for _, row := range []struct {
			query string
			args  []any
		}{
			{query: `INSERT INTO issues(id, sequence_no, type, title, description, status, priority, version, created_at, updated_at, archived_at) VALUES (?, 1, 'task', 'Visible issue', 'desc', 'ready', 'high', 1, ?, ?, NULL)`, args: []any{issueID, now.Add(1 * time.Second).Format(time.RFC3339Nano), now.Add(2 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at, archived_at) VALUES (?, 2, 'task', 'Archived issue', 'ready', 'high', 1, ?, ?, ?)`, args: []any{archivedIssueID, now.Add(3 * time.Second).Format(time.RFC3339Nano), now.Add(4 * time.Second).Format(time.RFC3339Nano), now.Add(5 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 3, 'task', 'Target issue', 'ready', 'high', 1, ?, ?)`, args: []any{relatedIssueID, now.Add(6 * time.Second).Format(time.RFC3339Nano), now.Add(7 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO labels(id, name, description, created_at) VALUES (?, 'visible', 'label', ?)`, args: []any{labelID, now.Add(8 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issue_labels(issue_id, label_id) VALUES (?, ?)`, args: []any{issueID, labelID}},
			{query: `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'blocks', ?)`, args: []any{relationID, issueID, relatedIssueID, now.Add(9 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, 'visible comment', ?)`, args: []any{commentID, issueID, now.Add(10 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, 'archived comment', ?)`, args: []any{"01ARZ3NDEKTSV4RRFFQ69G5FAK", archivedIssueID, now.Add(11 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'Decision', 'Reason', 'Detail', 'active', ?)`, args: []any{decisionID, issueID, now.Add(12 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'Archived decision', 'Reason', 'Detail', 'active', ?)`, args: []any{"01ARZ3NDEKTSV4RRFFQ69G5FAL", archivedIssueID, now.Add(13 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, result_summary, next_steps_json, verification_json) VALUES (?, ?, 'work', 'active', 1, 0, X'00', ?, ?, ?, ?, ?, ?)`, args: []any{attemptID, issueID, now.Add(14 * time.Second).Format(time.RFC3339Nano), now.Add(15 * time.Second).Format(time.RFC3339Nano), now.Add(16 * time.Second).Format(time.RFC3339Nano), "done", `[]`, `[]`}},
			{query: `INSERT INTO attempt_notes(id, attempt_id, kind, content, next_steps_json, important, created_at) VALUES (?, ?, 'checkpoint', 'note', ?, 1, ?)`, args: []any{attemptNoteID, attemptID, `[]`, now.Add(17 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at) VALUES (?, ?, ?, 'file', 'docs/example.md', 'artifact', '{"kind":"note"}', ?)`, args: []any{artifactID, issueID, attemptID, now.Add(18 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'issue_created', '{"kind":"created"}', ?)`, args: []any{issueID, now.Add(19 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'issue_created', '{"kind":"archived"}', ?)`, args: []any{archivedIssueID, now.Add(20 * time.Second).Format(time.RFC3339Nano)}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (NULL, 'project_event', '{"kind":"project"}', ?)`, args: []any{now.Add(21 * time.Second).Format(time.RFC3339Nano)}},
		} {
			if _, err := tx.ExecContext(ctx, row.query, row.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed export rows: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	first, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	second, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	firstBytes, err := domain.MarshalLogicalProjectDocument(first)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	secondBytes, err := domain.MarshalLogicalProjectDocument(second)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("exports differ across repeated calls\nfirst=%s\nsecond=%s", firstBytes, secondBytes)
	}
	if len(first.Issues) != 2 || first.Issues[0].ID != issueID || first.Issues[1].ID != relatedIssueID {
		t.Fatalf("issues = %#v", first.Issues)
	}
	if len(first.Comments) != 1 || first.Comments[0].ID != commentID {
		t.Fatalf("comments = %#v", first.Comments)
	}
	if len(first.Decisions) != 1 || first.Decisions[0].ID != decisionID {
		t.Fatalf("decisions = %#v", first.Decisions)
	}
	if len(first.Attempts) != 0 {
		t.Fatalf("attempts = %#v", first.Attempts)
	}
	if len(first.AttemptNotes) != 0 {
		t.Fatalf("attempt notes = %#v", first.AttemptNotes)
	}
	if len(first.Artifacts) != 0 {
		t.Fatalf("artifacts = %#v", first.Artifacts)
	}
	if len(first.Events) != 2 || first.Events[0].IssueID == nil || first.Events[1].IssueID != nil {
		t.Fatalf("events = %#v", first.Events)
	}
	if first.Comments[0].CreatedBySessionID != nil || first.Decisions[0].CreatedBySessionID != nil || first.Events[0].SessionID != nil {
		t.Fatalf("session references were leaked: %#v", first)
	}
	if len(first.IssueLabels) != 1 || first.IssueLabels[0].IssueID != issueID {
		t.Fatalf("issue labels = %#v", first.IssueLabels)
	}
	if len(first.Relations) != 1 || first.Relations[0].ID != relationID {
		t.Fatalf("relations = %#v", first.Relations)
	}
	if first.Project.ID != sqliteTestProjectID || first.Format != "rhizome-logical-project" || first.Version != 1 {
		t.Fatalf("document metadata = %#v", first)
	}
}

func TestProjectRepositoryAppliesLogicalImportWithRemappedReferences(t *testing.T) {
	db, _ := openProjectDatabase(t, "Imported", "Instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC)), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	parentIssueID, err := generator.New()
	if err != nil {
		t.Fatalf("parent issue ID generation: %v", err)
	}
	childIssueID, err := generator.New()
	if err != nil {
		t.Fatalf("child issue ID generation: %v", err)
	}
	labelID, err := generator.New()
	if err != nil {
		t.Fatalf("label ID generation: %v", err)
	}
	relationID, err := generator.New()
	if err != nil {
		t.Fatalf("relation ID generation: %v", err)
	}
	commentID, err := generator.New()
	if err != nil {
		t.Fatalf("comment ID generation: %v", err)
	}
	decisionID, err := generator.New()
	if err != nil {
		t.Fatalf("decision ID generation: %v", err)
	}
	attemptID, err := generator.New()
	if err != nil {
		t.Fatalf("attempt ID generation: %v", err)
	}
	attemptNoteID, err := generator.New()
	if err != nil {
		t.Fatalf("attempt note ID generation: %v", err)
	}
	artifactID, err := generator.New()
	if err != nil {
		t.Fatalf("artifact ID generation: %v", err)
	}
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    1,
		ExportedAt: "2026-07-17T18:24:06Z",
		Project: domain.LogicalProjectProject{
			ID:           sqliteTestProjectID,
			Name:         stringValuePointer("Imported project"),
			Instructions: stringValuePointer("Imported instructions"),
			CreatedAt:    "2026-07-17T18:24:06Z",
			UpdatedAt:    "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{{
			ID:        parentIssueID,
			Type:      "epic",
			Title:     "Epic",
			Status:    "open",
			Priority:  "high",
			CreatedAt: "2026-07-17T18:24:06Z",
			UpdatedAt: "2026-07-17T18:24:06Z",
		}, {
			ID:                 childIssueID,
			Type:               "task",
			Title:              "Task",
			Status:             "ready",
			Priority:           "medium",
			ParentID:           stringValuePointer(parentIssueID),
			CreatedBySessionID: nil,
			CreatedAt:          "2026-07-17T18:24:07Z",
			UpdatedAt:          "2026-07-17T18:24:07Z",
		}},
		Labels:       []domain.LogicalLabel{{ID: labelID, Name: "alpha", CreatedAt: "2026-07-17T18:24:06Z"}},
		IssueLabels:  []domain.LogicalIssueLabel{{IssueID: childIssueID, LabelID: labelID}},
		Relations:    []domain.LogicalRelation{{ID: relationID, SourceIssueID: parentIssueID, TargetIssueID: childIssueID, Type: "related_to", CreatedAt: "2026-07-17T18:24:08Z"}},
		Comments:     []domain.LogicalComment{{ID: commentID, IssueID: childIssueID, Content: "hello", CreatedAt: "2026-07-17T18:24:08Z"}},
		Decisions:    []domain.LogicalDecision{{ID: decisionID, IssueID: stringValuePointer(childIssueID), Title: "Decision", Summary: "Why", Content: "Detail", Status: "active", CreatedAt: "2026-07-17T18:24:09Z"}},
		Attempts:     []domain.LogicalAttempt{{ID: attemptID, IssueID: childIssueID, Kind: "work", Status: "completed", IssueVersionAtStart: 1, ContextEventIDAtStart: 0, LeaseExpiresAt: "2026-07-17T18:24:10Z", StartedAt: "2026-07-17T18:24:10Z", LastHeartbeatAt: "2026-07-17T18:24:10Z", FinishedAt: stringValuePointer("2026-07-17T18:24:11Z"), ResultSummary: stringValuePointer("done"), NextSteps: []string{"next"}, Verification: []string{"ok"}}},
		AttemptNotes: []domain.LogicalAttemptNote{{ID: attemptNoteID, AttemptID: attemptID, Kind: "checkpoint", Content: "note", NextSteps: []string{"next"}, Important: true, CreatedAt: "2026-07-17T18:24:12Z"}},
		Artifacts:    []domain.LogicalArtifact{{ID: artifactID, IssueID: childIssueID, AttemptID: stringValuePointer(attemptID), Type: "file", URI: "docs/example.md", Title: stringValuePointer("artifact"), Metadata: []byte(`{"type":"note"}`), CreatedAt: "2026-07-17T18:24:13Z"}},
		Events:       []domain.LogicalEvent{{SourceID: 1, IssueID: stringValuePointer(childIssueID), EventType: "issue_created", Payload: []byte(`{"kind":"created"}`), CreatedAt: "2026-07-17T18:24:14Z"}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	result, err := repository.ApplyLogicalProjectImport(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}
	if result.Counts.Issues != 2 || result.Counts.Labels != 1 || result.Counts.Attempts != 1 || len(result.Conflicts) != 0 || result.LatestEventID <= 0 {
		t.Fatalf("apply result = %#v", result)
	}

	var issueCount int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`).Scan(&issueCount)
	}); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 2 {
		t.Fatalf("issue count = %d, want 2", issueCount)
	}
	var nextIssueNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT next_issue_number FROM projects WHERE id = ?`, sqliteTestProjectID).Scan(&nextIssueNumber)
	}); err != nil {
		t.Fatalf("read next issue number: %v", err)
	}
	if nextIssueNumber != 9 {
		t.Fatalf("next_issue_number = %d, want 9", nextIssueNumber)
	}

	var parentID string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT parent_id FROM issues WHERE title = ? ORDER BY sequence_no LIMIT 1`, "Task").Scan(&parentID)
	}); err != nil {
		t.Fatalf("read parent id: %v", err)
	}
	if parentID == "" || parentID == parentIssueID {
		t.Fatalf("parent_id was not remapped: %q", parentID)
	}
	if parentID == "" {
		t.Fatalf("parent_id = empty, want remapped value")
	}

	var labelCount int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM labels`).Scan(&labelCount)
	}); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if labelCount != 1 {
		t.Fatalf("label count = %d, want 1", labelCount)
	}

	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	exportedBytes, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	if _, err := domain.ParseLogicalProjectImportPlan(exportedBytes); err != nil {
		t.Fatalf("exported document failed validation: %v", err)
	}
}

func TestProjectRepositoryRollsBackFailedImportAndPreservesSequence(t *testing.T) {
	db, _ := openProjectDatabase(t, "Failed", "Instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC)), rand.New(rand.NewSource(2)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueID, err := generator.New()
	if err != nil {
		t.Fatalf("issue ID generation: %v", err)
	}
	attemptID, err := generator.New()
	if err != nil {
		t.Fatalf("attempt ID generation: %v", err)
	}
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    1,
		ExportedAt: "2026-07-17T18:24:06Z",
		Project: domain.LogicalProjectProject{
			ID:           sqliteTestProjectID,
			Name:         stringValuePointer("Imported project"),
			Instructions: stringValuePointer("Imported instructions"),
			CreatedAt:    "2026-07-17T18:24:06Z",
			UpdatedAt:    "2026-07-17T18:24:06Z",
		},
		Issues:   []domain.LogicalIssue{{ID: issueID, Type: "task", Title: "Task", Status: "ready", Priority: "medium", CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"}},
		Attempts: []domain.LogicalAttempt{{ID: attemptID, IssueID: issueID, Kind: "work", Status: "failed", IssueVersionAtStart: 1, ContextEventIDAtStart: 0, LeaseExpiresAt: "2026-07-17T18:24:07Z", StartedAt: "2026-07-17T18:24:07Z", LastHeartbeatAt: "2026-07-17T18:24:07Z", FinishedAt: stringValuePointer("2026-07-17T18:24:08Z"), ResultSummary: stringValuePointer("failed")}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := repository.ApplyLogicalProjectImport(ctx, plan); err == nil {
		t.Fatal("ApplyLogicalProjectImport() succeeded for invalid attempt state")
	}

	var issueCount int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues`).Scan(&issueCount)
	}); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("issue count = %d, want 0", issueCount)
	}
	var nextIssueNumber int64
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT next_issue_number FROM projects WHERE id = ?`, sqliteTestProjectID).Scan(&nextIssueNumber)
	}); err != nil {
		t.Fatalf("read next issue number: %v", err)
	}
	if nextIssueNumber != 7 {
		t.Fatalf("next_issue_number = %d, want 7", nextIssueNumber)
	}
}

func TestProjectRepositoryReturnsConflictOnRetryAfterSuccessfulImport(t *testing.T) {
	db, _ := openProjectDatabase(t, "Retry", "Instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC)), rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueID, err := generator.New()
	if err != nil {
		t.Fatalf("issue ID generation: %v", err)
	}
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    1,
		ExportedAt: "2026-07-17T18:24:06Z",
		Project:    domain.LogicalProjectProject{ID: sqliteTestProjectID, Name: stringValuePointer("Imported project"), Instructions: stringValuePointer("Imported instructions"), CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"},
		Issues:     []domain.LogicalIssue{{ID: issueID, Type: "task", Title: "Task", Status: "ready", Priority: "medium", CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := repository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}
	result, err := repository.ApplyLogicalProjectImport(ctx, plan)
	if err != nil {
		t.Fatalf("second apply failed: %v", err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Code != "empty_destination_required" {
		t.Fatalf("retry result = %#v", result)
	}
}

func TestProjectRepositoryMapsTimestampCorruptionToStableError(t *testing.T) {
	db, _ := openProjectDatabase(t, "name", "instructions")
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, "UPDATE projects SET created_at = 'not-a-timestamp'")
		return err
	}); err != nil {
		t.Fatalf("corrupt timestamp: %v", err)
	}
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	_, err = repository.GetProject(context.Background())
	assertProjectDomainCode(t, err, domain.CodeStorageCorrupt)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || len(domainErr.Details) != 1 ||
		domainErr.Details[0].Field != "created_at" ||
		domainErr.Details[0].Code != "INVALID_TIMESTAMP" {
		t.Fatalf("corruption details = %#v", err)
	}
}

func TestProjectRepositoryRejectsMissingOrDuplicateProjectRows(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		db, _ := openProjectDatabase(t, "", "")
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM projects")
			return err
		}); err != nil {
			t.Fatalf("delete project: %v", err)
		}
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		_, err = repository.GetProject(context.Background())
		assertProjectDomainCode(t, err, domain.CodeProjectNotInitialized)
	})

	t.Run("duplicate", func(t *testing.T) {
		db, now := openProjectDatabase(t, "", "")
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO projects(id, next_issue_number, created_at, updated_at)
				VALUES (?, 1, ?, ?)`,
				"01ARZ3NDEKTSV4RRFFQ69G5FAS", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			return err
		}); err != nil {
			t.Fatalf("insert duplicate project: %v", err)
		}
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		_, err = repository.GetProject(context.Background())
		assertProjectDomainCode(t, err, domain.CodeStorageCorrupt)
	})
}

func TestProjectRepositoryReportsDestinationContent(t *testing.T) {
	t.Run("empty destination", func(t *testing.T) {
		db, _ := openProjectDatabase(t, "name", "instructions")
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		hasContent, err := repository.HasLogicalProjectImportDestinationContent(context.Background())
		if err != nil {
			t.Fatalf("HasLogicalProjectImportDestinationContent() error = %v", err)
		}
		if hasContent {
			t.Fatal("expected empty destination")
		}
	})

	t.Run("nonempty destination", func(t *testing.T) {
		db, _ := openProjectDatabase(t, "name", "instructions")
		if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'issue', 'open', 'medium', 1, ?, ?)", "01ARZ3NDEKTSV4RRFFQ69G5FAJ", time.Now().Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano))
			return err
		}); err != nil {
			t.Fatalf("insert issue: %v", err)
		}
		repository, err := sqlite.NewProjectRepository(db)
		if err != nil {
			t.Fatalf("NewProjectRepository() error = %v", err)
		}
		hasContent, err := repository.HasLogicalProjectImportDestinationContent(context.Background())
		if err != nil {
			t.Fatalf("HasLogicalProjectImportDestinationContent() error = %v", err)
		}
		if !hasContent {
			t.Fatal("expected nonempty destination")
		}
	})
}

func TestProjectRepositoryHasNoWriteSideEffects(t *testing.T) {
	db, _ := openProjectDatabase(t, "name", "instructions")
	var before, after struct {
		projects, events, migrations int
	}
	queryCounts := func(counts *struct {
		projects, events, migrations int
	}) error {
		return db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
			return query.QueryRowContext(ctx, `
				SELECT
					(SELECT count(*) FROM projects),
					(SELECT count(*) FROM issue_events),
					(SELECT count(*) FROM schema_migrations)`,
			).Scan(&counts.projects, &counts.events, &counts.migrations)
		})
	}
	if err := queryCounts(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := repository.GetProject(context.Background()); err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if err := queryCounts(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Fatalf("counts changed from %#v to %#v", before, after)
	}
}

func openProjectDatabase(t *testing.T, name, instructions string) (*sqlite.DB, time.Time) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "project.db"), sqlite.Options{
		RetryPolicy: &sqlite.RetryPolicy{
			Delays:  []time.Duration{},
			Sleeper: sqlite.SleepFunc(func(context.Context, time.Duration) error { return nil }),
		},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	now := time.Date(2026, 7, 14, 10, 11, 12, 0, time.UTC)
	if _, err := migrations.Migrate(context.Background(), db, fixedMigrationClock{now: now}); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		var nameValue, instructionsValue any
		if name != "" {
			nameValue = name
		}
		if instructions != "" {
			instructionsValue = instructions
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO projects(id, name, instructions, next_issue_number, created_at, updated_at)
			VALUES (?, ?, ?, 7, ?, ?)`,
			sqliteTestProjectID, nameValue, instructionsValue,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return db, now
}

type fixedMigrationClock struct {
	now time.Time
}

func (clock fixedMigrationClock) Now() time.Time {
	return clock.now
}

func assertProjectDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}

func stringValuePointer(value string) *string {
	return &value
}
