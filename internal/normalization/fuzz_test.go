package normalization_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"rhizome-mcp/internal/normalization"
)

func FuzzCanonicalizer(f *testing.F) {
	c, err := normalization.NewCanonicalizer(normalization.DefaultLimits())
	if err != nil {
		f.Fatalf("NewCanonicalizer failed: %v", err)
	}

	// Add seed corpus from existing tests and patterns
	seedDocs := []string{
		// From existing tests
		`{"z":{"b":2,"a":1},"array":[{"d":4,"c":3},2,1],"a":true}`,
		`{"unknown":1,"another":2}`,
		`{"a":1,"b":2}`,
		`{"z":true,"a":[3,2,1]}`,
		`{"b":2,"a":1.0}`,
		`{"a":1,"b":3}`,
		`{"a":{"b":{"c":1}}}`,
		`null`,
		`""`,
		`0`,
		`[]`,
		`{}`,

		// Edge cases
		"",
		"{",
		"[",
		"[[[[[[[[[[]]]]]]]]]]",
		`{"a":{"b":{"c":{"d":{"e":1}}}}}`,

		// Document exceeding byte limit - use strings.Repeat to build it
		strings.Repeat(`{"x":"y"}`, 150000),
	}

	for _, doc := range seedDocs {
		f.Add([]byte(doc))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := c.EncodeNormalizedJSON(raw)

		// If there's an error, verify it's well-formed with a non-empty message
		if err != nil {
			if err.Error() == "" {
				t.Fatalf("error with empty message: %v", err)
			}
			return
		}

		// No error - verify idempotence
		out2, err2 := c.EncodeNormalizedJSON(out)
		if err2 != nil {
			t.Fatalf("idempotence check failed: %v", err2)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("idempotence violated: first=%s, second=%s", out, out2)
		}

		// Verify output is valid JSON
		if !json.Valid(out) {
			t.Fatalf("output is not valid JSON: %s", out)
		}

		// Verify hash consistency - same canonical form should have same hash
		hash1, err := c.HashNormalizedJSON(raw)
		if err != nil {
			t.Fatalf("HashNormalizedJSON failed on first call: %v", err)
		}
		hash2, err := c.HashNormalizedJSON(out)
		if err != nil {
			t.Fatalf("HashNormalizedJSON failed on second call: %v", err)
		}
		if hash1 != hash2 {
			t.Fatalf("hash mismatch for equivalent canonical forms: %x != %x", hash1, hash2)
		}
	})
}
