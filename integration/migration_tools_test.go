//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

func TestIntegrationMigrationProfileExportValidateApply(t *testing.T) {
	t.Parallel()
	sourceEnv := newIntegrationEnvironment(t)
	destEnv := newIntegrationEnvironment(t)

	sourceSetupSession := sourceEnv.connect(t)
	buildRepresentativeFixture(t, sourceSetupSession)

	sourceSession := sourceEnv.connectWithServeArgs(t, "--profile", "migration")
	destSession := destEnv.connectWithServeArgs(t, "--profile", "migration")

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	toolsSource, err := sourceSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools on source: %v", err)
	}

	toolDest, err := destSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools on destination: %v", err)
	}

	sourceToolNames := toolNames(toolsSource.Tools)
	destToolNames := toolNames(toolDest.Tools)
	sort.Strings(sourceToolNames)
	sort.Strings(destToolNames)

	expectedTools := []string{"apply_import", "export_project", "get_project", "open_project", "validate_import"}
	if len(sourceToolNames) != len(expectedTools) {
		t.Fatalf("source profile advertised %d tools, want exactly %d: %v", len(sourceToolNames), len(expectedTools), sourceToolNames)
	}
	for i, expected := range expectedTools {
		if sourceToolNames[i] != expected {
			t.Fatalf("source tool %d = %q, want %q (all: %v)", i, sourceToolNames[i], expected, sourceToolNames)
		}
	}

	if len(destToolNames) != len(expectedTools) {
		t.Fatalf("destination profile advertised %d tools, want exactly %d: %v", len(destToolNames), len(expectedTools), destToolNames)
	}
	for i, expected := range expectedTools {
		if destToolNames[i] != expected {
			t.Fatalf("destination tool %d = %q, want %q (all: %v)", i, destToolNames[i], expected, destToolNames)
		}
	}

	sourceExport := callIntegrationTool(t, sourceSession, "export_project", map[string]any{"delivery": "inline"})
	if sourceExport.IsError {
		t.Fatalf("export_project on source result = %#v", sourceExport)
	}

	var sourceDocument domain.LogicalProjectDocument
	decodeIntegrationResult(t, sourceExport, &sourceDocument)

	sourceDocumentJSON, err := json.Marshal(sourceDocument)
	if err != nil {
		t.Fatalf("marshal source document: %v", err)
	}

	validateResult := callIntegrationTool(t, destSession, "validate_import", map[string]any{
		"document": string(sourceDocumentJSON),
	})
	if validateResult.IsError {
		t.Fatalf("validate_import result = %#v", validateResult)
	}

	var validation domain.LogicalProjectImportDryRun
	decodeIntegrationResult(t, validateResult, &validation)
	if len(validation.Conflicts) > 0 {
		t.Fatalf("validate_import returned conflicts: %#v", validation.Conflicts)
	}

	applyResult := callIntegrationTool(t, destSession, "apply_import", map[string]any{
		"document": string(sourceDocumentJSON),
	})
	if applyResult.IsError {
		t.Fatalf("apply_import result = %#v", applyResult)
	}

	var applyOutcome domain.LogicalProjectImportApplyResult
	decodeIntegrationResult(t, applyResult, &applyOutcome)

	destExport := callIntegrationTool(t, destSession, "export_project", map[string]any{"delivery": "inline"})
	if destExport.IsError {
		t.Fatalf("export_project on destination result = %#v", destExport)
	}

	var destDocument domain.LogicalProjectDocument
	decodeIntegrationResult(t, destExport, &destDocument)

	sourceCanonical := canonicalizeLogicalProjectDocumentWithMappings(sourceDocument, buildCanonicalIDMappings(sourceDocument))
	destCanonical := canonicalizeLogicalProjectDocumentWithMappings(destDocument, mergeCanonicalIDMappings(buildCanonicalIDMappings(sourceDocument), buildCanonicalIDMappings(destDocument)))

	sourceCanonicalJSON := mustMarshalDocument(t, sourceCanonical)
	destCanonicalJSON := mustMarshalDocument(t, destCanonical)
	if sourceCanonicalJSON != destCanonicalJSON {
		t.Fatalf("migration export/apply cycle produced non-equivalent documents\nsource-canonical=%s\ndest-canonical=%s", sourceCanonicalJSON, destCanonicalJSON)
	}
}

func TestIntegrationMigrationProfileValidateImportRejectsInvalidDocument(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connectWithServeArgs(t, "--profile", "migration")

	invalidDocument := `{"schema_version": "v1"}`

	beforeExport := callIntegrationTool(t, session, "export_project", map[string]any{"delivery": "inline"})
	var beforeDoc domain.LogicalProjectDocument
	decodeIntegrationResult(t, beforeExport, &beforeDoc)

	validateResult := callIntegrationTool(t, session, "validate_import", map[string]any{
		"document": invalidDocument,
	})
	if !validateResult.IsError {
		t.Fatalf("validate_import should reject invalid document but result = %#v", validateResult)
	}

	afterExport := callIntegrationTool(t, session, "export_project", map[string]any{"delivery": "inline"})
	var afterDoc domain.LogicalProjectDocument
	decodeIntegrationResult(t, afterExport, &afterDoc)

	beforeCanonical := canonicalizeLogicalProjectDocumentWithMappings(beforeDoc, buildCanonicalIDMappings(beforeDoc))
	afterCanonical := canonicalizeLogicalProjectDocumentWithMappings(afterDoc, buildCanonicalIDMappings(afterDoc))

	beforeJSON := mustMarshalDocument(t, beforeCanonical)
	afterJSON := mustMarshalDocument(t, afterCanonical)

	if beforeJSON != afterJSON {
		t.Fatalf("validate_import error should not have modified state\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}

	if validateResult.StructuredContent == nil {
		t.Fatalf("validate_import error should include structured error content")
	}

	errorMap, ok := validateResult.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("validate_import error structured content should be a map, got %T", validateResult.StructuredContent)
	}

	code, ok := errorMap["code"].(string)
	if !ok || code == "" {
		t.Fatalf("validate_import error should include a non-empty 'code' field, got %#v", errorMap)
	}
}

func buildRepresentativeFixture(t *testing.T, session *mcp.ClientSession) {
	t.Helper()

	createdEpic := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "epic",
		"title":                 "Migration test epic",
		"description":           "Epic for migration profile test.",
		"status":                "ready",
		"priority":              "high",
		"labels":                []string{"migration-test"},
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
		"title":                 "Migration test task",
		"description":           "Task for migration profile test.",
		"status":                "ready",
		"priority":              "medium",
		"parent_issue_id":       epic.DisplayID,
		"labels":                []string{"migration-test"},
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
		"content":  "Migration test comment.",
	}); result.IsError {
		t.Fatalf("add_comment result = %#v", result)
	}

	if result := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": task.DisplayID,
		"title":    "Migration test decision",
		"summary":  "A test decision for migration profile.",
		"content":  "Decision content for migration test.",
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
		"content":     "Migration test checkpoint.",
		"artifacts": []map[string]any{{
			"type": "file",
			"uri":  "docs/migration-test.md",
			"metadata": map[string]any{
				"kind": "migration-test",
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
		"result_summary":      "Migration test completed.",
		"target_issue_status": "done",
		"verification":        []string{"go test -tags=integration -run TestIntegrationMigration ."},
	})
	if finished.IsError {
		t.Fatalf("finish_attempt result = %#v", finished)
	}
}
