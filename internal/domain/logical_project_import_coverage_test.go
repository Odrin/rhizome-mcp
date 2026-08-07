package domain_test

import (
	"testing"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/domain"
)

func TestParseLogicalProjectImportPlanCoverageValidationPaths(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantCode  string
		wantField string
	}{
		{
			name: "rejects unsupported top-level fields",
			mutate: func(document map[string]any) {
				document["unexpected"] = true
			},
			wantCode:  "UNSUPPORTED_FIELD",
			wantField: "$.unexpected",
		},
		{
			name: "rejects blank nullable agent labels",
			mutate: func(document map[string]any) {
				attempts := []any{map[string]any{
					"id":                        ulid.Make().String(),
					"issue_id":                  document["issues"].([]any)[0].(map[string]any)["id"],
					"session_id":                nil,
					"agent_label":               " ",
					"kind":                      "work",
					"status":                    "completed",
					"issue_version_at_start":    1,
					"context_event_id_at_start": 0,
					"lease_expires_at":          "2026-07-17T18:24:06Z",
					"started_at":                "2026-07-17T18:24:06Z",
					"last_heartbeat_at":         "2026-07-17T18:24:06Z",
					"finished_at":               "2026-07-17T18:24:07Z",
					"result_summary":            nil,
					"next_steps":                []any{},
					"verification":              []any{},
					"failure_reason_code":       nil,
					"interruption_reason_code":  nil,
					"reason_details":            nil,
				}}
				document["attempts"] = attempts
			},
			wantCode:  "REQUIRED",
			wantField: "$.attempts[0].agent_label",
		},
		{
			name: "rejects decision supersedes references outside the same issue scope",
			mutate: func(document map[string]any) {
				issues := document["issues"].([]any)
				decisions := []any{
					map[string]any{
						"id":                    ulid.Make().String(),
						"issue_id":              issues[0].(map[string]any)["id"],
						"title":                 "Decision A",
						"summary":               "summary",
						"content":               "content",
						"status":                "active",
						"supersedes_id":         nil,
						"created_by_session_id": nil,
						"created_at":            "2026-07-17T18:24:06Z",
					},
					map[string]any{
						"id":                    ulid.Make().String(),
						"issue_id":              issues[1].(map[string]any)["id"],
						"title":                 "Decision B",
						"summary":               "summary",
						"content":               "content",
						"status":                "active",
						"supersedes_id":         nil,
						"created_by_session_id": nil,
						"created_at":            "2026-07-17T18:24:06Z",
					},
				}
				decisions[0].(map[string]any)["supersedes_id"] = decisions[1].(map[string]any)["id"]
				document["decisions"] = decisions
			},
			wantCode:  "INVALID_REFERENCE",
			wantField: "$.decisions[0].supersedes_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildLogicalProjectDocument(tc.mutate)
			_, err := domain.ParseLogicalProjectImportPlan(payload)
			if tc.wantCode == "UNSUPPORTED_FIELD" {
				assertDomainErrorDetail(t, err, domain.CodeUnsupportedField, tc.wantCode, tc.wantField)
				return
			}
			assertDetail(t, err, tc.wantCode, tc.wantField)
		})
	}
}

func TestParseLogicalProjectImportPlanCoverageReferenceBranches(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantField string
	}{
		{
			name: "rejects dangling issue references in comments",
			mutate: func(document map[string]any) {
				document["comments"] = []any{map[string]any{
					"id":                    ulid.Make().String(),
					"issue_id":              "not-a-ulid",
					"content":               "comment",
					"created_by_session_id": nil,
					"author_label":          nil,
					"created_at":            "2026-07-17T18:24:06Z",
					"edited_at":             nil,
				}}
			},
			wantField: "$.comments[0].issue_id",
		},
		{
			name: "rejects dangling attempt references in attempt notes",
			mutate: func(document map[string]any) {
				document["attempt_notes"] = []any{map[string]any{
					"id":         ulid.Make().String(),
					"attempt_id": "not-a-ulid",
					"kind":       "progress",
					"content":    "note",
					"next_steps": []any{},
					"important":  false,
					"created_at": "2026-07-17T18:24:06Z",
				}}
			},
			wantField: "$.attempt_notes[0].attempt_id",
		},
		{
			name: "rejects dangling attempt references in events",
			mutate: func(document map[string]any) {
				events := document["events"].([]any)
				event := events[0].(map[string]any)
				event["attempt_id"] = ulid.Make().String()
				event["issue_id"] = nil
				document["events"] = []any{event}
			},
			wantField: "$.events[0].attempt_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildLogicalProjectDocument(tc.mutate)
			_, err := domain.ParseLogicalProjectImportPlan(payload)
			assertDetail(t, err, "INVALID_REFERENCE", tc.wantField)
		})
	}
}

func TestParseLogicalProjectImportPlanCoverageRemainingPublicBranches(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		wantCode  string
		wantField string
	}{
		{
			name: "rejects non-UTC timestamps",
			payload: buildLogicalProjectDocument(func(document map[string]any) {
				document["exported_at"] = "2026-07-17T18:24:06+01:00"
			}),
			wantCode:  "INVALID_TIMESTAMP",
			wantField: "$.exported_at",
		},
		{
			name: "rejects malformed timestamps",
			payload: buildLogicalProjectDocument(func(document map[string]any) {
				document["project"].(map[string]any)["created_at"] = "not-a-time"
			}),
			wantCode:  "INVALID_TIMESTAMP",
			wantField: "$.project.created_at",
		},
		{
			name: "rejects artifact metadata that is not an object",
			payload: buildLogicalProjectDocument(func(document map[string]any) {
				artifacts := []any{map[string]any{
					"id":         ulid.Make().String(),
					"issue_id":   document["issues"].([]any)[0].(map[string]any)["id"],
					"attempt_id": nil,
					"type":       "file",
					"uri":        "docs/README.md",
					"title":      nil,
					"metadata":   []any{"not", "an", "object"},
					"created_at": "2026-07-17T18:24:06Z",
				}}
				document["artifacts"] = artifacts
			}),
			wantCode:  "INVALID_JSON_TYPE",
			wantField: "$.artifacts[0].metadata",
		},
		{
			name: "rejects blank nullable decision issue identifiers",
			payload: buildLogicalProjectDocument(func(document map[string]any) {
				document["decisions"] = []any{map[string]any{
					"id":                    ulid.Make().String(),
					"issue_id":              " ",
					"title":                 "Decision",
					"summary":               "summary",
					"content":               "content",
					"status":                "active",
					"supersedes_id":         nil,
					"created_by_session_id": nil,
					"created_at":            "2026-07-17T18:24:06Z",
				}}
			}),
			wantCode:  "REQUIRED",
			wantField: "$.decisions[0].issue_id",
		},
		{
			name: "rejects non-canonical ULIDs",
			payload: buildLogicalProjectDocument(func(document map[string]any) {
				document["issues"].([]any)[0].(map[string]any)["id"] = "01arzd214nq2j6m4y5v6x7z8"
			}),
			wantCode:  "INVALID_ULID",
			wantField: "$.issues[0].id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseLogicalProjectImportPlan(tc.payload)
			assertDetail(t, err, tc.wantCode, tc.wantField)
		})
	}
}

func TestParseLogicalProjectImportPlanCoverageAcceptsRichPayloadsAndMetadata(t *testing.T) {
	payload := buildLogicalProjectDocument(func(document map[string]any) {
		issues := document["issues"].([]any)
		issueID := issues[0].(map[string]any)["id"]
		document["artifacts"] = []any{map[string]any{
			"id":         ulid.Make().String(),
			"issue_id":   issueID,
			"attempt_id": nil,
			"type":       "file",
			"uri":        "docs/README.md",
			"title":      "Readme",
			"metadata": map[string]any{
				"source": "ci",
				"details": map[string]any{
					"flags": []any{map[string]any{"name": "review", "enabled": true}},
				},
			},
			"created_at": "2026-07-17T18:24:06Z",
		}}
		document["events"] = []any{map[string]any{
			"source_id":  1,
			"issue_id":   issueID,
			"event_type": "created",
			"session_id": nil,
			"attempt_id": nil,
			"payload": map[string]any{
				"kind":    "created",
				"details": []any{map[string]any{"name": "review", "ok": true}},
			},
			"created_at": "2026-07-17T18:24:06Z",
		}}
	})

	if _, err := domain.ParseLogicalProjectImportPlan(payload); err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
}

func TestLogicalProjectImportPlanAddDestinationConflictsSortsDeterministically(t *testing.T) {
	plan := domain.LogicalProjectImportPlan{}

	domain.AddDestinationConflicts(&plan, "Z", "zeta", "$.z")
	domain.AddDestinationConflicts(&plan, "A", "alpha", "$.a")
	domain.AddDestinationConflicts(&plan, "A", "alpha-2", "$.b")

	if len(plan.DryRun.Conflicts) != 3 {
		t.Fatalf("unexpected conflict count = %d", len(plan.DryRun.Conflicts))
	}

	got := make([]string, 0, len(plan.DryRun.Conflicts))
	for _, conflict := range plan.DryRun.Conflicts {
		got = append(got, conflict.Code+":"+conflict.Field)
	}

	want := []string{"A:$.a", "A:$.b", "Z:$.z"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conflict ordering[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
