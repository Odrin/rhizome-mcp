package domain_test

import (
	"encoding/json"
	"testing"

	"rhizome-mcp/internal/domain"
)

func importReferences() domain.LogicalEventPayloadReferences {
	return domain.LogicalEventPayloadReferences{
		Issues:         map[string]string{"01ARZ3NDEKTSV4RRFFQ69G5IA1": "01ARZ3NDEKTSV4RRFFQ69G5IB1"},
		Attempts:       map[string]string{"01ARZ3NDEKTSV4RRFFQ69G5IA2": "01ARZ3NDEKTSV4RRFFQ69G5IB2"},
		ReviewTargets:  map[string]string{"01ARZ3NDEKTSV4RRFFQ69G5IA3": "01ARZ3NDEKTSV4RRFFQ69G5IB3"},
		ReviewRequests: map[string]string{"01ARZ3NDEKTSV4RRFFQ69G5IA4": "01ARZ3NDEKTSV4RRFFQ69G5IB4"},
	}
}

// TestRemapLogicalEventPayloadCoversEveryReviewEventShape is ISSUE-232 AC1/AC4:
// every review event type the workflow writes carries request_id and target_id,
// and the transitions that have one carry attempt_id, so each shape must come
// out of an import naming destination rows.
func TestRemapLogicalEventPayloadCoversEveryReviewEventShape(t *testing.T) {
	for _, testCase := range []struct {
		eventType string
		payload   string
	}{
		{eventType: "review_requested", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3"}`},
		{eventType: "review_claimed", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3","attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2"}`},
		{eventType: "review_approved", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3","attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2","outcome":"approved"}`},
		{eventType: "review_changes_requested", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3","attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2","outcome":"changes_requested"}`},
		{eventType: "review_blocked", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3","attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2","outcome":"blocked","reason":"needs a migration"}`},
		{eventType: "review_cancelled", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3","attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2"}`},
		{eventType: "review_superseded", payload: `{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IA4","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3"}`},
	} {
		t.Run(testCase.eventType, func(t *testing.T) {
			remapped, err := domain.RemapLogicalEventPayload(testCase.eventType, json.RawMessage(testCase.payload), importReferences())
			if err != nil {
				t.Fatalf("RemapLogicalEventPayload() error = %v", err)
			}
			var before, after map[string]any
			if err := json.Unmarshal([]byte(testCase.payload), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(remapped, &after); err != nil {
				t.Fatalf("remapped payload is not a JSON object: %v", err)
			}
			if len(after) != len(before) {
				t.Fatalf("remapped payload = %s, want the same member set as %s", remapped, testCase.payload)
			}
			wantIDs := map[string]string{
				"request_id": "01ARZ3NDEKTSV4RRFFQ69G5IB4",
				"target_id":  "01ARZ3NDEKTSV4RRFFQ69G5IB3",
				"attempt_id": "01ARZ3NDEKTSV4RRFFQ69G5IB2",
			}
			for key, want := range wantIDs {
				if _, present := before[key]; !present {
					continue
				}
				if after[key] != want {
					t.Fatalf("remapped %s = %v, want the destination ID %s", key, after[key], want)
				}
			}
			// AC2: everything that is not an identity survives untouched.
			for key, value := range before {
				if _, isIdentity := wantIDs[key]; isIdentity {
					continue
				}
				if after[key] != value {
					t.Fatalf("remapped %s = %v, want the source value %v preserved", key, after[key], value)
				}
			}
		})
	}
}

// TestRemapLogicalEventPayloadLeavesNonReviewAndUnmappablePayloadsAlone pins
// the two deliberate non-rewrites: an ordinary event payload is a frozen audit
// fact, and a reference the document does not carry (a version 1 document has
// no review entities; a version 2 document excludes a claimed request while
// still exporting the events naming it) stays as history rather than failing
// the import.
func TestRemapLogicalEventPayloadLeavesNonReviewAndUnmappablePayloadsAlone(t *testing.T) {
	t.Run("an ordinary event payload is byte-identical", func(t *testing.T) {
		payload := json.RawMessage(`{"attempt_id":"01ARZ3NDEKTSV4RRFFQ69G5IA2","outcome":"completed"}`)
		remapped, err := domain.RemapLogicalEventPayload("attempt_completed", payload, importReferences())
		if err != nil {
			t.Fatalf("RemapLogicalEventPayload() error = %v", err)
		}
		if string(remapped) != string(payload) {
			t.Fatalf("remapped %s, want the payload carried verbatim", remapped)
		}
	})

	t.Run("an unmappable reference is kept", func(t *testing.T) {
		payload := json.RawMessage(`{"request_id":"01ARZ3NDEKTSV4RRFFQ69G5IZZ","target_id":"01ARZ3NDEKTSV4RRFFQ69G5IA3"}`)
		remapped, err := domain.RemapLogicalEventPayload("review_requested", payload, importReferences())
		if err != nil {
			t.Fatalf("RemapLogicalEventPayload() error = %v", err)
		}
		var after map[string]string
		if err := json.Unmarshal(remapped, &after); err != nil {
			t.Fatal(err)
		}
		if after["request_id"] != "01ARZ3NDEKTSV4RRFFQ69G5IZZ" {
			t.Fatalf("unmappable request_id = %q, want it kept as history", after["request_id"])
		}
		if after["target_id"] != "01ARZ3NDEKTSV4RRFFQ69G5IB3" {
			t.Fatalf("target_id = %q, want the resolvable reference still remapped", after["target_id"])
		}
	})

	t.Run("a review payload with nothing to remap is byte-identical", func(t *testing.T) {
		payload := json.RawMessage(`{"note":"hand-written"}`)
		remapped, err := domain.RemapLogicalEventPayload("review_requested", payload, importReferences())
		if err != nil {
			t.Fatalf("RemapLogicalEventPayload() error = %v", err)
		}
		if string(remapped) != string(payload) {
			t.Fatalf("remapped %s, want the payload carried verbatim", remapped)
		}
	})

	t.Run("a non-object review payload survives", func(t *testing.T) {
		payload := json.RawMessage(`[]`)
		remapped, err := domain.RemapLogicalEventPayload("review_requested", payload, importReferences())
		if err != nil {
			t.Fatalf("RemapLogicalEventPayload() error = %v", err)
		}
		if string(remapped) != string(payload) {
			t.Fatalf("remapped %s, want the payload carried verbatim", remapped)
		}
	})
}
