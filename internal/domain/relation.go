package domain

import (
	"strings"
	"time"
)

// RelationType describes the semantic direction of an issue relation.
type RelationType string

const (
	RelationTypeBlocks     RelationType = "blocks"
	RelationTypeRelatedTo  RelationType = "related_to"
	RelationTypeDuplicates RelationType = "duplicates"
)

// Valid reports whether relationType is supported.
func (relationType RelationType) Valid() bool {
	switch relationType {
	case RelationTypeBlocks, RelationTypeRelatedTo, RelationTypeDuplicates:
		return true
	default:
		return false
	}
}

// ParseRelationType parses a supported relation type.
func ParseRelationType(value string) (RelationType, error) {
	relationType := RelationType(value)
	if !relationType.Valid() {
		return "", invalidEnum("relation_type", value)
	}
	return relationType, nil
}

// RelationAction selects whether a relation is added or removed.
type RelationAction string

const (
	RelationActionAdd    RelationAction = "add"
	RelationActionRemove RelationAction = "remove"
)

// Valid reports whether action is supported.
func (action RelationAction) Valid() bool {
	return action == RelationActionAdd || action == RelationActionRemove
}

// IssueRelation is the persisted canonical relation between two issues.
type IssueRelation struct {
	ID            string
	SourceIssueID string
	TargetIssueID string
	Type          RelationType
	CreatedAt     time.Time
}

// ManageIssueRelationInput is a caller-owned request for one relation mutation.
// SourceIssueID and TargetIssueID accept a canonical ULID or ISSUE-N.
type ManageIssueRelationInput struct {
	Action        RelationAction
	SourceIssueID string
	TargetIssueID string
	RelationType  RelationType
}

// Validate checks request-local relation rules and normalizes display IDs.
// Canonicalization of related_to happens after both IDs are resolved in storage.
func (input ManageIssueRelationInput) Validate() (ManageIssueRelationInput, error) {
	if !input.Action.Valid() {
		return ManageIssueRelationInput{}, invalidEnum("action", string(input.Action))
	}
	if !input.RelationType.Valid() {
		return ManageIssueRelationInput{}, invalidEnum("relation_type", string(input.RelationType))
	}
	source, err := parseRelationIdentifier("source_issue_id", input.SourceIssueID)
	if err != nil {
		return ManageIssueRelationInput{}, err
	}
	target, err := parseRelationIdentifier("target_issue_id", input.TargetIssueID)
	if err != nil {
		return ManageIssueRelationInput{}, err
	}
	if source.Kind == target.Kind && source.Value == target.Value {
		return ManageIssueRelationInput{}, selfRelationError()
	}
	return ManageIssueRelationInput{
		Action:        input.Action,
		SourceIssueID: source.Value,
		TargetIssueID: target.Value,
		RelationType:  input.RelationType,
	}, nil
}

// CanonicalRelationEndpoints returns the persisted endpoint order. related_to is
// symmetric and therefore stored once in lexical internal-ID order.
func CanonicalRelationEndpoints(relationType RelationType, sourceID, targetID string) (string, string) {
	if relationType == RelationTypeRelatedTo && strings.Compare(sourceID, targetID) > 0 {
		return targetID, sourceID
	}
	return sourceID, targetID
}

func parseRelationIdentifier(field, value string) (IssueIdentifier, error) {
	if err := ValidateText(field, value, -1); err != nil {
		return IssueIdentifier{}, err
	}
	identifier, err := ParseIssueIdentifier(value)
	if err != nil {
		return IssueIdentifier{}, validationError(field, "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
	}
	return identifier, nil
}

func selfRelationError() *Error {
	return NewError(
		CodeInvalidArgument,
		"source_issue_id and target_issue_id must identify different issues",
		false,
		Detail{Field: "target_issue_id", Code: "SELF_RELATION"},
	)
}
