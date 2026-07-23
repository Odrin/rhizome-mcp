//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
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

	sourceCanonical := canonicalizeLogicalProjectDocumentWithMappings(sourceDocument, buildCanonicalIDMappings(sourceDocument))
	destCanonical := canonicalizeLogicalProjectDocumentWithMappings(destDocument, mergeCanonicalIDMappings(buildCanonicalIDMappings(sourceDocument), buildCanonicalIDMappings(destDocument)))
	sourceCanonicalJSON := mustMarshalDocument(t, sourceCanonical)
	destCanonicalJSON := mustMarshalDocument(t, destCanonical)
	if sourceCanonicalJSON != destCanonicalJSON {
		t.Fatalf("round-trip logical content mismatch\nsource=%s\ndest=%s\nsource-canonical=%s\ndest-canonical=%s", mustMarshalDocument(t, sourceDocument), mustMarshalDocument(t, destDocument), sourceCanonicalJSON, destCanonicalJSON)
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
}

func buildCanonicalIDMappings(document domain.LogicalProjectDocument) canonicalIDMappings {
	mappings := canonicalIDMappings{
		issueIDs:       make(map[string]string, len(document.Issues)),
		labelIDs:       make(map[string]string, len(document.Labels)),
		relationIDs:    make(map[string]string, len(document.Relations)),
		commentIDs:     make(map[string]string, len(document.Comments)),
		decisionIDs:    make(map[string]string, len(document.Decisions)),
		attemptIDs:     make(map[string]string, len(document.Attempts)),
		attemptNoteIDs: make(map[string]string, len(document.AttemptNotes)),
		artifactIDs:    make(map[string]string, len(document.Artifacts)),
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
	return normalized
}
