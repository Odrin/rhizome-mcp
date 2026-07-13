package domain_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"rhizome-mcp/internal/domain"
)

func TestValidateTextBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
		code  string
	}{
		{name: "empty at zero", value: "", max: 0},
		{name: "ASCII exactly at limit", value: "abc", max: 3},
		{name: "multi-byte runes exactly at limit", value: "界🙂", max: 2},
		{name: "one rune over", value: "界🙂x", max: 2, code: domain.CodeLimitExceeded},
		{name: "NUL", value: "a\x00b", max: 3, code: domain.CodeInvalidArgument},
		{name: "invalid UTF-8", value: string([]byte{utf8.RuneSelf, 0xff}), max: 2, code: domain.CodeInvalidArgument},
		{name: "unlimited", value: "long", max: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.ValidateText("field", tt.value, tt.max)
			if tt.code == "" && err != nil {
				t.Fatalf("ValidateText() error = %v", err)
			}
			if tt.code != "" && !errors.Is(err, &domain.Error{Code: tt.code}) {
				t.Fatalf("ValidateText() error = %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestCopyBounded(t *testing.T) {
	input := []string{"a", "b"}
	got, err := domain.CopyBounded("labels", input, 2)
	if err != nil {
		t.Fatalf("CopyBounded() error = %v", err)
	}
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("CopyBounded() = %#v, want %#v", got, input)
	}
	got[0] = "changed"
	if input[0] != "a" {
		t.Fatal("CopyBounded() did not return a defensive copy")
	}

	if _, err := domain.CopyBounded("labels", input, 1); !errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
		t.Fatalf("over-limit error = %v, want LIMIT_EXCEEDED", err)
	}
	if _, err := domain.CopyBounded("labels", input, -1); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("negative-limit error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestErrorDetailsAreStableAndErrorsCompatible(t *testing.T) {
	indexZero, indexOne := 0, 1
	details := []domain.Detail{
		{EntityIndex: &indexOne, Field: "z", Code: "B", Message: "second"},
		{EntityIndex: &indexZero, Field: "z", Code: "B", Message: "first batch"},
		{Field: "z", Code: "B", Message: "global second"},
		{Field: "a", Code: "A", Message: "global first"},
	}
	cause := errors.New("private cause")
	err := domain.WrapError(cause, "TEST_CODE", "safe message", true, details...)

	wantFields := []string{"a", "z", "z", "z"}
	gotFields := make([]string, len(err.Details))
	for i, detail := range err.Details {
		gotFields[i] = detail.Field
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("detail field order = %#v, want %#v", gotFields, wantFields)
	}
	if err.Details[2].EntityIndex == nil || *err.Details[2].EntityIndex != 0 || err.Details[3].EntityIndex == nil || *err.Details[3].EntityIndex != 1 {
		t.Fatalf("batch detail order = %#v", err.Details)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped error does not match cause")
	}
	if !errors.Is(err, &domain.Error{Code: "TEST_CODE"}) {
		t.Fatal("domain error does not match stable code")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr != err {
		t.Fatal("errors.As did not recover domain error")
	}

	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("json.Marshal() error = %v", marshalErr)
	}
	const wantJSON = `{"code":"TEST_CODE","message":"safe message","details":[{"field":"a","code":"A","message":"global first"},{"field":"z","code":"B","message":"global second"},{"entity_index":0,"field":"z","code":"B","message":"first batch"},{"entity_index":1,"field":"z","code":"B","message":"second"}],"retryable":true}`
	if string(encoded) != wantJSON {
		t.Fatalf("JSON = %s, want %s", encoded, wantJSON)
	}
}
