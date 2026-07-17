package domain_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/domain"
)

func TestParseLogicalProjectImportPlanValidationRules(t *testing.T) {
	t.Run("accepts null session references", func(t *testing.T) {
		payload := buildLogicalProjectDocument(nil)
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects non-null session references", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issues := document["issues"].([]any)
			issue := issues[0].(map[string]any)
			issue["created_by_session_id"] = ulid.Make().String()
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "UNSUPPORTED_SESSION_REFERENCE", "$.issues[0].created_by_session_id")
	})

	t.Run("rejects event session references", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			events := document["events"].([]any)
			event := events[0].(map[string]any)
			event["session_id"] = ulid.Make().String()
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "UNSUPPORTED_SESSION_REFERENCE", "$.events[0].session_id")
	})

	t.Run("accepts valid issue hierarchy", func(t *testing.T) {
		payload := buildLogicalProjectDocument(nil)
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects child issues that are not task or bug", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issues := document["issues"].([]any)
			issue := issues[1].(map[string]any)
			issue["type"] = "epic"
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INVALID_PARENT", "$.issues[1].parent_id")
	})

	t.Run("rejects parent references to non-epic issues", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issues := document["issues"].([]any)
			issue := issues[1].(map[string]any)
			issue["parent_id"] = issue["id"]
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INVALID_PARENT", "$.issues[1].parent_id")
	})

	t.Run("rejects epic parent references", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issues := document["issues"].([]any)
			issue := issues[0].(map[string]any)
			issue["parent_id"] = issues[1].(map[string]any)["id"]
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INVALID_PARENT", "$.issues[0].parent_id")
	})

	t.Run("accepts unique label names", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			labels := document["labels"].([]any)
			labels = append(labels, map[string]any{
				"id":          ulid.Make().String(),
				"name":        "Beta",
				"description": nil,
				"created_at":  "2026-07-17T18:24:06Z",
			})
			document["labels"] = labels
		})
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects duplicate label names case-insensitively", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			labels := document["labels"].([]any)
			labels = append(labels, map[string]any{
				"id":          ulid.Make().String(),
				"name":        "alpha",
				"description": nil,
				"created_at":  "2026-07-17T18:24:06Z",
			})
			document["labels"] = labels
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "DUPLICATE_LABEL_NAME", "$.labels[1].name")
	})

	t.Run("accepts unique issue-label tuples", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issueLabels := document["issue_labels"].([]any)
			issues := document["issues"].([]any)
			labels := document["labels"].([]any)
			issueLabels = append(issueLabels, map[string]any{
				"issue_id": issues[1].(map[string]any)["id"],
				"label_id": labels[0].(map[string]any)["id"],
			})
			document["issue_labels"] = issueLabels
		})
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects duplicate issue-label tuples", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			issueLabels := document["issue_labels"].([]any)
			issues := document["issues"].([]any)
			labels := document["labels"].([]any)
			issueLabels = append(issueLabels, map[string]any{
				"issue_id": issues[1].(map[string]any)["id"],
				"label_id": labels[0].(map[string]any)["id"],
			})
			issueLabels = append(issueLabels, map[string]any{
				"issue_id": issues[1].(map[string]any)["id"],
				"label_id": labels[0].(map[string]any)["id"],
			})
			document["issue_labels"] = issueLabels
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "DUPLICATE_ISSUE_LABEL", "$.issue_labels[1]")
	})

	t.Run("accepts canonical relation ordering", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			relations := document["relations"].([]any)
			issues := document["issues"].([]any)
			relations = append(relations, map[string]any{
				"id":                    ulid.Make().String(),
				"source_issue_id":       issues[0].(map[string]any)["id"],
				"target_issue_id":       issues[1].(map[string]any)["id"],
				"type":                  "related_to",
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
			})
			document["relations"] = relations
		})
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects noncanonical related_to ordering", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			relations := document["relations"].([]any)
			issues := document["issues"].([]any)
			relations = append(relations, map[string]any{
				"id":                    ulid.Make().String(),
				"source_issue_id":       issues[1].(map[string]any)["id"],
				"target_issue_id":       issues[0].(map[string]any)["id"],
				"type":                  "related_to",
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
			})
			document["relations"] = relations
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "NONCANONICAL_RELATION", "$.relations[0].target_issue_id")
	})

	t.Run("rejects duplicate relation identities", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			relations := document["relations"].([]any)
			issues := document["issues"].([]any)
			relations = append(relations, map[string]any{
				"id":                    ulid.Make().String(),
				"source_issue_id":       issues[0].(map[string]any)["id"],
				"target_issue_id":       issues[1].(map[string]any)["id"],
				"type":                  "blocks",
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
			})
			relations = append(relations, map[string]any{
				"id":                    ulid.Make().String(),
				"source_issue_id":       issues[0].(map[string]any)["id"],
				"target_issue_id":       issues[1].(map[string]any)["id"],
				"type":                  "blocks",
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
			})
			document["relations"] = relations
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "DUPLICATE_RELATION", "$.relations[1]")
	})

	t.Run("accepts safe artifact URIs", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			artifacts := document["artifacts"].([]any)
			issues := document["issues"].([]any)
			artifacts = append(artifacts, map[string]any{
				"id":         ulid.Make().String(),
				"issue_id":   issues[1].(map[string]any)["id"],
				"attempt_id": nil,
				"type":       "file",
				"uri":        "docs/readme.md",
				"title":      nil,
				"metadata":   nil,
				"created_at": "2026-07-17T18:24:06Z",
			})
			document["artifacts"] = artifacts
		})
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
			t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
		}
	})

	t.Run("rejects traversing artifact paths", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			artifacts := document["artifacts"].([]any)
			issues := document["issues"].([]any)
			artifacts = append(artifacts, map[string]any{
				"id":         ulid.Make().String(),
				"issue_id":   issues[1].(map[string]any)["id"],
				"attempt_id": nil,
				"type":       "file",
				"uri":        "../outside.txt",
				"title":      nil,
				"metadata":   nil,
				"created_at": "2026-07-17T18:24:06Z",
			})
			document["artifacts"] = artifacts
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INVALID_PATH", "$.artifacts[0].uri")
	})

	t.Run("rejects credentialed artifact URLs", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			artifacts := document["artifacts"].([]any)
			issues := document["issues"].([]any)
			artifacts = append(artifacts, map[string]any{
				"id":         ulid.Make().String(),
				"issue_id":   issues[1].(map[string]any)["id"],
				"attempt_id": nil,
				"type":       "url",
				"uri":        "https://user@example.com/path",
				"title":      nil,
				"metadata":   nil,
				"created_at": "2026-07-17T18:24:06Z",
			})
			document["artifacts"] = artifacts
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INVALID_URL", "$.artifacts[0].uri")
	})
}

func buildLogicalProjectDocument(mutator func(map[string]any)) []byte {
	projectID := ulid.Make().String()
	epicID := ulid.Make().String()
	taskID := ulid.Make().String()
	document := map[string]any{
		"format":      "rhizome-logical-project",
		"version":     1,
		"exported_at": "2026-07-17T18:24:06Z",
		"project":     map[string]any{"id": projectID, "name": nil, "instructions": nil, "created_at": "2026-07-17T18:24:06Z", "updated_at": "2026-07-17T18:24:06Z"},
		"issues": []any{
			map[string]any{
				"id":                    epicID,
				"type":                  "epic",
				"title":                 "Epic",
				"description":           nil,
				"acceptance_criteria":   nil,
				"status":                "ready",
				"priority":              "high",
				"parent_id":             nil,
				"blocked_reason":        nil,
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
				"updated_at":            "2026-07-17T18:24:06Z",
				"closed_at":             nil,
			},
			map[string]any{
				"id":                    taskID,
				"type":                  "task",
				"title":                 "Task",
				"description":           nil,
				"acceptance_criteria":   nil,
				"status":                "ready",
				"priority":              "high",
				"parent_id":             epicID,
				"blocked_reason":        nil,
				"created_by_session_id": nil,
				"created_at":            "2026-07-17T18:24:06Z",
				"updated_at":            "2026-07-17T18:24:06Z",
				"closed_at":             nil,
			},
		},
		"labels": []any{
			map[string]any{
				"id":          ulid.Make().String(),
				"name":        "Alpha",
				"description": nil,
				"created_at":  "2026-07-17T18:24:06Z",
			},
		},
		"issue_labels":  []any{},
		"relations":     []any{},
		"comments":      []any{},
		"decisions":     []any{},
		"attempts":      []any{},
		"attempt_notes": []any{},
		"artifacts":     []any{},
		"events":        []any{map[string]any{"source_id": 1, "issue_id": epicID, "event_type": "created", "session_id": nil, "attempt_id": nil, "payload": map[string]any{"kind": "created"}, "created_at": "2026-07-17T18:24:06Z"}},
	}
	if mutator != nil {
		mutator(document)
	}
	payload, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return payload
}

func assertDetail(t *testing.T, err error, wantCode, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected parse error")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %T, want *domain.Error", err)
	}
	if domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("ParseLogicalProjectImportPlan() error code = %q, want %q", domainErr.Code, domain.CodeInvalidArgument)
	}
	if len(domainErr.Details) == 0 {
		t.Fatalf("ParseLogicalProjectImportPlan() error details = %v", domainErr.Details)
	}
	detail := domainErr.Details[0]
	if detail.Code != wantCode {
		t.Fatalf("ParseLogicalProjectImportPlan() error detail code = %q, want %q", detail.Code, wantCode)
	}
	if detail.Field != wantField {
		t.Fatalf("ParseLogicalProjectImportPlan() error detail field = %q, want %q", detail.Field, wantField)
	}
}
