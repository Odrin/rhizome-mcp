package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestParseContentMode(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  domain.ContentMode
		code  string
	}{
		{name: "blank defaults to auto", value: "", want: domain.ContentModeAuto},
		{name: "whitespace defaults to auto", value: "   ", want: domain.ContentModeAuto},
		{name: "auto", value: "auto", want: domain.ContentModeAuto},
		{name: "structured", value: "structured", want: domain.ContentModeStructured},
		{name: "text", value: "text", want: domain.ContentModeText},
		{name: "unknown", value: "hybrid", code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseContentMode(tt.value)
			if tt.code != "" {
				if !errors.Is(err, &domain.Error{Code: tt.code}) {
					t.Fatalf("ParseContentMode(%q) error = %v, want %s", tt.value, err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseContentMode(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseContentMode(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestContentModeValid(t *testing.T) {
	if domain.ContentMode("bogus").Valid() {
		t.Fatal("bogus content mode reported valid")
	}
	for _, mode := range domain.AllContentModes {
		if !mode.Valid() {
			t.Fatalf("%q reported invalid", mode)
		}
	}
}
