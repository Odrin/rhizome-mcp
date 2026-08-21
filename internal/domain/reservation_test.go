package domain_test

import (
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestResourceKindValid(t *testing.T) {
	for _, kind := range []domain.ResourceKind{
		domain.ResourceKindFile, domain.ResourceKindDirectory,
		domain.ResourceKindGlob, domain.ResourceKindLogical,
	} {
		if !kind.Valid() {
			t.Fatalf("%q should be valid", kind)
		}
	}
	if domain.ResourceKind("symlink").Valid() {
		t.Fatal("unsupported kind should not be valid")
	}
}

func TestNormalizeRejectsUnsupportedKind(t *testing.T) {
	_, err := domain.Normalize(domain.Resource{Kind: "symlink", Path: "a"})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestNormalizePathAcceptsAndCanonicalizes(t *testing.T) {
	tests := []struct {
		name        string
		kind        domain.ResourceKind
		path        string
		wantDisplay string
		wantKey     string
	}{
		{"file simple", domain.ResourceKindFile, "a/b/c.go", "a/b/c.go", "file:a/b/c.go"},
		{"file case folds only in key", domain.ResourceKindFile, "A/B.txt", "A/B.txt", "file:a/b.txt"},
		{"file removes redundant segments", domain.ResourceKindFile, "a//b/./c", "a/b/c", "file:a/b/c"},
		{"file leading dot segment", domain.ResourceKindFile, "./a/b", "a/b", "file:a/b"},
		{"file non-ascii is byte-exact in key", domain.ResourceKindFile, "café/Ω", "café/Ω", "file:café/Ω"},
		{"directory simple", domain.ResourceKindDirectory, "src/pkg", "src/pkg", "directory:src/pkg"},
		{"glob star", domain.ResourceKindGlob, "src/*/pkg", "src/*/pkg", "glob:src/*/pkg"},
		{"glob starstar last", domain.ResourceKindGlob, "src/**", "src/**", "glob:src/**"},
		{"glob bare starstar", domain.ResourceKindGlob, "**", "**", "glob:**"},
		{"glob literal asterisk-free segment", domain.ResourceKindGlob, "a/b/c", "a/b/c", "glob:a/b/c"},
		{"file literal asterisk allowed", domain.ResourceKindFile, "a/*/b", "a/*/b", "file:a/*/b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource, err := domain.Normalize(domain.Resource{Kind: test.kind, Path: test.path})
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if resource.Display() != test.wantDisplay {
				t.Errorf("Display() = %q, want %q", resource.Display(), test.wantDisplay)
			}
			if resource.Key() != test.wantKey {
				t.Errorf("Key() = %q, want %q", resource.Key(), test.wantKey)
			}
			if resource.Kind() != test.kind {
				t.Errorf("Kind() = %q, want %q", resource.Kind(), test.kind)
			}
		})
	}
}

func TestNormalizePathRejectsForbiddenForms(t *testing.T) {
	tests := []struct {
		name     string
		kind     domain.ResourceKind
		path     string
		wantCode string
	}{
		{"empty", domain.ResourceKindFile, "", "EMPTY_ROOT_NOT_ALLOWED"},
		{"dot root", domain.ResourceKindFile, ".", "EMPTY_ROOT_NOT_ALLOWED"},
		{"all redundant segments", domain.ResourceKindFile, "././/", "EMPTY_ROOT_NOT_ALLOWED"},
		{"absolute", domain.ResourceKindFile, "/a/b", "ABSOLUTE_PATH_NOT_ALLOWED"},
		{"windows volume upper", domain.ResourceKindFile, "C:/a", "VOLUME_FORM_NOT_ALLOWED"},
		{"windows volume lower", domain.ResourceKindFile, "c:\\a", "BACKSLASH_NOT_ALLOWED"},
		{"backslash", domain.ResourceKindFile, "a\\b", "BACKSLASH_NOT_ALLOWED"},
		{"parent segment", domain.ResourceKindFile, "a/../b", "PARENT_SEGMENT_NOT_ALLOWED"},
		{"parent segment at start", domain.ResourceKindDirectory, "../a", "PARENT_SEGMENT_NOT_ALLOWED"},
		{"parent segment at end", domain.ResourceKindGlob, "a/..", "PARENT_SEGMENT_NOT_ALLOWED"},
		{"nul byte", domain.ResourceKindFile, "a/\x00/b", "NUL_NOT_ALLOWED"},
		{"glob question mark", domain.ResourceKindGlob, "a/?/b", "INVALID_GLOB_SEGMENT"},
		{"glob character class", domain.ResourceKindGlob, "a/[abc]", "INVALID_GLOB_SEGMENT"},
		{"glob brace", domain.ResourceKindGlob, "a/{b,c}", "INVALID_GLOB_SEGMENT"},
		{"glob embedded wildcard", domain.ResourceKindGlob, "a*b/c", "INVALID_GLOB_SEGMENT"},
		{"glob escape", domain.ResourceKindGlob, "a/\\*", "BACKSLASH_NOT_ALLOWED"},
		{"glob starstar not last", domain.ResourceKindGlob, "**/a", "STARSTAR_MUST_BE_LAST"},
		{"glob starstar in middle", domain.ResourceKindGlob, "a/**/b", "STARSTAR_MUST_BE_LAST"},
		{"glob double starstar", domain.ResourceKindGlob, "a/**/b/**", "STARSTAR_MUST_BE_LAST"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.Normalize(domain.Resource{Kind: test.kind, Path: test.path})
			assertDetailCode(t, err, test.wantCode)
		})
	}
}

func TestNormalizePathRejectsOverLimit(t *testing.T) {
	_, err := domain.Normalize(domain.Resource{
		Kind: domain.ResourceKindFile,
		Path: "a/" + strings.Repeat("b", domain.MaxResourcePathRunes+1),
	})
	assertDomainCode(t, err, domain.CodeLimitExceeded)
}

func TestNormalizeLogicalAcceptsAndTrims(t *testing.T) {
	resource, err := domain.Normalize(domain.Resource{
		Kind: domain.ResourceKindLogical, Namespace: "db.migration-1", Name: "  My Migration  ",
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if resource.Display() != "db.migration-1:My Migration" {
		t.Errorf("Display() = %q", resource.Display())
	}
	if resource.Key() != "db.migration-1:My Migration" {
		t.Errorf("Key() = %q, want namespace lowercase and name case-preserved", resource.Key())
	}
}

func TestNormalizeLogicalRejectsForbiddenForms(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		resName   string
		wantField string
		wantCode  string
	}{
		{"empty namespace", "", "n", "namespace", "INVALID_NAMESPACE"},
		{"uppercase namespace", "ABC", "n", "namespace", "INVALID_NAMESPACE"},
		{"namespace starts with digit", "1abc", "n", "namespace", "INVALID_NAMESPACE"},
		{"namespace invalid char", "ab_c", "n", "namespace", "INVALID_NAMESPACE"},
		{"namespace too long", strings.Repeat("a", 65), "n", "namespace", "MAX_RUNES"},
		{"empty name", "ns", "", "name", "REQUIRED"},
		{"blank name", "ns", "   ", "name", "REQUIRED"},
		{"name too long", "ns", strings.Repeat("a", 257), "name", "MAX_RUNES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.Normalize(domain.Resource{
				Kind: domain.ResourceKindLogical, Namespace: test.namespace, Name: test.resName,
			})
			assertDetailField(t, err, test.wantField, test.wantCode)
		})
	}
}

func mustNormalize(t *testing.T, kind domain.ResourceKind, path string) domain.NormalizedResource {
	t.Helper()
	resource, err := domain.Normalize(domain.Resource{Kind: kind, Path: path})
	if err != nil {
		t.Fatalf("Normalize(%s, %q) error = %v", kind, path, err)
	}
	return resource
}

func mustNormalizeLogical(t *testing.T, namespace, name string) domain.NormalizedResource {
	t.Helper()
	resource, err := domain.Normalize(domain.Resource{Kind: domain.ResourceKindLogical, Namespace: namespace, Name: name})
	if err != nil {
		t.Fatalf("Normalize(logical, %q, %q) error = %v", namespace, name, err)
	}
	return resource
}

func TestOverlaps(t *testing.T) {
	f := domain.ResourceKindFile
	d := domain.ResourceKindDirectory
	g := domain.ResourceKindGlob

	tests := []struct {
		name string
		a, b domain.NormalizedResource
		want bool
	}{
		{"equal files", mustNormalize(t, f, "a/b"), mustNormalize(t, f, "a/b"), true},
		{"equal files case-folded", mustNormalize(t, f, "A/B"), mustNormalize(t, f, "a/b"), true},
		{"different files", mustNormalize(t, f, "a/b"), mustNormalize(t, f, "a/c"), false},
		{"sibling segment is not a prefix", mustNormalize(t, f, "ab"), mustNormalize(t, f, "a"), false},

		{"equal directories", mustNormalize(t, d, "a"), mustNormalize(t, d, "a"), true},
		{"directory is self-conflicting", mustNormalize(t, d, "a/b"), mustNormalize(t, d, "a/b"), true},
		{"ancestor directory", mustNormalize(t, d, "a"), mustNormalize(t, d, "a/b"), true},
		{"descendant directory", mustNormalize(t, d, "a/b"), mustNormalize(t, d, "a"), true},
		{"unrelated directories", mustNormalize(t, d, "a"), mustNormalize(t, d, "b"), false},
		{"sibling directories are not prefixes", mustNormalize(t, d, "ab"), mustNormalize(t, d, "a"), false},

		{"directory equals file path", mustNormalize(t, d, "a"), mustNormalize(t, f, "a"), true},
		{"file equals directory path", mustNormalize(t, f, "a"), mustNormalize(t, d, "a"), true},
		{"file is descendant of directory", mustNormalize(t, d, "a"), mustNormalize(t, f, "a/b/c"), true},
		{"file is not a descendant (sibling)", mustNormalize(t, d, "a"), mustNormalize(t, f, "ab"), false},
		{"file outside directory", mustNormalize(t, d, "a"), mustNormalize(t, f, "b/c"), false},

		{"glob matches file", mustNormalize(t, g, "a/*"), mustNormalize(t, f, "a/b"), true},
		{"glob star does not cross segment boundary", mustNormalize(t, g, "a/*"), mustNormalize(t, f, "a/b/c"), false},
		{"glob starstar matches nested file", mustNormalize(t, g, "a/**"), mustNormalize(t, f, "a/b/c"), true},
		{"glob starstar matches itself", mustNormalize(t, g, "a/**"), mustNormalize(t, f, "a"), true},
		{"glob does not match unrelated file", mustNormalize(t, g, "a/*"), mustNormalize(t, f, "b/c"), false},

		{"glob matches directory itself", mustNormalize(t, g, "a/**"), mustNormalize(t, d, "a"), true},
		{"glob matches directory descendant via star", mustNormalize(t, g, "a/*"), mustNormalize(t, d, "a"), true},
		{"glob does not match unrelated directory", mustNormalize(t, g, "b/**"), mustNormalize(t, d, "a"), false},
		{"fixed-length glob does not reach beyond directory", mustNormalize(t, g, "a"), mustNormalize(t, d, "a/b"), false},

		{"identical globs", mustNormalize(t, g, "a/*/c"), mustNormalize(t, g, "a/*/c"), true},
		{"overlapping globs via star", mustNormalize(t, g, "a/*"), mustNormalize(t, g, "a/b"), true},
		{"disjoint fixed-length globs", mustNormalize(t, g, "a/*"), mustNormalize(t, g, "a/*/c"), false},
		{"disjoint literal prefixes", mustNormalize(t, g, "a/**"), mustNormalize(t, g, "b/**"), false},
		{"starstar absorbs longer literal prefix", mustNormalize(t, g, "a/**"), mustNormalize(t, g, "a/b/**"), true},
		{"both starstar different roots", mustNormalize(t, g, "a/**"), mustNormalize(t, g, "b/**"), false},

		{"logical equal", mustNormalizeLogical(t, "ns", "name"), mustNormalizeLogical(t, "ns", "name"), true},
		{"logical different namespace", mustNormalizeLogical(t, "ns1", "name"), mustNormalizeLogical(t, "ns2", "name"), false},
		{"logical different name", mustNormalizeLogical(t, "ns", "Name"), mustNormalizeLogical(t, "ns", "name"), false},
		{"logical case-sensitive name", mustNormalizeLogical(t, "ns", "Name"), mustNormalizeLogical(t, "ns", "name"), false},

		{"path never overlaps logical (file)", mustNormalize(t, f, "a"), mustNormalizeLogical(t, "a", "a"), false},
		{"path never overlaps logical (directory)", mustNormalize(t, d, "a"), mustNormalizeLogical(t, "a", "a"), false},
		{"path never overlaps logical (glob)", mustNormalize(t, g, "**"), mustNormalizeLogical(t, "a", "a"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := domain.Overlaps(test.a, test.b); got != test.want {
				t.Errorf("Overlaps(%s, %s) = %v, want %v", test.a.Key(), test.b.Key(), got, test.want)
			}
			if got := domain.Overlaps(test.b, test.a); got != test.want {
				t.Errorf("Overlaps(%s, %s) [reversed] = %v, want %v", test.b.Key(), test.a.Key(), got, test.want)
			}
		})
	}
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var domainErr *domain.Error
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	if domainErr.Code != code {
		t.Fatalf("code = %s, want %s", domainErr.Code, code)
	}
}

func assertDetailCode(t *testing.T, err error, code string) {
	t.Helper()
	var domainErr *domain.Error
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	for _, detail := range domainErr.Details {
		if detail.Code == code {
			return
		}
	}
	t.Fatalf("details = %+v, want a detail with code %s", domainErr.Details, code)
}

func assertDetailField(t *testing.T, err error, field, code string) {
	t.Helper()
	var domainErr *domain.Error
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	for _, detail := range domainErr.Details {
		if detail.Field == field && detail.Code == code {
			return
		}
	}
	t.Fatalf("details = %+v, want field %s code %s", domainErr.Details, field, code)
}
