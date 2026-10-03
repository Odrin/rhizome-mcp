package domain

// PreparedOverlap caches the folded path and glob shape for repeated
// comparisons against one normalized resource.
type PreparedOverlap struct {
	resource NormalizedResource
	folded   []string
	shape    globShape
}

// PrepareOverlap builds a reusable comparison shape for resource.
func PrepareOverlap(resource NormalizedResource) PreparedOverlap {
	prepared := PreparedOverlap{resource: resource}
	if resource.kind != ResourceKindLogical {
		prepared.folded = foldedSegments(resource.segments)
		if resource.kind == ResourceKindGlob {
			prepared.shape = shapeOf(resource.segments)
		}
	}
	return prepared
}

// FirstLiteralSegment returns the path's first ASCII-folded segment when
// it is literal. Distinct first literals prove two path languages disjoint.
func (p PreparedOverlap) FirstLiteralSegment() (string, bool) {
	if len(p.resource.segments) == 0 || p.resource.segments[0].kind != globSegmentLiteral {
		return "", false
	}
	return p.folded[0], true
}

// Overlaps compares cached shapes under exactly the same rules as Overlaps.
func (p PreparedOverlap) Overlaps(other PreparedOverlap) bool {
	a, b := p.resource, other.resource
	if a.kind == ResourceKindLogical || b.kind == ResourceKindLogical {
		return a.kind == b.kind && a.namespace == b.namespace && a.name == b.name
	}
	if first, ok := p.FirstLiteralSegment(); ok {
		if second, ok := other.FirstLiteralSegment(); ok && first != second {
			return false
		}
	}
	switch {
	case a.kind == ResourceKindFile && b.kind == ResourceKindFile:
		return equalSegments(p.folded, other.folded)
	case a.kind == ResourceKindDirectory && b.kind == ResourceKindDirectory:
		return isPrefixOrEqual(p.folded, other.folded) || isPrefixOrEqual(other.folded, p.folded)
	case a.kind == ResourceKindDirectory && b.kind == ResourceKindFile:
		return isPrefixOrEqual(p.folded, other.folded)
	case a.kind == ResourceKindFile && b.kind == ResourceKindDirectory:
		return isPrefixOrEqual(other.folded, p.folded)
	case a.kind == ResourceKindGlob && b.kind == ResourceKindGlob:
		return p.shape.intersects(other.shape)
	case a.kind == ResourceKindFile && b.kind == ResourceKindGlob:
		return other.shape.matchesPath(p.folded)
	case a.kind == ResourceKindGlob && b.kind == ResourceKindFile:
		return p.shape.matchesPath(other.folded)
	case a.kind == ResourceKindDirectory && b.kind == ResourceKindGlob:
		return other.shape.matchesPath(p.folded) || other.shape.intersectsDescendantsOf(p.folded)
	case a.kind == ResourceKindGlob && b.kind == ResourceKindDirectory:
		return p.shape.matchesPath(other.folded) || p.shape.intersectsDescendantsOf(other.folded)
	default:
		return false
	}
}

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
	return PrepareOverlap(a).Overlaps(PrepareOverlap(b))
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
