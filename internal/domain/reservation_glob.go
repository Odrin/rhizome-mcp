package domain

import "strings"

// globSegmentKind distinguishes the three segment forms the locked glob
// grammar permits: a literal, a whole-segment "*" (matches exactly one
// segment), and a terminal whole-segment "**" (matches zero or more
// trailing segments; may appear only once, only last).
type globSegmentKind int

const (
	globSegmentLiteral globSegmentKind = iota
	globSegmentStar
	globSegmentStarStar
)

// globSegment is one normalized path or glob segment. For a literal
// segment, text is the caller-spelling value; for star/starstar it is
// always "*" / "**".
type globSegment struct {
	kind globSegmentKind
	text string
}

func (segment globSegment) foldedText() string {
	if segment.kind != globSegmentLiteral {
		return segment.text
	}
	return asciiFold(segment.text)
}

// asciiFold folds A-Z to a-z byte-wise; every other byte, including any
// non-ASCII UTF-8 continuation byte, passes through unchanged. This is the
// locked comparison-key rule: conservative case-folding for ASCII only.
func asciiFold(value string) string {
	hasUpper := false
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char >= 'A' && char <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return value
	}
	folded := []byte(value)
	for index, char := range folded {
		if char >= 'A' && char <= 'Z' {
			folded[index] = char + ('a' - 'A')
		}
	}
	return string(folded)
}

// classifyGlobSegment validates one non-empty, non-".", non-".." path
// segment and classifies it. For file and directory kinds, "*" and "**"
// are ordinary (if unusual) literal characters -- only glob kind treats
// them as wildcards, and only glob kind forbids the other metacharacters.
func classifyGlobSegment(text string, kind ResourceKind) (globSegment, error) {
	if kind != ResourceKindGlob {
		return globSegment{kind: globSegmentLiteral, text: text}, nil
	}
	switch text {
	case "*":
		return globSegment{kind: globSegmentStar, text: "*"}, nil
	case "**":
		return globSegment{kind: globSegmentStarStar, text: "**"}, nil
	}
	if strings.ContainsAny(text, "*?[]{}\\") {
		return globSegment{}, validationError("path", "INVALID_GLOB_SEGMENT",
			"glob segments must be a literal, '*', or a terminal '**'; "+
				"'?', character classes, braces, and embedded wildcards are not allowed")
	}
	return globSegment{kind: globSegmentLiteral, text: text}, nil
}

// validateStarStarPlacement enforces that "**" appears at most once and,
// when present, only as the final segment. Only meaningful for glob kind;
// file and directory segments are never classified as star/starstar.
func validateStarStarPlacement(segments []globSegment, kind ResourceKind) error {
	if kind != ResourceKindGlob {
		return nil
	}
	for index, segment := range segments {
		if segment.kind == globSegmentStarStar && index != len(segments)-1 {
			return validationError("path", "STARSTAR_MUST_BE_LAST",
				"'**' may appear only once, as the final segment")
		}
	}
	return nil
}

// globShape is the folded-segment view of a normalized glob or path used
// by matching and intersection: a fixed-length prefix (literals and "*"),
// optionally followed by a trailing "**".
type globShape struct {
	prefix      []string // folded text for literals; "*" marks a wildcard position
	starIsGlob  []bool   // parallel to prefix: true when the position is "*"
	hasStarStar bool
}

func shapeOf(segments []globSegment) globShape {
	shape := globShape{}
	for _, segment := range segments {
		switch segment.kind {
		case globSegmentStarStar:
			shape.hasStarStar = true
		case globSegmentStar:
			shape.prefix = append(shape.prefix, "*")
			shape.starIsGlob = append(shape.starIsGlob, true)
		default:
			shape.prefix = append(shape.prefix, asciiFold(segment.text))
			shape.starIsGlob = append(shape.starIsGlob, false)
		}
	}
	return shape
}

// matchesPath reports whether glob shape g matches the exact folded
// segment sequence candidate (also a plain literal path's shape, i.e. no
// wildcards and hasStarStar=false).
func (g globShape) matchesPath(candidate []string) bool {
	if g.hasStarStar {
		if len(candidate) < len(g.prefix) {
			return false
		}
	} else if len(candidate) != len(g.prefix) {
		return false
	}
	for index, want := range g.prefix {
		if !g.starIsGlob[index] && want != candidate[index] {
			return false
		}
	}
	return true
}

// intersects reports whether the pattern languages of g and other share at
// least one path, using an exact finite segment-state comparison: the
// restricted grammar (one optional trailing "**", otherwise fixed-length
// literal/"*" segments) makes this decidable by comparing the shared
// prefix and then checking whether the shorter, non-extending side can
// still reach the longer side's length.
func (g globShape) intersects(other globShape) bool {
	shared := len(g.prefix)
	if len(other.prefix) < shared {
		shared = len(other.prefix)
	}
	for index := 0; index < shared; index++ {
		gWild := g.starIsGlob[index]
		oWild := other.starIsGlob[index]
		if !gWild && !oWild && g.prefix[index] != other.prefix[index] {
			return false
		}
	}
	if len(g.prefix) == len(other.prefix) {
		return true
	}
	if len(g.prefix) < len(other.prefix) {
		return g.hasStarStar
	}
	return other.hasStarStar
}

// intersectsDescendantsOf reports whether g matches at least one path that
// is a strict descendant of the literal directory segments dir (i.e. some
// path strictly longer than dir that has dir as a prefix). dir is always a
// plain literal path (a directory has no wildcards), so this only needs
// the plain-shape comparison, extended to require length > len(dir).
func (g globShape) intersectsDescendantsOf(dir []string) bool {
	shared := len(g.prefix)
	if len(dir) < shared {
		shared = len(dir)
	}
	for index := 0; index < shared; index++ {
		if !g.starIsGlob[index] && g.prefix[index] != dir[index] {
			return false
		}
	}
	switch {
	case len(g.prefix) > len(dir):
		// g's own fixed prefix already extends past dir: any witness of
		// exactly len(g.prefix) segments is a strict descendant of dir and
		// satisfies g's fixed prefix by construction.
		return true
	case len(g.prefix) == len(dir):
		// g needs its own "**" to reach any length beyond dir's.
		return g.hasStarStar
	default:
		// g's fixed prefix is shorter than dir; only "**" can reach dir's
		// length, let alone beyond it.
		return g.hasStarStar
	}
}
