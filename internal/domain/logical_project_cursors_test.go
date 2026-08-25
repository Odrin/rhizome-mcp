package domain_test

import (
	"testing"

	"rhizome-mcp/internal/domain"
)

// TestLogicalEventCursorMappingFloorsToTheDestinationLog covers ISSUE-231
// AC1/AC4: a cursor becomes the highest destination position at or below it,
// including for the sparse and high source IDs a real document carries
// (archived issues and active attempts are exported without their events, and
// a source log routinely runs far past a fresh destination's).
func TestLogicalEventCursorMappingFloorsToTheDestinationLog(t *testing.T) {
	mapping := domain.NewLogicalEventCursorMapping([]domain.LogicalEventCursorEntry{
		{SourceID: 900, DestinationID: 1},
		{SourceID: 1400, DestinationID: 2},
		{SourceID: 4100, DestinationID: 3},
	})

	for _, testCase := range []struct {
		name   string
		cursor int64
		want   int64
	}{
		{name: "nothing has happened yet", cursor: 0, want: 0},
		{name: "below every exported event", cursor: 899, want: 0},
		{name: "exactly the first event", cursor: 900, want: 1},
		{name: "between two exported events", cursor: 1399, want: 1},
		{name: "exactly the last event", cursor: 4100, want: 3},
		{name: "past the whole source log", cursor: 99999, want: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := mapping.Remap(testCase.cursor); got != testCase.want {
				t.Fatalf("Remap(%d) = %d, want %d", testCase.cursor, got, testCase.want)
			}
		})
	}
}

// TestLogicalEventCursorMappingWithoutEventsCollapsesToZero is the empty-log
// case: with nothing imported, no cursor can name a position that has already
// been accounted for, so every one of them must admit any later destination
// activity.
func TestLogicalEventCursorMappingWithoutEventsCollapsesToZero(t *testing.T) {
	mapping := domain.NewLogicalEventCursorMapping(nil)
	for _, cursor := range []int64{0, 1, 7, 900000} {
		if got := mapping.Remap(cursor); got != 0 {
			t.Fatalf("Remap(%d) on an empty mapping = %d, want 0", cursor, got)
		}
	}
}

// TestLogicalEventCursorMappingIsMonotonicAcrossUnorderedEntries: documents
// order events by created_at first (docs/07 §2), so the replay order -- and
// with it the destination IDs -- need not follow source-ID order. The mapping
// must still never hand back a position below an event that is at or before
// the cursor.
func TestLogicalEventCursorMappingIsMonotonicAcrossUnorderedEntries(t *testing.T) {
	mapping := domain.NewLogicalEventCursorMapping([]domain.LogicalEventCursorEntry{
		{SourceID: 50, DestinationID: 3},
		{SourceID: 10, DestinationID: 1},
		{SourceID: 30, DestinationID: 2},
	})
	if got := mapping.Remap(50); got != 3 {
		t.Fatalf("Remap(50) = %d, want 3", got)
	}
	// Source 30 replayed second (destination 2) while source 50 replayed
	// first (destination 3): a cursor at 30 must still cover destination 2.
	if got := mapping.Remap(30); got != 2 {
		t.Fatalf("Remap(30) = %d, want 2", got)
	}
	if got := mapping.Remap(9); got != 0 {
		t.Fatalf("Remap(9) = %d, want 0", got)
	}
	var previous int64
	for cursor := int64(0); cursor <= 60; cursor++ {
		got := mapping.Remap(cursor)
		if got < previous {
			t.Fatalf("Remap(%d) = %d dropped below the previous %d; the mapping must be non-decreasing", cursor, got, previous)
		}
		previous = got
	}
}
