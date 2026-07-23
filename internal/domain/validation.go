package domain

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTitleRunes is the maximum issue or decision title length.
	MaxTitleRunes = 300
	// MaxDescriptionRunes is the maximum issue description length.
	MaxDescriptionRunes = 100_000
	// MaxAcceptanceCriteriaRunes is the maximum acceptance criteria length.
	MaxAcceptanceCriteriaRunes = 50_000
	// MaxCommentRunes is the maximum comment length.
	MaxCommentRunes = 50_000
	// MaxDecisionSummaryRunes is the maximum decision summary length.
	MaxDecisionSummaryRunes = 2_000
	// MaxDecisionContentRunes is the maximum decision content length.
	MaxDecisionContentRunes = 100_000
	// MaxAttemptNoteRunes is the maximum attempt note or checkpoint length.
	MaxAttemptNoteRunes = 50_000
	// MaxLabelNameRunes is the maximum label name length.
	MaxLabelNameRunes = 64
	// MaxLabelsPerIssue is the maximum label count accepted for one issue.
	MaxLabelsPerIssue = 50
	// MaxRelationsPerOperation is the maximum relation count in one operation.
	MaxRelationsPerOperation = 100
	// MaxBatchIssues is the maximum issue count in one batch.
	MaxBatchIssues = 50
	// MaxBatchLabelAssignments is the maximum total label assignments in a plan.
	MaxBatchLabelAssignments = 50
	// MaxBatchDecisions is the maximum decision count in a plan.
	MaxBatchDecisions = 20
	// MaxLocalRefRunes bounds plan-local issue references.
	MaxLocalRefRunes = 64
	// MaxIdempotencyKeyRunes bounds an idempotency record key.
	MaxIdempotencyKeyRunes = 128
	// MaxGraphDepth is the maximum graph traversal depth.
	MaxGraphDepth = 5
	// MaxGraphNodes is the maximum graph node count.
	MaxGraphNodes = 500
	// MaxSearchResults is the maximum number of search results.
	MaxSearchResults = 100
	// MaxSearchSnippetRunes is the maximum search snippet length.
	MaxSearchSnippetRunes = 1_000
	// MaxBoardCollectionLimit bounds the board's blocked-issue and
	// active-attempt collections, matching the codebase's other bounded
	// collection limits.
	MaxBoardCollectionLimit = 100
)

// ValidateText rejects invalid UTF-8, NUL characters, invalid limits, and text
// whose rune count exceeds maxRunes. A negative maxRunes disables the length check.
func ValidateText(field, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return validationError(field, "INVALID_UTF8", "must contain valid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return validationError(field, "NUL_NOT_ALLOWED", "must not contain NUL characters")
	}
	if maxRunes >= 0 && utf8.RuneCountInString(value) > maxRunes {
		return NewError(
			CodeLimitExceeded,
			fmt.Sprintf("%s exceeds the maximum length of %d characters", field, maxRunes),
			false,
			Detail{Field: field, Code: "MAX_RUNES", Message: fmt.Sprintf("maximum %d", maxRunes)},
		)
	}
	return nil
}

// CopyBounded returns a defensive copy of values or a limit error. It never
// silently truncates. A negative maximum is invalid configuration.
func CopyBounded[T any](field string, values []T, maximum int) ([]T, error) {
	if maximum < 0 {
		return nil, NewError(
			CodeInvalidArgument,
			fmt.Sprintf("%s has an invalid negative limit", field),
			false,
			Detail{Field: field, Code: "INVALID_LIMIT"},
		)
	}
	if len(values) > maximum {
		return nil, NewError(
			CodeLimitExceeded,
			fmt.Sprintf("%s exceeds the maximum count of %d", field, maximum),
			false,
			Detail{Field: field, Code: "MAX_ITEMS", Message: fmt.Sprintf("maximum %d", maximum)},
		)
	}
	return slices.Clone(values), nil
}

func validationError(field, code, message string) *Error {
	return NewError(
		CodeInvalidArgument,
		field+" "+message,
		false,
		Detail{Field: field, Code: code},
	)
}
