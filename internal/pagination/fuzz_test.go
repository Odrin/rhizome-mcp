package pagination_test

import (
	"reflect"
	"strings"
	"testing"

	"rhizome-mcp/internal/pagination"
)

func FuzzCodecDecode(f *testing.F) {
	codec := pagination.NewCodec[testPayload](4096)

	// Add seed corpus entries
	seedCursors := []string{
		"",
		"!!!",
		strings.Repeat("A", 4096),
	}

	// Add existing test cursors
	testPayload1 := testPayload{Sequence: 42, ID: "01J00000000000000000000000"}
	validCursor, _ := codec.Encode(testPayload1)
	seedCursors = append(seedCursors, validCursor)

	// Add test cases from existing tests
	seedCursors = append(seedCursors, []string{
		"e30",
		"%%%",
		"e30=",
	}...)

	for _, seed := range seedCursors {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, cursor string) {
		payload, err := codec.Decode(cursor)

		// If there's an error, verify it's well-formed
		if err != nil {
			if err.Error() == "" {
				t.Fatalf("error with empty message: %v", err)
			}
			return
		}

		// No error - perform round-trip check
		reEncoded, encErr := codec.Encode(payload)
		if encErr != nil {
			t.Fatalf("re-encode failed: %v", encErr)
		}

		payload2, decErr := codec.Decode(reEncoded)
		if decErr != nil {
			t.Fatalf("re-decode failed: %v", decErr)
		}

		if !reflect.DeepEqual(payload, payload2) {
			t.Fatalf("round-trip mismatch: %#v != %#v", payload, payload2)
		}
	})
}
