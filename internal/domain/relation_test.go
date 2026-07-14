package domain

import (
	"errors"
	"testing"
)

func TestManageIssueRelationInputValidateNormalizesAndRejectsInvalidRequests(t *testing.T) {
	valid, err := (ManageIssueRelationInput{
		Action: RelationActionAdd, SourceIssueID: "issue-12", TargetIssueID: "ISSUE-13", RelationType: RelationTypeBlocks,
	}).Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if valid.SourceIssueID != "ISSUE-12" || valid.TargetIssueID != "ISSUE-13" {
		t.Fatalf("normalized input = %#v", valid)
	}

	for _, input := range []ManageIssueRelationInput{
		{Action: "replace", SourceIssueID: "ISSUE-1", TargetIssueID: "ISSUE-2", RelationType: RelationTypeBlocks},
		{Action: RelationActionAdd, SourceIssueID: "ISSUE-1", TargetIssueID: "ISSUE-2", RelationType: "contains"},
		{Action: RelationActionAdd, SourceIssueID: "ISSUE-1", TargetIssueID: "ISSUE-1", RelationType: RelationTypeBlocks},
		{Action: RelationActionAdd, SourceIssueID: "bad", TargetIssueID: "ISSUE-2", RelationType: RelationTypeBlocks},
	} {
		if _, err := input.Validate(); !errors.Is(err, &Error{Code: CodeInvalidArgument}) {
			t.Fatalf("Validate(%#v) error = %v, want INVALID_ARGUMENT", input, err)
		}
	}
}

func TestCanonicalRelationEndpointsOnlyReordersSymmetricRelations(t *testing.T) {
	source, target := CanonicalRelationEndpoints(RelationTypeRelatedTo, "01BZ", "01AY")
	if source != "01AY" || target != "01BZ" {
		t.Fatalf("related_to endpoints = %q, %q", source, target)
	}
	source, target = CanonicalRelationEndpoints(RelationTypeBlocks, "01BZ", "01AY")
	if source != "01BZ" || target != "01AY" {
		t.Fatalf("blocks endpoints = %q, %q", source, target)
	}
}
