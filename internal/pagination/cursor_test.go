package pagination_test

import (
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"rhizome-mcp/internal/pagination"
)

type testPayload struct {
	Sequence int    `json:"sequence"`
	ID       string `json:"id"`
}

func TestCodecDeterministicRoundTrip(t *testing.T) {
	codec := pagination.NewCodec[testPayload](0)
	want := testPayload{Sequence: 42, ID: "01J00000000000000000000000"}

	first, err := codec.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	second, err := codec.Encode(want)
	if err != nil {
		t.Fatalf("second Encode() error = %v", err)
	}
	if first != second {
		t.Fatalf("Encode() was not deterministic: %q != %q", first, second)
	}
	got, err := codec.Decode(first)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}
}

func TestCodecRejectsInvalidCursors(t *testing.T) {
	encodeRaw := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	codec := pagination.NewCodec[testPayload](128)

	tests := []struct {
		name   string
		cursor string
		want   error
	}{
		{name: "empty", cursor: "", want: pagination.ErrMalformedCursor},
		{name: "malformed base64", cursor: "%%%", want: pagination.ErrMalformedCursor},
		{name: "padded base64", cursor: "e30=", want: pagination.ErrMalformedCursor},
		{name: "malformed JSON", cursor: encodeRaw("{"), want: pagination.ErrMalformedCursor},
		{name: "trailing JSON", cursor: encodeRaw(`{"version":1,"payload":{"sequence":1,"id":"x"}} {}`), want: pagination.ErrMalformedCursor},
		{name: "unknown envelope field", cursor: encodeRaw(`{"version":1,"payload":{"sequence":1,"id":"x"},"extra":true}`), want: pagination.ErrMalformedCursor},
		{name: "unknown payload field", cursor: encodeRaw(`{"version":1,"payload":{"sequence":1,"id":"x","extra":true}}`), want: pagination.ErrMalformedCursor},
		{name: "unknown version", cursor: encodeRaw(`{"version":2,"payload":{"sequence":1,"id":"x"}}`), want: pagination.ErrUnsupportedVersion},
		{name: "oversized", cursor: string(make([]byte, 129)), want: pagination.ErrCursorTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codec.Decode(tt.cursor)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCodecRejectsOversizedEncoding(t *testing.T) {
	codec := pagination.NewCodec[testPayload](8)
	if _, err := codec.Encode(testPayload{ID: "too large"}); !errors.Is(err, pagination.ErrCursorTooLarge) {
		t.Fatalf("Encode() error = %v, want ErrCursorTooLarge", err)
	}
}
