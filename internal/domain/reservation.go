package domain

import "strings"

// ResourceKind identifies the kind of a reservable resource. See docs/02
// §18 for the full contract this file implements.
type ResourceKind string

const (
	ResourceKindFile      ResourceKind = "file"
	ResourceKindDirectory ResourceKind = "directory"
	ResourceKindGlob      ResourceKind = "glob"
	ResourceKindLogical   ResourceKind = "logical"
)

// Valid reports whether kind is one of the four supported resource kinds.
func (kind ResourceKind) Valid() bool {
	switch kind {
	case ResourceKindFile, ResourceKindDirectory, ResourceKindGlob, ResourceKindLogical:
		return true
	default:
		return false
	}
}

const (
	// MaxReservationResources bounds the resource count in one reservation
	// mutation.
	MaxReservationResources = 50
	// MaxResourcePathRunes bounds a file, directory, or glob path.
	MaxResourcePathRunes = 4096
	// MaxLogicalNamespaceRunes bounds a logical resource namespace
	// ([a-z][a-z0-9.-]{0,63}, so at most 64 runes total).
	MaxLogicalNamespaceRunes = 64
	// MaxLogicalNameRunes bounds a logical resource name.
	MaxLogicalNameRunes = 256
)

// Resource is one caller-supplied reservation target in display form,
// before normalization.
type Resource struct {
	Kind ResourceKind
	// Path is the file, directory, or glob path. Unused for logical.
	Path string
	// Namespace and Name identify a logical resource. Unused for path kinds.
	Namespace string
	Name      string
}

// NormalizedResource is a validated, canonically comparable resource. The
// zero value is not a valid resource; construct one with Normalize.
type NormalizedResource struct {
	kind ResourceKind
	// display preserves caller spelling: the redundant-segment-cleaned path
	// for path kinds, or "namespace:name" for logical.
	display string
	// segments is the case-folded comparison key, one entry per path
	// segment; nil for logical.
	segments []globSegment
	// namespace and name are the logical comparison key; unused for path
	// kinds. namespace is already lowercase by construction (the grammar
	// only accepts lowercase); name is case-sensitive and left as-is.
	namespace string
	name      string
}

// Kind returns the resource's kind.
func (r NormalizedResource) Kind() ResourceKind { return r.kind }

// Display returns the caller-spelling form: the path with redundant "."
// and empty segments removed (for file/directory/glob), or
// "namespace:name" (for logical).
func (r NormalizedResource) Display() string { return r.display }

// Key returns a stable, canonical string safe for storage, equality, and
// deduplication: the ASCII-folded path (for file/directory/glob) or
// "namespace:name" (for logical, name unfolded since it is case-sensitive).
func (r NormalizedResource) Key() string {
	switch r.kind {
	case ResourceKindLogical:
		return r.namespace + ":" + r.name
	default:
		parts := make([]string, len(r.segments))
		for index, segment := range r.segments {
			parts[index] = segment.foldedText()
		}
		return string(r.kind) + ":" + strings.Join(parts, "/")
	}
}

// Normalize validates and canonicalizes one resource. It rejects malformed
// input with a stable domain.Error and never silently repairs anything
// other than removing redundant "." and empty path segments.
func Normalize(input Resource) (NormalizedResource, error) {
	if !input.Kind.Valid() {
		return NormalizedResource{}, invalidEnum("kind", string(input.Kind))
	}
	if input.Kind == ResourceKindLogical {
		return normalizeLogical(input)
	}
	return normalizePath(input)
}

func normalizeLogical(input Resource) (NormalizedResource, error) {
	if err := ValidateText("namespace", input.Namespace, MaxLogicalNamespaceRunes); err != nil {
		return NormalizedResource{}, err
	}
	if !validLogicalNamespace(input.Namespace) {
		return NormalizedResource{}, validationError("namespace", "INVALID_NAMESPACE",
			"must match [a-z][a-z0-9.-]{0,63}")
	}
	name := strings.TrimSpace(input.Name)
	if err := ValidateText("name", name, MaxLogicalNameRunes); err != nil {
		return NormalizedResource{}, err
	}
	if name == "" {
		return NormalizedResource{}, validationError("name", "REQUIRED", "must not be blank")
	}
	return NormalizedResource{
		kind:      ResourceKindLogical,
		display:   input.Namespace + ":" + name,
		namespace: input.Namespace,
		name:      name,
	}, nil
}

func validLogicalNamespace(namespace string) bool {
	if namespace == "" {
		return false
	}
	for index := 0; index < len(namespace); index++ {
		char := namespace[index]
		switch {
		case index == 0:
			if char < 'a' || char > 'z' {
				return false
			}
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '-':
			// allowed
		default:
			return false
		}
	}
	return true
}

func normalizePath(input Resource) (NormalizedResource, error) {
	if err := ValidateText("path", input.Path, MaxResourcePathRunes); err != nil {
		return NormalizedResource{}, err
	}
	if strings.ContainsRune(input.Path, '\\') {
		return NormalizedResource{}, validationError("path", "BACKSLASH_NOT_ALLOWED",
			"must use forward slashes, not backslashes")
	}
	if strings.HasPrefix(input.Path, "/") {
		return NormalizedResource{}, validationError("path", "ABSOLUTE_PATH_NOT_ALLOWED",
			"must be project-relative, not absolute")
	}
	if isWindowsVolumeForm(input.Path) {
		return NormalizedResource{}, validationError("path", "VOLUME_FORM_NOT_ALLOWED",
			"must not use a Windows volume prefix")
	}

	raw := strings.Split(input.Path, "/")
	segments := make([]globSegment, 0, len(raw))
	for _, part := range raw {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return NormalizedResource{}, validationError("path", "PARENT_SEGMENT_NOT_ALLOWED",
				"must not contain a '..' segment")
		}
		segment, err := classifyGlobSegment(part, input.Kind)
		if err != nil {
			return NormalizedResource{}, err
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return NormalizedResource{}, validationError("path", "EMPTY_ROOT_NOT_ALLOWED",
			"must not be empty or resolve to '.'")
	}
	if err := validateStarStarPlacement(segments, input.Kind); err != nil {
		return NormalizedResource{}, err
	}

	display := make([]string, len(segments))
	for index, segment := range segments {
		display[index] = segment.text
	}
	return NormalizedResource{
		kind:     input.Kind,
		display:  strings.Join(display, "/"),
		segments: segments,
	}, nil
}

func isWindowsVolumeForm(path string) bool {
	// "C:" / "c:/..." / "C:\..." style volume prefixes.
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	char := path[0]
	return (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}
