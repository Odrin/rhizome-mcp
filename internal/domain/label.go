package domain

import (
	"sort"
	"strings"
	"time"
)

// Label is the persisted projection of a reusable issue label. NormalizedName
// is the trimmed name with ASCII A-Z folded to a-z. This deliberately matches
// SQLite NOCASE, which is ASCII-only and is not Unicode case folding.
type Label struct {
	ID             string
	Name           string
	NormalizedName string
	Description    *string
	CreatedAt      time.Time
}

// NormalizeLabelName validates a label name and returns its trimmed display
// representation and ASCII-NOCASE canonical representation. Leading and
// trailing Unicode whitespace is not persisted. Names that differ only by
// ASCII case are equivalent; non-ASCII case variants are deliberately not.
func NormalizeLabelName(name string) (displayName, normalizedName string, err error) {
	if err := ValidateText("labels", name, MaxLabelNameRunes); err != nil {
		return "", "", err
	}
	displayName = strings.TrimSpace(name)
	if displayName == "" {
		return "", "", validationError("labels", "REQUIRED", "must not contain blank names")
	}
	return displayName, asciiLower(displayName), nil
}

// NormalizeLabelNames validates label assignments, rejects canonical
// duplicates, and returns trimmed display names ordered by normalized name.
// It accepts an explicit empty list and never silently removes duplicates.
func NormalizeLabelNames(names []string) ([]string, error) {
	values, err := CopyBounded("labels", names, MaxLabelsPerIssue)
	if err != nil {
		return nil, err
	}
	type named struct {
		display    string
		normalized string
	}
	normalized := make([]named, 0, len(values))
	for _, name := range values {
		display, canonical, err := NormalizeLabelName(name)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, named{display: display, normalized: canonical})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].normalized < normalized[j].normalized
	})
	for i := 1; i < len(normalized); i++ {
		if normalized[i-1].normalized == normalized[i].normalized {
			return nil, validationError("labels", "DUPLICATE", "must not contain duplicate names")
		}
	}
	result := make([]string, len(normalized))
	for i, value := range normalized {
		result[i] = value.display
	}
	return result, nil
}

func asciiLower(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

// ListLabelsInput requests one deterministic page of labels. Query is an
// optional trimmed ASCII-NOCASE prefix of normalized names. Limit defaults to
// 50 and cannot exceed 100. Cursor is an opaque value returned by a prior
// page.
type ListLabelsInput struct {
	Query  string
	Limit  int
	Cursor string
}

// Validate validates list bounds and normalizes the optional query prefix.
func (input ListLabelsInput) Validate() (ListLabelsInput, error) {
	if input.Limit < 0 || input.Limit > 100 {
		return ListLabelsInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 100")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	if err := ValidateText("query", input.Query, MaxLabelNameRunes); err != nil {
		return ListLabelsInput{}, err
	}
	return ListLabelsInput{
		Query:  asciiLower(strings.TrimSpace(input.Query)),
		Limit:  limit,
		Cursor: input.Cursor,
	}, nil
}

// LabelList is one cursor-paginated deterministic label page.
type LabelList struct {
	Items      []Label
	NextCursor *string
	HasMore    bool
}
