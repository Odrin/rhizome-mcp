package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/domain"
)

// buildReservationDocument returns a document carrying one finished attempt
// and one reservation owned by it. mutateReservation may adjust the
// reservation record; mutateNamespace may adjust the namespace envelope
// (its version, or an extra key). Either may be nil.
func buildReservationDocument(mutateReservation, mutateNamespace func(map[string]any)) []byte {
	attemptID := ulid.Make().String()
	reservationID := ulid.Make().String()
	return buildLogicalProjectDocument(func(document map[string]any) {
		document["version"] = 2
		issueID := document["issues"].([]any)[0].(map[string]any)["id"]
		document["attempts"] = []any{map[string]any{
			"id":                        attemptID,
			"issue_id":                  issueID,
			"session_id":                nil,
			"agent_label":               nil,
			"kind":                      "work",
			"status":                    "completed",
			"issue_version_at_start":    1,
			"context_event_id_at_start": 0,
			"lease_expires_at":          "2026-07-17T18:24:07Z",
			"started_at":                "2026-07-17T18:24:07Z",
			"last_heartbeat_at":         "2026-07-17T18:24:07Z",
			"finished_at":               "2026-07-17T18:24:08Z",
			"result_summary":            nil,
			"next_steps":                []any{},
			"verification":              []any{},
			"failure_reason_code":       nil,
			"interruption_reason_code":  nil,
			"reason_details":            nil,
		}}
		reservation := map[string]any{
			"id":               reservationID,
			"issue_id":         issueID,
			"attempt_id":       attemptID,
			"kind":             "file",
			"display_value":    "src/main.go",
			"comparison_value": "src/main.go",
			"normalized_json":  map[string]any{"kind": "file", "segments": []any{"src", "main.go"}},
			"status":           "released",
			"created_at":       "2026-07-17T18:24:07Z",
			"released_at":      "2026-07-17T18:24:08Z",
			"release_reason":   "completed",
		}
		if mutateReservation != nil {
			mutateReservation(reservation)
		}
		namespace := map[string]any{"version": 1, "records": []any{reservation}}
		if mutateNamespace != nil {
			mutateNamespace(namespace)
		}
		document["extensions"] = map[string]any{"reservations": namespace}
	})
}

// TestLogicalProjectReservationsExtensionIsVersionGated pins that the
// reservations namespace is a version-2 addition only: v1's key table is
// frozen, so a v1 document cannot smuggle reservations in through
// extensions any more than it can through a new top-level array.
func TestLogicalProjectReservationsExtensionIsVersionGated(t *testing.T) {
	payload := buildLogicalProjectDocument(func(document map[string]any) {
		document["version"] = 1
		document["extensions"] = map[string]any{"reservations": map[string]any{"version": 1, "records": []any{}}}
	})
	_, err := domain.ParseLogicalProjectImportPlan(payload)
	assertDomainErrorDetail(t, err, domain.CodeUnsupportedField, domain.CodeUnsupportedField, "$.extensions")
}

func TestLogicalProjectReservationsExtensionRoundTripsThroughParse(t *testing.T) {
	plan, err := domain.ParseLogicalProjectImportPlan(buildReservationDocument(nil, nil))
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	if plan.DryRun.Counts.Reservations != 1 {
		t.Fatalf("dry run reservations count = %d, want 1", plan.DryRun.Counts.Reservations)
	}
	records, err := plan.Document.DecodeReservationsExtension()
	if err != nil {
		t.Fatalf("DecodeReservationsExtension() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Kind != "file" || record.DisplayValue != "src/main.go" || record.ComparisonValue != "src/main.go" {
		t.Fatalf("record = %#v", record)
	}
	if record.Status != "released" || record.ReleaseReason != "completed" || record.ReleasedAt == "" {
		t.Fatalf("record release state = %#v", record)
	}
	if !json.Valid(record.NormalizedJSON) {
		t.Fatalf("normalized_json = %s, want valid JSON", record.NormalizedJSON)
	}

	// Destination IDs must cover reservations, or the repository would
	// reject the plan as missing an identifier at insert time.
	destIDs, err := domain.NewLogicalProjectImportDestinationIDs(plan.Document, func() (string, error) {
		return ulid.Make().String(), nil
	})
	if err != nil {
		t.Fatalf("NewLogicalProjectImportDestinationIDs() error = %v", err)
	}
	destID, ok := destIDs.ReservationIDs[record.ID]
	if !ok || destID == record.ID {
		t.Fatalf("reservation destination ID = %q (present=%t), want a distinct minted ID", destID, ok)
	}
}

// TestLogicalProjectReservationsExtensionAbsentMeansEmpty pins the
// compatibility rule docs/07 §7 states: an omitted v2 section means
// "nothing to report," never "malformed."
func TestLogicalProjectReservationsExtensionAbsentMeansEmpty(t *testing.T) {
	payload := buildLogicalProjectDocument(func(document map[string]any) {
		document["version"] = 2
	})
	plan, err := domain.ParseLogicalProjectImportPlan(payload)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	if plan.DryRun.Counts.Reservations != 0 {
		t.Fatalf("reservations count = %d, want 0", plan.DryRun.Counts.Reservations)
	}
	records, err := plan.Document.DecodeReservationsExtension()
	if err != nil || records != nil {
		t.Fatalf("DecodeReservationsExtension() = %v, %v, want nil, nil", records, err)
	}
}

func TestLogicalProjectReservationsExtensionRejectsBadNamespaceEnvelope(t *testing.T) {
	t.Run("unsupported namespace version", func(t *testing.T) {
		payload := buildReservationDocument(nil, func(namespace map[string]any) {
			namespace["version"] = 2
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDomainErrorDetail(t, err, "UNSUPPORTED_FORMAT_VERSION", "UNSUPPORTED_FORMAT_VERSION", "$.extensions.reservations.version")
	})

	t.Run("unknown namespace key", func(t *testing.T) {
		payload := buildReservationDocument(nil, func(namespace map[string]any) {
			namespace["records_v2"] = []any{}
		})
		if _, err := domain.ParseLogicalProjectImportPlan(payload); err == nil {
			t.Fatal("expected a decode error for an unknown namespace key")
		}
	})
}

func TestLogicalProjectReservationsExtensionRejectsInconsistentRecords(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		code   string
		field  string
	}{
		{
			name:   "active reservation",
			mutate: func(r map[string]any) { r["status"] = "active"; r["released_at"] = nil; r["release_reason"] = nil },
			code:   "UNSUPPORTED_ACTIVE_RESERVATION",
			field:  "$.extensions.reservations.records[0].status",
		},
		{
			name:   "unknown status",
			mutate: func(r map[string]any) { r["status"] = "expired" },
			code:   "INVALID_ENUM",
			field:  "$.extensions.reservations.records[0].status",
		},
		{
			name:   "dangling attempt reference",
			mutate: func(r map[string]any) { r["attempt_id"] = ulid.Make().String() },
			code:   "INVALID_REFERENCE",
			field:  "$.extensions.reservations.records[0].attempt_id",
		},
		{
			name:   "normalized_json is not an object",
			mutate: func(r map[string]any) { r["normalized_json"] = "src/main.go" },
			code:   "INVALID_JSON",
			field:  "$.extensions.reservations.records[0].normalized_json",
		},
		{
			name:   "unknown resource kind",
			mutate: func(r map[string]any) { r["kind"] = "socket" },
			code:   "INVALID_ENUM",
			field:  "$.extensions.reservations.records[0].kind",
		},
		{
			name:   "blank display value",
			mutate: func(r map[string]any) { r["display_value"] = "" },
			code:   "REQUIRED",
			field:  "$.extensions.reservations.records[0].display_value",
		},
		{
			name:   "blank comparison value",
			mutate: func(r map[string]any) { r["comparison_value"] = "" },
			code:   "REQUIRED",
			field:  "$.extensions.reservations.records[0].comparison_value",
		},
		{
			name:   "released without a reason",
			mutate: func(r map[string]any) { r["release_reason"] = "" },
			code:   "REQUIRED",
			field:  "$.extensions.reservations.records[0].release_reason",
		},
		{
			name:   "unknown release reason",
			mutate: func(r map[string]any) { r["release_reason"] = "abandoned" },
			code:   "INVALID_ENUM",
			field:  "$.extensions.reservations.records[0].release_reason",
		},
		{
			name:   "released without a timestamp",
			mutate: func(r map[string]any) { r["released_at"] = "" },
			code:   "REQUIRED",
			field:  "$.extensions.reservations.records[0].released_at",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := domain.ParseLogicalProjectImportPlan(buildReservationDocument(testCase.mutate, nil))
			assertDetail(t, err, testCase.code, testCase.field)
		})
	}

	// A reservation naming a different issue than its own attempt does
	// describes a row the acquisition path could never have produced.
	t.Run("issue disagrees with the owning attempt", func(t *testing.T) {
		payload := buildLogicalProjectDocument(func(document map[string]any) {
			document["version"] = 2
			issues := document["issues"].([]any)
			attemptID := ulid.Make().String()
			document["attempts"] = []any{map[string]any{
				"id":                        attemptID,
				"issue_id":                  issues[0].(map[string]any)["id"],
				"session_id":                nil,
				"agent_label":               nil,
				"kind":                      "work",
				"status":                    "completed",
				"issue_version_at_start":    1,
				"context_event_id_at_start": 0,
				"lease_expires_at":          "2026-07-17T18:24:07Z",
				"started_at":                "2026-07-17T18:24:07Z",
				"last_heartbeat_at":         "2026-07-17T18:24:07Z",
				"finished_at":               "2026-07-17T18:24:08Z",
				"result_summary":            nil,
				"next_steps":                []any{},
				"verification":              []any{},
				"failure_reason_code":       nil,
				"interruption_reason_code":  nil,
				"reason_details":            nil,
			}}
			document["extensions"] = map[string]any{"reservations": map[string]any{
				"version": 1,
				"records": []any{map[string]any{
					"id":               ulid.Make().String(),
					"issue_id":         issues[1].(map[string]any)["id"],
					"attempt_id":       attemptID,
					"kind":             "file",
					"display_value":    "src/main.go",
					"comparison_value": "src/main.go",
					"normalized_json":  map[string]any{"kind": "file"},
					"status":           "released",
					"created_at":       "2026-07-17T18:24:07Z",
					"released_at":      "2026-07-17T18:24:08Z",
					"release_reason":   "completed",
				}},
			}}
		})
		_, err := domain.ParseLogicalProjectImportPlan(payload)
		assertDetail(t, err, "INCONSISTENT_REFERENCE", "$.extensions.reservations.records[0].issue_id")
	})
}
