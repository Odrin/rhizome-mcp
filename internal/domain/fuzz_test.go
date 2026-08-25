package domain_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/domain"
)

func FuzzParseLogicalProjectImportPlan(f *testing.F) {
	// Add seed corpus entries
	seedDocs := [][]byte{
		// Empty and minimal cases
		[]byte(""),
		[]byte(" "),
		[]byte("{}"),
		[]byte("[]"),
		[]byte(`{"issues":[]}`),

		// Valid basic document
		buildLogicalProjectDocument(nil),

		// Large document that exercises MaxLogicalProjectImportBytes
		buildLargeLogicalProjectDocument(),
	}

	for _, doc := range seedDocs {
		f.Add(doc)
	}

	f.Fuzz(func(t *testing.T, document []byte) {
		plan, err := domain.ParseLogicalProjectImportPlan(document)

		// If there's an error, verify it's a domain error with valid codes
		if err != nil {
			// Every rejection must arrive as a structured domain error: this
			// parser sits behind an MCP tool, and a raw error here would reach
			// the client without a code or a retryable flag.
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("non-domain error %T from ParseLogicalProjectImportPlan: %v", err, err)
			}
			if domainErr.Message == "" {
				t.Fatalf("domain error with empty message: %#v", domainErr)
			}

			// Verify the error code is one of the documented codes this parser returns
			allowedCodes := map[string]bool{
				domain.CodeInvalidArgument:          true,
				domain.CodeLimitExceeded:            true,
				domain.CodeUnsupportedFormatVersion: true,
				domain.CodeUnsupportedField:         true,
			}
			if !allowedCodes[domainErr.Code] {
				t.Fatalf("unexpected domain error code: %q", domainErr.Code)
			}
			return
		}

		// No error - verify determinism: parse the same document again
		plan2, err2 := domain.ParseLogicalProjectImportPlan(document)
		if err2 != nil {
			t.Fatalf("second parse failed: %v", err2)
		}
		if !reflect.DeepEqual(plan, plan2) {
			t.Fatalf("parse result not deterministic")
		}

		// Verify all counts are non-negative
		if plan.DryRun.Counts.Issues < 0 || plan.DryRun.Counts.Labels < 0 ||
			plan.DryRun.Counts.Relations < 0 || plan.DryRun.Counts.Comments < 0 ||
			plan.DryRun.Counts.Decisions < 0 || plan.DryRun.Counts.Attempts < 0 ||
			plan.DryRun.Counts.AttemptNotes < 0 || plan.DryRun.Counts.Artifacts < 0 ||
			plan.DryRun.Counts.ReviewTargets < 0 || plan.DryRun.Counts.ReviewRequests < 0 ||
			plan.DryRun.Counts.ReviewOutcomes < 0 || plan.DryRun.Counts.Reservations < 0 {
			t.Fatalf("negative count in plan: %#v", plan.DryRun.Counts)
		}

		// Verify document entities don't exceed counts
		if len(plan.Document.Issues) > plan.DryRun.Counts.Issues+1000 ||
			len(plan.Document.Labels) > plan.DryRun.Counts.Labels+1000 ||
			len(plan.Document.Comments) > plan.DryRun.Counts.Comments+1000 {
			t.Fatalf("document entities exceed reasonable limits")
		}
	})
}

// buildLargeLogicalProjectDocument creates a document exceeding MaxLogicalProjectImportBytes
func buildLargeLogicalProjectDocument() []byte {
	projectID := ulid.Make().String()
	epicID := ulid.Make().String()

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
		},
		"labels":        []any{},
		"issue_labels":  []any{},
		"relations":     []any{},
		"comments":      []any{},
		"decisions":     []any{},
		"attempts":      []any{},
		"attempt_notes": []any{},
		"artifacts":     []any{},
		"events":        []any{},
	}

	// Add a large string to exceed MaxLogicalProjectImportBytes
	largeString := strings.Repeat("x", 2*domain.MaxLogicalProjectImportBytes)
	document["project"].(map[string]any)["instructions"] = largeString

	payload, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return payload
}
