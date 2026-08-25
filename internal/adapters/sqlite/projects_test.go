package sqlite_test

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"math/rand"
	"path/filepath"
	"strings"
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
			VALUES (?, 'later_migration', 'checksum', ?)`, migrations.CurrentVersion()+1, sqlite.FormatStorageTime(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, payload, created_at)
			VALUES (NULL, 'project_event', '{}', ?)`, sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed latest event: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	service, err := application.NewProjectService(repository, generator)
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
	if got.SchemaVersion != migrations.CurrentVersion()+1 || got.LatestEventID != 2 {
		t.Fatalf("derived values = schema %d, event %d; want %d, 2", got.SchemaVersion, got.LatestEventID, migrations.CurrentVersion()+1)
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
			{query: `INSERT INTO issues(id, sequence_no, type, title, description, status, priority, version, created_at, updated_at, archived_at) VALUES (?, 1, 'task', 'Visible issue', 'desc', 'ready', 'high', 1, ?, ?, NULL)`, args: []any{issueID, sqlite.FormatStorageTime(now.Add(1 * time.Second)), sqlite.FormatStorageTime(now.Add(2 * time.Second))}},
			{query: `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at, archived_at) VALUES (?, 2, 'task', 'Archived issue', 'ready', 'high', 1, ?, ?, ?)`, args: []any{archivedIssueID, sqlite.FormatStorageTime(now.Add(3 * time.Second)), sqlite.FormatStorageTime(now.Add(4 * time.Second)), sqlite.FormatStorageTime(now.Add(5 * time.Second))}},
			{query: `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 3, 'task', 'Target issue', 'ready', 'high', 1, ?, ?)`, args: []any{relatedIssueID, sqlite.FormatStorageTime(now.Add(6 * time.Second)), sqlite.FormatStorageTime(now.Add(7 * time.Second))}},
			{query: `INSERT INTO labels(id, name, description, created_at) VALUES (?, 'visible', 'label', ?)`, args: []any{labelID, sqlite.FormatStorageTime(now.Add(8 * time.Second))}},
			{query: `INSERT INTO issue_labels(issue_id, label_id) VALUES (?, ?)`, args: []any{issueID, labelID}},
			{query: `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, 'blocks', ?)`, args: []any{relationID, issueID, relatedIssueID, sqlite.FormatStorageTime(now.Add(9 * time.Second))}},
			{query: `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, 'visible comment', ?)`, args: []any{commentID, issueID, sqlite.FormatStorageTime(now.Add(10 * time.Second))}},
			{query: `INSERT INTO comments(id, issue_id, content, created_at) VALUES (?, ?, 'archived comment', ?)`, args: []any{"01ARZ3NDEKTSV4RRFFQ69G5FAK", archivedIssueID, sqlite.FormatStorageTime(now.Add(11 * time.Second))}},
			{query: `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'Decision', 'Reason', 'Detail', 'active', ?)`, args: []any{decisionID, issueID, sqlite.FormatStorageTime(now.Add(12 * time.Second))}},
			{query: `INSERT INTO decisions(id, issue_id, title, summary, content, status, created_at) VALUES (?, ?, 'Archived decision', 'Reason', 'Detail', 'active', ?)`, args: []any{"01ARZ3NDEKTSV4RRFFQ69G5FAL", archivedIssueID, sqlite.FormatStorageTime(now.Add(13 * time.Second))}},
			{query: `INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, result_summary, next_steps_json, verification_json) VALUES (?, ?, 'work', 'active', 1, 0, X'00', ?, ?, ?, ?, ?, ?)`, args: []any{attemptID, issueID, sqlite.FormatStorageTime(now.Add(14 * time.Second)), sqlite.FormatStorageTime(now.Add(15 * time.Second)), sqlite.FormatStorageTime(now.Add(16 * time.Second)), "done", `[]`, `[]`}},
			{query: `INSERT INTO attempt_notes(id, attempt_id, kind, content, next_steps_json, important, created_at) VALUES (?, ?, 'checkpoint', 'note', ?, 1, ?)`, args: []any{attemptNoteID, attemptID, `[]`, sqlite.FormatStorageTime(now.Add(17 * time.Second))}},
			{query: `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at) VALUES (?, ?, ?, 'file', 'docs/example.md', 'artifact', '{"kind":"note"}', ?)`, args: []any{artifactID, issueID, attemptID, sqlite.FormatStorageTime(now.Add(18 * time.Second))}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'issue_created', '{"kind":"created"}', ?)`, args: []any{issueID, sqlite.FormatStorageTime(now.Add(19 * time.Second))}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (?, 'issue_created', '{"kind":"archived"}', ?)`, args: []any{archivedIssueID, sqlite.FormatStorageTime(now.Add(20 * time.Second))}},
			{query: `INSERT INTO issue_events(issue_id, event_type, payload, created_at) VALUES (NULL, 'project_event', '{"kind":"project"}', ?)`, args: []any{sqlite.FormatStorageTime(now.Add(21 * time.Second))}},
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
	if first.Project.ID != sqliteTestProjectID || first.Format != "rhizome-logical-project" || first.Version != 2 {
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
			ID:                 childIssueID,
			Type:               "task",
			Title:              "Task",
			Status:             "ready",
			Priority:           "medium",
			ParentID:           stringValuePointer(parentIssueID),
			CreatedBySessionID: nil,
			CreatedAt:          "2026-07-17T18:24:07Z",
			UpdatedAt:          "2026-07-17T18:24:07Z",
		}, {
			ID:        parentIssueID,
			Type:      "epic",
			Title:     "Epic",
			Status:    "open",
			Priority:  "high",
			CreatedAt: "2026-07-17T18:24:06Z",
			UpdatedAt: "2026-07-17T18:24:06Z",
		}},
		Labels:       []domain.LogicalLabel{{ID: labelID, Name: "alpha", CreatedAt: "2026-07-17T18:24:06Z"}},
		IssueLabels:  []domain.LogicalIssueLabel{{IssueID: childIssueID, LabelID: labelID}},
		Relations:    []domain.LogicalRelation{{ID: relationID, SourceIssueID: parentIssueID, TargetIssueID: childIssueID, Type: "blocks", CreatedAt: "2026-07-17T18:24:08Z"}},
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
	plan = assignImportDestinationIDs(t, plan)

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

func TestProjectRepositoryCanonicalizesRelatedToAfterImportRemapping(t *testing.T) {
	db, _ := openProjectDatabase(t, "Imported", "Instructions")
	ctx := context.Background()
	sourceIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FAA"
	targetIssueID := "01ARZ3NDEKTSV4RRFFQ69G5FAB"
	relationID := "01ARZ3NDEKTSV4RRFFQ69G5FAC"
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    1,
		ExportedAt: "2026-07-17T18:24:06Z",
		Project: domain.LogicalProjectProject{
			ID:        sqliteTestProjectID,
			CreatedAt: "2026-07-17T18:24:06Z",
			UpdatedAt: "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{
			{ID: targetIssueID, Type: "task", Title: "Target", Status: "ready", Priority: "medium", CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"},
			{ID: sourceIssueID, Type: "task", Title: "Source", Status: "ready", Priority: "medium", CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"},
		},
		Relations: []domain.LogicalRelation{{
			ID: relationID, SourceIssueID: sourceIssueID, TargetIssueID: targetIssueID,
			Type: "related_to", CreatedAt: "2026-07-17T18:24:06Z",
		}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	plan = assignImportDestinationIDs(t, plan)

	originalReader := cryptorand.Reader
	cryptorand.Reader = bytes.NewReader(bytes.Join([][]byte{
		bytes.Repeat([]byte{0x01}, 10),
		bytes.Repeat([]byte{0x02}, 10),
		bytes.Repeat([]byte{0x03}, 10),
	}, nil))
	t.Cleanup(func() { cryptorand.Reader = originalReader })
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := repository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	var storedSourceID, storedTargetID string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT source_issue_id, target_issue_id FROM issue_relations`).Scan(&storedSourceID, &storedTargetID)
	}); err != nil {
		t.Fatalf("read imported relation: %v", err)
	}
	if storedSourceID >= storedTargetID {
		t.Fatalf("related_to endpoints = %q, %q; want canonical lexical order", storedSourceID, storedTargetID)
	}

	constraintDB, _ := openProjectDatabase(t, "Imported", "Instructions")
	if err := constraintDB.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_imported_relations
			BEFORE INSERT ON issue_relations
			BEGIN
				SELECT RAISE(ABORT, 'forced relation constraint');
			END`)
		return err
	}); err != nil {
		t.Fatalf("create relation rejection trigger: %v", err)
	}
	cryptorand.Reader = bytes.NewReader(bytes.Repeat([]byte{0x04}, 64))
	constraintRepository, err := sqlite.NewProjectRepository(constraintDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	_, err = constraintRepository.ApplyLogicalProjectImport(ctx, plan)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageConstraint {
		t.Fatalf("ApplyLogicalProjectImport() error = %#v, want storage constraint", err)
	}
	var foundPath, foundDiagnostic bool
	for _, detail := range domainErr.Details {
		if detail.EntityIndex != nil && *detail.EntityIndex == 0 && detail.Field == "$.relations[0]" {
			foundPath = true
		}
		if detail.Code == "SQLITE_CONSTRAINT" && strings.Contains(detail.Message, "forced relation constraint") {
			foundDiagnostic = true
		}
	}
	if !foundPath || !foundDiagnostic {
		t.Fatalf("constraint details = %#v", domainErr.Details)
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
	plan = assignImportDestinationIDs(t, plan)

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
	plan = assignImportDestinationIDs(t, plan)

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
				"01ARZ3NDEKTSV4RRFFQ69G5FAS", sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
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
			_, err := tx.ExecContext(ctx, "INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'issue', 'open', 'medium', 1, ?, ?)", "01ARZ3NDEKTSV4RRFFQ69G5FAJ", sqlite.FormatStorageTime(time.Now()), sqlite.FormatStorageTime(time.Now()))
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

// TestProjectRepositoryExportIncludesReviewSourcedEventsAndImportPreservesThem
// is a regression test for ISSUE-190 AC5: before the event log was unified,
// review-lifecycle events lived in a separate review_events table that
// ExportLogicalProject never read, so they were silently absent from every
// logical export. Now that they are ordinary issue_events rows (source =
// 'review'), they must appear in export like any other event, and survive
// an import round trip.
//
// This test used to assert the opposite of the source check below, on the
// grounds that the format was "locked at version 1 with no new fields" so
// the tag could not be carried. ISSUE-215 repealed that premise: v2 carries
// an optional per-event `source`, and dropping it is not cosmetic -- review
// staleness reads that column (docs/09), so a restored project whose review
// events came back tagged 'issue' behaves differently from the original.
// The tag is part of the round trip now.
func TestProjectRepositoryExportIncludesReviewSourcedEventsAndImportPreservesThem(t *testing.T) {
	db, now := openProjectDatabase(t, "source project", "instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueID, err := generator.New()
	if err != nil {
		t.Fatalf("issue ID generation: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'reviewed issue', 'review', 'high', 1, ?, ?)`,
			issueID, sqlite.FormatStorageTime(now.Add(1*time.Second)), sqlite.FormatStorageTime(now.Add(2*time.Second))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at, source) VALUES (?, 'issue_created', '{"kind":"created"}', ?, 'issue')`,
			issueID, sqlite.FormatStorageTime(now.Add(3*time.Second))); err != nil {
			return err
		}
		// A real review_targets/review_requests pair backing the
		// review_requested event below, so the exported document's v2
		// review_events entry resolves referentially (request_id/target_id
		// must match an included review_requests/review_targets row).
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FB2', ?, 1, 0, '[]', 1, ?)`,
			issueID, sqlite.FormatStorageTime(now.Add(3*time.Second))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, version, created_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FB1', '01ARZ3NDEKTSV4RRFFQ69G5FB2', ?, 1, 0, '[]', 'open', 1, ?)`,
			issueID, sqlite.FormatStorageTime(now.Add(4*time.Second))); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at, source) VALUES (?, 'review_requested', '{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5FB1","target_id":"01ARZ3NDEKTSV4RRFFQ69G5FB2"}', ?, 'review')`,
			issueID, sqlite.FormatStorageTime(now.Add(4*time.Second)))
		return err
	}); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	sourceRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := sourceRepository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.Events) != 2 {
		t.Fatalf("exported events = %#v, want 2 (both the plain issue event and the review-sourced event)", exported.Events)
	}
	var sawReviewRequested bool
	for _, event := range exported.Events {
		if event.EventType == "review_requested" {
			sawReviewRequested = true
			if !strings.Contains(string(event.Payload), `"request_id":"01ARZ3NDEKTSV4RRFFQ69G5FB1"`) {
				t.Fatalf("review event payload = %s, want request_id preserved", event.Payload)
			}
		}
	}
	if !sawReviewRequested {
		t.Fatalf("exported events = %#v, want review_requested present (it must not be invisible to export)", exported.Events)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	plan = assignImportDestinationIDs(t, plan)

	destinationDB, _ := openProjectDatabase(t, "destination project", "instructions")
	destinationRepository, err := sqlite.NewProjectRepository(destinationDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destinationRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	var importedCount int
	var importedSource string
	if err := destinationDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'review_requested'`).Scan(&importedCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT source FROM issue_events WHERE event_type = 'review_requested'`).Scan(&importedSource)
	}); err != nil {
		t.Fatalf("read imported events: %v", err)
	}
	if importedCount != 1 {
		t.Fatalf("imported review_requested event count = %d, want 1", importedCount)
	}
	if importedSource != "review" {
		t.Fatalf("imported event source = %q, want 'review' preserved through the round trip", importedSource)
	}

	// The other side of the tag: plain issue events must not be promoted to
	// review events either.
	var importedIssueSourced int
	if err := destinationDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE event_type = 'issue_created' AND source = 'issue'`).Scan(&importedIssueSourced)
	}); err != nil {
		t.Fatalf("read imported events: %v", err)
	}
	if importedIssueSourced != 1 {
		t.Fatalf("imported issue-sourced event count = %d, want 1", importedIssueSourced)
	}
}

// TestProjectRepositoryAppliesVersion2ReviewEntitiesWithRemappedReferences is
// ISSUE-215 AC2's round-trip coverage: a hand-built version 2 document
// carrying one issue and its full review lifecycle (target, request,
// outcome) imports into an empty destination with every ID remapped (never
// reusing the source document's IDs verbatim) and every cross-entity
// reference (review_requests.target_id/issue_id,
// review_outcomes.request_id/attempt_id) translated consistently.
func TestProjectRepositoryAppliesVersion2ReviewEntitiesWithRemappedReferences(t *testing.T) {
	db, _ := openProjectDatabase(t, "Imported with reviews", "Instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 17, 18, 24, 6, 0, time.UTC)), rand.New(rand.NewSource(1)))
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
	targetID, err := generator.New()
	if err != nil {
		t.Fatalf("target ID generation: %v", err)
	}
	requestID, err := generator.New()
	if err != nil {
		t.Fatalf("request ID generation: %v", err)
	}
	outcomeID, err := generator.New()
	if err != nil {
		t.Fatalf("outcome ID generation: %v", err)
	}
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    2,
		ExportedAt: "2026-07-17T18:24:20Z",
		Project: domain.LogicalProjectProject{
			ID: sqliteTestProjectID, CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{{
			ID: issueID, Type: "task", Title: "Reviewed task", Status: "done", Priority: "medium",
			CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		}},
		Attempts: []domain.LogicalAttempt{{
			ID: attemptID, IssueID: issueID, Kind: "review", Status: "completed",
			IssueVersionAtStart: 2, ContextEventIDAtStart: 0,
			LeaseExpiresAt: "2026-07-17T18:24:10Z", StartedAt: "2026-07-17T18:24:10Z", LastHeartbeatAt: "2026-07-17T18:24:10Z",
			FinishedAt: stringValuePointer("2026-07-17T18:24:11Z"), ResultSummary: stringValuePointer("approved"),
			NextSteps: []string{}, Verification: []string{},
		}},
		ReviewTargets: []domain.LogicalReviewTarget{{
			ID: targetID, IssueID: issueID, IssueVersion: 2, LatestEventID: 0,
			ArtifactIDs: []string{}, CreatedAt: "2026-07-17T18:24:07Z",
		}},
		ReviewRequests: []domain.LogicalReviewRequest{{
			ID: requestID, TargetID: targetID, IssueID: issueID, TargetIssueVersion: 2, TargetEventID: 0,
			ArtifactIDs: []string{}, Status: "approved",
			CreatedAt: "2026-07-17T18:24:08Z", ResolvedAt: stringValuePointer("2026-07-17T18:24:11Z"),
		}},
		ReviewOutcomes: []domain.LogicalReviewOutcome{{
			ID: outcomeID, RequestID: requestID, AttemptID: attemptID, Outcome: "approved",
			CreatedAt: "2026-07-17T18:24:11Z",
		}},
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	if plan.DryRun.Counts.ReviewTargets != 1 || plan.DryRun.Counts.ReviewRequests != 1 || plan.DryRun.Counts.ReviewOutcomes != 1 {
		t.Fatalf("dry run counts = %#v", plan.DryRun.Counts)
	}
	plan = assignImportDestinationIDs(t, plan)

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	result, err := repository.ApplyLogicalProjectImport(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}
	if result.Counts.ReviewTargets != 1 || result.Counts.ReviewRequests != 1 || result.Counts.ReviewOutcomes != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("apply result = %#v", result)
	}

	var destTargetID, destRequestID, destOutcomeID, destIssueID, destAttemptID string
	var requestTargetID, requestIssueID, requestStatus string
	var outcomeRequestID, outcomeAttemptID, outcomeOutcome string
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT id, issue_id FROM review_targets`).Scan(&destTargetID, &destIssueID); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT id, target_id, issue_id, status FROM review_requests`).Scan(&destRequestID, &requestTargetID, &requestIssueID, &requestStatus); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT id, request_id, attempt_id, outcome FROM review_outcomes`).Scan(&destOutcomeID, &outcomeRequestID, &outcomeAttemptID, &outcomeOutcome); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT id FROM work_attempts WHERE kind = 'review'`).Scan(&destAttemptID)
	}); err != nil {
		t.Fatalf("read imported review rows: %v", err)
	}

	if destTargetID == "" || destTargetID == targetID {
		t.Fatalf("review_targets.id = %q, want a remapped, non-empty ID", destTargetID)
	}
	if destRequestID == "" || destRequestID == requestID {
		t.Fatalf("review_requests.id = %q, want a remapped, non-empty ID", destRequestID)
	}
	if destOutcomeID == "" || destOutcomeID == outcomeID {
		t.Fatalf("review_outcomes.id = %q, want a remapped, non-empty ID", destOutcomeID)
	}
	if destIssueID == "" || destIssueID == issueID {
		t.Fatalf("review_targets.issue_id = %q, want a remapped, non-empty ID", destIssueID)
	}
	if requestTargetID != destTargetID {
		t.Fatalf("review_requests.target_id = %q, want it to match the remapped review_targets.id %q", requestTargetID, destTargetID)
	}
	if requestIssueID != destIssueID {
		t.Fatalf("review_requests.issue_id = %q, want it to match the remapped issues.id %q", requestIssueID, destIssueID)
	}
	if requestStatus != "approved" {
		t.Fatalf("review_requests.status = %q, want approved", requestStatus)
	}
	if outcomeRequestID != destRequestID {
		t.Fatalf("review_outcomes.request_id = %q, want it to match the remapped review_requests.id %q", outcomeRequestID, destRequestID)
	}
	if outcomeAttemptID != destAttemptID {
		t.Fatalf("review_outcomes.attempt_id = %q, want it to match the remapped work_attempts.id %q", outcomeAttemptID, destAttemptID)
	}
	if outcomeOutcome != "approved" {
		t.Fatalf("review_outcomes.outcome = %q, want approved", outcomeOutcome)
	}

	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("re-export ExportLogicalProject() error = %v", err)
	}
	if exported.Version != 2 || len(exported.ReviewTargets) != 1 || len(exported.ReviewRequests) != 1 || len(exported.ReviewOutcomes) != 1 {
		t.Fatalf("re-exported document = %#v", exported)
	}
}

// TestProjectRepositoryRoundTripsReleasedReservationThroughExtensionsNamespace
// covers ISSUE-182 AC1: a released reservation owned by a finished attempt
// crosses the interchange boundary via Extensions["reservations"], and an
// import of that document lands the row with remapped issue_id/attempt_id
// (destination IDs) while every other column survives unchanged.
func TestProjectRepositoryRoundTripsReleasedReservationThroughExtensionsNamespace(t *testing.T) {
	db, now := openProjectDatabase(t, "Reservations", "Instructions")
	ctx := context.Background()

	const (
		issueID       = "01ARZ3NDEKTSV4RRFFQ69G5FEA"
		attemptID     = "01ARZ3NDEKTSV4RRFFQ69G5FEB"
		reservationID = "01ARZ3NDEKTSV4RRFFQ69G5FEC"
	)
	createdAt := now.Add(1 * time.Second)
	finishedAt := now.Add(2 * time.Second)
	releasedAt := now.Add(3 * time.Second)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Reservation issue', 'done', 'medium', 1, ?, ?)`,
			issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json
		) VALUES (?, ?, 'work', 'completed', 1, 0, X'03', ?, ?, ?, ?, 'done', '[]', '[]')`,
			attemptID, issueID,
			sqlite.FormatStorageTime(finishedAt), sqlite.FormatStorageTime(createdAt),
			sqlite.FormatStorageTime(finishedAt), sqlite.FormatStorageTime(finishedAt)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at, released_at, release_reason
		) VALUES (?, ?, ?, 'file', 'src/a.go', 'src/a.go', '{"path":"src/a.go"}', 'released', 1, ?, ?, 'completed')`,
			reservationID, issueID, attemptID,
			sqlite.FormatStorageTime(createdAt), sqlite.FormatStorageTime(releasedAt))
		return err
	}); err != nil {
		t.Fatalf("seed reservation fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	reservations, err := exported.DecodeReservationsExtension()
	if err != nil {
		t.Fatalf("DecodeReservationsExtension() error = %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %#v, want exactly one", reservations)
	}
	got := reservations[0]
	if got.ID != reservationID || got.IssueID != issueID || got.AttemptID != attemptID ||
		got.Kind != "file" || got.DisplayValue != "src/a.go" || got.ComparisonValue != "src/a.go" ||
		string(got.NormalizedJSON) != `{"path":"src/a.go"}` || got.Status != "released" ||
		got.ReleaseReason != "completed" {
		t.Fatalf("exported reservation = %#v", got)
	}
	rawExtension, ok := exported.Extensions[domain.LogicalReservationsExtensionKey]
	if !ok {
		t.Fatalf("extensions = %#v, want a %q key", exported.Extensions, domain.LogicalReservationsExtensionKey)
	}
	var extension domain.LogicalReservationsExtension
	if err := json.Unmarshal(rawExtension, &extension); err != nil {
		t.Fatalf("unmarshal raw extension payload: %v", err)
	}
	if extension.Version != domain.LogicalReservationsExtensionVersion {
		t.Fatalf("extension version = %d, want %d", extension.Version, domain.LogicalReservationsExtensionVersion)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	if plan.DryRun.Counts.Reservations != 1 {
		t.Fatalf("dry run reservation count = %d, want 1", plan.DryRun.Counts.Reservations)
	}
	plan = assignImportDestinationIDs(t, plan)

	destDB, _ := openProjectDatabase(t, "Reservations destination", "Instructions")
	destRepository, err := sqlite.NewProjectRepository(destDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	result, err := destRepository.ApplyLogicalProjectImport(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}
	if result.Counts.Reservations != 1 {
		t.Fatalf("apply result reservation count = %d, want 1", result.Counts.Reservations)
	}

	destIssueID := plan.DestinationIDs.IssueIDs[issueID]
	destAttemptID := plan.DestinationIDs.AttemptIDs[attemptID]
	if destIssueID == "" || destIssueID == issueID || destAttemptID == "" || destAttemptID == attemptID {
		t.Fatalf("destination ids not remapped: issue=%q attempt=%q", destIssueID, destAttemptID)
	}

	var rowIssueID, rowAttemptID, rowKind, rowDisplayValue, rowComparisonValue, rowNormalizedJSON, rowStatus, rowReleaseReason string
	if err := destDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, release_reason FROM resource_reservations`).
			Scan(&rowIssueID, &rowAttemptID, &rowKind, &rowDisplayValue, &rowComparisonValue, &rowNormalizedJSON, &rowStatus, &rowReleaseReason)
	}); err != nil {
		t.Fatalf("read imported reservation row: %v", err)
	}
	if rowIssueID != destIssueID {
		t.Fatalf("imported reservation issue_id = %q, want remapped %q", rowIssueID, destIssueID)
	}
	if rowAttemptID != destAttemptID {
		t.Fatalf("imported reservation attempt_id = %q, want remapped %q", rowAttemptID, destAttemptID)
	}
	if rowKind != "file" || rowDisplayValue != "src/a.go" || rowComparisonValue != "src/a.go" ||
		rowStatus != "released" || rowReleaseReason != "completed" {
		t.Fatalf("imported reservation columns = kind=%q display=%q comparison=%q status=%q reason=%q",
			rowKind, rowDisplayValue, rowComparisonValue, rowStatus, rowReleaseReason)
	}
	// normalized_json is re-indented by MarshalLogicalProjectDocument's
	// json.MarshalIndent on the way through the document, so it is no
	// longer byte-identical to the compact form seeded above; assert JSON
	// content survived rather than exact bytes.
	if !strings.Contains(rowNormalizedJSON, "src/a.go") {
		t.Fatalf("imported reservation normalized_json = %q, want to contain %q", rowNormalizedJSON, "src/a.go")
	}
}

// TestProjectRepositoryExportOmitsReservationsExtensionWhenNothingCrossesTheBoundary
// covers ISSUE-182 AC2: an active reservation, and a released reservation
// whose owning attempt is still active, must never cross the interchange
// boundary -- and when nothing is exportable the extensions map must not
// carry an empty "reservations" key, preserving the existing deterministic
// snapshot shape for projects that never touched a reservation.
func TestProjectRepositoryExportOmitsReservationsExtensionWhenNothingCrossesTheBoundary(t *testing.T) {
	db, now := openProjectDatabase(t, "Reservations exclusion", "Instructions")
	ctx := context.Background()

	const (
		issueID                  = "01ARZ3NDEKTSV4RRFFQ69G5FFA"
		attemptID                = "01ARZ3NDEKTSV4RRFFQ69G5FFB"
		activeReservationID      = "01ARZ3NDEKTSV4RRFFQ69G5FFC"
		releasedButActiveOwnerID = "01ARZ3NDEKTSV4RRFFQ69G5FFD"
	)
	timestamp := sqlite.FormatStorageTime(now)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Excluded reservations issue', 'ready', 'medium', 1, ?, ?)`,
			issueID, timestamp, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
			lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
		) VALUES (?, ?, 'work', 'active', 1, 0, X'04', ?, ?, ?)`,
			attemptID, issueID, timestamp, timestamp, timestamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at
		) VALUES (?, ?, ?, 'file', 'src/active.go', 'src/active.go', '{}', 'active', 1, ?)`,
			activeReservationID, issueID, attemptID, timestamp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
			id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at, released_at, release_reason
		) VALUES (?, ?, ?, 'file', 'src/released.go', 'src/released.go', '{}', 'released', 1, ?, ?, 'completed')`,
			releasedButActiveOwnerID, issueID, attemptID, timestamp, timestamp)
		return err
	}); err != nil {
		t.Fatalf("seed exclusion fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if _, ok := exported.Extensions[domain.LogicalReservationsExtensionKey]; ok {
		t.Fatalf("extensions = %#v, want no %q key when nothing is exportable", exported.Extensions, domain.LogicalReservationsExtensionKey)
	}
	if len(exported.Extensions) != 0 {
		t.Fatalf("extensions = %#v, want empty", exported.Extensions)
	}
	reservations, err := exported.DecodeReservationsExtension()
	if err != nil {
		t.Fatalf("DecodeReservationsExtension() error = %v", err)
	}
	if reservations != nil {
		t.Fatalf("reservations = %#v, want nil", reservations)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	if _, err := domain.ParseLogicalProjectImportPlan(data); err != nil {
		t.Fatalf("re-parsing exported document failed: %v", err)
	}
}

// TestProjectRepositoryImportRejectsInvalidReservationRecords covers
// ISSUE-182 AC3: every reservation-shaped defect that
// validateLogicalReservations (and DecodeReservationsExtension) rejects,
// each isolated to a single hand-built document defect at a time.
func TestProjectRepositoryImportRejectsInvalidReservationRecords(t *testing.T) {
	const (
		issueID       = "01ARZ3NDEKTSV4RRFFQ69G5FGA"
		otherIssueID  = "01ARZ3NDEKTSV4RRFFQ69G5FGB"
		attemptID     = "01ARZ3NDEKTSV4RRFFQ69G5FGC"
		reservationID = "01ARZ3NDEKTSV4RRFFQ69G5FGD"
		unknownID     = "01ARZ3NDEKTSV4RRFFQ69G5FGE"
	)

	validReservation := func() domain.LogicalReservation {
		return domain.LogicalReservation{
			ID: reservationID, IssueID: issueID, AttemptID: attemptID,
			Kind: "file", DisplayValue: "src/a.go", ComparisonValue: "src/a.go",
			NormalizedJSON: json.RawMessage(`{"path":"src/a.go"}`),
			Status:         "released",
			CreatedAt:      "2026-07-17T18:24:10Z",
			ReleasedAt:     "2026-07-17T18:24:11Z",
			ReleaseReason:  "completed",
		}
	}
	baseDocument := func() domain.LogicalProjectDocument {
		return domain.LogicalProjectDocument{
			Format:     "rhizome-logical-project",
			Version:    2,
			ExportedAt: "2026-07-17T18:24:20Z",
			Project: domain.LogicalProjectProject{
				ID: sqliteTestProjectID, CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
			},
			Issues: []domain.LogicalIssue{
				{ID: issueID, Type: "task", Title: "Reservation owner", Status: "done", Priority: "medium",
					CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"},
				{ID: otherIssueID, Type: "task", Title: "Unrelated issue", Status: "done", Priority: "medium",
					CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z"},
			},
			Attempts: []domain.LogicalAttempt{{
				ID: attemptID, IssueID: issueID, Kind: "work", Status: "completed",
				IssueVersionAtStart: 1, ContextEventIDAtStart: 0,
				LeaseExpiresAt: "2026-07-17T18:24:09Z", StartedAt: "2026-07-17T18:24:09Z", LastHeartbeatAt: "2026-07-17T18:24:09Z",
				FinishedAt: stringValuePointer("2026-07-17T18:24:10Z"), ResultSummary: stringValuePointer("done"),
				NextSteps: []string{}, Verification: []string{},
			}},
		}
	}
	withExtension := func(payload json.RawMessage) []byte {
		document := baseDocument()
		document.Extensions = map[string]json.RawMessage{domain.LogicalReservationsExtensionKey: payload}
		data, err := domain.MarshalLogicalProjectDocument(document)
		if err != nil {
			t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
		}
		return data
	}
	marshalRecordsVersion := func(version int, records ...domain.LogicalReservation) json.RawMessage {
		payload, err := json.Marshal(domain.LogicalReservationsExtension{Version: version, Records: records})
		if err != nil {
			t.Fatalf("marshal reservations extension: %v", err)
		}
		return payload
	}
	marshalRecords := func(records ...domain.LogicalReservation) json.RawMessage {
		return marshalRecordsVersion(domain.LogicalReservationsExtensionVersion, records...)
	}
	mutate := func(fn func(*domain.LogicalReservation)) []byte {
		reservation := validReservation()
		fn(&reservation)
		return withExtension(marshalRecords(reservation))
	}
	unknownKeyPayload := func() json.RawMessage {
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(marshalRecords(validReservation()), &generic); err != nil {
			t.Fatalf("unmarshal base extension payload: %v", err)
		}
		generic["unexpected"] = json.RawMessage(`true`)
		payload, err := json.Marshal(generic)
		if err != nil {
			t.Fatalf("marshal extension payload with unknown key: %v", err)
		}
		return payload
	}
	// missingNormalizedJSONPayload drops the normalized_json key entirely
	// rather than setting it to a malformed string: json.RawMessage forces
	// any *present* value through Go's JSON tokenizer on the way in, so
	// "invalid JSON" text can never survive a real marshal/parse round
	// trip -- the only way isValidEventPayload's len(payload) == 0 branch
	// (its "not valid JSON" case, since an absent key decodes to a nil
	// RawMessage) is reachable from a real document is a missing key.
	missingNormalizedJSONPayload := func() json.RawMessage {
		recordBytes, err := json.Marshal(validReservation())
		if err != nil {
			t.Fatalf("marshal base reservation record: %v", err)
		}
		var recordFields map[string]json.RawMessage
		if err := json.Unmarshal(recordBytes, &recordFields); err != nil {
			t.Fatalf("unmarshal base reservation record: %v", err)
		}
		delete(recordFields, "normalized_json")
		extension := struct {
			Version int                          `json:"version"`
			Records []map[string]json.RawMessage `json:"records"`
		}{Version: domain.LogicalReservationsExtensionVersion, Records: []map[string]json.RawMessage{recordFields}}
		payload, err := json.Marshal(extension)
		if err != nil {
			t.Fatalf("marshal extension payload with missing normalized_json: %v", err)
		}
		return payload
	}

	const recordsBase = "$.extensions." + domain.LogicalReservationsExtensionKey + ".records[0]"

	cases := []struct {
		name        string
		data        []byte
		wantTopCode string
		wantCode    string
		wantField   string
	}{
		{
			name:        "active status is rejected outright",
			data:        mutate(func(r *domain.LogicalReservation) { r.Status = "active" }),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "UNSUPPORTED_ACTIVE_RESERVATION", wantField: recordsBase + ".status",
		},
		{
			name:        "attempt_id must resolve to an included attempt",
			data:        mutate(func(r *domain.LogicalReservation) { r.AttemptID = unknownID }),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "INVALID_REFERENCE", wantField: recordsBase + ".attempt_id",
		},
		{
			name:        "issue_id must match the owning attempt's issue",
			data:        mutate(func(r *domain.LogicalReservation) { r.IssueID = otherIssueID }),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "INCONSISTENT_REFERENCE", wantField: recordsBase + ".issue_id",
		},
		{
			name:        "release_reason is required on a released row",
			data:        mutate(func(r *domain.LogicalReservation) { r.ReleaseReason = "" }),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "REQUIRED", wantField: recordsBase + ".release_reason",
		},
		{
			name:        "release_reason must be one of the locked enum values",
			data:        mutate(func(r *domain.LogicalReservation) { r.ReleaseReason = "bogus" }),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "INVALID_ENUM", wantField: recordsBase + ".release_reason",
		},
		{
			name:        "normalized_json must be valid JSON",
			data:        withExtension(missingNormalizedJSONPayload()),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "INVALID_JSON", wantField: recordsBase + ".normalized_json",
		},
		{
			name:        "namespace version 2 is unsupported by this build",
			data:        withExtension(marshalRecordsVersion(2, validReservation())),
			wantTopCode: domain.CodeUnsupportedFormatVersion, wantCode: "UNSUPPORTED_FORMAT_VERSION",
			wantField: "$.extensions." + domain.LogicalReservationsExtensionKey + ".version",
		},
		{
			name:        "namespace payload rejects an unknown key",
			data:        withExtension(unknownKeyPayload()),
			wantTopCode: domain.CodeInvalidArgument, wantCode: "INVALID_JSON",
			wantField: "$.extensions." + domain.LogicalReservationsExtensionKey,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := domain.ParseLogicalProjectImportPlan(testCase.data)
			if err == nil {
				t.Fatal("ParseLogicalProjectImportPlan() error = nil, want an error")
			}
			assertReservationImportDetail(t, err, testCase.wantTopCode, testCase.wantCode, testCase.wantField)
		})
	}
}

// assertReservationImportDetail asserts err is a *domain.Error carrying
// wantTopCode as its top-level code and, as its first detail, wantCode at
// wantField.
func assertReservationImportDetail(t *testing.T, err error, wantTopCode, wantCode, wantField string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v (%T), want *domain.Error", err, err)
	}
	if domainErr.Code != wantTopCode {
		t.Fatalf("error code = %q, want %q (error: %v)", domainErr.Code, wantTopCode, domainErr)
	}
	if len(domainErr.Details) == 0 {
		t.Fatalf("error details = %#v, want at least one detail", domainErr.Details)
	}
	detail := domainErr.Details[0]
	if detail.Code != wantCode || detail.Field != wantField {
		t.Fatalf("error detail = %#v, want code %q field %q", detail, wantCode, wantField)
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
			sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
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

// assignImportDestinationIDs mints a destination ID map for plan the same
// way application.ProjectService.ApplyLogicalProjectImport now does, for
// tests that call ProjectRepository.ApplyLogicalProjectImport directly.
func assignImportDestinationIDs(t *testing.T, plan domain.LogicalProjectImportPlan) domain.LogicalProjectImportPlan {
	t.Helper()
	generator, err := ids.NewGenerator(clock.NewFakeClock(time.Date(2026, 7, 14, 10, 11, 12, 0, time.UTC)), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	destinationIDs, err := domain.NewLogicalProjectImportDestinationIDs(plan.Document, generator.New)
	if err != nil {
		t.Fatalf("NewLogicalProjectImportDestinationIDs() error = %v", err)
	}
	plan.DestinationIDs = destinationIDs
	return plan
}

// TestProjectRepositoryRestoresIssueVersionSoImportedReviewsStayFresh is
// ISSUE-230's regression: every issue used to import at version 1 while its
// review target and request kept the frozen source version, so a request
// that was fresh and claimable at export arrived permanently stale. The v2
// document now carries the issue's own version, and export-import-export
// preserves it.
func TestProjectRepositoryRestoresIssueVersionSoImportedReviewsStayFresh(t *testing.T) {
	db, now := openProjectDatabase(t, "versioned source", "instructions")
	ctx := context.Background()
	generator, err := ids.NewGenerator(clock.NewFakeClock(now), rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueID, err := generator.New()
	if err != nil {
		t.Fatalf("issue ID generation: %v", err)
	}
	var sourceEventID int64
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		// version 3: the issue was updated twice after creation, exactly the
		// state a review request is normally created against.
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'reviewed at version 3', 'review', 'high', 3, ?, ?)`,
			issueID, sqlite.FormatStorageTime(now.Add(1*time.Second)), sqlite.FormatStorageTime(now.Add(2*time.Second))); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at, source) VALUES (?, 'issue_updated', '{"kind":"updated"}', ?, 'issue') RETURNING id`,
			issueID, sqlite.FormatStorageTime(now.Add(3*time.Second))).Scan(&sourceEventID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FC2', ?, 3, ?, '[]', 1, ?)`,
			issueID, sourceEventID, sqlite.FormatStorageTime(now.Add(4*time.Second))); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, version, created_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FC1', '01ARZ3NDEKTSV4RRFFQ69G5FC2', ?, 3, ?, '[]', 'open', 1, ?)`,
			issueID, sourceEventID, sqlite.FormatStorageTime(now.Add(5*time.Second)))
		return err
	}); err != nil {
		t.Fatalf("seed versioned review: %v", err)
	}

	sourceRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	sourceReviews, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("NewReviewRepository() error = %v", err)
	}
	sourceRequest, err := sourceReviews.GetReviewRequest(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FC1")
	if err != nil {
		t.Fatalf("source GetReviewRequest() error = %v", err)
	}
	if sourceRequest.TargetStale {
		t.Fatalf("source review request is stale before export; the fixture is wrong")
	}

	exported, err := sourceRepository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.Issues) != 1 || exported.Issues[0].Version == nil || *exported.Issues[0].Version != 3 {
		t.Fatalf("exported issues = %#v, want one issue carrying version 3", exported.Issues)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	plan = assignImportDestinationIDs(t, plan)

	destinationDB, _ := openProjectDatabase(t, "versioned destination", "instructions")
	destinationRepository, err := sqlite.NewProjectRepository(destinationDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destinationRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	var destinationVersion int64
	var destinationRequestID string
	if err := destinationDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT version FROM issues`).Scan(&destinationVersion); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT id FROM review_requests`).Scan(&destinationRequestID)
	}); err != nil {
		t.Fatalf("read imported rows: %v", err)
	}
	if destinationVersion != 3 {
		t.Fatalf("imported issues.version = %d, want the source's 3", destinationVersion)
	}

	destinationReviews, err := sqlite.NewReviewRepository(destinationDB)
	if err != nil {
		t.Fatalf("NewReviewRepository() error = %v", err)
	}
	destinationRequest, err := destinationReviews.GetReviewRequest(ctx, destinationRequestID)
	if err != nil {
		t.Fatalf("destination GetReviewRequest() error = %v", err)
	}
	// AC2: an open request that was fresh at export is still fresh, which
	// also proves the event cursor landed below the destination's own log.
	if destinationRequest.TargetStale {
		t.Fatalf("imported review request is stale; it was fresh and claimable at export")
	}
	if destinationRequest.Request.Status != domain.ReviewRequestStatusOpen {
		t.Fatalf("imported review request status = %q, want open", destinationRequest.Request.Status)
	}

	// AC5: the third leg of export-import-export carries the version too.
	reExported, err := destinationRepository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("re-export ExportLogicalProject() error = %v", err)
	}
	if len(reExported.Issues) != 1 || reExported.Issues[0].Version == nil || *reExported.Issues[0].Version != 3 {
		t.Fatalf("re-exported issues = %#v, want one issue carrying version 3", reExported.Issues)
	}
}

// TestProjectRepositoryOmitsTheDefaultIssueVersionFromExports keeps the
// ISSUE-230 addition invisible to a project that never updated an issue: the
// exported document is byte-for-byte what it was before the field existed.
func TestProjectRepositoryOmitsTheDefaultIssueVersionFromExports(t *testing.T) {
	db, now := openProjectDatabase(t, "unversioned source", "instructions")
	ctx := context.Background()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES ('01ARZ3NDEKTSV4RRFFQ69G5FD1', 1, 'task', 'never updated', 'ready', 'medium', 1, ?, ?)`,
			sqlite.FormatStorageTime(now.Add(1*time.Second)), sqlite.FormatStorageTime(now.Add(1*time.Second)))
		return err
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.Issues) != 1 || exported.Issues[0].Version != nil {
		t.Fatalf("exported issues = %#v, want the default version omitted", exported.Issues)
	}
	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	if strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("exported document carries a default issue version: %s", data)
	}
}

// TestProjectRepositoryRemapsReviewEventPayloadsSoReExportParses is
// ISSUE-232's regression. Review event payloads are the only record of which
// request and target a review event belongs to (migration 008 folded
// review_events into issue_events and dropped those columns), and export
// promotes them back out of the payload into typed, referentially-checked
// review_events records. Imported verbatim they named source rows, so the
// destination's own next export no longer parsed.
func TestProjectRepositoryRemapsReviewEventPayloadsSoReExportParses(t *testing.T) {
	db, now := openProjectDatabase(t, "payload source", "instructions")
	ctx := context.Background()

	const (
		issueID   = "01ARZ3NDEKTSV4RRFFQ69G5JA1"
		attemptID = "01ARZ3NDEKTSV4RRFFQ69G5JA2"
		targetID  = "01ARZ3NDEKTSV4RRFFQ69G5JA3"
		requestID = "01ARZ3NDEKTSV4RRFFQ69G5JA4"
		outcomeID = "01ARZ3NDEKTSV4RRFFQ69G5JA5"
	)
	later := now.Add(2 * time.Second)
	requestedPayload := `{"request_id":"` + requestID + `","target_id":"` + targetID + `"}`
	approvedPayload := `{"request_id":"` + requestID + `","target_id":"` + targetID + `","attempt_id":"` + attemptID + `","outcome":"approved"}`

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Payload issue', 'done', 'medium', 1, ?, ?)`,
				[]any{issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json) VALUES (?, ?, 'review', 'completed', 1, 0, X'05', ?, ?, ?, ?, 'approved', '[]', '[]')`,
				[]any{attemptID, issueID, sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at) VALUES (?, ?, 1, 0, '[]', '["implementation"]', 1, ?)`,
				[]any{targetID, issueID, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at) VALUES (?, ?, ?, 1, 0, '[]', '["implementation"]', 'approved', NULL, NULL, 1, ?, ?)`,
				[]any{requestID, targetID, issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_outcomes(id, request_id, attempt_id, outcome, reason, version, created_at) VALUES (?, ?, ?, 'approved', NULL, 1, ?)`,
				[]any{outcomeID, requestID, attemptID, sqlite.FormatStorageTime(later)}},
			{`INSERT INTO issue_events(issue_id, event_type, attempt_id, payload, created_at, source) VALUES (?, 'review_requested', NULL, ?, ?, 'review')`,
				[]any{issueID, requestedPayload, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO issue_events(issue_id, event_type, attempt_id, payload, created_at, source) VALUES (?, 'review_approved', ?, ?, ?, 'review')`,
				[]any{issueID, attemptID, approvedPayload, sqlite.FormatStorageTime(later)}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed review payload fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.ReviewEvents) != 2 {
		t.Fatalf("exported review events = %#v, want 2", exported.ReviewEvents)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	plan = assignImportDestinationIDs(t, plan)

	destinationDB, _ := openProjectDatabase(t, "payload destination", "instructions")
	destinationRepository, err := sqlite.NewProjectRepository(destinationDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destinationRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	var destRequestID, destTargetID, destAttemptID string
	if err := destinationDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT id FROM review_requests`).Scan(&destRequestID); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT id FROM review_targets`).Scan(&destTargetID); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT id FROM work_attempts`).Scan(&destAttemptID)
	}); err != nil {
		t.Fatalf("read imported review rows: %v", err)
	}
	if destRequestID == requestID || destTargetID == targetID || destAttemptID == attemptID {
		t.Fatalf("destination IDs were not remapped: request %q target %q attempt %q", destRequestID, destTargetID, destAttemptID)
	}

	// AC3: the third leg must be a document that parses -- which is exactly
	// what a payload naming a source request breaks, because export promotes
	// it into review_events.request_id and referential closure is checked.
	reExported, err := destinationRepository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("re-export ExportLogicalProject() error = %v", err)
	}
	reExportedData, err := domain.MarshalLogicalProjectDocument(reExported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() re-export error = %v", err)
	}
	if _, err := domain.ParseLogicalProjectImportPlan(reExportedData); err != nil {
		t.Fatalf("re-exported document does not parse: %v", err)
	}

	if len(reExported.ReviewEvents) != 2 {
		t.Fatalf("re-exported review events = %#v, want 2", reExported.ReviewEvents)
	}
	for _, event := range reExported.ReviewEvents {
		if event.RequestID != destRequestID || event.TargetID != destTargetID {
			t.Fatalf("re-exported %s names request %q / target %q, want the destination rows %q / %q",
				event.EventType, event.RequestID, event.TargetID, destRequestID, destTargetID)
		}
	}

	// AC1: the attempt identity inside the payload is remapped too, and AC2:
	// the outcome the payload also carries is untouched.
	var approvedPayloadText string
	if err := destinationDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT payload FROM issue_events WHERE event_type = 'review_approved'`).Scan(&approvedPayloadText)
	}); err != nil {
		t.Fatalf("read imported payload: %v", err)
	}
	var approved map[string]string
	if err := json.Unmarshal([]byte(approvedPayloadText), &approved); err != nil {
		t.Fatalf("imported payload is not an object: %v", err)
	}
	if approved["attempt_id"] != destAttemptID {
		t.Fatalf("imported payload attempt_id = %q, want the destination attempt %q", approved["attempt_id"], destAttemptID)
	}
	if approved["outcome"] != "approved" {
		t.Fatalf("imported payload outcome = %q, want it preserved", approved["outcome"])
	}
}

// TestProjectRepositoryRejectsExtensionOnlyDestinations is ISSUE-233's
// regression. Both empty-destination guards listed only the tables that
// existed when logical interchange was written, so a project holding nothing
// but workflow-gate state looked empty to both: preflight reported no
// content, and the in-transaction race-closing check let the import merge
// into it despite the format's no-merge contract.
func TestProjectRepositoryRejectsExtensionOnlyDestinations(t *testing.T) {
	const policyID = "01ARZ3NDEKTSV4RRFFQ69G5KA1"
	for _, testCase := range []struct {
		name  string
		seed  func(ctx context.Context, tx sqlite.Executor, now time.Time) error
		table string
	}{
		{
			name:  "workflow policies only",
			table: "workflow_policies",
			seed: func(ctx context.Context, tx sqlite.Executor, now time.Time) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at) VALUES (?, '{"issue_types":["task"]}', '[{"key":"impl","kind":"attempt_evidence","evidence_key":"impl"}]', 'active', 1, ?, ?)`,
					policyID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
				return err
			},
		},
		{
			name:  "policy audit trail only",
			table: "workflow_policy_events",
			seed: func(ctx context.Context, tx sqlite.Executor, now time.Time) error {
				if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at) VALUES (?, '{"issue_types":["task"]}', '[]', 'archived', 1, ?, ?)`,
					policyID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now)); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO workflow_policy_events(policy_id, event_type, session_id, prior_version, new_version, payload, created_at) VALUES (?, 'policy_created', NULL, NULL, 1, '{}', ?)`,
					policyID, sqlite.FormatStorageTime(now))
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, now := openProjectDatabase(t, "extension destination", "instructions")
			ctx := context.Background()
			if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
				return testCase.seed(ctx, tx, now)
			}); err != nil {
				t.Fatalf("seed %s: %v", testCase.table, err)
			}

			repository, err := sqlite.NewProjectRepository(db)
			if err != nil {
				t.Fatalf("NewProjectRepository() error = %v", err)
			}
			// AC1, preflight half.
			hasContent, err := repository.HasLogicalProjectImportDestinationContent(ctx)
			if err != nil {
				t.Fatalf("HasLogicalProjectImportDestinationContent() error = %v", err)
			}
			if !hasContent {
				t.Fatalf("a destination holding %s rows reported itself empty", testCase.table)
			}

			// AC1, in-transaction half: the race-closing check has to reach
			// the same verdict, since it is the one that actually guards the
			// write.
			document := domain.LogicalProjectDocument{
				Format:     "rhizome-logical-project",
				Version:    2,
				ExportedAt: "2026-07-17T18:24:20Z",
				Project: domain.LogicalProjectProject{
					ID: sqliteTestProjectID, CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
				},
				Issues: []domain.LogicalIssue{{
					ID: "01ARZ3NDEKTSV4RRFFQ69G5KA2", Type: "task", Title: "Imported task", Status: "ready", Priority: "medium",
					CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
				}},
			}
			data, err := domain.MarshalLogicalProjectDocument(document)
			if err != nil {
				t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
			}
			plan, err := domain.ParseLogicalProjectImportPlan(data)
			if err != nil {
				t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
			}
			plan = assignImportDestinationIDs(t, plan)
			result, err := repository.ApplyLogicalProjectImport(ctx, plan)
			if err != nil {
				t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
			}
			// AC3: the documented destination-not-empty conflict.
			if len(result.Conflicts) != 1 || result.Conflicts[0].Code != "empty_destination_required" {
				t.Fatalf("apply result conflicts = %#v, want one empty_destination_required", result.Conflicts)
			}

			// AC3: and nothing was written or disturbed.
			var importedIssues, policies, policyEvents int
			var policyStatus string
			if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
				if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issues`).Scan(&importedIssues); err != nil {
					return err
				}
				if err := query.QueryRowContext(ctx, `SELECT count(*) FROM workflow_policies`).Scan(&policies); err != nil {
					return err
				}
				if err := query.QueryRowContext(ctx, `SELECT count(*) FROM workflow_policy_events`).Scan(&policyEvents); err != nil {
					return err
				}
				return query.QueryRowContext(ctx, `SELECT status FROM workflow_policies WHERE id = ?`, policyID).Scan(&policyStatus)
			}); err != nil {
				t.Fatalf("read destination after the rejected import: %v", err)
			}
			if importedIssues != 0 {
				t.Fatalf("issues after a rejected import = %d, want 0", importedIssues)
			}
			if policies != 1 || policyStatus == "" {
				t.Fatalf("workflow policies after a rejected import = %d (status %q), want the destination's own left untouched", policies, policyStatus)
			}
			if testCase.table == "workflow_policy_events" && policyEvents != 1 {
				t.Fatalf("policy events after a rejected import = %d, want the destination's own left untouched", policyEvents)
			}
		})
	}
}
