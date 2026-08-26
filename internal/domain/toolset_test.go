package domain_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestParseToolsets(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []domain.Toolset
		errText string
	}{
		{name: "blank means unconfigured", value: "", want: nil},
		{name: "whitespace means unconfigured", value: "   ", want: nil},
		{name: "single group", value: "issues", want: []domain.Toolset{domain.ToolsetIssues}},
		{name: "several groups keep input order", value: "planning,issues", want: []domain.Toolset{domain.ToolsetPlanning, domain.ToolsetIssues}},
		{name: "entries are trimmed", value: " issues , review ", want: []domain.Toolset{domain.ToolsetIssues, domain.ToolsetReview}},
		{name: "core is accepted though redundant", value: "core,sync", want: []domain.Toolset{domain.ToolsetCore, domain.ToolsetSync}},
		{name: "every group parses", value: "core,issues,planning,review,knowledge,lifecycle,governance,migration,sync", want: domain.AllToolsets},
		{name: "unknown name", value: "issues,repos", errText: `unsupported toolset "repos"`},
		{name: "profile name is not a toolset", value: "agent", errText: `unsupported toolset "agent"`},
		{name: "duplicate", value: "issues,issues", errText: `listed more than once`},
		{name: "empty entry", value: "issues,,review", errText: "empty entry"},
		{name: "trailing comma", value: "issues,", errText: "empty entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseToolsets(tt.value)
			if tt.errText != "" {
				if err == nil {
					t.Fatalf("ParseToolsets(%q) = %v, want error containing %q", tt.value, got, tt.errText)
				}
				if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
					t.Fatalf("ParseToolsets(%q) error = %v, want %s", tt.value, err, domain.CodeInvalidArgument)
				}
				if !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("ParseToolsets(%q) error = %v, want it to contain %q", tt.value, err, tt.errText)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToolsets(%q) error = %v", tt.value, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseToolsets(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestParseToolsetsErrorNamesEveryValidToolset asserts the unsupported-name
// error is actionable: it lists the complete valid vocabulary.
func TestParseToolsetsErrorNamesEveryValidToolset(t *testing.T) {
	_, err := domain.ParseToolsets("repos")
	if err == nil {
		t.Fatal("ParseToolsets(\"repos\") unexpectedly succeeded")
	}
	for _, toolset := range domain.AllToolsets {
		if !strings.Contains(err.Error(), string(toolset)) {
			t.Errorf("error %v does not name valid toolset %q", err, toolset)
		}
	}
}
