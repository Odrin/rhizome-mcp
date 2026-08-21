package domain

// Overlaps reports whether two normalized resources conflict under the
// locked overlap rules: equal files conflict; a directory conflicts with
// itself and with every file, directory, or glob whose language includes
// the directory path or a descendant of it; a file conflicts with a glob
// that matches it; two globs conflict iff their pattern languages
// intersect; a path resource and a logical resource never overlap; two
// logical resources conflict only on an exact normalized namespace and
// name. Overlap is symmetric and independent of input order and of
// anything on the filesystem -- this is a pure, lexical comparison over
// each resource's normalized comparison key.
func Overlaps(a, b NormalizedResource) bool {
	if a.kind == ResourceKindLogical || b.kind == ResourceKindLogical {
		if a.kind != b.kind {
			return false
		}
		return a.namespace == b.namespace && a.name == b.name
	}

	aFolded := foldedSegments(a.segments)
	bFolded := foldedSegments(b.segments)

	switch {
	case a.kind == ResourceKindFile && b.kind == ResourceKindFile:
		return equalSegments(aFolded, bFolded)

	case a.kind == ResourceKindDirectory && b.kind == ResourceKindDirectory:
		return isPrefixOrEqual(aFolded, bFolded) || isPrefixOrEqual(bFolded, aFolded)

	case a.kind == ResourceKindDirectory && b.kind == ResourceKindFile:
		return equalSegments(aFolded, bFolded) || isStrictPrefix(aFolded, bFolded)
	case a.kind == ResourceKindFile && b.kind == ResourceKindDirectory:
		return equalSegments(bFolded, aFolded) || isStrictPrefix(bFolded, aFolded)

	case a.kind == ResourceKindGlob && b.kind == ResourceKindGlob:
		return shapeOf(a.segments).intersects(shapeOf(b.segments))

	case a.kind == ResourceKindFile && b.kind == ResourceKindGlob:
		return shapeOf(b.segments).matchesPath(aFolded)
	case a.kind == ResourceKindGlob && b.kind == ResourceKindFile:
		return shapeOf(a.segments).matchesPath(bFolded)

	case a.kind == ResourceKindDirectory && b.kind == ResourceKindGlob:
		shape := shapeOf(b.segments)
		return shape.matchesPath(aFolded) || shape.intersectsDescendantsOf(aFolded)
	case a.kind == ResourceKindGlob && b.kind == ResourceKindDirectory:
		shape := shapeOf(a.segments)
		return shape.matchesPath(bFolded) || shape.intersectsDescendantsOf(bFolded)

	default:
		return false
	}
}

func foldedSegments(segments []globSegment) []string {
	folded := make([]string, len(segments))
	for index, segment := range segments {
		folded[index] = segment.foldedText()
	}
	return folded
}

func equalSegments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// isPrefixOrEqual reports whether prefix's segments equal or are a leading
// subsequence of candidate's segments.
func isPrefixOrEqual(prefix, candidate []string) bool {
	if len(prefix) > len(candidate) {
		return false
	}
	for index := range prefix {
		if prefix[index] != candidate[index] {
			return false
		}
	}
	return true
}

// isStrictPrefix reports whether prefix's segments are a strict leading
// subsequence of candidate's segments (candidate is a proper descendant).
func isStrictPrefix(prefix, candidate []string) bool {
	return len(candidate) > len(prefix) && isPrefixOrEqual(prefix, candidate)
}
