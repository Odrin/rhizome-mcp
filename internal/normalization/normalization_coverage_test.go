package normalization_test

import (
	"crypto/sha256"
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/normalization"
)

func TestEncodeNormalizedEscapesSpecialCharactersAndPreservesCanonicalOrder(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())

	input := map[string]any{
		"z": "slash\\quote\"backspace\bformfeed\fnewline\ncarriage\rreturn\tand\u0001",
		"a": []any{true, nil, map[string]any{"inner": "value"}},
	}

	got, err := canonicalizer.EncodeNormalized(input)
	if err != nil {
		t.Fatalf("EncodeNormalized() error = %v", err)
	}

	const want = `{"a":[true,null,{"inner":"value"}],"z":"slash\\quote\"backspace\bformfeed\fnewline\ncarriage\rreturn\tand\u0001"}`
	if string(got) != want {
		t.Fatalf("EncodeNormalized() = %s, want %s", got, want)
	}
}

func TestNilCanonicalizerMethodsRejectWithStableDomainErrorsAndZeroHashes(t *testing.T) {
	t.Parallel()
	var nilCanonicalizer *normalization.Canonicalizer

	t.Run("EncodeNormalized", func(t *testing.T) {
		_, err := nilCanonicalizer.EncodeNormalized("value")
		assertDomainError(t, err, domain.CodeInvalidArgument, "MISSING_CANONICALIZER")
	})

	t.Run("EncodeNormalizedJSON", func(t *testing.T) {
		_, err := nilCanonicalizer.EncodeNormalizedJSON([]byte(`{"ok":true}`))
		assertDomainError(t, err, domain.CodeInvalidArgument, "MISSING_CANONICALIZER")
	})

	t.Run("HashNormalized", func(t *testing.T) {
		got, err := nilCanonicalizer.HashNormalized("value")
		assertDomainError(t, err, domain.CodeInvalidArgument, "MISSING_CANONICALIZER")
		if got != ([sha256.Size]byte{}) {
			t.Fatalf("HashNormalized() = %x, want zero hash", got)
		}
	})

	t.Run("HashNormalizedJSON", func(t *testing.T) {
		got, err := nilCanonicalizer.HashNormalizedJSON([]byte(`{"ok":true}`))
		assertDomainError(t, err, domain.CodeInvalidArgument, "MISSING_CANONICALIZER")
		if got != ([sha256.Size]byte{}) {
			t.Fatalf("HashNormalizedJSON() = %x, want zero hash", got)
		}
	})
}

func TestEncodeNormalizedExercisesInterfacePointerNilAndArrayBranches(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())

	type container struct {
		Payload any `json:"payload"`
	}

	got, err := canonicalizer.EncodeNormalized(container{Payload: []any{(*int)(nil), nil, 7}})
	if err != nil {
		t.Fatalf("EncodeNormalized() error = %v", err)
	}

	const want = `{"payload":[null,null,7]}`
	if string(got) != want {
		t.Fatalf("EncodeNormalized() = %s, want %s", got, want)
	}
}

func TestEncodeNormalizedJSONRejectsMalformedObjectAndArrayClosers(t *testing.T) {
	t.Parallel()
	canonicalizer := newCanonicalizer(t, normalization.DefaultLimits())

	for _, raw := range []string{`{"a":1`, `[{"a":1}`} {
		_, err := canonicalizer.EncodeNormalizedJSON([]byte(raw))
		if err == nil {
			t.Fatalf("EncodeNormalizedJSON(%q) accepted malformed input", raw)
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("EncodeNormalizedJSON(%q) error = %v, want *domain.Error", raw, err)
		}
		if domainErr.Code != domain.CodeInvalidArgument {
			t.Fatalf("EncodeNormalizedJSON(%q) code = %s, want %s", raw, domainErr.Code, domain.CodeInvalidArgument)
		}
		if len(domainErr.Details) != 1 || domainErr.Details[0].Code != "MALFORMED_JSON" {
			t.Fatalf("EncodeNormalizedJSON(%q) details = %+v, want MALFORMED_JSON", raw, domainErr.Details)
		}
	}
}
