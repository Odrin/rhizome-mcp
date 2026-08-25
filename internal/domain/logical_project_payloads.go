package domain

import "encoding/json"

// LogicalEventPayloadReferences carries the source-to-destination ID maps an
// imported event payload may need, one per entity kind a payload contract can
// name.
type LogicalEventPayloadReferences struct {
	Issues         map[string]string
	Attempts       map[string]string
	ReviewTargets  map[string]string
	ReviewRequests map[string]string
}

// logicalReviewEventPayloadKeys maps each key the review event payload
// contract defines (payloadForReviewEvent in the SQLite adapter writes them;
// every review_* event type carries request_id and target_id, and the rest
// appear as the transition provides them) to the map that resolves it. Keys
// outside this table -- outcome, reason, and anything a future transition
// adds -- are not identities and are carried through untouched.
func logicalReviewEventPayloadKeys(references LogicalEventPayloadReferences) map[string]map[string]string {
	return map[string]map[string]string{
		"request_id": references.ReviewRequests,
		"target_id":  references.ReviewTargets,
		"attempt_id": references.Attempts,
		"issue_id":   references.Issues,
	}
}

// RemapLogicalEventPayload rewrites the entity identities inside one imported
// event's payload so they name destination rows.
//
// Only review event payloads are rewritten, and deliberately so. A review
// event's payload is not opaque: it is the *only* place a review event's
// request and target are recorded (migration 008 folded review_events into
// issue_events, dropping those columns), so export promotes them back out of
// the payload into the document's typed review_events records, where they are
// checked for referential integrity like any other reference. Left verbatim
// through an import, they name source rows that no longer exist, and the
// destination's own next export is a document that no longer parses
// (ISSUE-232).
//
// Every other event payload stays byte-identical. Those are frozen audit
// facts naming the source project's rows, the same rule the gates namespace's
// snapshot blobs follow (docs/07 §4.1); nothing derives a reference field
// from them.
//
// A reference the document does not carry is left alone rather than treated
// as an error: a version 1 document has no review entities at all, and a
// version 2 document excludes a claimed request while still exporting the
// events that name it. Rewriting what can be resolved and leaving the rest as
// history is what keeps both importable.
func RemapLogicalEventPayload(eventType string, payload json.RawMessage, references LogicalEventPayloadReferences) (json.RawMessage, error) {
	if !ReviewEventType(eventType).Valid() {
		return payload, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		// Not a JSON object (payloads are only required to be valid JSON):
		// there is no member to rewrite, so it survives as it is.
		return payload, nil
	}
	rewritten := false
	for key, lookup := range logicalReviewEventPayloadKeys(references) {
		raw, present := fields[key]
		if !present || len(lookup) == 0 {
			continue
		}
		var sourceID string
		if err := json.Unmarshal(raw, &sourceID); err != nil {
			continue
		}
		destinationID, mapped := lookup[sourceID]
		if !mapped {
			continue
		}
		encoded, err := json.Marshal(destinationID)
		if err != nil {
			return nil, WrapError(err, CodeStorageFailure, "cannot encode remapped event payload identifier", false)
		}
		fields[key] = encoded
		rewritten = true
	}
	if !rewritten {
		return payload, nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, WrapError(err, CodeStorageFailure, "cannot encode remapped event payload", false)
	}
	return encoded, nil
}
