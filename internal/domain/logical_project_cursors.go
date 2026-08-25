package domain

import "sort"

// LogicalEventCursorEntry pairs one exported event's historical source ID
// with the destination event ID its replay was assigned.
type LogicalEventCursorEntry struct {
	SourceID      int64
	DestinationID int64
}

// LogicalEventCursorMapping translates a durable cursor that names an
// issue_events position in the *source* project into the equivalent position
// in the destination project.
//
// Imported events receive fresh destination IDs, but every cursor that points
// into the event log -- a review target's latest_event_id, a review request's
// or approval's target_event_id, an attempt's context_event_id_at_start --
// used to be restored verbatim. A source project whose log had run past the
// destination's therefore left cursors above every ID the destination could
// ever assign, so the "did anything happen after this position" question
// every one of them exists to answer silently answered "no" forever
// (ISSUE-231).
//
// The mapping is a floor: a cursor becomes the highest destination ID among
// the events whose source ID is at or below it, and 0 when no exported event
// is. That is exactly the semantics the cursors carry -- everything at or
// before the cursor has been accounted for, everything after it has not -- so
// it holds for the cursors that name an event the document does not contain
// too. Documents are sparse by construction: archived issues and active
// attempts are excluded with their events (docs/07 §5), and a version 1
// document has no review entities at all, so a cursor beyond the exported log
// is ordinary rather than exceptional.
type LogicalEventCursorMapping struct {
	// sourceIDs is ascending; destinationCeilings[i] is the greatest
	// destination ID among sourceIDs[0..i], so a floor lookup is one binary
	// search. The running maximum matters because nothing requires the
	// destination to assign IDs in source-ID order: documents are ordered by
	// created_at first (docs/07 §2).
	sourceIDs           []int64
	destinationCeilings []int64
}

// NewLogicalEventCursorMapping builds the mapping from the source/destination
// ID pairs collected while replaying a document's events, in any order.
func NewLogicalEventCursorMapping(entries []LogicalEventCursorEntry) LogicalEventCursorMapping {
	ordered := make([]LogicalEventCursorEntry, len(entries))
	copy(ordered, entries)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].SourceID < ordered[right].SourceID })

	mapping := LogicalEventCursorMapping{
		sourceIDs:           make([]int64, len(ordered)),
		destinationCeilings: make([]int64, len(ordered)),
	}
	var ceiling int64
	for index, entry := range ordered {
		if entry.DestinationID > ceiling {
			ceiling = entry.DestinationID
		}
		mapping.sourceIDs[index] = entry.SourceID
		mapping.destinationCeilings[index] = ceiling
	}
	return mapping
}

// Remap returns the destination position equivalent to a source cursor. A
// cursor below every exported event -- including the 0 that means "nothing
// has happened yet" -- maps to 0, so any destination activity is correctly
// seen as happening after it.
func (mapping LogicalEventCursorMapping) Remap(cursor int64) int64 {
	index := sort.Search(len(mapping.sourceIDs), func(position int) bool {
		return mapping.sourceIDs[position] > cursor
	})
	if index == 0 {
		return 0
	}
	return mapping.destinationCeilings[index-1]
}
