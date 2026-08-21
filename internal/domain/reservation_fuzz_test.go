package domain_test

import (
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

// FuzzNormalizePathIdempotent proves that, for every path resource kind,
// re-normalizing an already-normalized Display() value always succeeds and
// reproduces the same Display() and Key() -- normalization has no second
// fixed point.
func FuzzNormalizePathIdempotent(f *testing.F) {
	for _, seed := range []string{
		"a/b/c", "./a", "a//b", "a/../b", "/abs", "C:/vol", "a\\b",
		"*", "**", "a/*/b", "a/**", "a/?/b", "a/[x]/b", "a*b",
		"", ".", "././", "a/./b/../c", strings.Repeat("x/", 300),
	} {
		f.Add(seed)
	}
	kinds := []domain.ResourceKind{domain.ResourceKindFile, domain.ResourceKindDirectory, domain.ResourceKindGlob}
	f.Fuzz(func(t *testing.T, path string) {
		for _, kind := range kinds {
			first, err := domain.Normalize(domain.Resource{Kind: kind, Path: path})
			if err != nil {
				continue // invalid input; nothing to prove about idempotence
			}
			second, err := domain.Normalize(domain.Resource{Kind: kind, Path: first.Display()})
			if err != nil {
				t.Fatalf("kind %s: re-normalizing valid Display() %q failed: %v", kind, first.Display(), err)
			}
			if second.Display() != first.Display() {
				t.Fatalf("kind %s: Display() not idempotent: %q -> %q -> %q", kind, path, first.Display(), second.Display())
			}
			if second.Key() != first.Key() {
				t.Fatalf("kind %s: Key() not idempotent: %q -> %q -> %q", kind, path, first.Key(), second.Key())
			}
		}
	})
}

// FuzzNormalizeLogicalIdempotent proves the same fixed-point property for
// logical resources: re-normalizing an already-normalized namespace/name
// pair always succeeds and reproduces the same Key().
func FuzzNormalizeLogicalIdempotent(f *testing.F) {
	seeds := []struct{ namespace, name string }{
		{"ns", "name"}, {"a.b-c", "  spaced  "}, {"ABC", "x"}, {"", ""}, {"1abc", "x"},
	}
	for _, seed := range seeds {
		f.Add(seed.namespace, seed.name)
	}
	f.Fuzz(func(t *testing.T, namespace, name string) {
		first, err := domain.Normalize(domain.Resource{Kind: domain.ResourceKindLogical, Namespace: namespace, Name: name})
		if err != nil {
			return
		}
		display := first.Display()
		splitAt := strings.Index(display, ":")
		if splitAt < 0 {
			t.Fatalf("Display() %q missing ':' separator", display)
		}
		second, err := domain.Normalize(domain.Resource{
			Kind: domain.ResourceKindLogical, Namespace: display[:splitAt], Name: display[splitAt+1:],
		})
		if err != nil {
			t.Fatalf("re-normalizing valid Display() %q failed: %v", display, err)
		}
		if second.Key() != first.Key() {
			t.Fatalf("Key() not idempotent: %q -> %q -> %q", namespace+":"+name, first.Key(), second.Key())
		}
	})
}

// FuzzOverlapsSymmetric proves Overlaps(a, b) == Overlaps(b, a) for every
// pair of successfully normalized resources, across every path-kind
// combination -- overlap must never depend on argument order.
func FuzzOverlapsSymmetric(f *testing.F) {
	seeds := []struct{ a, b string }{
		{"a/b", "a/b"}, {"a", "a/b"}, {"a/*", "a/b"}, {"a/**", "a/b/c"},
		{"a/*", "a/*/c"}, {"a/**", "b/**"}, {"a", "ab"}, {"a/**", "a"},
	}
	for _, seed := range seeds {
		f.Add(seed.a, seed.b)
	}
	kinds := []domain.ResourceKind{domain.ResourceKindFile, domain.ResourceKindDirectory, domain.ResourceKindGlob}
	f.Fuzz(func(t *testing.T, pathA, pathB string) {
		for _, kindA := range kinds {
			resourceA, err := domain.Normalize(domain.Resource{Kind: kindA, Path: pathA})
			if err != nil {
				continue
			}
			for _, kindB := range kinds {
				resourceB, err := domain.Normalize(domain.Resource{Kind: kindB, Path: pathB})
				if err != nil {
					continue
				}
				forward := domain.Overlaps(resourceA, resourceB)
				backward := domain.Overlaps(resourceB, resourceA)
				if forward != backward {
					t.Fatalf("Overlaps not symmetric: (%s %q, %s %q) = %v, reversed = %v",
						kindA, resourceA.Display(), kindB, resourceB.Display(), forward, backward)
				}
			}
		}
	})
}
