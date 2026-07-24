package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestParseToolProfile(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  domain.ToolProfile
		code  string
	}{
		{name: "blank defaults to full", value: "", want: domain.ToolProfileFull},
		{name: "whitespace defaults to full", value: "   ", want: domain.ToolProfileFull},
		{name: "full", value: "full", want: domain.ToolProfileFull},
		{name: "agent", value: "agent", want: domain.ToolProfileAgent},
		{name: "read-only", value: "read-only", want: domain.ToolProfileReadOnly},
		{name: "migration", value: "migration", want: domain.ToolProfileMigration},
		{name: "unknown", value: "read-write", code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseToolProfile(tt.value)
			if tt.code != "" {
				if !errors.Is(err, &domain.Error{Code: tt.code}) {
					t.Fatalf("ParseToolProfile(%q) error = %v, want %s", tt.value, err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToolProfile(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("ParseToolProfile(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestToolProfileValid(t *testing.T) {
	if domain.ToolProfile("bogus").Valid() {
		t.Fatal("bogus profile reported valid")
	}
	for _, profile := range domain.AllToolProfiles {
		if !profile.Valid() {
			t.Fatalf("%q reported invalid", profile)
		}
	}
}
