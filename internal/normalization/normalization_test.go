package normalization_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/normalization"
)

func newCanonicalizer(t *testing.T, limits normalization.Limits) *normalization.Canonicalizer {
	t.Helper()
	canonicalizer, err := normalization.NewCanonicalizer(limits)
	if err != nil {
		t.Fatalf("NewCanonicalizer() error = %v", err)
	}
	return canonicalizer
}

func TestEncodeNormalizedJSONOrdersObjectsRecursivelyAndPreservesArrays(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	raw := []byte(`{"z":{"b":2,"a":1},"array":[{"d":4,"c":3},2,1],"a":true}`)
	got, err := canonicalizer.EncodeNormalizedJSON(raw)
	if err != nil {
		t.Fatalf("EncodeNormalizedJSON() error = %v", err)
	}
	const want = `{"a":true,"array":[{"c":3,"d":4},2,1],"z":{"a":1,"b":2}}`
	if string(got) != want {
		t.Fatalf("EncodeNormalizedJSON() = %s, want %s", got, want)
	}
}

func TestEncodeNormalizedSupportsDocumentedTypedValues(t *testing.T) {
	t.Parallel()
	type request struct {
		Name    string         `json:"name"`
		Count   uint64         `json:"count"`
		Precise json.Number    `json:"precise"`
		Labels  []string       `json:"labels"`
		Extra   map[string]any `json:"extra,omitempty"`
		Omitted string         `json:"omitted,omitempty"`
	}
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	got, err := canonicalizer.EncodeNormalized(request{
		Name: "already normalized", Count: math.MaxUint64, Precise: json.Number("9007199254740993.0"),
		Labels: []string{"second", "first"}, Extra: map[string]any{"z": nil, "a": -2},
	})
	if err != nil {
		t.Fatalf("EncodeNormalized() error = %v", err)
	}
	const want = `{"count":18446744073709551615,"extra":{"a":-2,"z":null},"labels":["second","first"],"name":"already normalized","precise":9007199254740993}`
	if string(got) != want {
		t.Fatalf("EncodeNormalized() = %s, want %s", got, want)
	}
}

func TestNumberCanonicalizationPreservesPrecisionAndNormalizesSpelling(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	tests := []struct {
		input string
		want  string
	}{
		{input: `9007199254740993123456789`, want: `9.007199254740993123456789e24`},
		{input: `1.2300`, want: `1.23`},
		{input: `123e-2`, want: `1.23`},
		{input: `1e+20`, want: `100000000000000000000`},
		{input: `1e21`, want: `1e21`},
		{input: `0.000001`, want: `0.000001`},
		{input: `0.0000001`, want: `1e-7`},
		{input: `-0.0e99`, want: `0`},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := canonicalizer.EncodeNormalizedJSON([]byte(test.input))
			if err != nil {
				t.Fatalf("EncodeNormalizedJSON() error = %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("EncodeNormalizedJSON(%s) = %s, want %s", test.input, got, test.want)
			}
		})
	}
}

func TestRawJSONRejectsDuplicatesAndTrailingDataButAcceptsArbitraryKeys(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	got, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"unknown":1,"another":2}`))
	if err != nil {
		t.Fatalf("schema-free keys should be accepted: %v", err)
	}
	if string(got) != `{"another":2,"unknown":1}` {
		t.Fatalf("arbitrary-key encoding = %s", got)
	}

	tests := []struct {
		name   string
		raw    string
		detail string
	}{
		{name: "duplicate", raw: `{"a":1,"\u0061":2}`, detail: "DUPLICATE_KEY"},
		{name: "trailing value", raw: `{} []`, detail: "TRAILING_DATA"},
		{name: "trailing token", raw: `{} x`, detail: "TRAILING_DATA"},
		{name: "empty", raw: ``, detail: "MALFORMED_JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := canonicalizer.EncodeNormalizedJSON([]byte(test.raw))
			assertDomainError(t, err, domain.CodeInvalidArgument, test.detail)
		})
	}
}

type customJSON struct{}

func (customJSON) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

func TestEncodeNormalizedRejectsUnsupportedAndInvalidValues(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	cycle := map[string]any{}
	cycle["self"] = cycle
	tests := []struct {
		name   string
		value  any
		detail string
	}{
		{name: "NaN", value: math.NaN(), detail: "INVALID_NUMBER"},
		{name: "positive infinity", value: math.Inf(1), detail: "INVALID_NUMBER"},
		{name: "negative infinity", value: math.Inf(-1), detail: "INVALID_NUMBER"},
		{name: "function", value: func() {}, detail: "UNSUPPORTED_VALUE"},
		{name: "complex", value: complex(1, 2), detail: "UNSUPPORTED_VALUE"},
		{name: "non-string map key", value: map[int]string{1: "x"}, detail: "UNSUPPORTED_VALUE"},
		{name: "byte slice", value: []byte("x"), detail: "UNSUPPORTED_VALUE"},
		{name: "custom marshaler", value: customJSON{}, detail: "UNSUPPORTED_VALUE"},
		{name: "cycle", value: cycle, detail: "UNSUPPORTED_VALUE"},
		{name: "invalid number", value: json.Number("01"), detail: "INVALID_NUMBER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := canonicalizer.EncodeNormalized(test.value)
			assertDomainError(t, err, domain.CodeInvalidArgument, test.detail)
		})
	}
}

func TestInvalidStringsAndKeysAreRejected(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name   string
		encode func() error
		detail string
	}{
		{name: "typed invalid UTF-8 value", encode: func() error { _, err := canonicalizer.EncodeNormalized(invalidUTF8); return err }, detail: "INVALID_UTF8"},
		{name: "typed NUL value", encode: func() error { _, err := canonicalizer.EncodeNormalized("a\x00b"); return err }, detail: "NUL_NOT_ALLOWED"},
		{name: "typed invalid UTF-8 key", encode: func() error { _, err := canonicalizer.EncodeNormalized(map[string]int{invalidUTF8: 1}); return err }, detail: "INVALID_UTF8"},
		{name: "typed NUL key", encode: func() error { _, err := canonicalizer.EncodeNormalized(map[string]int{"a\x00b": 1}); return err }, detail: "NUL_NOT_ALLOWED"},
		{name: "raw invalid UTF-8", encode: func() error { _, err := canonicalizer.EncodeNormalizedJSON([]byte{'"', 0xff, '"'}); return err }, detail: "INVALID_UTF8"},
		{name: "raw escaped NUL value", encode: func() error { _, err := canonicalizer.EncodeNormalizedJSON([]byte(`"a\u0000b"`)); return err }, detail: "NUL_NOT_ALLOWED"},
		{name: "raw escaped NUL key", encode: func() error { _, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"a\u0000b":1}`)); return err }, detail: "NUL_NOT_ALLOWED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertDomainError(t, test.encode(), domain.CodeInvalidArgument, test.detail)
		})
	}
}

func TestEveryConfiguredBound(t *testing.T) {
	t.Parallel()
	defaults := normalization.DefaultLimits()
	tests := []struct {
		name   string
		limits normalization.Limits
		raw    string
		detail string
	}{
		{name: "input bytes", limits: withLimit(defaults, "input", 1), raw: `{}`, detail: "MAX_INPUT_BYTES"},
		{name: "nesting depth", limits: withLimit(defaults, "depth", 1), raw: `{"a":[]}`, detail: "MAX_DEPTH"},
		{name: "object members", limits: withLimit(defaults, "object", 1), raw: `{"a":1,"b":2}`, detail: "MAX_OBJECT_MEMBERS"},
		{name: "array elements", limits: withLimit(defaults, "array", 1), raw: `[1,2]`, detail: "MAX_ARRAY_ELEMENTS"},
		{name: "output bytes", limits: withLimit(defaults, "output", 1), raw: `null`, detail: "MAX_OUTPUT_BYTES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonicalizer := newCanonicalizer(t, test.limits)
			_, err := canonicalizer.EncodeNormalizedJSON([]byte(test.raw))
			assertDomainError(t, err, domain.CodeLimitExceeded, test.detail)
		})
	}
	typedTests := []struct {
		name   string
		limits normalization.Limits
		value  any
		detail string
	}{
		{name: "typed nesting depth", limits: withLimit(defaults, "depth", 1), value: map[string]any{"a": []any{}}, detail: "MAX_DEPTH"},
		{name: "typed object members", limits: withLimit(defaults, "object", 1), value: map[string]int{"a": 1, "b": 2}, detail: "MAX_OBJECT_MEMBERS"},
		{name: "typed array elements", limits: withLimit(defaults, "array", 1), value: []int{1, 2}, detail: "MAX_ARRAY_ELEMENTS"},
		{name: "typed output bytes", limits: withLimit(defaults, "output", 1), value: "long", detail: "MAX_OUTPUT_BYTES"},
	}
	for _, test := range typedTests {
		t.Run(test.name, func(t *testing.T) {
			canonicalizer := newCanonicalizer(t, test.limits)
			_, err := canonicalizer.EncodeNormalized(test.value)
			assertDomainError(t, err, domain.CodeLimitExceeded, test.detail)
		})
	}

	zero := normalization.Limits{}
	canonicalizer := newCanonicalizer(t, zero)
	if got, err := canonicalizer.EncodeNormalizedJSON(nil); err == nil || got != nil {
		t.Fatalf("zero limits with empty input = %q, %v; want validation error", got, err)
	}
	negative := defaults
	negative.MaxDepth = -1
	if _, err := normalization.NewCanonicalizer(negative); err == nil {
		t.Fatal("NewCanonicalizer() accepted a negative bound")
	}
}

func TestLimitsAreInclusive(t *testing.T) {
	t.Parallel()
	limits := normalization.Limits{MaxInputBytes: 7, MaxDepth: 1, MaxObjectMembers: 1, MaxArrayElements: 1, MaxOutputBytes: 7}
	canonicalizer := newCanonicalizer(t, limits)
	got, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("exact bounds rejected: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("exact-bound output = %s", got)
	}
}

func TestHashesAreDeterministicAndContentSensitive(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	first, err := canonicalizer.HashNormalizedJSON([]byte(`{"b":2,"a":1.0}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalizer.HashNormalized(map[string]any{"a": json.Number("1e0"), "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent normalized values have different hashes: %x != %x", first, second)
	}
	if got := hex.EncodeToString(first[:]); got != "43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777" {
		t.Fatalf("hash = %s", got)
	}
	changed, err := canonicalizer.HashNormalizedJSON([]byte(`{"a":1,"b":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("hash did not change with canonical content")
	}
	encoded, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != sha256.Sum256(encoded) {
		t.Fatal("HashNormalizedJSON() is not SHA-256 of canonical bytes")
	}
}

func TestCanonicalizerConcurrentUse(t *testing.T) {
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	const goroutines = 64
	want := `{"a":[3,2,1],"z":true}`
	start := make(chan struct{})
	errorsChannel := make(chan error, goroutines)
	var group sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for iteration := 0; iteration < 100; iteration++ {
				got, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"z":true,"a":[3,2,1]}`))
				if err != nil {
					errorsChannel <- err
					return
				}
				if string(got) != want {
					errorsChannel <- errors.New("non-deterministic concurrent result")
					return
				}
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
}

func TestErrorsDoNotExposeInput(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())
	const secret = "full-sensitive-input"
	_, err := canonicalizer.EncodeNormalizedJSON([]byte(`{"` + secret + `":`))
	if err == nil {
		t.Fatal("malformed input was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed input: %v", err)
	}
}

func withLimit(limits normalization.Limits, field string, value int) normalization.Limits {
	switch field {
	case "input":
		limits.MaxInputBytes = value
	case "depth":
		limits.MaxDepth = value
	case "object":
		limits.MaxObjectMembers = value
	case "array":
		limits.MaxArrayElements = value
	case "output":
		limits.MaxOutputBytes = value
	}
	return limits
}

func assertDomainError(t *testing.T, err error, code, detailCode string) {
	t.Helper()
	if !errors.Is(err, &domain.Error{Code: code}) {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	if len(domainErr.Details) != 1 || domainErr.Details[0].Code != detailCode {
		t.Fatalf("error details = %+v, want %s", domainErr.Details, detailCode)
	}
}
