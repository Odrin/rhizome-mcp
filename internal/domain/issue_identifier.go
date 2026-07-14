package domain

import (
	"fmt"
	"strconv"
	"strings"

	"rhizome-mcp/internal/ids"
)

// IssueIdentifierKind identifies how an issue reference is resolved.
type IssueIdentifierKind uint8

const (
	// IssueIdentifierInternalID resolves by the canonical internal ULID.
	IssueIdentifierInternalID IssueIdentifierKind = iota + 1
	// IssueIdentifierDisplayID resolves by the project-local sequence number.
	IssueIdentifierDisplayID
)

// IssueIdentifier is a normalized issue reference. Value is either a canonical
// ULID or a canonical ISSUE-N display ID; SequenceNo is populated for display
// identifiers.
type IssueIdentifier struct {
	Kind       IssueIdentifierKind
	Value      string
	SequenceNo int64
}

// ParseIssueIdentifier accepts only a canonical internal ULID or an ISSUE-N
// display identifier. The ISSUE prefix is case-insensitive, while the decimal
// sequence is ASCII-only and must be positive, canonical, and representable as
// a SQLite INTEGER.
func ParseIssueIdentifier(value string) (IssueIdentifier, error) {
	if _, err := ids.ParseStrict(value); err == nil {
		return IssueIdentifier{
			Kind:  IssueIdentifierInternalID,
			Value: value,
		}, nil
	}

	const prefix = "ISSUE-"
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return IssueIdentifier{}, invalidIssueIdentifier()
	}
	digits := value[len(prefix):]
	if digits == "" || digits[0] == '0' {
		return IssueIdentifier{}, invalidIssueIdentifier()
	}
	for index := 0; index < len(digits); index++ {
		if digits[index] < '0' || digits[index] > '9' {
			return IssueIdentifier{}, invalidIssueIdentifier()
		}
	}
	sequenceNo, err := strconv.ParseUint(digits, 10, 63)
	if err != nil || sequenceNo == 0 {
		return IssueIdentifier{}, invalidIssueIdentifier()
	}
	sequence := int64(sequenceNo)
	return IssueIdentifier{
		Kind:       IssueIdentifierDisplayID,
		Value:      fmt.Sprintf("ISSUE-%d", sequence),
		SequenceNo: sequence,
	}, nil
}

func invalidIssueIdentifier() *Error {
	return validationError("issue_id", "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
}
