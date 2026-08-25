package mcp_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	annotationMatrixDocPath    = "../../../docs/03-mcp-tools.md"
	annotationMatrixDocSection = "### 4.1. Annotation matrix"
)

var annotationMatrixRowPattern = regexp.MustCompile("^\\| `([a-z_]+)` \\|([^|]*)\\|([^|]*)\\|([^|]*)\\|([^|]*)\\|$")

// TestAnnotationMatrixDocMatchesExpectedHints fails when the documented
// annotation matrix in docs/03 section 4.1 drifts from expectedToolHints
// (annotations_test.go), which TestToolAnnotationMatrixMatchesCatalog in turn
// asserts against the live tools/list catalog. Together the two tests keep
// doc, in-code matrix, and runtime annotations transitively identical — the
// same drift-test pattern TestErrorCodesDocMatchesDomain applies to the
// error-code catalog in section 13.
func TestAnnotationMatrixDocMatchesExpectedHints(t *testing.T) {
	documented := documentedAnnotationMatrix(t)

	for name, want := range expectedToolHints {
		got, ok := documented[name]
		if !ok {
			t.Errorf("tool %q is registered but has no row in %s %s", name, annotationMatrixDocPath, annotationMatrixDocSection)
			continue
		}
		if got.ReadOnlyHint != want.ReadOnlyHint {
			t.Errorf("%s: documented readOnly = %v, want %v", name, got.ReadOnlyHint, want.ReadOnlyHint)
		}
		if !boolPointerEqual(got.DestructiveHint, want.DestructiveHint) {
			t.Errorf("%s: documented destructive = %v, want %v", name, derefBool(got.DestructiveHint), derefBool(want.DestructiveHint))
		}
		if got.IdempotentHint != want.IdempotentHint {
			t.Errorf("%s: documented idempotent = %v, want %v", name, got.IdempotentHint, want.IdempotentHint)
		}
		if !boolPointerEqual(got.OpenWorldHint, want.OpenWorldHint) {
			t.Errorf("%s: documented openWorld = %v, want %v", name, derefBool(got.OpenWorldHint), derefBool(want.OpenWorldHint))
		}
	}
	for name := range documented {
		if _, ok := expectedToolHints[name]; !ok {
			t.Errorf("%s %s documents tool %q but no such tool is registered", annotationMatrixDocPath, annotationMatrixDocSection, name)
		}
	}
}

func documentedAnnotationMatrix(t *testing.T) map[string]sdkmcp.ToolAnnotations {
	t.Helper()
	content, err := os.ReadFile(annotationMatrixDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", annotationMatrixDocPath, err)
	}
	_, section, found := strings.Cut(string(content), annotationMatrixDocSection)
	if !found {
		t.Fatalf("%s does not contain section %q", annotationMatrixDocPath, annotationMatrixDocSection)
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}

	matrix := make(map[string]sdkmcp.ToolAnnotations)
	for _, line := range strings.Split(section, "\n") {
		match := annotationMatrixRowPattern.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		name := match[1]
		if _, duplicate := matrix[name]; duplicate {
			t.Errorf("%s %s has a duplicate row for %q", annotationMatrixDocPath, annotationMatrixDocSection, name)
		}
		matrix[name] = sdkmcp.ToolAnnotations{
			ReadOnlyHint:    annotationCellChecked(t, name, "readOnly", match[2]),
			DestructiveHint: boolPointer(annotationCellChecked(t, name, "destructive", match[3])),
			IdempotentHint:  annotationCellChecked(t, name, "idempotent", match[4]),
			OpenWorldHint:   boolPointer(annotationCellChecked(t, name, "openWorld", match[5])),
		}
	}
	if len(matrix) == 0 {
		t.Fatalf("no annotation rows parsed from %s %s", annotationMatrixDocPath, annotationMatrixDocSection)
	}
	return matrix
}

func annotationCellChecked(t *testing.T, tool, column, cell string) bool {
	t.Helper()
	switch strings.TrimSpace(cell) {
	case "✓":
		return true
	case "":
		return false
	default:
		t.Fatalf("%s row %q column %s cell %q must be ✓ or blank", annotationMatrixDocSection, tool, column, cell)
		return false
	}
}
