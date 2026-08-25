//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestIntegrationLogicalProjectRoundTrip(t *testing.T) {
	sourceEnv := newIntegrationEnvironment(t)
	destEnv := newIntegrationEnvironment(t)
	session := sourceEnv.connect(t)

	createdEpic := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "epic",
		"title":                 "Round trip epic",
		"description":           "Create a representative logical interchange document.",
		"status":                "ready",
		"priority":              "high",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var epic struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, createdEpic, &epic)
	if createdEpic.IsError || epic.ID == "" || epic.DisplayID == "" {
		t.Fatalf("create_issue epic result = %#v, decoded = %#v", createdEpic, epic)
	}

	createdTask := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "Round trip task",
		"description":           "Exercise logical export/import around a terminal attempt.",
		"status":                "ready",
		"priority":              "medium",
		"parent_issue_id":       epic.DisplayID,
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, createdTask, &task)
	if createdTask.IsError || task.ID == "" || task.DisplayID == "" {
		t.Fatalf("create_issue task result = %#v, decoded = %#v", createdTask, task)
	}

	if result := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
		"action":          "add",
		"source_issue_id": epic.DisplayID,
		"target_issue_id": task.DisplayID,
		"relation_type":   "duplicates",
	}); result.IsError {
		t.Fatalf("manage_issue_relation result = %#v", result)
	}
	if result := callIntegrationTool(t, session, "add_comment", map[string]any{
		"issue_id": task.DisplayID,
		"content":  "Round-trip comment for logical interchange.",
	}); result.IsError {
		t.Fatalf("add_comment result = %#v", result)
	}
	if result := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": task.DisplayID,
		"title":    "Record round-trip decision",
		"summary":  "The logical import/export workflow should preserve durable decisions.",
		"content":  "Round-trip test content.",
		"status":   "active",
	}); result.IsError {
		t.Fatalf("record_decision result = %#v", result)
	}

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      task.DisplayID,
		"lease_seconds": 60,
		// Reserved atomically with the claim so finish_attempt's automatic
		// force-release (ISSUE-182) leaves behind a released reservation
		// owned by a now-terminal attempt -- exactly the row shape that
		// crosses the interchange boundary via Extensions["reservations"].
		"resources": []map[string]any{{
			"kind": "file",
			"path": "docs/roundtrip.md",
		}},
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	note := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  claim.Attempt.ID,
		"lease_token": claim.LeaseToken,
		"kind":        "checkpoint",
		"content":     "Round-trip checkpoint with artifact.",
		"artifacts": []map[string]any{{
			"type": "file",
			"uri":  "docs/roundtrip.md",
			"metadata": map[string]any{
				"kind": "roundtrip",
			},
		}},
	})
	if note.IsError {
		t.Fatalf("save_attempt_note result = %#v", note)
	}

	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          claim.Attempt.ID,
		"lease_token":         claim.LeaseToken,
		"outcome":             "completed",
		"result_summary":      "The round-trip workflow passed.",
		"target_issue_status": "done",
		"verification":        []string{"go test -tags=integration ."},
	})
	var completion struct {
		Attempt struct {
			Status string `json:"status"`
		} `json:"attempt"`
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, finished, &completion)
	if finished.IsError || completion.Attempt.Status != "completed" || completion.Issue.Status != "done" {
		t.Fatalf("finish_attempt result = %#v, decoded = %#v", finished, completion)
	}

	sourceDocument := mustExportLogicalProjectDocument(t, sourceEnv)
	mustApplyLogicalProjectDocument(t, destEnv, sourceDocument)
	destDocument := mustExportLogicalProjectDocument(t, destEnv)

	// Assert both sides actually carry the reservation before comparing
	// them. The canonical comparison below is symmetric, so an export that
	// silently dropped the namespace would still match an import that
	// never received one -- the round trip would pass while guaranteeing
	// nothing about reservations at all.
	assertRoundTripReservation(t, "source", sourceDocument)
	assertRoundTripReservation(t, "dest", destDocument)

	sourceCanonical := canonicalizeLogicalProjectDocumentWithMappings(sourceDocument, buildCanonicalIDMappings(sourceDocument))
	destCanonical := canonicalizeLogicalProjectDocumentWithMappings(destDocument, mergeCanonicalIDMappings(buildCanonicalIDMappings(sourceDocument), buildCanonicalIDMappings(destDocument)))
	sourceCanonicalJSON := mustMarshalDocument(t, sourceCanonical)
	destCanonicalJSON := mustMarshalDocument(t, destCanonical)
	if sourceCanonicalJSON != destCanonicalJSON {
		t.Fatalf("round-trip logical content mismatch\nsource=%s\ndest=%s\nsource-canonical=%s\ndest-canonical=%s", mustMarshalDocument(t, sourceDocument), mustMarshalDocument(t, destDocument), sourceCanonicalJSON, destCanonicalJSON)
	}
}

// assertRoundTripReservation fails unless document carries exactly the one
// released reservation the round-trip fixture creates, with the display
// value it was reserved under.
func assertRoundTripReservation(t *testing.T, side string, document domain.LogicalProjectDocument) {
	t.Helper()
	reservations, err := document.DecodeReservationsExtension()
	if err != nil {
		t.Fatalf("%s DecodeReservationsExtension() error = %v", side, err)
	}
	if len(reservations) != 1 {
		t.Fatalf("%s reservations = %d, want 1", side, len(reservations))
	}
	reservation := reservations[0]
	if reservation.DisplayValue != "docs/roundtrip.md" || reservation.Kind != "file" {
		t.Fatalf("%s reservation = %#v", side, reservation)
	}
	if reservation.Status != "released" || reservation.ReleasedAt == "" || reservation.ReleaseReason == "" {
		t.Fatalf("%s reservation release state = %#v", side, reservation)
	}
}

// TestIntegrationLogicalProjectVersion1DocumentImportsWithoutExtensions is
// the ISSUE-182 v1-compatibility guarantee: a version 1 document, which
// never carries an "extensions" key at all (the v1 key table is frozen --
// see docs/07 §7), must still import successfully now that the importer
// also understands version 2's reservations namespace.
func TestIntegrationLogicalProjectVersion1DocumentImportsWithoutExtensions(t *testing.T) {
	destEnv := newIntegrationEnvironment(t)

	const issueID = "01ARZ3NDEKTSV4RRFFQ69G5FJB"
	document := domain.LogicalProjectDocument{
		Format:     "rhizome-logical-project",
		Version:    1,
		ExportedAt: "2026-07-17T18:24:20Z",
		Project: domain.LogicalProjectProject{
			ID: "01ARZ3NDEKTSV4RRFFQ69G5FJA", CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		},
		Issues: []domain.LogicalIssue{{
			ID: issueID, Type: "task", Title: "v1 compatibility issue", Status: "ready", Priority: "medium",
			CreatedAt: "2026-07-17T18:24:06Z", UpdatedAt: "2026-07-17T18:24:06Z",
		}},
	}

	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	if strings.Contains(string(data), `"extensions"`) {
		t.Fatalf("version 1 document unexpectedly carries an extensions key: %s", data)
	}

	mustApplyLogicalProjectDocument(t, destEnv, document)

	imported := mustExportLogicalProjectDocument(t, destEnv)
	var found bool
	for _, issue := range imported.Issues {
		if issue.Title == "v1 compatibility issue" {
			found = true
		}
	}
	if !found {
		t.Fatalf("imported issues = %#v, want the v1 document's issue to have been applied", imported.Issues)
	}
}

func mustExportLogicalProjectDocument(t *testing.T, env integrationEnvironment) domain.LogicalProjectDocument {
	t.Helper()
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open logical project database %s: %v", databasePath, err)
	}
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Errorf("close logical project database %s: %v", databasePath, closeErr)
		}
	}()
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("create project repository: %v", err)
	}
	document, err := projectRepository.ExportLogicalProject(context.Background())
	if err != nil {
		t.Fatalf("export logical project document: %v", err)
	}
	return document
}

func mustApplyLogicalProjectDocument(t *testing.T, env integrationEnvironment, document domain.LogicalProjectDocument) {
	t.Helper()
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open logical project database %s: %v", databasePath, err)
	}
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Errorf("close logical project database %s: %v", databasePath, closeErr)
		}
	}()
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("create project repository: %v", err)
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("marshal logical project document: %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("parse logical project import plan: %v", err)
	}
	// Every destination ID the import needs is minted before the write
	// transaction runs (application.ProjectService.ApplyLogicalProjectImport's
	// own sequence, mirrored here since this test drives the repository
	// directly instead of through that service).
	plan.DestinationIDs, err = domain.NewLogicalProjectImportDestinationIDs(plan.Document, func() (string, error) { return newIntegrationULID(t), nil })
	if err != nil {
		t.Fatalf("generate logical project import destination ids: %v", err)
	}
	if _, err := projectRepository.ApplyLogicalProjectImport(context.Background(), plan); err != nil {
		t.Fatalf("apply logical project import: %v", err)
	}
}

func mustMarshalDocument(t *testing.T, document domain.LogicalProjectDocument) string {
	t.Helper()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("marshal logical project document: %v", err)
	}
	return string(data)
}

type canonicalIDMappings struct {
	issueIDs       map[string]string
	labelIDs       map[string]string
	relationIDs    map[string]string
	commentIDs     map[string]string
	decisionIDs    map[string]string
	attemptIDs     map[string]string
	attemptNoteIDs map[string]string
	artifactIDs    map[string]string
	reservationIDs map[string]string
	policyIDs      map[string]string
	evidenceIDs    map[string]string
	approvalIDs    map[string]string
}

func buildCanonicalIDMappings(document domain.LogicalProjectDocument) canonicalIDMappings {
	// DecodeReservationsExtension is safe to call unchecked here: by the
	// time a document reaches this test helper it has already round-tripped
	// through ParseLogicalProjectImportPlan (via mustApplyLogicalProjectDocument)
	// or come straight out of ExportLogicalProject, both of which guarantee
	// a well-formed (or absent) reservations namespace.
	reservations, _ := document.DecodeReservationsExtension()
	gates, _ := document.DecodeGatesExtension()
	mappings := canonicalIDMappings{
		issueIDs:       make(map[string]string, len(document.Issues)),
		labelIDs:       make(map[string]string, len(document.Labels)),
		relationIDs:    make(map[string]string, len(document.Relations)),
		commentIDs:     make(map[string]string, len(document.Comments)),
		decisionIDs:    make(map[string]string, len(document.Decisions)),
		attemptIDs:     make(map[string]string, len(document.Attempts)),
		attemptNoteIDs: make(map[string]string, len(document.AttemptNotes)),
		artifactIDs:    make(map[string]string, len(document.Artifacts)),
		reservationIDs: make(map[string]string, len(reservations)),
		policyIDs:      make(map[string]string, len(gates.Policies)),
		evidenceIDs:    make(map[string]string, len(gates.Evidence)),
		approvalIDs:    make(map[string]string, len(gates.ReviewApprovals)),
	}
	for index := range document.Issues {
		placeholder := fmt.Sprintf("issue-%d", index)
		mappings.issueIDs[document.Issues[index].ID] = placeholder
	}
	for index := range document.Labels {
		placeholder := fmt.Sprintf("label-%d", index)
		mappings.labelIDs[document.Labels[index].ID] = placeholder
	}
	for index := range document.Relations {
		placeholder := fmt.Sprintf("relation-%d", index)
		mappings.relationIDs[document.Relations[index].ID] = placeholder
	}
	for index := range document.Comments {
		placeholder := fmt.Sprintf("comment-%d", index)
		mappings.commentIDs[document.Comments[index].ID] = placeholder
	}
	for index := range document.Decisions {
		placeholder := fmt.Sprintf("decision-%d", index)
		mappings.decisionIDs[document.Decisions[index].ID] = placeholder
	}
	for index := range document.Attempts {
		placeholder := fmt.Sprintf("attempt-%d", index)
		mappings.attemptIDs[document.Attempts[index].ID] = placeholder
	}
	for index := range document.AttemptNotes {
		placeholder := fmt.Sprintf("attempt-note-%d", index)
		mappings.attemptNoteIDs[document.AttemptNotes[index].ID] = placeholder
	}
	for index := range document.Artifacts {
		placeholder := fmt.Sprintf("artifact-%d", index)
		mappings.artifactIDs[document.Artifacts[index].ID] = placeholder
	}
	for index := range reservations {
		placeholder := fmt.Sprintf("reservation-%d", index)
		mappings.reservationIDs[reservations[index].ID] = placeholder
	}
	for index := range gates.Policies {
		mappings.policyIDs[gates.Policies[index].ID] = fmt.Sprintf("policy-%d", index)
	}
	for index := range gates.Evidence {
		mappings.evidenceIDs[gates.Evidence[index].ID] = fmt.Sprintf("evidence-%d", index)
	}
	for index := range gates.ReviewApprovals {
		mappings.approvalIDs[gates.ReviewApprovals[index].ID] = fmt.Sprintf("approval-%d", index)
	}
	return mappings
}

func normalizeEventPayload(payload json.RawMessage, issueIDs, relationIDs, commentIDs, decisionIDs, attemptIDs, attemptNoteIDs map[string]string) json.RawMessage {
	if len(payload) == 0 {
		return payload
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return payload
	}
	var update func(any)
	update = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				switch key {
				case "parent_id", "source_issue_id", "target_issue_id", "issue_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := issueIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "relation_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := relationIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "comment_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := commentIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "decision_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := decisionIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "attempt_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := attemptIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "note_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := attemptNoteIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				}
				update(child)
			}
		case []any:
			for _, child := range typed {
				update(child)
			}
		}
	}
	update(decoded)
	data, err := json.Marshal(decoded)
	if err != nil {
		return payload
	}
	return data
}

func canonicalizeLogicalProjectDocument(document domain.LogicalProjectDocument) domain.LogicalProjectDocument {
	return canonicalizeLogicalProjectDocumentWithMappings(document, buildCanonicalIDMappings(document))
}

func mergeCanonicalIDMappings(sourceMappings, destinationMappings canonicalIDMappings) canonicalIDMappings {
	merged := destinationMappings
	for id, placeholder := range sourceMappings.issueIDs {
		merged.issueIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.labelIDs {
		merged.labelIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.relationIDs {
		merged.relationIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.commentIDs {
		merged.commentIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.decisionIDs {
		merged.decisionIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.attemptIDs {
		merged.attemptIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.attemptNoteIDs {
		merged.attemptNoteIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.artifactIDs {
		merged.artifactIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.reservationIDs {
		merged.reservationIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.policyIDs {
		merged.policyIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.evidenceIDs {
		merged.evidenceIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.approvalIDs {
		merged.approvalIDs[id] = placeholder
	}
	return merged
}

func canonicalizeLogicalProjectDocumentWithMappings(document domain.LogicalProjectDocument, mappings canonicalIDMappings) domain.LogicalProjectDocument {
	normalized := document
	normalized.ExportedAt = ""
	normalized.Project.ID = ""

	issueIDs := make(map[string]string, len(normalized.Issues)+len(mappings.issueIDs))
	for id, placeholder := range mappings.issueIDs {
		issueIDs[id] = placeholder
	}
	for index := range normalized.Issues {
		issue := normalized.Issues[index]
		placeholder := fmt.Sprintf("issue-%d", index)
		if explicit, ok := mappings.issueIDs[issue.ID]; ok {
			placeholder = explicit
		}
		issueIDs[issue.ID] = placeholder
		issue.ID = placeholder
		if issue.ParentID != nil {
			parentPlaceholder := issueIDs[*issue.ParentID]
			issue.ParentID = &parentPlaceholder
		}
		issue.ClosedAt = nil
		normalized.Issues[index] = issue
	}

	labelIDs := make(map[string]string, len(normalized.Labels)+len(mappings.labelIDs))
	for id, placeholder := range mappings.labelIDs {
		labelIDs[id] = placeholder
	}
	for index := range normalized.Labels {
		label := normalized.Labels[index]
		placeholder := fmt.Sprintf("label-%d", index)
		if explicit, ok := mappings.labelIDs[label.ID]; ok {
			placeholder = explicit
		}
		labelIDs[label.ID] = placeholder
		label.ID = placeholder
		normalized.Labels[index] = label
	}

	for index := range normalized.IssueLabels {
		normalized.IssueLabels[index].IssueID = issueIDs[normalized.IssueLabels[index].IssueID]
		normalized.IssueLabels[index].LabelID = labelIDs[normalized.IssueLabels[index].LabelID]
	}
	sort.Slice(normalized.IssueLabels, func(i, j int) bool {
		if normalized.IssueLabels[i].IssueID == normalized.IssueLabels[j].IssueID {
			return normalized.IssueLabels[i].LabelID < normalized.IssueLabels[j].LabelID
		}
		return normalized.IssueLabels[i].IssueID < normalized.IssueLabels[j].IssueID
	})

	relationIDs := make(map[string]string, len(normalized.Relations)+len(mappings.relationIDs))
	for id, placeholder := range mappings.relationIDs {
		relationIDs[id] = placeholder
	}
	for index := range normalized.Relations {
		relation := normalized.Relations[index]
		placeholder := fmt.Sprintf("relation-%d", index)
		if explicit, ok := mappings.relationIDs[relation.ID]; ok {
			placeholder = explicit
		}
		relationIDs[relation.ID] = placeholder
		relation.ID = placeholder
		relation.SourceIssueID = issueIDs[relation.SourceIssueID]
		relation.TargetIssueID = issueIDs[relation.TargetIssueID]
		normalized.Relations[index] = relation
	}

	commentIDs := make(map[string]string, len(normalized.Comments)+len(mappings.commentIDs))
	for id, placeholder := range mappings.commentIDs {
		commentIDs[id] = placeholder
	}
	for index := range normalized.Comments {
		comment := normalized.Comments[index]
		placeholder := fmt.Sprintf("comment-%d", index)
		if explicit, ok := mappings.commentIDs[comment.ID]; ok {
			placeholder = explicit
		}
		commentIDs[comment.ID] = placeholder
		comment.ID = placeholder
		comment.IssueID = issueIDs[comment.IssueID]
		normalized.Comments[index] = comment
	}

	decisionIDs := make(map[string]string, len(normalized.Decisions)+len(mappings.decisionIDs))
	for id, placeholder := range mappings.decisionIDs {
		decisionIDs[id] = placeholder
	}
	for index := range normalized.Decisions {
		decision := normalized.Decisions[index]
		placeholder := fmt.Sprintf("decision-%d", index)
		if explicit, ok := mappings.decisionIDs[decision.ID]; ok {
			placeholder = explicit
		}
		decisionIDs[decision.ID] = placeholder
		decision.ID = placeholder
		if decision.IssueID != nil {
			issuePlaceholder := issueIDs[*decision.IssueID]
			decision.IssueID = &issuePlaceholder
		}
		if decision.SupersedesID != nil {
			supersedesPlaceholder := decisionIDs[*decision.SupersedesID]
			decision.SupersedesID = &supersedesPlaceholder
		}
		normalized.Decisions[index] = decision
	}

	attemptIDs := make(map[string]string, len(normalized.Attempts)+len(mappings.attemptIDs))
	for id, placeholder := range mappings.attemptIDs {
		attemptIDs[id] = placeholder
	}
	for index := range normalized.Attempts {
		attempt := normalized.Attempts[index]
		placeholder := fmt.Sprintf("attempt-%d", index)
		if explicit, ok := mappings.attemptIDs[attempt.ID]; ok {
			placeholder = explicit
		}
		attemptIDs[attempt.ID] = placeholder
		attempt.ID = placeholder
		attempt.IssueID = issueIDs[attempt.IssueID]
		normalized.Attempts[index] = attempt
	}

	attemptNoteIDs := make(map[string]string, len(normalized.AttemptNotes)+len(mappings.attemptNoteIDs))
	for id, placeholder := range mappings.attemptNoteIDs {
		attemptNoteIDs[id] = placeholder
	}
	for index := range normalized.AttemptNotes {
		note := normalized.AttemptNotes[index]
		placeholder := fmt.Sprintf("attempt-note-%d", index)
		if explicit, ok := mappings.attemptNoteIDs[note.ID]; ok {
			placeholder = explicit
		}
		attemptNoteIDs[note.ID] = placeholder
		note.ID = placeholder
		note.AttemptID = attemptIDs[note.AttemptID]
		normalized.AttemptNotes[index] = note
	}

	artifactIDs := make(map[string]string, len(normalized.Artifacts)+len(mappings.artifactIDs))
	for id, placeholder := range mappings.artifactIDs {
		artifactIDs[id] = placeholder
	}
	for index := range normalized.Artifacts {
		artifact := normalized.Artifacts[index]
		placeholder := fmt.Sprintf("artifact-%d", index)
		if explicit, ok := mappings.artifactIDs[artifact.ID]; ok {
			placeholder = explicit
		}
		artifactIDs[artifact.ID] = placeholder
		artifact.ID = placeholder
		artifact.IssueID = issueIDs[artifact.IssueID]
		if artifact.AttemptID != nil {
			attemptPlaceholder := attemptIDs[*artifact.AttemptID]
			artifact.AttemptID = &attemptPlaceholder
		}
		normalized.Artifacts[index] = artifact
	}

	for index := range normalized.Events {
		event := normalized.Events[index]
		event.SourceID = int64(index + 1)
		if event.IssueID != nil {
			issuePlaceholder := issueIDs[*event.IssueID]
			event.IssueID = &issuePlaceholder
		}
		if event.AttemptID != nil {
			attemptPlaceholder := attemptIDs[*event.AttemptID]
			event.AttemptID = &attemptPlaceholder
		}
		event.Payload = nil
		normalized.Events[index] = event
	}

	_ = artifactIDs

	// Extensions["reservations"] (ISSUE-182) rides outside the top-level
	// arrays canonicalized above, so it needs its own remap pass: swap the
	// reservation's own ID plus its issue_id/attempt_id references for the
	// same placeholders those maps already produced, then re-encode the
	// namespace. comparison_value and normalized_json carry no IDs and are
	// left untouched.
	if reservations, err := normalized.DecodeReservationsExtension(); err == nil && len(reservations) > 0 {
		reservationIDs := make(map[string]string, len(reservations)+len(mappings.reservationIDs))
		for id, placeholder := range mappings.reservationIDs {
			reservationIDs[id] = placeholder
		}
		canonicalReservations := make([]domain.LogicalReservation, len(reservations))
		for index := range reservations {
			reservation := reservations[index]
			placeholder := fmt.Sprintf("reservation-%d", index)
			if explicit, ok := mappings.reservationIDs[reservation.ID]; ok {
				placeholder = explicit
			}
			reservationIDs[reservation.ID] = placeholder
			reservation.ID = placeholder
			reservation.IssueID = issueIDs[reservation.IssueID]
			reservation.AttemptID = attemptIDs[reservation.AttemptID]
			canonicalReservations[index] = reservation
		}
		if payload, err := json.Marshal(domain.LogicalReservationsExtension{
			Version: domain.LogicalReservationsExtensionVersion,
			Records: canonicalReservations,
		}); err == nil {
			extensions := make(map[string]json.RawMessage, len(normalized.Extensions))
			for key, value := range normalized.Extensions {
				extensions[key] = value
			}
			extensions[domain.LogicalReservationsExtensionKey] = payload
			normalized.Extensions = extensions
		}
	}

	// Extensions["gates"] (ISSUE-175) follows the reservations pass: swap
	// each record's own ID and its reference columns for the placeholders
	// the maps above already produced, and normalize the audit events'
	// autoincrement source IDs. The frozen requirement/source-policy blobs
	// are deliberately left untouched -- they are carried and re-inserted
	// verbatim on import, so both sides hold identical bytes.
	if gates, err := normalized.DecodeGatesExtension(); err == nil && !gates.IsEmpty() {
		remap := func(table map[string]string, id string) string {
			if placeholder, ok := table[id]; ok {
				return placeholder
			}
			return id
		}
		for index := range gates.Policies {
			gates.Policies[index].ID = remap(mappings.policyIDs, gates.Policies[index].ID)
		}
		for index := range gates.PolicyEvents {
			gates.PolicyEvents[index].SourceID = int64(index + 1)
			gates.PolicyEvents[index].PolicyID = remap(mappings.policyIDs, gates.PolicyEvents[index].PolicyID)
		}
		for index := range gates.AttemptSnapshots {
			gates.AttemptSnapshots[index].AttemptID = remap(attemptIDs, gates.AttemptSnapshots[index].AttemptID)
		}
		for index := range gates.Evidence {
			evidence := gates.Evidence[index]
			evidence.ID = remap(mappings.evidenceIDs, evidence.ID)
			evidence.AttemptID = remap(attemptIDs, evidence.AttemptID)
			evidence.IssueID = remap(issueIDs, evidence.IssueID)
			for artifactIndex := range evidence.ArtifactIDs {
				evidence.ArtifactIDs[artifactIndex] = remap(artifactIDs, evidence.ArtifactIDs[artifactIndex])
			}
			gates.Evidence[index] = evidence
		}
		for index := range gates.EvidenceEvents {
			event := gates.EvidenceEvents[index]
			event.SourceID = int64(index + 1)
			event.EvidenceID = remap(mappings.evidenceIDs, event.EvidenceID)
			event.AttemptID = remap(attemptIDs, event.AttemptID)
			event.IssueID = remap(issueIDs, event.IssueID)
			gates.EvidenceEvents[index] = event
		}
		for index := range gates.ReviewApprovals {
			approval := gates.ReviewApprovals[index]
			approval.ID = remap(mappings.approvalIDs, approval.ID)
			approval.IssueID = remap(issueIDs, approval.IssueID)
			approval.AttemptID = remap(attemptIDs, approval.AttemptID)
			gates.ReviewApprovals[index] = approval
		}
		if payload, err := json.Marshal(gates); err == nil {
			extensions := make(map[string]json.RawMessage, len(normalized.Extensions))
			for key, value := range normalized.Extensions {
				extensions[key] = value
			}
			extensions[domain.LogicalGatesExtensionKey] = payload
			normalized.Extensions = extensions
		}
	}

	return normalized
}

// TestIntegrationLogicalProjectRestoresIssueVersionForReviews is ISSUE-230's
// integration regression: an issue at version 2+ with an open, claimable
// review request must arrive in the destination project still claimable.
// Before the fix every imported issue landed at version 1 while the request
// kept its frozen target version, so the restored request was immediately
// stale and no reviewer could ever claim it.
func TestIntegrationLogicalProjectRestoresIssueVersionForReviews(t *testing.T) {
	sourceEnv := newIntegrationEnvironment(t)
	destEnv := newIntegrationEnvironment(t)
	session := sourceEnv.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":        "task",
		"title":       "Versioned review target",
		"description": "Exercise interchange of an issue whose version moved past 1.",
		"status":      "review",
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" {
		t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
	}
	for version := int64(1); version <= 2; version++ {
		updated := callIntegrationTool(t, session, "update_issue", map[string]any{
			"issue_id":         issue.DisplayID,
			"expected_version": version,
			"changes":          map[string]any{"description": fmt.Sprintf("Implementation revision %d.", version)},
		})
		if updated.IsError {
			t.Fatalf("update_issue result = %#v", updated)
		}
	}

	db, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, sourceEnv), sqlite.Options{})
	if err != nil {
		t.Fatalf("open source project database: %v", err)
	}
	sourceVersion, sourceEventID := currentReviewTarget(t, db, issue.ID)
	if sourceVersion < 3 {
		t.Fatalf("source issue version = %d, want 3 after two updates", sourceVersion)
	}
	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	if _, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: sourceVersion,
		TargetEventID:      sourceEventID,
		ArtifactIDs:        []string{},
		OccurredAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create review request: %v", err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatalf("close source project database: %v", err)
	}

	document := mustExportLogicalProjectDocument(t, sourceEnv)
	var exportedVersion *int64
	for _, exported := range document.Issues {
		if exported.Title == "Versioned review target" {
			exportedVersion = exported.Version
		}
	}
	if exportedVersion == nil || *exportedVersion != sourceVersion {
		t.Fatalf("exported issue version = %v, want %d", exportedVersion, sourceVersion)
	}
	mustApplyLogicalProjectDocument(t, destEnv, document)

	destDB, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, destEnv), sqlite.Options{})
	if err != nil {
		t.Fatalf("open destination project database: %v", err)
	}
	var destIssueID, destRequestID string
	var destVersion int64
	if err := destDB.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT id, version FROM issues`).Scan(&destIssueID, &destVersion); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT id FROM review_requests`).Scan(&destRequestID)
	}); err != nil {
		t.Fatalf("read imported rows: %v", err)
	}
	if err := destDB.Close(context.Background()); err != nil {
		t.Fatalf("close destination project database: %v", err)
	}
	if destVersion != sourceVersion {
		t.Fatalf("imported issue version = %d, want the source's %d", destVersion, sourceVersion)
	}
	if destIssueID == issue.ID {
		t.Fatalf("imported issue kept the source ID %q; IDs must be remapped", destIssueID)
	}

	destSession := destEnv.connect(t)
	got := callIntegrationTool(t, destSession, "get_review_request", map[string]any{"review_request_id": destRequestID})
	var reviewOutput struct {
		Status             string `json:"status"`
		Claimable          bool   `json:"claimable"`
		TargetIssueVersion int64  `json:"target_issue_version"`
	}
	decodeIntegrationResult(t, got, &reviewOutput)
	if got.IsError || reviewOutput.Status != "open" || !reviewOutput.Claimable {
		t.Fatalf("imported review request = %#v, decoded = %#v; want an open, claimable request", got, reviewOutput)
	}
	if reviewOutput.TargetIssueVersion != sourceVersion {
		t.Fatalf("imported target_issue_version = %d, want %d", reviewOutput.TargetIssueVersion, sourceVersion)
	}
}
